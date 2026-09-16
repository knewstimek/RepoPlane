// Package application composes RepoPlane services and infrastructure.
package application

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"repoplane/internal/catalog"
	"repoplane/internal/config"
	"repoplane/internal/cursor"
	"repoplane/internal/dataquery"
	"repoplane/internal/httptransport"
	"repoplane/internal/mcpserver"
	"repoplane/internal/memorybackup"
	"repoplane/internal/pathfacts"
	"repoplane/internal/records"
	"repoplane/internal/runner"
	"repoplane/internal/runtimeaccess"
	"repoplane/internal/search"
	"repoplane/internal/store"
	storesqlite "repoplane/internal/store/sqlite"
	"repoplane/internal/workspace"
)

const cursorKeyBytes = 32

type Application struct {
	version         string
	mu              sync.Mutex
	settings        config.Settings
	router          *serviceRouter
	runtimeAccess   *runtimeaccess.Service
	httpRunning     *httptransport.Running
	httpProfile     *httptransport.Profile
	httpProfilePath string
	auditRepository store.AuditRepository
}

func Open(ctx context.Context, settings config.Settings, version string) (*Application, error) {
	if settings.ToolSurface == "" {
		settings.ToolSurface = mcpserver.SurfaceTypedV1
	}
	if settings.ToolSurface != mcpserver.SurfaceTypedV1 && settings.ToolSurface != mcpserver.SurfaceToolboxV1 {
		return nil, errors.New("unsupported MCP tool surface")
	}
	if settings.Transport == "http" {
		profile, err := httptransport.LoadProfile(settings.HTTPProfile)
		if err != nil {
			return nil, err
		}
		if _, err := httptransport.TokenVerifier(profile); err != nil {
			return nil, err
		}
	}
	var runtimeAccess *runtimeaccess.Service
	bundle, err := buildBundle(ctx, settings, func() bool {
		return runtimeAccess != nil && runtimeAccess.Enabled(runtimeaccess.KindCache)
	})
	if err != nil {
		return nil, err
	}
	settings.Workspace = bundle.root.Resolved()
	settings.StateDir, _ = filepath.Abs(settings.StateDir)
	runtimeAccess = runtimeaccess.New(bundle.root, settings.Transport != "http", runtimeaccess.Initial{
		IntentWrite: settings.EnableIntentionWrites, ReportImport: settings.EnableReportImport,
		Runner: settings.EnableRunner, Cache: settings.EnableCache,
	})
	return &Application{version: version, settings: settings, router: &serviceRouter{current: bundle}, runtimeAccess: runtimeAccess}, nil
}

func buildBundle(ctx context.Context, settings config.Settings, cacheEnabled func() bool) (*serviceBundle, error) {
	root, err := workspace.Open(settings.Workspace)
	if err != nil {
		return nil, err
	}
	if inside, err := pathInside(root.Resolved(), settings.StateDir); err != nil {
		return nil, fmt.Errorf("validate state directory: %w", err)
	} else if inside {
		return nil, errors.New("state directory must be outside the workspace")
	}
	if err := os.MkdirAll(settings.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	resolvedState, err := filepath.EvalSymlinks(settings.StateDir)
	if err != nil {
		return nil, fmt.Errorf("resolve state directory: %w", err)
	}
	if inside, err := pathInside(root.Resolved(), resolvedState); err != nil {
		return nil, fmt.Errorf("validate resolved state directory: %w", err)
	} else if inside {
		return nil, errors.New("resolved state directory must be outside the workspace")
	}
	repository, err := storesqlite.Open(ctx, filepath.Join(settings.StateDir, "repoplane.db"))
	if err != nil {
		return nil, err
	}
	recordRepository, err := storesqlite.OpenRecords(ctx, filepath.Join(settings.StateDir, "records.db"))
	if err != nil {
		_ = repository.Close()
		return nil, err
	}
	fail := func(err error) (*serviceBundle, error) {
		_ = repository.Close()
		_ = recordRepository.Close()
		return nil, err
	}
	now := time.Now().UTC()
	if err := repository.UpsertWorkspace(ctx, store.Workspace{
		ID: root.ID(), RootFingerprint: root.ID(), CreatedAt: now, LastSeenAt: now,
	}); err != nil {
		return fail(err)
	}
	key, err := loadOrCreateKey(filepath.Join(settings.StateDir, "cursor.key"))
	if err != nil {
		return fail(err)
	}
	codec, err := cursor.NewCodec(key)
	if err != nil {
		return fail(err)
	}
	indexer := catalog.NewIndexer(root, repository, settings.CatalogRoots, settings.CandidateRoots)
	service := catalog.NewService(root.ID(), repository, indexer, codec)
	if _, err := indexer.Refresh(ctx); err != nil {
		return fail(fmt.Errorf("initial catalog refresh: %w", err))
	}
	searchBackend, err := search.NewRGBackend(ctx)
	if err != nil {
		return fail(err)
	}
	adapterBackend := search.NewAdapterBackend(ctx, searchBackend, root.Resolved(), settings.SymbolIndexes)
	searchService := search.NewService(root, repository, codec, adapterBackend)
	pathService := pathfacts.NewService(root, searchBackend, settings.RuleFiles)
	dataService := dataquery.NewService(root, repository, codec)
	recordService := records.NewService(root, recordRepository, repository, codec)
	service.EnableExecution()
	cacheKey, err := loadOrCreateKey(filepath.Join(settings.StateDir, "cache.key"))
	if err != nil {
		return fail(err)
	}
	runnerService := runner.NewService(root, service, recordRepository, repository, recordService, settings.StateDir, cacheKey)
	if cacheEnabled == nil {
		cacheEnabled = func() bool { return false }
	}
	runnerService.SetCacheEnabled(cacheEnabled)
	if err := runnerService.Recover(ctx); err != nil {
		return fail(err)
	}
	memoryService := memorybackup.New(root, recordRepository, runnerService, settings.StateDir)
	return &serviceBundle{root: root, repository: repository, recordRepository: recordRepository, catalog: service,
		search: searchService, pathFacts: pathService, dataQuery: dataService, records: recordService,
		runner: runnerService, memoryBackup: memoryService}, nil
}

func pathInside(root, candidate string) (bool, error) {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return false, err
	}
	rootVolume := filepath.VolumeName(rootAbsolute)
	candidateVolume := filepath.VolumeName(absolute)
	if !strings.EqualFold(rootVolume, candidateVolume) {
		return false, nil
	}
	if runtime.GOOS == "windows" {
		rootAbsolute, absolute = strings.ToLower(rootAbsolute), strings.ToLower(absolute)
	}
	relative, err := filepath.Rel(rootAbsolute, absolute)
	if err != nil {
		return false, err
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}

func (a *Application) Run(ctx context.Context) error {
	if a.settings.Transport == "http" {
		a.mu.Lock()
		err := a.startHTTPLocked(ctx, a.settings.HTTPProfile)
		running := a.httpRunning
		a.mu.Unlock()
		if err != nil {
			return err
		}
		select {
		case err := <-running.Done():
			return err
		case <-ctx.Done():
			a.mu.Lock()
			err = a.stopHTTPLocked()
			a.mu.Unlock()
			return err
		}
	}
	return mcpserver.RunStdio(ctx, a.version, a.MCPOptions())
}

func (a *Application) MCPOptions() mcpserver.Options {
	options := mcpserver.Options{
		Surface: a.settings.ToolSurface,
		Catalog: a.router, Search: searchRoute{a.router}, PathFacts: a.router, DataQuery: dataRoute{a.router},
		Records: recordsRoute{a.router}, CheckpointWriter: a.router, MemoWriter: a.router, ReportImporter: a.router,
		Runner: a.router, RuntimeAccess: a.runtimeAccess, RuntimeConfig: a, MemoryBackup: a.router,
	}
	return options
}

func (a *Application) ExportMemory(ctx context.Context, request memorybackup.Request) (memorybackup.Response, error) {
	return a.router.Export(ctx, request)
}

func (a *Application) RestoreMemory(ctx context.Context, archive string, byteLimit uint64) (memorybackup.Response, error) {
	return a.router.Restore(ctx, archive, byteLimit)
}

func (a *Application) Close() error {
	a.mu.Lock()
	httpErr := a.stopHTTPLocked()
	a.mu.Unlock()
	a.router.mu.Lock()
	bundle := a.router.current
	a.router.current = nil
	a.router.mu.Unlock()
	if bundle == nil {
		return httpErr
	}
	return errors.Join(httpErr, bundle.close())
}

func loadOrCreateKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != cursorKeyBytes {
			return nil, errors.New("cursor key has invalid length")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read cursor key: %w", err)
	}

	key = make([]byte, cursorKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate cursor key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return loadOrCreateKey(path)
	}
	if err != nil {
		return nil, fmt.Errorf("create cursor key: %w", err)
	}
	if _, err := file.Write(key); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write cursor key: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sync cursor key: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close cursor key: %w", err)
	}
	return key, nil
}
