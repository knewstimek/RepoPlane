// Package runner implements RepoPlane's optional registered-capability execution layer.
package runner

import (
	"time"

	"repoplane/internal/contracts"
)

const (
	DefaultStreamByteLimit   = uint64(8 * 1024 * 1024)
	DefaultArtifactByteLimit = uint64(64 * 1024 * 1024)
	DefaultRunArtifactLimit  = uint64(512 * 1024 * 1024)
)

type PrepareRequest struct {
	CapabilityID       string         `json:"capability_id" jsonschema:"registered catalog capability ID"`
	CapabilityRevision string         `json:"capability_revision" jsonschema:"selected catalog capability revision"`
	Arguments          map[string]any `json:"arguments,omitempty" jsonschema:"typed values declared by the capability argument schema"`
	Configuration      string         `json:"configuration,omitempty" jsonschema:"bounded execution configuration name; default: default"`
	CacheMode          string         `json:"cache_mode,omitempty" jsonschema:"cache lookup mode: auto or bypass; default: auto"`
	TimeLimitMS        int64          `json:"time_limit_ms,omitempty" jsonschema:"deadline ms; default 5000, max 30000"`
}

type CacheDecision struct {
	Policy            string   `json:"policy"`
	Mode              string   `json:"mode"`
	Status            string   `json:"status"`
	Eligible          bool     `json:"eligible"`
	Reason            string   `json:"reason"`
	Key               string   `json:"key,omitempty"`
	SourceRunRef      string   `json:"source_run_ref,omitempty"`
	QualificationRefs []string `json:"qualification_refs"`
}

type PreflightResult struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Requirement string `json:"requirement"`
	Status      string `json:"status"`
	Summary     string `json:"summary"`
	Identity    string `json:"identity,omitempty"`
}

type PlanResult struct {
	ID                   string            `json:"id"`
	CapabilityID         string            `json:"capability_id"`
	CapabilityRevision   string            `json:"capability_revision"`
	Configuration        string            `json:"configuration"`
	Argv                 []string          `json:"argv"`
	CWD                  string            `json:"cwd"`
	ExecutableIdentity   string            `json:"executable_identity"`
	InputHashes          map[string]string `json:"input_hashes,omitempty"`
	InputCount           int               `json:"input_count"`
	OutputPaths          []string          `json:"output_paths"`
	PreflightRecordRef   string            `json:"preflight_record_ref"`
	Ready                bool              `json:"ready"`
	TimeoutSec           uint64            `json:"timeout_sec"`
	PreparedAt           time.Time         `json:"prepared_at"`
	ExecutionFingerprint string            `json:"execution_fingerprint"`
	Cache                CacheDecision     `json:"cache"`
}

type PrepareResponse struct {
	Status   contracts.Status    `json:"status"`
	Plan     PlanResult          `json:"plan"`
	Checks   []PreflightResult   `json:"checks"`
	Warnings []contracts.Warning `json:"warnings"`
}

type ExecuteRequest struct {
	PlanID      string `json:"plan_id" jsonschema:"prepared run plan ID"`
	TimeLimitMS int64  `json:"time_limit_ms,omitempty" jsonschema:"start deadline ms; default 5000, max 30000"`
}

type ExecuteResponse struct {
	Status   contracts.Status    `json:"status"`
	RunID    string              `json:"run_id"`
	State    string              `json:"state"`
	Warnings []contracts.Warning `json:"warnings"`
}

type InspectRequest struct {
	RunID        string `json:"run_id" jsonschema:"prepared or started run ID"`
	Action       string `json:"action,omitempty" jsonschema:"status, detail, stdout, stderr, artifact, cancel; default status"`
	ArtifactRef  string `json:"artifact_ref,omitempty" jsonschema:"artifact record ref required by the artifact action"`
	ResponseView string `json:"response_view,omitempty" jsonschema:"ref (default) omits content; bytes returns raw pages or full receipt"`
	Offset       uint64 `json:"offset,omitempty" jsonschema:"zero-based stream byte offset"`
	ByteLimit    uint64 `json:"byte_limit,omitempty" jsonschema:"stream bytes; default 65536, max 1048576"`
	TimeLimitMS  int64  `json:"time_limit_ms,omitempty" jsonschema:"deadline ms; default 5000, max 30000"`
}

type artifactPayload struct {
	RunRef            string `json:"run_ref"`
	Path              string `json:"path"`
	ContentHash       string `json:"content_hash"`
	Size              int64  `json:"size"`
	Storage           string `json:"storage"`
	Basis             string `json:"basis"`
	WriterAttribution string `json:"writer_attribution"`
}

type StreamResult struct {
	Ref          string `json:"ref"`
	FileRef      string `json:"file_ref,omitempty"`
	SizeBytes    uint64 `json:"size_bytes"`
	Offset       uint64 `json:"offset"`
	NextOffset   uint64 `json:"next_offset"`
	BytesBase64  string `json:"bytes_base64,omitempty"`
	EOF          bool   `json:"eof"`
	Truncated    bool   `json:"truncated"`
	Availability string `json:"availability"`
}

type InspectResponse struct {
	Status   contracts.Status    `json:"status"`
	Run      map[string]any      `json:"run"`
	Stream   *StreamResult       `json:"stream,omitempty"`
	Warnings []contracts.Warning `json:"warnings"`
}

type runPayload struct {
	State                string            `json:"state"`
	CapabilityID         string            `json:"capability_id"`
	CapabilityRevision   string            `json:"capability_revision"`
	Configuration        string            `json:"configuration"`
	Arguments            map[string]any    `json:"arguments"`
	Argv                 []string          `json:"argv"`
	CWD                  string            `json:"cwd"`
	ExecutableRef        string            `json:"executable_ref"`
	ExecutableIdentity   string            `json:"executable_identity"`
	ExecutionFingerprint string            `json:"execution_fingerprint"`
	Cache                CacheDecision     `json:"cache"`
	CacheKeyChecks       map[string]string `json:"cache_key_checks"`
	InputHashes          map[string]string `json:"input_hashes"`
	OutputPaths          []string          `json:"output_paths"`
	OutputsBefore        map[string]string `json:"outputs_before"`
	OutputsAfter         map[string]string `json:"outputs_after"`
	PreflightRecordRef   string            `json:"preflight_record_ref"`
	Ready                bool              `json:"ready"`
	TimeoutSec           uint64            `json:"timeout_sec"`
	ArtifactMode         string            `json:"artifact_mode"`
	PreparedAt           time.Time         `json:"prepared_at"`
	StartedAt            *time.Time        `json:"started_at"`
	FinishedAt           *time.Time        `json:"finished_at"`
	ExitCode             *int              `json:"exit_code"`
	TerminationReason    string            `json:"termination_reason"`
	StdoutBytes          uint64            `json:"stdout_bytes"`
	StderrBytes          uint64            `json:"stderr_bytes"`
	StdoutTruncated      bool              `json:"stdout_truncated"`
	StderrTruncated      bool              `json:"stderr_truncated"`
	ArtifactRefs         []string          `json:"artifact_refs"`
	ObservationPartial   bool              `json:"observation_partial"`
	StreamsRetained      bool              `json:"streams_retained"`
}
