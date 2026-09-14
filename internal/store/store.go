// Package store defines database-independent persistence contracts.
//
// Interfaces describe RepoPlane domain operations and their atomicity instead
// of exposing SQL transactions or generic CRUD. Database adapters live in
// subpackages such as store/sqlite and must pass the same conformance suite.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotFound          = errors.New("store: not found")
	ErrConflict          = errors.New("store: revision conflict")
	ErrGenerationChanged = errors.New("store: generation changed")
)

// Repository is the complete persistence dependency used by RepoPlane's
// read-only MVP. Feature services should depend on the narrower embedded
// interfaces whenever possible.
type Repository interface {
	WorkspaceRepository
	CatalogRepository
	ResultSetRepository
	Close() error
}

// WorkspaceRepository stores opaque workspace identities. RootFingerprint is
// adapter data and must never be treated as an authorization decision.
type WorkspaceRepository interface {
	UpsertWorkspace(ctx context.Context, workspace Workspace) error
	GetWorkspace(ctx context.Context, id string) (Workspace, error)
}

// CatalogRepository publishes immutable catalog generations. Replacing a
// generation is atomic: readers see either the previous complete generation or
// the new complete generation, never an intermediate mix.
type CatalogRepository interface {
	PublishCatalogGeneration(ctx context.Context, generation CatalogGeneration) error
	CurrentCatalogGeneration(ctx context.Context, workspaceID string) (CatalogGenerationMeta, error)
	GetCatalogItem(ctx context.Context, workspaceID, generationID, itemID string) (CatalogItem, error)
	ListCatalogIssues(ctx context.Context, workspaceID, generationID string) ([]CatalogIssue, error)
	SearchCatalog(ctx context.Context, query CatalogQuery) (CatalogPage, error)
}

// ResultSetRepository persists the ordered IDs and observed hashes produced by
// one bounded query. Pagination reads this fixed sequence instead of rerunning
// the mutable query.
type ResultSetRepository interface {
	CreateResultSet(ctx context.Context, set ResultSet) error
	ReadResultPage(ctx context.Context, id string, from uint64, limit uint64) (ResultPage, error)
	DeleteExpiredResultSets(ctx context.Context, now time.Time, limit uint64) (uint64, error)
}

type Workspace struct {
	ID              string
	RootFingerprint string
	CreatedAt       time.Time
	LastSeenAt      time.Time
}

type CatalogGenerationMeta struct {
	ID                string
	WorkspaceID       string
	SourceFingerprint string
	CreatedAt         time.Time
}

type CatalogGeneration struct {
	Meta   CatalogGenerationMeta
	Items  []CatalogItem
	Issues []CatalogIssue
}

type CatalogItem struct {
	ID                   string
	Revision             string
	SourceRef            string
	ExecutionFingerprint string
	Document             json.RawMessage
	Terms                []CatalogTerm
}

type CatalogTerm struct {
	Field  string
	Term   string
	Weight int
}

type CatalogIssue struct {
	Code      string
	SourceRef string
	Detail    json.RawMessage
}

// CatalogQuery carries a fully normalized query. Adapters must apply the
// ordering contract: exact ID first, then Score descending, then ID ascending.
type CatalogQuery struct {
	WorkspaceID  string
	GenerationID string
	ExactID      string
	Terms        []string
	Limit        uint64
}

type CatalogMatch struct {
	Item  CatalogItem
	Score int64
}

type CatalogPage struct {
	GenerationID string
	Matches      []CatalogMatch
	Matched      *uint64
	Complete     bool
}

type ResultSet struct {
	ID           string
	WorkspaceID  string
	QueryHash    string
	GenerationID string
	CreatedAt    time.Time
	ExpiresAt    time.Time
	Metadata     json.RawMessage
	Items        []ResultItem
}

type ResultItem struct {
	Ordinal  uint64
	ItemRef  string
	ItemHash string
	Payload  json.RawMessage
}

type ResultPage struct {
	ResultSetID string
	Items       []ResultItem
	Total       uint64
	Metadata    json.RawMessage
	NextOrdinal *uint64
}
