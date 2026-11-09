package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"repoplane/internal/cursor"
	"repoplane/internal/store"
	storesqlite "repoplane/internal/store/sqlite"
	"repoplane/internal/workspace"
)

func newServiceFixture(t *testing.T, manifests map[string]string) (*Service, string) {
	t.Helper()
	rootPath := t.TempDir()
	catalogPath := filepath.Join(rootPath, "catalog")
	if err := os.Mkdir(catalogPath, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range manifests {
		if err := os.WriteFile(filepath.Join(catalogPath, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
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
	codec, err := cursor.NewCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	indexer := NewIndexer(root, repository, []string{"catalog"})
	indexer.now = func() time.Time { return now }
	service := NewService(root.ID(), repository, indexer, codec)
	service.now = func() time.Time { return now }
	return service, catalogPath
}

func TestServicePaginatesFixedCatalogResults(t *testing.T) {
	service, catalogPath := newServiceFixture(t, map[string]string{
		"a.yaml": "id: a.tool\nrevision: 1\nsummary: test alpha\n",
		"b.yaml": "id: b.tool\nrevision: 1\nsummary: test beta\n",
		"c.yaml": "id: c.tool\nrevision: 1\nsummary: test gamma\n",
	})
	first, err := service.Query(context.Background(), QueryRequest{Mode: "search", Query: "test", ItemLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Counts.Matched == nil || *first.Counts.Matched != 3 || first.NextCursor == nil || !*first.Truncated {
		t.Fatalf("unexpected first page: %+v", first)
	}
	// A later source edit must not mix current catalog data into the fixed set.
	if err := os.WriteFile(filepath.Join(catalogPath, "c.yaml"), []byte(
		"id: c.tool\nrevision: 2\nsummary: changed after first page\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := service.Query(context.Background(), QueryRequest{Cursor: *first.NextCursor, ItemLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].Summary != "test gamma" || second.NextCursor != nil || *second.Truncated {
		t.Fatalf("unexpected second page: %+v", second)
	}
	if first.SnapshotRef == nil || second.SnapshotRef == nil || *first.SnapshotRef != *second.SnapshotRef {
		t.Fatalf("snapshot changed between pages: %v %v", first.SnapshotRef, second.SnapshotRef)
	}
}

func TestServiceRanksExactIDFirstAndGetReturnsManifest(t *testing.T) {
	service, _ := newServiceFixture(t, map[string]string{
		"exact.yaml": "id: schema.validate\nrevision: 1\nsummary: exact item\n",
		"other.yaml": "id: other.tool\nrevision: 1\nsummary: schema validate helper\n",
	})
	search, err := service.Query(context.Background(), QueryRequest{Mode: "search", Query: "schema.validate"})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Items) == 0 || search.Items[0].ID != "schema.validate" || search.Items[0].MatchReason != "exact_id" {
		t.Fatalf("exact ID was not first: %+v", search.Items)
	}
	get, err := service.Query(context.Background(), QueryRequest{Mode: "get", ID: "schema.validate"})
	if err != nil {
		t.Fatal(err)
	}
	if len(get.Items) != 1 || get.Items[0].Manifest == nil || get.Items[0].Manifest.ID != "schema.validate" {
		t.Fatalf("unexpected get response: %+v", get)
	}
}

func TestExecutionAvailabilityRequiresHostAndManifestOptIn(t *testing.T) {
	service, _ := newServiceFixture(t, map[string]string{
		"runner.yaml": "id: test.run\nrevision: 1\nsummary: runner\nexecution:\n  kind: cli\n  executable_ref: go\n  cwd: .\n  argv_template: [version]\n  trusted_for_run: true\n",
	})
	result, err := service.Query(context.Background(), QueryRequest{Mode: "get", ID: "test.run"})
	if err != nil || len(result.Items) != 1 || result.Items[0].ExecutionAvailable {
		t.Fatalf("execution available without host opt-in: response=%+v err=%v", result, err)
	}
	service.EnableExecution()
	result, err = service.Query(context.Background(), QueryRequest{Mode: "get", ID: "test.run"})
	if err != nil || len(result.Items) != 1 || !result.Items[0].ExecutionAvailable {
		t.Fatalf("execution unavailable after both opt-ins: response=%+v err=%v", result, err)
	}
}

func TestServiceAuditReportsManifestFailures(t *testing.T) {
	service, _ := newServiceFixture(t, map[string]string{
		"one.yaml":     "id: duplicate.tool\nrevision: 1\nsummary: one\n",
		"two.yaml":     "id: duplicate.tool\nrevision: 1\nsummary: two\n",
		"invalid.yaml": "id: INVALID\nrevision: 1\nsummary: invalid\n",
	})
	audit, err := service.Query(context.Background(), QueryRequest{Mode: "audit"})
	if err != nil {
		t.Fatal(err)
	}
	if len(audit.Items) != 3 || audit.Counts.Matched == nil || *audit.Counts.Matched != 3 {
		t.Fatalf("unexpected audit response: %+v", audit)
	}
}

func TestServiceReportsUnconfiguredCatalogCandidates(t *testing.T) {
	service, catalogPath := newServiceFixture(t, map[string]string{
		"tool.yaml": "id: configured.tool\nrevision: 1\nsummary: configured\n",
	})
	rootPath := filepath.Dir(catalogPath)
	for _, path := range []string{
		filepath.Join(rootPath, "code", ".git"),
		filepath.Join(rootPath, "code", ".repoplane", "catalog"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	status, err := service.Query(context.Background(), QueryRequest{Mode: "status"})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Warnings) != 1 || status.Warnings[0].Code != "catalog_candidate_unconfigured" || status.Warnings[0].Ref == nil || *status.Warnings[0].Ref != "code/.repoplane/catalog" {
		t.Fatalf("warnings=%+v", status.Warnings)
	}
	_, err = service.Query(context.Background(), QueryRequest{Mode: "get", ID: "missing.tool"})
	var missing *MissingCapabilityError
	if !errors.Is(err, store.ErrNotFound) || !errors.As(err, &missing) || len(missing.CandidateRoots) != 1 || missing.CandidateRoots[0] != "code/.repoplane/catalog" {
		t.Fatalf("missing item error=%v", err)
	}
	for _, request := range []QueryRequest{
		{Mode: "search", Query: "missing.tool"},
		{Mode: "search", Query: "configured.tool"},
		{Mode: "list"},
	} {
		response, err := service.Query(context.Background(), request)
		if err != nil || len(response.Warnings) == 0 || response.Warnings[0].Code != "catalog_candidate_unconfigured" ||
			response.Warnings[0].Ref == nil || *response.Warnings[0].Ref != "code/.repoplane/catalog" {
			t.Fatalf("request=%+v response=%+v err=%v", request, response, err)
		}
	}
	service.indexer.catalogRoots = []string{"catalog", "code"}
	status, err = service.Query(context.Background(), QueryRequest{Mode: "status"})
	if err != nil || len(status.Warnings) != 0 {
		t.Fatalf("configured parent warnings=%+v err=%v", status.Warnings, err)
	}
}

func TestServiceLookupReportsCatalogIssues(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"yaml", "id: broken.tool\nrevision: 1\nsummary: [unterminated\n"},
		{"schema", "id: broken.tool\nrevision: 1\nsummary: broken\narguments:\n  value: {type: number}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, catalogPath := newServiceFixture(t, map[string]string{
				"valid.yaml":  "id: valid.tool\nrevision: 1\nsummary: valid\n",
				"broken.yaml": tc.body,
			})
			for _, request := range []QueryRequest{
				{Mode: "search", Query: "broken.tool"},
				{Mode: "search", Query: "valid.tool"},
				{Mode: "list"},
			} {
				response, err := service.Query(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, warning := range response.Warnings {
					if warning.Code == "catalog_issues_present" {
						found = strings.Contains(warning.Message, "1 audit issues") &&
							strings.Contains(warning.Message, "catalog_query(mode=audit)")
					}
				}
				if !found {
					t.Fatalf("request=%+v warnings=%+v", request, response.Warnings)
				}
				encoded, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				request.ByteLimit = uint64(len(encoded) - 1)
				if _, err := service.Query(context.Background(), request); !errors.Is(err, ErrResponseTooLarge) {
					t.Fatalf("warning exceeded byte limit: %v", err)
				}
			}
			_, err := service.Query(context.Background(), QueryRequest{Mode: "get", ID: "broken.tool"})
			var missing *MissingCapabilityError
			if !errors.Is(err, store.ErrNotFound) || !errors.As(err, &missing) || missing.IssueCount != 1 {
				t.Fatalf("missing error=%+v", err)
			}
			_, err = service.ResolveCapability(context.Background(), "broken.tool")
			if !errors.As(err, &missing) || missing.IssueCount != 1 {
				t.Fatalf("resolve error=%+v", err)
			}
			audit, err := service.Query(context.Background(), QueryRequest{Mode: "audit"})
			if err != nil || len(audit.Items) != 1 || len(audit.Items[0].Detail) == 0 {
				t.Fatalf("audit=%+v err=%v", audit, err)
			}
			if err := os.Remove(filepath.Join(catalogPath, "broken.yaml")); err != nil {
				t.Fatal(err)
			}
			clean, err := service.Query(context.Background(), QueryRequest{Mode: "search", Query: "broken.tool"})
			if err != nil || len(clean.Warnings) != 1 || clean.Warnings[0].Code != "catalog_no_match" {
				t.Fatalf("clean no-match=%+v err=%v", clean, err)
			}
			_, err = service.Query(context.Background(), QueryRequest{Mode: "get", ID: "broken.tool"})
			if !errors.As(err, &missing) || missing.IssueCount != 0 {
				t.Fatalf("clean missing error=%+v", err)
			}
		})
	}
}

func TestServiceWarningBudgetPreservesPagination(t *testing.T) {
	manifests := map[string]string{"invalid.yaml": "id: INVALID\nrevision: 1\nsummary: invalid\n"}
	for _, id := range []string{"a.tool", "b.tool", "c.tool"} {
		manifests[id+".yaml"] = "id: " + id + "\nrevision: 1\nsummary: " + strings.Repeat("x", 600) + "\n"
	}
	service, _ := newServiceFixture(t, manifests)
	full, err := service.Query(context.Background(), QueryRequest{Mode: "list"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	budget := uint64(len(encoded) - 400)
	page, err := service.Query(context.Background(), QueryRequest{Mode: "list", ByteLimit: budget})
	if err != nil || len(page.Items) == 0 || len(page.Items) >= 3 || page.NextCursor == nil ||
		len(page.Warnings) != 1 || page.Warnings[0].Code != "catalog_issues_present" {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	total := len(page.Items)
	for page.NextCursor != nil {
		page, err = service.Query(context.Background(), QueryRequest{Cursor: *page.NextCursor, ByteLimit: budget})
		if err != nil || len(page.Warnings) != 0 || len(page.Items) == 0 {
			t.Fatalf("cursor page=%+v err=%v", page, err)
		}
		total += len(page.Items)
	}
	if total != 3 {
		t.Fatalf("paginated total=%d, want 3", total)
	}
}

func TestServiceEnforcesResponseByteLimit(t *testing.T) {
	service, _ := newServiceFixture(t, map[string]string{
		"tool.yaml": "id: test.tool\nrevision: 1\nsummary: a response that cannot fit a tiny envelope budget\n",
	})
	_, err := service.Query(context.Background(), QueryRequest{
		Mode: "list", ItemLimit: 1, ByteLimit: 64,
	})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error=%v, want ErrResponseTooLarge", err)
	}
}
