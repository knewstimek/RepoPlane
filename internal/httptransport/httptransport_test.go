package httptransport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"repoplane/internal/catalog"
	"repoplane/internal/cursor"
	"repoplane/internal/mcpserver"
	"repoplane/internal/store"
	storesqlite "repoplane/internal/store/sqlite"
	"repoplane/internal/workspace"
)

func localProfile(tokenFile string) Profile {
	return Profile{Listen: "127.0.0.1:8080", Endpoint: "/mcp", ResourceURI: "http://example.test/mcp", AllowedHosts: []string{"example.test"}, Auth: AuthProfile{Mode: "local_token", TokenFile: tokenFile, Scopes: []string{"repoplane.read"}}, Limits: LimitsProfile{BodyBytes: 1 << 20, ConcurrentRequests: 4, PerPrincipal: 2, RequestsPerMinute: 60, ReadHeaderSeconds: 2, IdleSeconds: 2, ShutdownSeconds: 2}, Audit: AuditProfile{RetentionDays: 30}}
}

type failingAudit struct{}

func (failingAudit) AdmitAudit(context.Context, store.AuditEvent) error {
	return errors.New("unavailable")
}
func (failingAudit) ResolveAuditOperation(context.Context, string, string) error {
	return errors.New("unavailable")
}
func (failingAudit) CompleteAudit(context.Context, string, string, uint64, time.Time) error {
	return nil
}
func (failingAudit) DeleteExpiredAudit(context.Context, time.Time, uint64) (uint64, error) {
	return 0, nil
}
func (failingAudit) Close() error { return nil }

func TestProfileRejectsUnsafePublicAndUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "http.yaml")
	body := "listen: 0.0.0.0:8080\nendpoint: /mcp\nresource_uri: https://example.invalid/mcp\nauth:\n  mode: local_token\n  token_file: token\n  scopes: [repoplane.read]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfile(path); err == nil {
		t.Fatal("unsafe public profile succeeded")
	}
	if err := os.WriteFile(path, []byte("unknown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfile(path); err == nil {
		t.Fatal("unknown profile field succeeded")
	}
}

func TestPublishedHTTPProfilesParse(t *testing.T) {
	for _, name := range []string{"http-profile.local.yaml", "http-profile.oauth.yaml"} {
		if _, err := LoadProfile(filepath.Join("..", "..", "examples", name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestLocalTokenAndScopeAuthorization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	token := strings.Repeat("a", 32)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	verifier, err := TokenVerifier(localProfile(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier(context.Background(), "wrong", nil); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("wrong token err=%v", err)
	}
	info, err := verifier(context.Background(), token, nil)
	if err != nil || authorizeInfo(info, "catalog_query") != nil {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	if authorizeInfo(info, mcpserver.ToolboxRead) != nil {
		t.Fatal("read toolbox was not authorized by read scope")
	}
	if !errors.Is(authorizeInfo(info, "run_execute"), mcpserver.ErrAuthorizationDenied) {
		t.Fatal("runner scope unexpectedly authorized")
	}
	if !errors.Is(authorizeInfo(info, mcpserver.ToolboxRunner), mcpserver.ErrAuthorizationDenied) {
		t.Fatal("runner toolbox scope unexpectedly authorized")
	}
}

func TestIntrospectionRequiresAudienceAndExpiry(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "client" || pass != "secret" {
			t.Error("missing introspection basic auth")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":true,"exp":4102444800,"scope":"repoplane.read","sub":"subject","aud":"https://resource.example/mcp"}`))
	}))
	defer server.Close()
	profile := Profile{ResourceURI: "https://resource.example/mcp", Auth: AuthProfile{Mode: "oauth_introspection", IntrospectionURL: server.URL, Audience: "https://resource.example/mcp"}}
	verifier := introspectionVerifier(profile, server.Client(), "client", "secret")
	info, err := verifier(context.Background(), "opaque", nil)
	if err != nil || info.UserID != "subject" || len(info.Scopes) != 1 {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	profile.Auth.Audience = "https://other.example/mcp"
	if _, err := introspectionVerifier(profile, server.Client(), "client", "secret")(context.Background(), "opaque", nil); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("audience err=%v", err)
	}
}

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	clone.Host = "example.test"
	return t.base.RoundTrip(clone)
}

func TestStreamableHTTPAuthenticationOriginAndAudit(t *testing.T) {
	dir := t.TempDir()
	token := strings.Repeat("b", 32)
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := localProfile(tokenFile)
	verifier, err := TokenVerifier(profile)
	if err != nil {
		t.Fatal(err)
	}
	auditDB, err := storesqlite.OpenAudit(context.Background(), filepath.Join(dir, "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer auditDB.Close()
	workspacePath := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(filepath.Join(workspacePath, "catalog"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "catalog", "one.yaml"), []byte("id: http.example\nrevision: 1\nsummary: HTTP example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := workspace.Open(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	indexDB, err := storesqlite.Open(context.Background(), filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer indexDB.Close()
	now := time.Now().UTC()
	if err := indexDB.UpsertWorkspace(context.Background(), store.Workspace{ID: root.ID(), RootFingerprint: root.ID(), CreatedAt: now, LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	codec, _ := cursor.NewCodec([]byte("0123456789abcdef0123456789abcdef"))
	indexer := catalog.NewIndexer(root, indexDB, []string{"catalog"})
	if _, err := indexer.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	catalogService := catalog.NewService(root.ID(), indexDB, indexer, codec)
	handler, err := Handler("test", mcpserver.Options{Catalog: catalogService}, profile, verifier, auditDB, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	unauthorized, err := http.Post(server.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusForbidden && unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d", unauthorized.StatusCode)
	}
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{}`))
	request.Host = "example.test"
	request.Header.Set("Origin", "https://evil.example")
	badOrigin, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	badOrigin.Body.Close()
	if badOrigin.StatusCode != http.StatusForbidden {
		t.Fatalf("origin status=%d", badOrigin.StatusCode)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "http-test", Version: "test"}, nil)
	httpClient := &http.Client{Transport: bearerTransport{base: http.DefaultTransport, token: token}, Timeout: 5 * time.Second}
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: httpClient, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "catalog_query" {
		t.Fatalf("tools=%+v err=%v", tools, err)
	}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "catalog_query", Arguments: map[string]any{"mode": "list"}})
	if err != nil || result.IsError {
		t.Fatalf("catalog result=%+v err=%v", result, err)
	}
	legacyBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"legacy-test","version":"test"}}}`
	legacyRequest, _ := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(legacyBody))
	legacyRequest.Host = "example.test"
	legacyRequest.Header.Set("Authorization", "Bearer "+token)
	legacyRequest.Header.Set("Content-Type", "application/json")
	legacyRequest.Header.Set("Accept", "application/json, text/event-stream")
	legacyResponse, err := http.DefaultClient.Do(legacyRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer legacyResponse.Body.Close()
	legacyData, _ := io.ReadAll(legacyResponse.Body)
	if legacyResponse.StatusCode != http.StatusOK || !strings.Contains(string(legacyData), "2025-11-25") {
		t.Fatalf("legacy status=%d body=%s", legacyResponse.StatusCode, legacyData)
	}
	legacyList := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	legacyRequest, _ = http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(legacyList))
	legacyRequest.Host = "example.test"
	legacyRequest.Header.Set("Authorization", "Bearer "+token)
	legacyRequest.Header.Set("Content-Type", "application/json")
	legacyRequest.Header.Set("Accept", "application/json, text/event-stream")
	legacyRequest.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	legacyResponse, err = http.DefaultClient.Do(legacyRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer legacyResponse.Body.Close()
	legacyData, _ = io.ReadAll(legacyResponse.Body)
	if legacyResponse.StatusCode != http.StatusOK || !strings.Contains(string(legacyData), "catalog_query") {
		t.Fatalf("legacy list status=%d body=%s", legacyResponse.StatusCode, legacyData)
	}
}

func TestHTTPInsufficientScopeAndAuditAdmissionFailClosed(t *testing.T) {
	dir := t.TempDir()
	token := strings.Repeat("c", 32)
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := localProfile(tokenFile)
	verifier, err := TokenVerifier(profile)
	if err != nil {
		t.Fatal(err)
	}
	auditDB, err := storesqlite.OpenAudit(context.Background(), filepath.Join(dir, "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer auditDB.Close()
	handler, err := Handler("test", mcpserver.Options{}, profile, verifier, auditDB, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://example.test/mcp", strings.NewReader(`{}`))
	request.Host = "example.test"
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Mcp-Method", "tools/call")
	request.Header.Set("Mcp-Name", "run_execute")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Header().Get("WWW-Authenticate"), "repoplane.runner.execute") {
		t.Fatalf("code=%d challenge=%q", recorder.Code, recorder.Header().Get("WWW-Authenticate"))
	}
	failHandler, err := Handler("test", mcpserver.Options{}, profile, verifier, failingAudit{}, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "http://example.test/mcp", strings.NewReader(`{}`))
	request.Host = "example.test"
	request.Header.Set("Authorization", "Bearer "+token)
	recorder = httptest.NewRecorder()
	failHandler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure code=%d", recorder.Code)
	}
}

func TestAdmissionLimiterBoundsPrincipalConcurrencyAndMap(t *testing.T) {
	limiter := newAdmissionLimiter(LimitsProfile{ConcurrentRequests: 2, PerPrincipal: 1, RequestsPerMinute: 3})
	release, _, ok := limiter.acquire("one")
	if !ok {
		t.Fatal("first admission rejected")
	}
	if _, _, ok := limiter.acquire("one"); ok {
		t.Fatal("per-principal concurrency was not enforced")
	}
	otherRelease, _, ok := limiter.acquire("two")
	if !ok {
		t.Fatal("second principal rejected")
	}
	if _, _, ok := limiter.acquire("three"); ok {
		t.Fatal("global concurrency was not enforced")
	}
	release()
	otherRelease()
}
