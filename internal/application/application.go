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

	"repoplane/internal/catalog"
	"repoplane/internal/config"
	"repoplane/internal/cursor"
	"repoplane/internal/dataquery"
	"repoplane/internal/mcpserver"
	"repoplane/internal/pathfacts"
	"repoplane/internal/search"
	"repoplane/internal/store"
	storesqlite "repoplane/internal/store/sqlite"
	"repoplane/internal/workspace"
)

const cursorKeyBytes = 32

type Application struct {
	version    string
	repository store.Repository
	catalog    *catalog.Service
	search     *search.Service
	pathFacts  *pathfacts.Service
	dataQuery  *dataquery.Service
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
	fail := func(err error) (*Application, error) {
		_ = repository.Close()
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
	searchService := search.NewService(root, repository, codec, searchBackend)
	pathService := pathfacts.NewService(root, searchBackend, settings.RuleFiles)
	dataService := dataquery.NewService(root, repository, codec)
	return &Application{
		version: version, repository: repository, catalog: service,
		search: searchService, pathFacts: pathService, dataQuery: dataService,
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
	return mcpserver.RunStdio(ctx, a.version, a.MCPOptions())
}

func (a *Application) MCPOptions() mcpserver.Options {
	return mcpserver.Options{
		Catalog: a.catalog, Search: a.search, PathFacts: a.pathFacts, DataQuery: a.dataQuery,
	}
}

func (a *Application) Close() error { return a.repository.Close() }

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
