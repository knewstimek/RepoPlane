// Package contracts contains transport-independent RepoPlane API contracts.
package contracts

// Status describes whether the requested operation itself succeeded.
type Status string

const (
	StatusOK          Status = "ok"
	StatusPartial     Status = "partial"
	StatusError       Status = "error"
	StatusUnsupported Status = "unsupported"
)

// CountRelation describes how matched should be interpreted.
type CountRelation string

const (
	CountExact         CountRelation = "exact"
	CountLowerBound    CountRelation = "lower_bound"
	CountUnknown       CountRelation = "unknown"
	CountNotApplicable CountRelation = "not_applicable"
)

// ScanState describes whether the declared scan scope was exhausted.
type ScanState string

const (
	ScanComplete      ScanState = "complete"
	ScanPartial       ScanState = "partial"
	ScanNotApplicable ScanState = "not_applicable"
)

// Basis identifies where a returned fact came from.
type Basis string

const (
	BasisDeclared    Basis = "declared"
	BasisObserved    Basis = "observed"
	BasisDerived     Basis = "derived"
	BasisHeuristic   Basis = "heuristic"
	BasisLLMProposed Basis = "llm_proposed"
)

// Validity relates recorded conditions to the current workspace state.
type Validity string

const (
	ValidityCurrent Validity = "current"
	ValidityStale   Validity = "stale"
	ValidityUnknown Validity = "unknown"
)

// Counts reports matched and returned item counts without inventing totals.
// Matched is nil when Relation is unknown or not_applicable.
type Counts struct {
	Matched  *uint64       `json:"matched"`
	Relation CountRelation `json:"relation"`
	Returned uint64        `json:"returned"`
}

// Scan reports coverage of the declared search scope.
type Scan struct {
	State    ScanState `json:"state"`
	ScopeRef *string   `json:"scope_ref"`
}

// Warning is a stable machine-readable warning with optional evidence.
type Warning struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Ref     *string `json:"ref"`
}

// Response is the shared envelope for list and search operations. Items must
// serialize as an array, including when empty. Truncated is nil when unknown.
type Response[T any] struct {
	Status      Status    `json:"status"`
	Items       []T       `json:"items"`
	Counts      Counts    `json:"counts"`
	Scan        Scan      `json:"scan"`
	Truncated   *bool     `json:"truncated"`
	NextCursor  *string   `json:"next_cursor"`
	SnapshotRef *string   `json:"snapshot_ref"`
	Warnings    []Warning `json:"warnings"`
}

// EmptyItems normalizes nil slices required to serialize as JSON arrays.
func EmptyItems[T any]() []T { return make([]T, 0) }

// EmptyWarnings normalizes nil warnings required to serialize as JSON arrays.
func EmptyWarnings() []Warning { return make([]Warning, 0) }
