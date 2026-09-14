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

// RecordRepository is the complete durable-record adapter. Feature services
// depend on the narrower interfaces below; this aggregate is only for wiring.
type RecordRepository interface {
	RecordReader
	CheckpointWriter
	MemoWriter
	ReportImporter
	ObservationWriter
	Close() error
}

type RecordReader interface {
	GetRecord(ctx context.Context, projectID, workspaceID, id string) (Record, error)
	QueryRecords(ctx context.Context, query RecordQuery) (RecordPage, error)
}

type CheckpointWriter interface {
	CreateCheckpoint(ctx context.Context, create RecordCreate) (Record, error)
	UpdateCheckpoint(ctx context.Context, update RecordUpdate) (Record, error)
}

type MemoWriter interface {
	CreateMemo(ctx context.Context, create RecordCreate) (Record, error)
	UpdateMemo(ctx context.Context, update RecordUpdate) (Record, error)
}

type ReportImporter interface {
	ImportVerification(ctx context.Context, create RecordCreate, sourceHash, parserRevision string) (record Record, duplicate bool, err error)
}

// ObservationWriter persists server-observed environment, run, and artifact
// facts. It is deliberately separate from user intention writers.
type ObservationWriter interface {
	CreateObservation(ctx context.Context, create RecordCreate) (Record, error)
	UpdateObservation(ctx context.Context, kind string, update RecordUpdate) (Record, error)
}

type Record struct {
	ID            string
	Kind          string
	SchemaVersion string
	ProjectID     string
	WorkspaceID   string
	Revision      uint64
	Source        string
	WriterClass   string
	Validity      string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Payload       json.RawMessage
	EvidenceRefs  []string
	Supersedes    string
}

type RecordCreate struct {
	Record Record
}

type RecordUpdate struct {
	ProjectID        string
	WorkspaceID      string
	ID               string
	ExpectedRevision uint64
	Payload          json.RawMessage
	EvidenceRefs     []string
	Validity         string
	Supersedes       string
}

type RecordQuery struct {
	ProjectID    string
	WorkspaceID  string
	Kind         string
	Validity     string
	Source       string
	UpdatedAfter time.Time
	Limit        uint64
}

type RecordPage struct {
	Records  []Record
	Matched  uint64
	Complete bool
}

// Repository is the complete persistence dependency used by RepoPlane's
// read-only MVP. Feature services should depend on the narrower embedded
// interfaces whenever possible.
type Repository interface {
	WorkspaceRepository
	CatalogRepository
	ResultSetRepository
	CacheRepository
	Close() error
}

// CacheRepository stores regenerable reuse decisions. Durable run and artifact
// evidence remains in RecordRepository; deleting this index only causes misses.
type CacheRepository interface {
	GetCacheEntry(ctx context.Context, projectID, workspaceID, key string) (CacheEntry, error)
	PublishCacheObservation(ctx context.Context, entry CacheEntry) (CacheEntry, error)
	MarkCacheHit(ctx context.Context, projectID, workspaceID, key string, usedAt time.Time) (CacheEntry, error)
	QuarantineCacheEntry(ctx context.Context, projectID, workspaceID, key, reason string, at time.Time) error
	ListProtectedCacheHashes(ctx context.Context, projectID, workspaceID string, now time.Time, limit uint64) ([]string, bool, error)
	DeleteExpiredCacheEntries(ctx context.Context, projectID, workspaceID string, now time.Time, limit uint64) (uint64, error)
}

type CacheOutput struct {
	Path        string `json:"path"`
	ContentHash string `json:"content_hash"`
	Size        int64  `json:"size"`
	ArtifactRef string `json:"artifact_ref"`
}

type CacheEntry struct {
	Key                string
	ProjectID          string
	WorkspaceID        string
	CapabilityID       string
	CapabilityRevision string
	Configuration      string
	State              string
	Reason             string
	SourceRunRef       string
	Outputs            []CacheOutput
	QualificationRefs  []string
	CreatedAt          time.Time
	ObservedAt         time.Time
	LastUsedAt         time.Time
	ExpiresAt          time.Time
	ObservationCount   uint64
	HitCount           uint64
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
