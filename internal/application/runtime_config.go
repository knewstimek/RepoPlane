package application

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"repoplane/internal/config"
	"repoplane/internal/httptransport"
	"repoplane/internal/runtimeaccess"
	"repoplane/internal/runtimeconfig"
	storesqlite "repoplane/internal/store/sqlite"
	"repoplane/internal/workspace"
)

func (a *Application) Status(context.Context) (runtimeconfig.Response, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return runtimeconfig.Response{Status: "ok", Configuration: a.snapshotLocked()}, nil
}

func (a *Application) Apply(ctx context.Context, request runtimeconfig.Request) (runtimeconfig.Response, error) {
	if err := runtimeconfig.Validate(request); err != nil {
		return runtimeconfig.Response{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if request.Action == runtimeconfig.ActionStatus {
		return runtimeconfig.Response{Status: "ok", Configuration: a.snapshotLocked()}, nil
	}
	if request.Target == runtimeconfig.TargetHTTPTransport {
		changed, err := a.configureHTTPLocked(ctx, request)
		if err != nil {
			return runtimeconfig.Response{}, err
		}
		return runtimeconfig.Response{Status: "ok", Changed: changed, Configuration: a.snapshotLocked()}, nil
	}

	next := cloneSettings(a.settings)
	changed, refreshed, err := mutateSettings(&next, request)
	if err != nil {
		return runtimeconfig.Response{}, err
	}
	if !changed && !refreshed {
		return runtimeconfig.Response{Status: "ok", Configuration: a.snapshotLocked()}, nil
	}
	if err := validateRuntimePaths(next, request); err != nil {
		return runtimeconfig.Response{}, err
	}
	if err := a.switchBundleLocked(ctx, next); err != nil {
		return runtimeconfig.Response{}, err
	}
	return runtimeconfig.Response{Status: "ok", Changed: changed, Refreshed: true, Configuration: a.snapshotLocked()}, nil
}

func (a *Application) snapshotLocked() runtimeconfig.Snapshot {
		result := runtimeconfig.Snapshot{
		runtimeconfig.TargetWorkspace:     {a.settings.Workspace},
		runtimeconfig.TargetStateDir:      {a.settings.StateDir},
		runtimeconfig.TargetCatalogRoot:   append([]string(nil), a.settings.CatalogRoots...),
		runtimeconfig.TargetCandidateRoot: append([]string(nil), a.settings.CandidateRoots...),
		runtimeconfig.TargetRuleFile:      append([]string(nil), a.settings.RuleFiles...),
		runtimeconfig.TargetSymbolIndex:   append([]string(nil), a.settings.SymbolIndexes...),
		runtimeconfig.TargetToolSurface:   {a.settings.ToolSurface},
	}
	result[runtimeconfig.TargetHTTPTransport] = make([]string, 0)
	if a.httpRunning != nil {
		result[runtimeconfig.TargetHTTPTransport] = []string{a.httpProfilePath}
	}
	return result
}

func cloneSettings(value config.Settings) config.Settings {
	value.CatalogRoots = append([]string(nil), value.CatalogRoots...)
	value.CandidateRoots = append([]string(nil), value.CandidateRoots...)
	value.RuleFiles = append([]string(nil), value.RuleFiles...)
	value.SymbolIndexes = append([]string(nil), value.SymbolIndexes...)
	return value
}

func mutateSettings(settings *config.Settings, request runtimeconfig.Request) (bool, bool, error) {
	if request.Target == runtimeconfig.TargetWorkspace || request.Target == runtimeconfig.TargetStateDir {
		value, err := filepath.Abs(filepath.Clean(request.Values[0]))
		if err != nil || !filepath.IsAbs(request.Values[0]) {
			return false, false, runtimeconfig.ErrInvalid
		}
		if request.Target == runtimeconfig.TargetWorkspace {
			changed := !samePath(settings.Workspace, value)
			settings.Workspace = value
			return changed, changed, nil
		}
		changed := !samePath(settings.StateDir, value)
		settings.StateDir = value
		return changed, changed, nil
	}
	var values *[]string
	switch request.Target {
	case runtimeconfig.TargetCatalogRoot:
		values = &settings.CatalogRoots
	case runtimeconfig.TargetCandidateRoot:
		values = &settings.CandidateRoots
	case runtimeconfig.TargetRuleFile:
		values = &settings.RuleFiles
	case runtimeconfig.TargetSymbolIndex:
		values = &settings.SymbolIndexes
	default:
		return false, false, runtimeconfig.ErrInvalid
	}
	if request.Action == runtimeconfig.ActionRefresh {
		return false, true, nil
	}
	normalized := normalizeValues(request.Values)
	before := append([]string(nil), (*values)...)
	switch request.Action {
	case runtimeconfig.ActionAdd:
		if !containsPath(*values, normalized[0]) {
			*values = append(*values, normalized[0])
		}
	case runtimeconfig.ActionRemove:
		result := make([]string, 0, len(*values))
		for _, value := range *values {
			if !samePath(value, normalized[0]) {
				result = append(result, value)
			}
		}
		*values = result
	case runtimeconfig.ActionReplace:
		*values = uniquePaths(normalized)
	default:
		return false, false, runtimeconfig.ErrInvalid
	}
	return !samePaths(before, *values), true, nil
}

func normalizeValues(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(value))))
	}
	return result
}
func uniquePaths(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !containsPath(result, value) {
			result = append(result, value)
		}
	}
	return result
}
func containsPath(values []string, wanted string) bool {
	for _, value := range values {
		if samePath(value, wanted) {
			return true
		}
	}
	return false
}
func samePaths(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !samePath(left[i], right[i]) {
			return false
		}
	}
	return true
}
func samePath(left, right string) bool {
	left = filepath.Clean(filepath.FromSlash(left))
	right = filepath.Clean(filepath.FromSlash(right))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func validateRuntimePaths(settings config.Settings, request runtimeconfig.Request) error {
	root, err := workspace.Open(settings.Workspace)
	if err != nil {
		return err
	}
	if request.Target == runtimeconfig.TargetWorkspace || request.Target == runtimeconfig.TargetStateDir || request.Action == runtimeconfig.ActionRemove || request.Action == runtimeconfig.ActionRefresh {
		return nil
	}
	for _, value := range request.Values {
		if filepath.IsAbs(filepath.FromSlash(value)) {
			return runtimeconfig.ErrInvalid
		}
		if _, err := root.ResolvePrimaryExisting(filepath.FromSlash(value)); err != nil {
			return fmt.Errorf("configured source unavailable: %w", err)
		}
	}
	return nil
}

func (a *Application) switchBundleLocked(ctx context.Context, nextSettings config.Settings) error {
	a.router.mu.RLock()
	current := a.router.current
	release, err := current.runner.BeginMemorySnapshot()
	a.router.mu.RUnlock()
	if err != nil {
		return runtimeconfig.ErrBusy
	}
	next, err := buildBundle(ctx, nextSettings, func() bool { return a.runtimeAccess.Enabled(runtimeaccess.KindCache) })
	if err != nil {
		release()
		return err
	}
	oldSettings := a.settings
	profilePath := a.httpProfilePath
	hadHTTP := a.httpRunning != nil
	if hadHTTP {
		if err := a.stopHTTPLocked(); err != nil {
			release()
			_ = next.close()
			return err
		}
	}
	old := a.router.swap(next)
	a.settings = nextSettings
	a.settings.Workspace = next.root.Resolved()
	a.settings.StateDir, _ = filepath.Abs(nextSettings.StateDir)
	a.runtimeAccess.RebindRoot(next.root)
	if hadHTTP {
		if err := a.startHTTPLocked(ctx, profilePath); err != nil {
			a.router.swap(old)
			a.settings = oldSettings
			a.runtimeAccess.RebindRoot(old.root)
			release()
			_ = next.close()
			_ = a.startHTTPLocked(ctx, profilePath)
			return err
		}
	}
	release()
	return old.close()
}

func (a *Application) configureHTTPLocked(ctx context.Context, request runtimeconfig.Request) (bool, error) {
	if request.Action == runtimeconfig.ActionStop {
		if a.httpRunning == nil {
			return false, nil
		}
		return true, a.stopHTTPLocked()
	}
	profilePath, err := filepath.Abs(filepath.Clean(request.Values[0]))
	if err != nil || !filepath.IsAbs(request.Values[0]) {
		return false, runtimeconfig.ErrInvalid
	}
	oldPath := a.httpProfilePath
	hadOld := a.httpRunning != nil
	if hadOld {
		if err := a.stopHTTPLocked(); err != nil {
			return false, err
		}
	}
	if err := a.startHTTPLocked(ctx, profilePath); err != nil {
		if hadOld {
			_ = a.startHTTPLocked(ctx, oldPath)
		}
		return false, err
	}
	return !hadOld || !samePath(oldPath, profilePath), nil
}

func (a *Application) startHTTPLocked(ctx context.Context, profilePath string) error {
	profile, err := httptransport.LoadProfile(profilePath)
	if err != nil {
		return err
	}
	verifier, err := httptransport.TokenVerifier(profile)
	if err != nil {
		return err
	}
	audit, err := storesqlite.OpenAudit(ctx, filepath.Join(a.settings.StateDir, "audit.db"))
	if err != nil {
		return err
	}
	auditKey, err := loadOrCreateKey(filepath.Join(a.settings.StateDir, "audit.key"))
	if err != nil {
		_ = audit.Close()
		return err
	}
	_, _ = audit.DeleteExpiredAudit(ctx, time.Now().UTC().AddDate(0, 0, -profile.Audit.RetentionDays), 256)
	options := a.MCPOptions()
	// HTTP keeps the same stable tool contract, but cannot consume or create
	// local stdio leases. Host startup flags remain its second authorization gate.
	a.router.mu.RLock()
	httpRoot := a.router.current.root
	a.router.mu.RUnlock()
	options.RuntimeAccess = runtimeaccess.New(httpRoot, false, runtimeaccess.Initial{
		IntentWrite: a.settings.EnableIntentionWrites, ReportImport: a.settings.EnableReportImport,
		Runner: a.settings.EnableRunner, Cache: a.settings.EnableCache,
	})
	running, err := httptransport.Start(a.version, options, profile, verifier, audit, auditKey)
	if err != nil {
		_ = audit.Close()
		return err
	}
	a.httpRunning, a.httpProfile, a.httpProfilePath, a.auditRepository = running, &profile, profilePath, audit
	return nil
}

func (a *Application) stopHTTPLocked() error {
	if a.httpRunning == nil {
		return nil
	}
	timeout := 10 * time.Second
	if a.httpProfile != nil {
		timeout = a.httpProfile.ShutdownTimeout()
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	err := a.httpRunning.Close(ctx)
	cancel()
	if a.auditRepository != nil {
		err = errors.Join(err, a.auditRepository.Close())
	}
	a.httpRunning, a.httpProfile, a.auditRepository = nil, nil, nil
	a.httpProfilePath = ""
	return err
}
