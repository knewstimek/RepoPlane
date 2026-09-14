package application

import (
	"context"
	"errors"
	"sync"

	"repoplane/internal/catalog"
	"repoplane/internal/dataquery"
	"repoplane/internal/memorybackup"
	"repoplane/internal/pathfacts"
	"repoplane/internal/records"
	"repoplane/internal/runner"
	"repoplane/internal/search"
	"repoplane/internal/store"
	"repoplane/internal/workspace"
)

type serviceBundle struct {
	root             *workspace.Root
	repository       store.Repository
	recordRepository store.RecordRepository
	catalog          *catalog.Service
	search           *search.Service
	pathFacts        *pathfacts.Service
	dataQuery        *dataquery.Service
	records          *records.Service
	runner           *runner.Service
	memoryBackup     *memorybackup.Service
}

func (b *serviceBundle) close() error {
	return errors.Join(b.runner.Close(), b.repository.Close(), b.recordRepository.Close())
}

type serviceRouter struct {
	mu      sync.RWMutex
	current *serviceBundle
}

func (r *serviceRouter) swap(next *serviceBundle) *serviceBundle {
	r.mu.Lock()
	previous := r.current
	r.current = next
	r.mu.Unlock()
	return previous
}

func (r *serviceRouter) Query(ctx context.Context, request catalog.QueryRequest) (catalog.QueryResponse, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current.catalog.Query(ctx, request)
}
func (r *serviceRouter) SearchQuery(ctx context.Context, request search.Request) (search.Response, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current.search.Query(ctx, request)
}
func (r *serviceRouter) Explain(ctx context.Context, request pathfacts.Request) (pathfacts.Response, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current.pathFacts.Explain(ctx, request)
}
func (r *serviceRouter) DataQuery(ctx context.Context, request dataquery.Request) (dataquery.Response, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current.dataQuery.Query(ctx, request)
}
func (r *serviceRouter) RecordsQuery(ctx context.Context, request records.QueryRequest) (records.QueryResponse, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current.records.Query(ctx, request)
}
func (r *serviceRouter) WriteCheckpoint(ctx context.Context, request records.CheckpointRequest) (records.MutationResponse, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current.records.WriteCheckpoint(ctx, request)
}
func (r *serviceRouter) WriteMemo(ctx context.Context, request records.MemoRequest) (records.MutationResponse, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current.records.WriteMemo(ctx, request)
}
func (r *serviceRouter) ImportReport(ctx context.Context, request records.ImportRequest) (records.MutationResponse, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current.records.ImportReport(ctx, request)
}
func (r *serviceRouter) Prepare(ctx context.Context, request runner.PrepareRequest) (runner.PrepareResponse, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current.runner.Prepare(ctx, request)
}
func (r *serviceRouter) Execute(ctx context.Context, request runner.ExecuteRequest) (runner.ExecuteResponse, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current.runner.Execute(ctx, request)
}
func (r *serviceRouter) Inspect(ctx context.Context, request runner.InspectRequest) (runner.InspectResponse, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current.runner.Inspect(ctx, request)
}
func (r *serviceRouter) Export(ctx context.Context, request memorybackup.Request) (memorybackup.Response, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current.memoryBackup.Export(ctx, request)
}
func (r *serviceRouter) Restore(ctx context.Context, archive string, byteLimit uint64) (memorybackup.Response, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current.memoryBackup.Restore(ctx, archive, byteLimit)
}

// Adapter methods avoid ambiguous Query method names while satisfying the
// small interfaces consumed by the MCP server.
type searchRoute struct{ router *serviceRouter }

func (r searchRoute) Query(ctx context.Context, request search.Request) (search.Response, error) {
	return r.router.SearchQuery(ctx, request)
}

type dataRoute struct{ router *serviceRouter }

func (r dataRoute) Query(ctx context.Context, request dataquery.Request) (dataquery.Response, error) {
	return r.router.DataQuery(ctx, request)
}

type recordsRoute struct{ router *serviceRouter }

func (r recordsRoute) Query(ctx context.Context, request records.QueryRequest) (records.QueryResponse, error) {
	return r.router.RecordsQuery(ctx, request)
}
