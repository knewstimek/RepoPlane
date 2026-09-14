package httptransport

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"repoplane/internal/mcpserver"
	"repoplane/internal/store"
)

func Authorizer(ctx context.Context, tool string) error {
	info := auth.TokenInfoFromContext(ctx)
	return authorizeInfo(info, tool)
}

func authorizeInfo(info *auth.TokenInfo, tool string) error {
	if info == nil {
		return mcpserver.ErrAuthorizationDenied
	}
	required, ok := requiredScope(tool)
	if !ok {
		return mcpserver.ErrAuthorizationDenied
	}
	if !slices.Contains(info.Scopes, required) {
		return mcpserver.ErrAuthorizationDenied
	}
	return nil
}

func requiredScope(tool string) (string, bool) {
	switch tool {
	case "catalog_query", "workspace_search", "path_explain", "data_query", "project_records":
		return "repoplane.read", true
	case "checkpoint_write", "memo_write":
		return "repoplane.intent.write", true
	case "check_report_import":
		return "repoplane.report.import", true
	case "run_prepare", "run_execute", "run_inspect":
		return "repoplane.runner.execute", true
	}
	return "", false
}

func Run(ctx context.Context, version string, options mcpserver.Options, profile Profile, verifier auth.TokenVerifier, audit store.AuditRepository, auditKey []byte) error {
	handler, err := Handler(version, options, profile, verifier, audit, auditKey)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: profile.Listen, Handler: handler, ReadHeaderTimeout: profile.ReadHeaderTimeout(), ReadTimeout: profile.ReadTimeout(), IdleTimeout: profile.IdleTimeout(), MaxHeaderBytes: 32 * 1024}
	errCh := make(chan error, 1)
	go func() {
		if profile.TLS.CertFile != "" {
			errCh <- server.ListenAndServeTLS(profile.TLS.CertFile, profile.TLS.KeyFile)
		} else {
			errCh <- server.ListenAndServe()
		}
	}()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), profile.ShutdownTimeout())
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func Handler(version string, options mcpserver.Options, profile Profile, verifier auth.TokenVerifier, audit store.AuditRepository, auditKey []byte) (http.Handler, error) {
	options.Authorize = Authorizer
	mcpService := mcpserver.New(version, options)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpService }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: profile.Limits.BodyBytes, PropagateRequestCancellation: true, DisableLocalhostProtection: true})
	limiter := newAdmissionLimiter(profile.Limits)
	resourceMetadataURL := protectedMetadataURL(profile.ResourceURI)
	if profile.Auth.Mode != "oauth_introspection" {
		resourceMetadataURL = ""
	}
	handler := http.Handler(mcpHandler)
	handler = auditMiddleware(handler, audit, auditKey, profile.ResourceURI, limiter)
	handler = scopeMiddleware(handler, resourceMetadataURL)
	handler = auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{ResourceMetadataURL: resourceMetadataURL})(handler)
	handler = hostMiddleware(handler, profile.AllowedHosts)
	origin := http.NewCrossOriginProtection()
	for _, trusted := range profile.AllowedOrigins {
		if err := origin.AddTrustedOrigin(trusted); err != nil {
			return nil, fmt.Errorf("trusted Origin: %w", err)
		}
	}
	handler = origin.Handler(handler)
	mux := http.NewServeMux()
	mux.Handle(profile.Endpoint, exactPath(profile.Endpoint, handler))
	if profile.Auth.Mode == "oauth_introspection" {
		metadata := &oauthex.ProtectedResourceMetadata{Resource: profile.ResourceURI, AuthorizationServers: profile.Auth.AuthorizationServers, ScopesSupported: []string{"repoplane.read", "repoplane.intent.write", "repoplane.report.import", "repoplane.runner.execute"}, BearerMethodsSupported: []string{"header"}, ResourceName: "RepoPlane"}
		metadataHandler := auth.ProtectedResourceMetadataHandler(metadata)
		mux.Handle("/.well-known/oauth-protected-resource", metadataHandler)
		mux.Handle("/.well-known/oauth-protected-resource/", metadataHandler)
	}
	return mux, nil
}

func protectedMetadataURL(resource string) string {
	parsed, err := url.Parse(resource)
	if err != nil {
		return ""
	}
	path := strings.Trim(parsed.EscapedPath(), "/")
	parsed.RawQuery, parsed.Fragment = "", ""
	if path == "" {
		parsed.Path = "/.well-known/oauth-protected-resource"
	} else {
		parsed.Path = "/.well-known/oauth-protected-resource/" + path
	}
	parsed.RawPath = ""
	return parsed.String()
}

func scopeMiddleware(next http.Handler, metadataURL string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Mcp-Method"), "tools/call") && r.Header.Get("Mcp-Name") != "" {
			required, known := requiredScope(r.Header.Get("Mcp-Name"))
			info := auth.TokenInfoFromContext(r.Context())
			if !known || info == nil || !slices.Contains(info.Scopes, required) {
				challenge := fmt.Sprintf(`Bearer error="insufficient_scope", scope=%q`, required)
				if metadataURL != "" {
					challenge += fmt.Sprintf(`, resource_metadata=%q`, metadataURL)
				}
				w.Header().Set("WWW-Authenticate", challenge)
				http.Error(w, "insufficient scope", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func exactPath(path string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hostMiddleware(next http.Handler, allowed []string) http.Handler {
	set := make(map[string]struct{}, len(allowed))
	for _, host := range allowed {
		set[strings.ToLower(host)] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := set[strings.ToLower(r.Host)]; !ok {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type responseCounter struct {
	http.ResponseWriter
	status int
	bytes  uint64
}

func (w *responseCounter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseCounter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += uint64(n)
	return n, err
}
func (w *responseCounter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func auditMiddleware(next http.Handler, repository store.AuditRepository, key []byte, resource string, limiter *admissionLimiter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := auth.TokenInfoFromContext(r.Context())
		principal := "unknown"
		if info != nil && info.UserID != "" {
			principal = info.UserID
		}
		release, retry, ok := limiter.acquire(principal)
		if !ok {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retry))
			http.Error(w, "request limit reached", http.StatusTooManyRequests)
			return
		}
		defer release()
		requestID, err := randomID()
		if err != nil {
			http.Error(w, "audit unavailable", http.StatusServiceUnavailable)
			return
		}
		principalHash := keyedHash(key, principal)
		requestBytes := uint64(0)
		if r.ContentLength > 0 {
			requestBytes = uint64(r.ContentLength)
		}
		event := store.AuditEvent{RequestID: requestID, PrincipalHash: principalHash, WorkspaceID: keyedHash(key, resource), Method: bounded(r.Header.Get("Mcp-Method"), 128), Tool: bounded(r.Header.Get("Mcp-Name"), 128), Decision: "admitted", Status: "started", RequestBytes: requestBytes, StartedAt: time.Now().UTC()}
		if event.Method == "" {
			event.Method = r.Method
		}
		if err := repository.AdmitAudit(r.Context(), event); err != nil {
			http.Error(w, "audit unavailable", http.StatusServiceUnavailable)
			return
		}
		counter := &responseCounter{ResponseWriter: w}
		next.ServeHTTP(counter, r)
		status := fmt.Sprintf("http_%d", counter.status)
		completeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := repository.CompleteAudit(completeCtx, requestID, status, counter.bytes, time.Now().UTC()); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "repoplane: audit completion failed")
		}
	})
}

func randomID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "http_" + hex.EncodeToString(data), nil
}
func keyedHash(key []byte, value string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
}
func bounded(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

type principalState struct {
	minute int64
	count  int
	active int
	seen   time.Time
}
type admissionLimiter struct {
	mu     sync.Mutex
	global chan struct{}
	per    int
	rate   int
	states map[string]*principalState
}

func newAdmissionLimiter(l LimitsProfile) *admissionLimiter {
	return &admissionLimiter{global: make(chan struct{}, l.ConcurrentRequests), per: l.PerPrincipal, rate: l.RequestsPerMinute, states: make(map[string]*principalState)}
}
func (l *admissionLimiter) acquire(principal string) (func(), int, bool) {
	select {
	case l.global <- struct{}{}:
	default:
		return func() {}, 1, false
	}
	now := time.Now()
	minute := now.Unix() / 60
	l.mu.Lock()
	if len(l.states) >= 1024 {
		for key, state := range l.states {
			if now.Sub(state.seen) > 2*time.Minute && state.active == 0 {
				delete(l.states, key)
			}
		}
	}
	state := l.states[principal]
	if state == nil {
		if len(l.states) >= 1024 {
			l.mu.Unlock()
			<-l.global
			return func() {}, 1, false
		}
		state = &principalState{}
		l.states[principal] = state
	}
	if state.minute != minute {
		state.minute, state.count = minute, 0
	}
	if state.count >= l.rate || state.active >= l.per {
		l.mu.Unlock()
		<-l.global
		return func() {}, int(60 - now.Unix()%60), false
	}
	state.count++
	state.active++
	state.seen = now
	l.mu.Unlock()
	return func() { l.mu.Lock(); state.active--; state.seen = time.Now(); l.mu.Unlock(); <-l.global }, 0, true
}
