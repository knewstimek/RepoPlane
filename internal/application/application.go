// Package application composes RepoPlane services and infrastructure.
package application

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"

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
	version          string
	repository       store.Repository
	recordRepository store.RecordRepository
	catalog          *catalog.Service
	search           *search.Service
	pathFacts        *pathfacts.Service
	dataQuery        *dataquery.Service
	records          *records.Service
	runner           *runner.Service
	runtimeAccess    *runtimeaccess.Service
	memoryBackup     *memorybackup.Service
	transport        string
	httpProfile      *httptransport.Profile
	httpVerifier     auth.TokenVerifier
	auditRepository  store.AuditRepository
	auditKey         []byte
}

func Open(ctx context.Context, settings config.Settings, version string) (*Application, error) {
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
	fail := func(err error) (*Application, error) {
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
	runtimeAccess := runtimeaccess.New(root, settings.Transport != "http", runtimeaccess.Initial{
		IntentWrite: settings.EnableIntentionWrites, ReportImport: settings.EnableReportImport,
		Runner: settings.EnableRunner, Cache: settings.EnableCache,
	})
	service.EnableExecution()
	cacheKey, err := loadOrCreateKey(filepath.Join(settings.StateDir, "cache.key"))
	if err != nil {
		return fail(err)
	}
	runnerService := runner.NewService(root, service, recordRepository, repository, recordService, settings.StateDir, cacheKey)
	runnerService.SetCacheEnabled(func() bool { return runtimeAccess.Enabled(runtimeaccess.KindCache) })
	if err := runnerService.Recover(ctx); err != nil {
		return fail(err)
	}
	var httpProfile *httptransport.Profile
	var httpVerifier auth.TokenVerifier
	var auditRepository store.AuditRepository
	var auditKey []byte
	if settings.Transport == "http" {
		profile, err := httptransport.LoadProfile(settings.HTTPProfile)
		if err != nil {
			return fail(err)
		}
		verifier, err := httptransport.TokenVerifier(profile)
		if err != nil {
			return fail(err)
		}
		auditRepository, err = storesqlite.OpenAudit(ctx, filepath.Join(settings.StateDir, "audit.db"))
		if err != nil {
			return fail(err)
		}
		auditKey, err = loadOrCreateKey(filepath.Join(settings.StateDir, "audit.key"))
		if err != nil {
			_ = auditRepository.Close()
			return fail(err)
		}
		_, _ = auditRepository.DeleteExpiredAudit(ctx, time.Now().UTC().AddDate(0, 0, -profile.Audit.RetentionDays), 256)
		httpProfile, httpVerifier = &profile, verifier
	}
	memoryService := memorybackup.New(root, recordRepository, runnerService, settings.StateDir)
	return &Application{
		version: version, repository: repository, recordRepository: recordRepository, catalog: service,
		search: searchService, pathFacts: pathService, dataQuery: dataService, records: recordService,
		runner: runnerService, runtimeAccess: runtimeAccess, memoryBackup: memoryService,
		transport: settings.Transport, httpProfile: httpProfile, httpVerifier: httpVerifier,
		auditRepository: auditRepository, auditKey: auditKey,
	}, nil
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
	relative, err := filepath.Rel(rootAbsolute, absolute)
	if err != nil {
		return false, err
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}

func (a *Application) Run(ctx context.Context) error {
	if a.transport == "http" {
		return httptransport.Run(ctx, a.version, a.MCPOptions(), *a.httpProfile, a.httpVerifier, a.auditRepository, a.auditKey)
	}
	return mcpserver.RunStdio(ctx, a.version, a.MCPOptions())
}

func (a *Application) MCPOptions() mcpserver.Options {
	options := mcpserver.Options{
		Catalog: a.catalog, Search: a.search, PathFacts: a.pathFacts, DataQuery: a.dataQuery,
		Records: a.records, CheckpointWriter: a.records, MemoWriter: a.records, ReportImporter: a.records,
		Runner: a.runner, RuntimeAccess: a.runtimeAccess, MemoryBackup: a.memoryBackup,
	}
	return options
}

func (a *Application) ExportMemory(ctx context.Context, request memorybackup.Request) (memorybackup.Response, error) {
	return a.memoryBackup.Export(ctx, request)
}

func (a *Application) RestoreMemory(ctx context.Context, archive string, byteLimit uint64) (memorybackup.Response, error) {
	return a.memoryBackup.Restore(ctx, archive, byteLimit)
}

func (a *Application) Close() error {
	var runnerErr error
	if a.runner != nil {
		runnerErr = a.runner.Close()
	}
	var auditErr error
	if a.auditRepository != nil {
		auditErr = a.auditRepository.Close()
	}
	return errors.Join(runnerErr, auditErr, a.repository.Close(), a.recordRepository.Close())
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
