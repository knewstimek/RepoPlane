package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"repoplane/internal/catalog"
	"repoplane/internal/contracts"
	"repoplane/internal/store"
	"repoplane/internal/workspace"
)

const (
	maximumPlanPayload = 64 * 1024
	maximumInputFiles  = 256
	defaultTimeoutSec  = uint64(300)
	maximumProbeOutput = 4096
	preflightProbeTime = 10 * time.Second
)

var (
	ErrNotExecutable         = errors.New("runner: capability is not executable")
	ErrUnsupportedScriptType = errors.New("runner: unsupported script type")
	ErrPlanStale             = errors.New("runner: plan is stale")
	ErrRunState              = errors.New("runner: invalid run state")
	ErrSnapshotBusy          = errors.New("runner: active process prevents memory snapshot")
	argumentName             = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	environmentName          = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	hostAlias                = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	templateArgument         = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]{0,63})\}`)
)

type UnsupportedScriptTypeError struct {
	Extension string
}

func (e *UnsupportedScriptTypeError) Error() string {
	return fmt.Sprintf("runner: direct %s execution is unsupported", e.Extension)
}

func (e *UnsupportedScriptTypeError) Unwrap() error { return ErrUnsupportedScriptType }

type PatternLimitError struct {
	Resource           string
	LimitKind          string
	Maximum            int
	ObservedLowerBound int
	Pattern            string
}

func (e *PatternLimitError) Error() string {
	if e.Pattern == "" {
		return fmt.Sprintf("runner: %s %s limit exceeded: maximum %d, observed at least %d", e.Resource, e.LimitKind, e.Maximum, e.ObservedLowerBound)
	}
	return fmt.Sprintf("runner: %s %s limit exceeded for pattern %q: maximum %d, observed at least %d", e.Resource, e.LimitKind, e.Pattern, e.Maximum, e.ObservedLowerBound)
}

func (e *PatternLimitError) Unwrap() error { return contracts.ErrLimitExceeded }

type capabilityResolver interface {
	ResolveCapability(ctx context.Context, id string) (catalog.Capability, error)
}

type qualificationResolver interface {
	ResolveCacheQualifications(ctx context.Context, capabilityID, configuration string, checkIDs []string) ([]string, bool, error)
}

type runningProcess struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type hostFactRecord struct {
	record   store.Record
	alias    string
	role     string
	os       string
	tier     string
	services []string
}

type Service struct {
	root                 *workspace.Root
	projectID            string
	workspaceID          string
	catalog              capabilityResolver
	reader               store.RecordReader
	writer               store.ObservationWriter
	cache                store.CacheRepository
	qualifications       qualificationResolver
	cacheKey             []byte
	cacheEnabled         func() bool
	stateDir             string
	streamByteLimit      uint64
	artifactByteLimit    uint64
	runArtifactByteLimit uint64
	retentionRunCount    int
	retentionAge         time.Duration
	now                  func() time.Time
	mu                   sync.Mutex
	running              map[string]*runningProcess
}

// SetCacheEnabled supplies the current host/session cache authorization. A nil
// callback preserves the legacy behavior based on cache repository/key presence.
func (s *Service) SetCacheEnabled(enabled func() bool) { s.cacheEnabled = enabled }

// BeginMemorySnapshot prevents new executions while a portable memory archive
// copies retained streams and artifacts. The caller must invoke the returned
// release function.
func (s *Service) BeginMemorySnapshot() (func(), error) {
	s.mu.Lock()
	if len(s.running) != 0 {
		s.mu.Unlock()
		return nil, ErrSnapshotBusy
	}
	return s.mu.Unlock, nil
}

func NewService(root *workspace.Root, catalogService capabilityResolver, records store.RecordRepository, cache store.CacheRepository, qualifications qualificationResolver, stateDir string, cacheKey []byte) *Service {
	return &Service{
		root: root, projectID: root.ID(), workspaceID: root.ID(), catalog: catalogService,
		reader: records, writer: records, cache: cache, qualifications: qualifications, stateDir: stateDir, cacheKey: append([]byte(nil), cacheKey...),
		streamByteLimit: DefaultStreamByteLimit, artifactByteLimit: DefaultArtifactByteLimit,
		runArtifactByteLimit: DefaultRunArtifactLimit, now: time.Now, running: make(map[string]*runningProcess),
		retentionRunCount: 200, retentionAge: 14 * 24 * time.Hour,
	}
}

func (s *Service) Prepare(ctx context.Context, request PrepareRequest) (PrepareResponse, error) {
	if request.CapabilityID == "" || request.CapabilityRevision == "" {
		return PrepareResponse{}, errors.New("capability_id and capability_revision are required")
	}
	if len(request.CapabilityID) > 128 || len(request.CapabilityRevision) > 128 || len(request.Configuration) > 128 || len(request.Arguments) > 64 {
		return PrepareResponse{}, contracts.ErrLimitExceeded
	}
	if request.Configuration == "" {
		request.Configuration = "default"
	}
	if request.CacheMode == "" {
		request.CacheMode = "auto"
	}
	if request.CacheMode != "auto" && request.CacheMode != "bypass" {
		return PrepareResponse{}, errors.New("cache_mode must be auto or bypass")
	}
	limits, err := contracts.NormalizeLimits(contracts.LimitRequest{TimeLimitMS: request.TimeLimitMS})
	if err != nil {
		return PrepareResponse{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, limits.TimeLimit)
	defer cancel()
	capability, err := s.catalog.ResolveCapability(ctx, request.CapabilityID)
	if err != nil {
		return PrepareResponse{}, err
	}
	if capability.Revision != request.CapabilityRevision {
		return PrepareResponse{}, ErrPlanStale
	}
	manifest := capability.Manifest
	if manifest.Execution == nil || !manifest.Execution.TrustedForRun {
		return PrepareResponse{}, ErrNotExecutable
	}
	argv, normalizedArgs, err := expandArguments(manifest, request.Arguments)
	if err != nil {
		return PrepareResponse{}, err
	}
	for name, declaration := range manifest.Arguments {
		if declaration.Type != "project_path" {
			continue
		}
		value, exists := normalizedArgs[name]
		if !exists {
			continue
		}
		text := filepath.ToSlash(value.(string))
		if filepath.IsAbs(text) {
			return PrepareResponse{}, errors.New("project_path argument must be workspace-relative")
		}
		if _, _, err := s.root.ResolveForLookup(filepath.FromSlash(text)); err != nil {
			return PrepareResponse{}, err
		}
		normalizedArgs[name] = text
	}
	executable, executableIdentity, err := s.resolveExecutable(ctx, manifest.Execution.ExecutableRef)
	if err != nil {
		return PrepareResponse{}, err
	}
	cwdAbsolute, cwdRelative, err := s.resolveCWD(manifest.Execution.CWD)
	if err != nil {
		return PrepareResponse{}, err
	}
	checks, ready, warnings := s.preflight(ctx, manifest, executable, executableIdentity, cwdAbsolute)
	hostChecks, hostWarnings := s.hostFactChecks(ctx, manifest, normalizedArgs)
	checks = append(checks, hostChecks...)
	warnings = append(warnings, hostWarnings...)
	inputHashes, err := s.snapshotPatterns(ctx, manifest.Inputs, "inputs")
	if err != nil {
		return PrepareResponse{}, err
	}
	outputsBefore, err := s.snapshotPatterns(ctx, manifest.Outputs, "outputs")
	if err != nil {
		return PrepareResponse{}, err
	}
	cacheDecision, cacheChecks, err := s.prepareCache(ctx, capability, request, argv, inputHashes, checks)
	if err != nil {
		return PrepareResponse{}, err
	}
	preflightID, err := recordID("environment_")
	if err != nil {
		return PrepareResponse{}, err
	}
	now := s.now().UTC()
	preflightPayload, _ := json.Marshal(map[string]any{
		"capability_id": manifest.ID, "capability_revision": capability.Revision,
		"configuration": request.Configuration, "checks": checks, "ready": ready,
		"observed_at": now,
	})
	if len(preflightPayload) > maximumPlanPayload {
		return PrepareResponse{}, contracts.ErrLimitExceeded
	}
	preflightRecord, err := s.writer.CreateObservation(ctx, store.RecordCreate{Record: store.Record{
		ID: preflightID, Kind: "environment", SchemaVersion: "environment-record.v1",
		ProjectID: s.projectID, WorkspaceID: s.workspaceID, Revision: 1,
		Source: "observed", WriterClass: "server", Validity: "current",
		CreatedAt: now, UpdatedAt: now, Payload: preflightPayload,
		EvidenceRefs: []string{capability.SourceRef},
	}})
	if err != nil {
		return PrepareResponse{}, err
	}
	timeout := manifest.Execution.TimeoutSec
	if timeout == 0 {
		timeout = defaultTimeoutSec
	}
	artifactMode := manifest.Execution.ArtifactMode
	if artifactMode == "" {
		artifactMode = "metadata"
	}
	runID, err := recordID("run_")
	if err != nil {
		return PrepareResponse{}, err
	}
	payload := runPayload{
		State: "prepared", CapabilityID: manifest.ID, CapabilityRevision: capability.Revision,
		Configuration: request.Configuration, Arguments: normalizedArgs, Argv: argv,
		CWD: cwdRelative, ExecutableRef: manifest.Execution.ExecutableRef,
		ExecutableIdentity: executableIdentity, ExecutionFingerprint: capability.ExecutionFingerprint,
		InputHashes: inputHashes, OutputPaths: append([]string(nil), manifest.Outputs...), OutputsBefore: outputsBefore,
		OutputsAfter:       map[string]string{},
		PreflightRecordRef: "record:" + preflightRecord.ID, Ready: ready, TimeoutSec: timeout,
		ArtifactMode: artifactMode, PreparedAt: now, ArtifactRefs: []string{},
		StreamsRetained: false,
		Cache:           cacheDecision, CacheKeyChecks: cacheChecks,
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > maximumPlanPayload {
		return PrepareResponse{}, contracts.ErrLimitExceeded
	}
	runRecord, err := s.writer.CreateObservation(ctx, store.RecordCreate{Record: store.Record{
		ID: runID, Kind: "run", SchemaVersion: "run-receipt.v2",
		ProjectID: s.projectID, WorkspaceID: s.workspaceID, Revision: 1,
		Source: "observed", WriterClass: "server", Validity: "current",
		CreatedAt: now, UpdatedAt: now, Payload: encoded,
		EvidenceRefs: []string{capability.SourceRef, "record:" + preflightRecord.ID},
	}})
	if err != nil {
		return PrepareResponse{}, err
	}
	plan := planResult(runRecord.ID, payload)
	plan.InputHashes = nil
	status := contracts.StatusOK
	if !ready {
		status = contracts.StatusPartial
	}
	return PrepareResponse{Status: status, Plan: plan, Checks: checks, Warnings: warnings}, nil
}

func planResult(id string, payload runPayload) PlanResult {
	return PlanResult{
		ID: id, CapabilityID: payload.CapabilityID, CapabilityRevision: payload.CapabilityRevision,
		Configuration: payload.Configuration, Argv: append([]string(nil), payload.Argv...), CWD: payload.CWD,
		ExecutableIdentity: payload.ExecutableIdentity, InputHashes: payload.InputHashes,
		InputCount:  len(payload.InputHashes),
		OutputPaths: append([]string(nil), payload.OutputPaths...), PreflightRecordRef: payload.PreflightRecordRef,
		Ready: payload.Ready, TimeoutSec: payload.TimeoutSec, PreparedAt: payload.PreparedAt,
		ExecutionFingerprint: payload.ExecutionFingerprint,
		Cache:                payload.Cache,
	}
}

func (s *Service) resolveExecutable(ctx context.Context, ref string) (string, string, error) {
	if filepath.IsAbs(ref) {
		return "", "", errors.New("absolute executable_ref is not allowed")
	}
	var path string
	var err error
	if strings.ContainsAny(ref, `/\`) {
		path, err = s.root.ResolvePrimaryExisting(filepath.FromSlash(ref))
	} else {
		path, err = exec.LookPath(ref)
	}
	if err != nil {
		return "", "", fmt.Errorf("resolve executable: %w", err)
	}
	if err := validateProcessExecutable(path); err != nil {
		return "", "", err
	}
	identity, err := hashFile(ctx, path, 512*1024*1024)
	if err != nil {
		return "", "", fmt.Errorf("hash executable: %w", err)
	}
	return path, identity, nil
}

func (s *Service) resolveCWD(value string) (string, string, error) {
	if value == "" || value == "." || value == "repository" {
		return s.root.Resolved(), ".", nil
	}
	if filepath.IsAbs(value) {
		return "", "", errors.New("absolute execution cwd is not allowed")
	}
	path, err := s.root.ResolvePrimaryExisting(filepath.FromSlash(value))
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", "", errors.New("execution cwd must be a workspace directory")
	}
	relative, err := filepath.Rel(s.root.Resolved(), path)
	if err != nil {
		return "", "", err
	}
	return path, filepath.ToSlash(relative), nil
}

func expandArguments(manifest catalog.Manifest, provided map[string]any) ([]string, map[string]any, error) {
	if provided == nil {
		provided = map[string]any{}
	}
	for name := range provided {
		if !argumentName.MatchString(name) {
			return nil, nil, errors.New("invalid argument name")
		}
		if _, exists := manifest.Arguments[name]; !exists {
			return nil, nil, fmt.Errorf("unknown argument %q", name)
		}
	}
	normalized := make(map[string]any, len(manifest.Arguments))
	values := make(map[string]string, len(manifest.Arguments))
	for name, declaration := range manifest.Arguments {
		value, exists := provided[name]
		if !exists && declaration.Default != nil {
			value, exists = declaration.Default, true
		}
		if !exists {
			if declaration.Required {
				return nil, nil, fmt.Errorf("argument %q is required", name)
			}
			continue
		}
		text, normalizedValue, err := normalizeArgument(declaration.Type, value)
		if err != nil {
			return nil, nil, fmt.Errorf("argument %q: %w", name, err)
		}
		values[name], normalized[name] = text, normalizedValue
	}
	argv := make([]string, 0, len(manifest.Execution.ArgvTemplate))
	for _, element := range manifest.Execution.ArgvTemplate {
		missing := ""
		replaced := templateArgument.ReplaceAllStringFunc(element, func(match string) string {
			name := templateArgument.FindStringSubmatch(match)[1]
			value, exists := values[name]
			if !exists {
				missing = name
				return match
			}
			return value
		})
		if missing != "" {
			return nil, nil, fmt.Errorf("argv template requires argument %q", missing)
		}
		argv = append(argv, replaced)
	}
	return argv, normalized, nil
}

func normalizeArgument(kind string, value any) (string, any, error) {
	switch kind {
	case "string", "project_path", "host_ref":
		text, ok := value.(string)
		if !ok || len(text) > 8192 {
			return "", nil, errors.New("must be a bounded string")
		}
		if kind == "host_ref" {
			if !hostAlias.MatchString(text) {
				return "", nil, errors.New("must be a host alias")
			}
			text = strings.ToLower(text)
		}
		return text, text, nil
	case "boolean", "bool":
		flag, ok := value.(bool)
		if !ok {
			return "", nil, errors.New("must be a boolean")
		}
		return strconv.FormatBool(flag), flag, nil
	case "integer":
		switch number := value.(type) {
		case json.Number:
			if _, err := number.Int64(); err != nil {
				return "", nil, errors.New("must be an integer")
			}
			return number.String(), number, nil
		case float64:
			if number != float64(int64(number)) {
				return "", nil, errors.New("must be an integer")
			}
			return strconv.FormatInt(int64(number), 10), int64(number), nil
		case int:
			return strconv.Itoa(number), number, nil
		case int64:
			return strconv.FormatInt(number, 10), number, nil
		default:
			return "", nil, errors.New("must be an integer")
		}
	default:
		return "", nil, fmt.Errorf("unsupported type %q", kind)
	}
}

func (s *Service) hostFactChecks(ctx context.Context, manifest catalog.Manifest, arguments map[string]any) ([]PreflightResult, []contracts.Warning) {
	names := make([]string, 0)
	for name, declaration := range manifest.Arguments {
		if declaration.Type == "host_ref" {
			if _, exists := arguments[name]; exists {
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		return []PreflightResult{}, contracts.EmptyWarnings()
	}
	sort.Strings(names)
	if s.reader == nil {
		return unknownHostFactChecks(names, arguments, "host fact store unavailable")
	}
	aliases := make([]string, 0, len(names))
	seenAliases := make(map[string]struct{}, len(names))
	for _, name := range names {
		alias := arguments[name].(string)
		if _, exists := seenAliases[alias]; !exists {
			seenAliases[alias] = struct{}{}
			aliases = append(aliases, alias)
		}
	}
	query := store.RecordQuery{ProjectID: s.projectID, WorkspaceID: s.workspaceID, Kind: "memo", Validity: "current", Limit: 1_000}
	if len(aliases) <= 16 {
		query.Terms = aliases
	}
	page, err := s.reader.QueryRecords(ctx, query)
	if err != nil {
		return unknownHostFactChecks(names, arguments, "current host facts unavailable")
	}
	byAlias := make(map[string][]hostFactRecord)
	for _, record := range page.Records {
		var payload struct {
			MemoKind string `json:"memo_kind"`
			Host     struct {
				Alias    string   `json:"alias"`
				Role     string   `json:"role"`
				OS       string   `json:"os"`
				Tier     string   `json:"tier"`
				Services []string `json:"services"`
			} `json:"host"`
		}
		if record.SchemaVersion != "memo.v2" || json.Unmarshal(record.Payload, &payload) != nil || payload.MemoKind != "host_fact" || payload.Host.Alias == "" {
			continue
		}
		alias := strings.ToLower(payload.Host.Alias)
		byAlias[alias] = append(byAlias[alias], hostFactRecord{record: record, alias: alias, role: payload.Host.Role, os: payload.Host.OS, tier: payload.Host.Tier, services: payload.Host.Services})
	}
	checks := make([]PreflightResult, 0, len(names))
	warnings := contracts.EmptyWarnings()
	for _, name := range names {
		alias := arguments[name].(string)
		facts := byAlias[alias]
		check := PreflightResult{ID: "host." + name, Kind: "host_fact", Requirement: "informational"}
		if len(facts) == 0 {
			if page.Complete {
				check.Status, check.Summary = "unknown", "no current host fact for "+alias
				warnings = append(warnings, contracts.Warning{Code: "host_fact_missing", Message: check.Summary})
			} else {
				check.Status, check.Summary = "unknown", "current host fact scan was partial for "+alias
				warnings = append(warnings, contracts.Warning{Code: "host_fact_unknown", Message: check.Summary})
			}
		} else if !page.Complete || conflictingHostFacts(facts) {
			check.Status, check.Summary = "unknown", "current host facts conflict or are partial for "+alias
			ref := "record:" + facts[0].record.ID
			warnings = append(warnings, contracts.Warning{Code: "host_fact_conflict", Message: check.Summary, Ref: &ref})
		} else {
			selected := facts[0]
			check.Status = "passed"
			check.Identity = "record:" + selected.record.ID
			services := selected.services
			if len(services) > 4 {
				services = services[:4]
			}
			check.Summary = boundedHostSummary(strings.Join([]string{selected.alias, "role=" + selected.role, "os=" + selected.os, "tier=" + selected.tier, "services=" + strings.Join(services, ",")}, " "))
		}
		checks = append(checks, check)
	}
	return checks, warnings
}

func unknownHostFactChecks(names []string, arguments map[string]any, summary string) ([]PreflightResult, []contracts.Warning) {
	checks := make([]PreflightResult, 0, len(names))
	warnings := make([]contracts.Warning, 0, len(names))
	for _, name := range names {
		alias, _ := arguments[name].(string)
		checks = append(checks, PreflightResult{ID: "host." + name, Kind: "host_fact", Requirement: "informational", Status: "unknown", Summary: summary + " for " + alias})
		warnings = append(warnings, contracts.Warning{Code: "host_fact_unknown", Message: summary + " for " + alias})
	}
	return checks, warnings
}

func conflictingHostFacts(facts []hostFactRecord) bool {
	for _, current := range facts[1:] {
		if !strings.EqualFold(current.os, facts[0].os) || !strings.EqualFold(current.role, facts[0].role) {
			return true
		}
	}
	return false
}

func boundedHostSummary(value string) string {
	const maximumRunes = 320
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= maximumRunes {
		return value
	}
	return string(runes[:maximumRunes-1]) + "…"
}

func (s *Service) preflight(ctx context.Context, manifest catalog.Manifest, executable, executableIdentity, executionCWD string) ([]PreflightResult, bool, []contracts.Warning) {
	checks := []PreflightResult{
		{ID: "runner.platform", Kind: "platform", Requirement: "required", Status: "passed", Summary: runtime.GOOS + "/" + runtime.GOARCH},
		{ID: "runner.executable", Kind: "executable", Requirement: "required", Status: "passed", Summary: filepath.Base(executable), Identity: executableIdentity},
	}
	ready := true
	warnings := contracts.EmptyWarnings()
	for _, declaration := range manifest.Execution.Preflight {
		result := s.runPreflightCheckAt(ctx, declaration, executionCWD)
		checks = append(checks, result)
		if result.Status != "passed" {
			if result.Requirement == "required" {
				ready = false
			}
			warnings = append(warnings, contracts.Warning{Code: "preflight_" + result.Status, Message: result.ID + ": " + result.Summary})
		}
	}
	return checks, ready, warnings
}

func (s *Service) runPreflightCheck(ctx context.Context, declaration catalog.PreflightCheck) PreflightResult {
	return s.runPreflightCheckAt(ctx, declaration, s.root.Resolved())
}

func (s *Service) runPreflightCheckAt(ctx context.Context, declaration catalog.PreflightCheck, executionCWD string) PreflightResult {
	requirement := declaration.Requirement
	if requirement == "" {
		requirement = "recommended"
	}
	result := PreflightResult{ID: declaration.ID, Kind: declaration.Kind, Requirement: requirement, Status: "unknown", Summary: "observation unavailable"}
	switch declaration.Kind {
	case "platform":
		result.Status, result.Summary = "passed", runtime.GOOS+"/"+runtime.GOARCH
	case "environment":
		if !environmentName.MatchString(declaration.Ref) {
			result.Status, result.Summary = "failed", "invalid environment variable name"
		} else if _, exists := os.LookupEnv(declaration.Ref); exists {
			result.Status, result.Summary = "passed", "set (value withheld)"
		} else {
			result.Status, result.Summary = "missing", "not set"
		}
	case "git":
		command := exec.CommandContext(ctx, "git", "-C", executionCWD, "rev-parse", "HEAD")
		output, err := boundedCommandOutput(command, maximumProbeOutput)
		if err == nil {
			value := strings.TrimSpace(string(output))
			if regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`).MatchString(value) {
				result.Identity = value
			}
			status := exec.CommandContext(ctx, "git", "-C", executionCWD, "status", "--porcelain=v1", "--untracked-files=no")
			statusOutput, statusErr := boundedCommandOutput(status, maximumProbeOutput)
			if statusErr != nil {
				result.Status, result.Summary = "unknown", "Git worktree state unavailable"
			} else if len(bytes.TrimSpace(statusOutput)) == 0 {
				result.Status, result.Summary = "passed", "repository observed; worktree clean"
			} else {
				result.Status, result.Summary = "passed", "repository observed; worktree dirty"
			}
		} else {
			result.Status, result.Summary = "missing", "Git repository not observed"
		}
	case "file":
		if declaration.Ref == "" || filepath.IsAbs(declaration.Ref) {
			result.Status, result.Summary = "failed", "workspace-relative file ref is required"
			break
		}
		path, err := s.root.ResolvePrimaryExisting(filepath.FromSlash(declaration.Ref))
		if err != nil {
			result.Status, result.Summary = "missing", "file not found"
			break
		}
		identity, err := hashFile(ctx, path, 512*1024*1024)
		if err != nil {
			result.Status, result.Summary = "unknown", "file identity unavailable"
			break
		}
		result.Status, result.Summary, result.Identity = "passed", filepath.ToSlash(declaration.Ref), identity
	case "executable":
		path, identity, err := s.resolveExecutable(ctx, declaration.Ref)
		if err != nil {
			result.Status, result.Summary = "missing", "executable not found"
			break
		}
		result.Status, result.Summary, result.Identity = "passed", filepath.Base(path), identity
		if len(declaration.Argv) > 0 {
			probeCtx, cancel := context.WithTimeout(ctx, preflightProbeTime)
			defer cancel()
			command, commandErr := newProcessCommand(probeCtx, path, declaration.Argv)
			if commandErr != nil {
				result.Status, result.Summary = "failed", "declared version probe is unsupported"
				break
			}
			output, probeErr := boundedCommandOutput(command, maximumProbeOutput)
			if probeErr != nil {
				result.Status, result.Summary = "failed", "declared version probe failed"
			} else {
				sum := sha256.Sum256(append(append([]byte(identity), 0), output...))
				result.Identity = "sha256:" + hex.EncodeToString(sum[:])
				if line := firstLine(string(output)); line != "" {
					result.Summary = s.sanitizeProbeSummary(line)
				}
			}
		}
	}
	return result
}

func (s *Service) sanitizeProbeSummary(value string) string {
	for _, sensitive := range []string{s.root.Lexical(), s.root.Resolved()} {
		value = replaceFold(value, sensitive, "WORKSPACE")
	}
	if home, err := os.UserHomeDir(); err == nil {
		value = replaceFold(value, home, "USER_HOME")
	}
	if len(value) > maximumProbeOutput {
		value = value[:maximumProbeOutput]
	}
	return value
}

func replaceFold(value, old, replacement string) string {
	if old == "" {
		return value
	}
	for {
		index := strings.Index(strings.ToLower(value), strings.ToLower(old))
		if index < 0 {
			return value
		}
		value = value[:index] + replacement + value[index+len(old):]
	}
}

func boundedCommandOutput(command *exec.Cmd, limit int64) ([]byte, error) {
	buffer := &boundedMemoryWriter{limit: int(limit)}
	command.Stdout = buffer
	command.Stderr = io.Discard
	err := command.Run()
	return buffer.Bytes(), err
}

type boundedMemoryWriter struct {
	buffer bytes.Buffer
	limit  int
}

func (w *boundedMemoryWriter) Write(data []byte) (int, error) {
	accepted := len(data)
	remaining := w.limit - w.buffer.Len()
	if remaining > len(data) {
		remaining = len(data)
	}
	if remaining > 0 {
		_, _ = w.buffer.Write(data[:remaining])
	}
	return accepted, nil
}

func (w *boundedMemoryWriter) Bytes() []byte { return append([]byte(nil), w.buffer.Bytes()...) }

func firstLine(value string) string {
	if index := strings.IndexAny(value, "\r\n"); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value)
}

func recordID(prefix string) (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(data), nil
}

func hashFile(ctx context.Context, path string, maximum int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximum {
		return "", contracts.ErrLimitExceeded
	}
	hasher := sha256.New()
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			_, _ = hasher.Write(buffer[:count])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

func (s *Service) snapshotPatterns(ctx context.Context, patterns []string, resource string) (map[string]string, error) {
	if len(patterns) > 128 {
		return nil, &PatternLimitError{Resource: resource, LimitKind: "pattern_count", Maximum: 128, ObservedLowerBound: len(patterns)}
	}
	result := make(map[string]string)
	for _, pattern := range patterns {
		if pattern == "" || filepath.IsAbs(pattern) || strings.HasPrefix(filepath.Clean(filepath.FromSlash(pattern)), "..") {
			return nil, errors.New("input/output pattern must remain inside workspace")
		}
		matcher, err := compileGlob(filepath.ToSlash(pattern))
		if err != nil {
			return nil, errors.New("invalid input/output pattern")
		}
		matched := false
		err = filepath.WalkDir(s.root.Resolved(), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				if path != s.root.Resolved() && (entry.Name() == ".git" || entry.Name() == ".repoplane") {
					return filepath.SkipDir
				}
				return nil
			}
			relative, err := filepath.Rel(s.root.Resolved(), path)
			if err != nil || !matcher.MatchString(filepath.ToSlash(relative)) {
				return nil
			}
			matched = true
			key := filepath.ToSlash(relative)
			if _, exists := result[key]; !exists && len(result) >= maximumInputFiles {
				return &PatternLimitError{
					Resource: resource, LimitKind: "matched_file_count", Maximum: maximumInputFiles,
					ObservedLowerBound: maximumInputFiles + 1, Pattern: filepath.ToSlash(pattern),
				}
			}
			hash, err := hashFile(ctx, path, 512*1024*1024)
			if err != nil {
				return err
			}
			result[key] = hash
			return nil
		})
		if err != nil {
			return nil, err
		}
		if !matched {
			result["pattern:"+filepath.ToSlash(pattern)] = "missing"
		}
	}
	return result, nil
}

type globMatcher struct{ *regexp.Regexp }

func compileGlob(pattern string) (globMatcher, error) {
	var builder strings.Builder
	builder.WriteString("^")
	for index := 0; index < len(pattern); {
		switch pattern[index] {
		case '*':
			if index+1 < len(pattern) && pattern[index+1] == '*' {
				index += 2
				if index < len(pattern) && pattern[index] == '/' {
					builder.WriteString("(?:.*/)?")
					index++
				} else {
					builder.WriteString(".*")
				}
			} else {
				builder.WriteString("[^/]*")
				index++
			}
		case '?':
			builder.WriteString("[^/]")
			index++
		default:
			builder.WriteString(regexp.QuoteMeta(string(pattern[index])))
			index++
		}
	}
	builder.WriteString("$")
	compiled, err := regexp.Compile(builder.String())
	return globMatcher{compiled}, err
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
