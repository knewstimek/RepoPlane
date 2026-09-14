package catalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"repoplane/internal/store"
	storesqlite "repoplane/internal/store/sqlite"
	"repoplane/internal/workspace"
)

func TestIndexerBuildsDeterministicGenerationAndPreservesIssues(t *testing.T) {
	rootPath := t.TempDir()
	catalogPath := filepath.Join(rootPath, "catalog")
	if err := os.Mkdir(catalogPath, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(catalogPath, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("valid.yaml", "id: valid.tool\nrevision: 1\nsummary: valid tool\ntags: [test]\n")
	write("duplicate-a.yaml", "id: duplicate.tool\nrevision: 1\nsummary: duplicate a\n")
	write("duplicate-b.json", `{"id":"duplicate.tool","revision":1,"summary":"duplicate b"}`)
	write("invalid.yaml", "id: INVALID\nrevision: 1\nsummary: invalid\n")
	write("future.toml", "id = 'future.tool'\n")

	root, err := workspace.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	now := time.Unix(2_000_000_000, 0).UTC()
	if err := repository.UpsertWorkspace(context.Background(), store.Workspace{
		ID: root.ID(), RootFingerprint: root.ID(), CreatedAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	indexer := NewIndexer(root, repository, []string{"catalog"})
	indexer.now = func() time.Time { return now }

	first, err := indexer.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := indexer.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Meta.ID != second.Meta.ID || first.Meta.SourceFingerprint != second.Meta.SourceFingerprint {
		t.Fatalf("generation is not deterministic: %+v %+v", first.Meta, second.Meta)
	}
	if len(first.Items) != 1 || first.Items[0].ID != "valid.tool" {
		t.Fatalf("unexpected indexed items: %+v", first.Items)
	}
	if len(first.Issues) != 4 {
		t.Fatalf("issues=%d, want invalid, unsupported, and two duplicate declarations", len(first.Issues))
	}

	meta, err := indexer.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	metaAgain, err := indexer.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != metaAgain.ID {
		t.Fatalf("unchanged refresh changed generation: %q != %q", meta.ID, metaAgain.ID)
	}
	issues, err := repository.ListCatalogIssues(context.Background(), root.ID(), meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 4 {
		t.Fatalf("stored issues=%d, want 4", len(issues))
	}
}

func TestIndexerMissingDefaultRootProducesEmptyCatalog(t *testing.T) {
	root, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repository := &catalogStoreStub{}
	indexer := NewIndexer(root, repository, []string{"catalog"})
	generation, err := indexer.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(generation.Items) != 0 || len(generation.Issues) != 0 {
		t.Fatalf("empty catalog returned data: %+v", generation)
	}
}

func TestIndexerAuditsExecutionCandidates(t *testing.T) {
	rootPath := t.TempDir()
	for _, directory := range []string{"catalog", "tools", "scripts"} {
		if err := os.Mkdir(filepath.Join(rootPath, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(rootPath, "tools", "registered.py"), []byte("print('ok')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "scripts", "unregistered.ps1"), []byte("Write-Output ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := `id: registered.tool
revision: 1
summary: registered tool
execution:
  kind: cli
  executable_ref: tools/registered.py
  cwd: repository
  argv_template: []
  trusted_for_run: false
`
	if err := os.WriteFile(filepath.Join(rootPath, "catalog", "registered.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := `id: missing.tool
revision: 1
summary: missing tool
execution:
  kind: cli
  executable_ref: tools/missing.py
  cwd: repository
  argv_template: []
  trusted_for_run: false
`
	if err := os.WriteFile(filepath.Join(rootPath, "catalog", "missing.yaml"), []byte(missing), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := workspace.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := NewIndexer(root, &catalogStoreStub{}, []string{"catalog"}, []string{"tools", "scripts"}).Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	codes := make(map[string]int)
	for _, issue := range generation.Issues {
		codes[issue.Code]++
	}
	if codes["missing_source"] != 1 || codes["unregistered_candidate"] != 1 {
		t.Fatalf("unexpected audit issues: %+v", generation.Issues)
	}
}

func TestIndexerFlagsExecutableChangeWithoutRevisionChange(t *testing.T) {
	ctx := context.Background()
	rootPath := t.TempDir()
	for _, directory := range []string{"catalog", "tools"} {
		if err := os.Mkdir(filepath.Join(rootPath, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(rootPath, "tools", "worker.py")
	if err := os.WriteFile(executable, []byte("print('first')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := "id: worker.tool\nrevision: 1\nsummary: worker\nexecution:\n  kind: cli\n  executable_ref: tools/worker.py\n  cwd: repository\n  argv_template: []\n  trusted_for_run: false\n"
	if err := os.WriteFile(filepath.Join(rootPath, "catalog", "worker.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := workspace.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := storesqlite.Open(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	now := time.Unix(2_000_000_000, 0).UTC()
	if err := repository.UpsertWorkspace(ctx, store.Workspace{ID: root.ID(), RootFingerprint: root.ID(), CreatedAt: now, LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	indexer := NewIndexer(root, repository, []string{"catalog"}, []string{"tools"})
	if _, err := indexer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("print('changed')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := indexer.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	issues, err := repository.ListCatalogIssues(ctx, root.ID(), meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, issue := range issues {
		if issue.Code == "needs_review" {
			found = true
		}
	}
	if !found {
		t.Fatalf("needs_review not found in %+v", issues)
	}
}

type catalogStoreStub struct{}

func (*catalogStoreStub) PublishCatalogGeneration(context.Context, store.CatalogGeneration) error {
	return nil
}
func (*catalogStoreStub) CurrentCatalogGeneration(context.Context, string) (store.CatalogGenerationMeta, error) {
	return store.CatalogGenerationMeta{}, store.ErrNotFound
}
func (*catalogStoreStub) GetCatalogItem(context.Context, string, string, string) (store.CatalogItem, error) {
	return store.CatalogItem{}, store.ErrNotFound
}
func (*catalogStoreStub) ListCatalogIssues(context.Context, string, string) ([]store.CatalogIssue, error) {
	return nil, nil
}
func (*catalogStoreStub) SearchCatalog(context.Context, store.CatalogQuery) (store.CatalogPage, error) {
	return store.CatalogPage{}, errors.New("not implemented")
}
