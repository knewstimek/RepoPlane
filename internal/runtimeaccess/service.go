// Package runtimeaccess manages ephemeral, user-approved stdio capabilities.
package runtimeaccess

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"repoplane/internal/runtimeconfig"
	"repoplane/internal/workspace"
)

const (
	KindReadPath     = "read_path"
	KindIntentWrite  = "intent_write"
	KindReportImport = "report_import"
	KindRunner       = "runner_execute"
	KindCache        = "cache_reuse"
	KindMemoryExport = "memory_export"
	ActionStatus     = "status"
	ActionGrant      = "grant"
	ActionRevoke     = "revoke"
	pendingTTL       = 5 * time.Minute
	maxPending       = 64
)

var (
	ErrDisabled = errors.New("runtime access is unavailable for this transport")
	ErrDeclined = errors.New("runtime access request declined")
	ErrInvalid  = errors.New("runtime access request is invalid")
	ErrPending  = errors.New("runtime access approval is missing or expired")
)

type Initial struct {
	IntentWrite  bool
	ReportImport bool
	Runner       bool
	Cache        bool
}

type Request struct {
	Action  string `json:"action" jsonschema:"status, grant, or revoke"`
	Kind    string `json:"kind,omitempty" jsonschema:"read_path, intent_write, report_import, runner_execute, cache_reuse, or memory_export"`
	Path    string `json:"path,omitempty" jsonschema:"workspace-relative read path or absolute memory export destination"`
	GrantID string `json:"grant_id,omitempty" jsonschema:"grant ID returned by status or grant"`
}

type Grant struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Path      string `json:"path,omitempty"`
	Revocable bool   `json:"revocable"`
}

type Response struct {
	Status string  `json:"status"`
	Grants []Grant `json:"grants"`
}

type pending struct {
	kind      string
	path      string
	readPlan  *workspace.ReadGrantPlan
	config    *runtimeconfig.Request
	expiresAt time.Time
}

type Service struct {
	mu      sync.Mutex
	root    *workspace.Root
	allowed bool
	fixed   map[string]bool
	enabled map[string]bool
	grants  map[string]Grant
	pending map[string]pending
	now     func() time.Time
}

func New(root *workspace.Root, allowed bool, initial Initial) *Service {
	fixed := map[string]bool{
		KindIntentWrite: initial.IntentWrite, KindReportImport: initial.ReportImport,
		KindRunner: initial.Runner, KindCache: initial.Cache,
	}
	enabled := make(map[string]bool, len(fixed))
	grants := make(map[string]Grant)
	for kind, value := range fixed {
		enabled[kind] = value
		if value {
			id := "host_" + kind
			grants[id] = Grant{ID: id, Kind: kind, Revocable: false}
		}
	}
	return &Service{root: root, allowed: allowed, fixed: fixed, enabled: enabled, grants: grants, pending: make(map[string]pending), now: time.Now}
}

func (s *Service) Enabled(kind string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled[kind]
}

func (s *Service) Available() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allowed
}

func (s *Service) GrantedPath(kind, path string) bool {
	_, ok := s.ApprovedPath(kind, path)
	return ok
}

// ApprovedPath returns the canonical path captured by the grant. Callers use
// this value instead of resolving the caller's alias again after approval.
func (s *Service) ApprovedPath(kind, path string) (string, bool) {
	if kind != KindMemoryExport || !filepath.IsAbs(path) {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, grant := range s.grants {
		if grant.Kind == kind && samePath(grant.Path, resolved) {
			return grant.Path, true
		}
	}
	return "", false
}

func (s *Service) Status() Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Response{Status: "ok", Grants: sortedGrants(s.grants)}
}

// Begin validates and records an approval request without changing authority.
func (s *Service) Begin(kind, path string) (token, message string, err error) {
	if !s.allowed {
		return "", "", ErrDisabled
	}
	if kind == KindMemoryExport {
		if !filepath.IsAbs(path) {
			return "", "", ErrInvalid
		}
		path, err = filepath.EvalSymlinks(filepath.Clean(path))
		if err != nil {
			return "", "", ErrInvalid
		}
	} else {
		path = filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	}
	if err := validate(kind, path); err != nil {
		return "", "", err
	}
	var readPlan *workspace.ReadGrantPlan
	if kind == KindReadPath {
		plan, err := s.root.PrepareReadGrant(filepath.FromSlash(path))
		if err != nil {
			return "", "", err
		}
		readPlan = &plan
	}
	token, err = randomToken()
	if err != nil {
		return "", "", err
	}
	now := s.now().UTC()
	s.mu.Lock()
	for id, item := range s.pending {
		if !item.expiresAt.After(now) {
			delete(s.pending, id)
		}
	}
	if len(s.pending) >= maxPending {
		s.mu.Unlock()
		return "", "", errors.New("runtime access approval queue is full")
	}
	s.pending[token] = pending{kind: kind, path: path, readPlan: readPlan, expiresAt: now.Add(pendingTTL)}
	s.mu.Unlock()
	if kind == KindReadPath {
		message = fmt.Sprintf("Allow RepoPlane to read %q outside the primary workspace for this MCP process? This does not allow writes or execution.", path)
	} else if kind == KindMemoryExport {
		message = fmt.Sprintf("Allow RepoPlane to write portable memory backups into %q for this MCP process? Archives exclude profiles, tokens, and secret keys.", path)
	} else {
		message = fmt.Sprintf("Allow RepoPlane capability %q for this MCP process?", kind)
	}
	return token, message, nil
}

// BeginConfig binds a validated configuration proposal to a one-shot approval.
func (s *Service) BeginConfig(request runtimeconfig.Request) (token, message string, err error) {
	if !s.allowed {
		return "", "", ErrDisabled
	}
	if err := runtimeconfig.Validate(request); err != nil || request.Action == runtimeconfig.ActionStatus {
		return "", "", ErrInvalid
	}
	token, err = randomToken()
	if err != nil {
		return "", "", err
	}
	now := s.now().UTC()
	s.mu.Lock()
	for id, item := range s.pending {
		if !item.expiresAt.After(now) {
			delete(s.pending, id)
		}
	}
	if len(s.pending) >= maxPending {
		s.mu.Unlock()
		return "", "", errors.New("runtime access approval queue is full")
	}
	copyRequest := request
	copyRequest.Values = append([]string(nil), request.Values...)
	s.pending[token] = pending{config: &copyRequest, expiresAt: now.Add(pendingTTL)}
	s.mu.Unlock()
	message = fmt.Sprintf("Allow RepoPlane runtime configuration action %q on %q", request.Action, request.Target)
	if len(request.Values) != 0 {
		message += fmt.Sprintf(" with values %q", request.Values)
	}
	return token, message + " for this MCP process?", nil
}

// CompleteConfig consumes a proposal token and applies the exact stored request.
func (s *Service) CompleteConfig(ctx context.Context, token string, accepted bool, controller runtimeconfig.Controller) (runtimeconfig.Response, error) {
	if !s.allowed {
		return runtimeconfig.Response{}, ErrDisabled
	}
	now := s.now().UTC()
	s.mu.Lock()
	item, ok := s.pending[token]
	delete(s.pending, token)
	s.mu.Unlock()
	if !ok || !item.expiresAt.After(now) || item.config == nil {
		return runtimeconfig.Response{}, ErrPending
	}
	if !accepted {
		return runtimeconfig.Response{}, ErrDeclined
	}
	if controller == nil {
		return runtimeconfig.Response{}, runtimeconfig.ErrUnavailable
	}
	return controller.Apply(ctx, *item.config)
}

// RebindRoot moves path authority to a replacement workspace. Capability
// grants remain active, while path grants and pending proposals are discarded.
func (s *Service) RebindRoot(root *workspace.Root) {
	s.mu.Lock()
	s.root = root
	for id, grant := range s.grants {
		if grant.Kind == KindReadPath {
			delete(s.grants, id)
		}
	}
	s.pending = make(map[string]pending)
	s.mu.Unlock()
}

// Complete consumes one server-issued approval token and applies it only when
// the client reports an explicit user acceptance.
func (s *Service) Complete(token string, accepted bool) (Response, error) {
	if !s.allowed {
		return Response{}, ErrDisabled
	}
	now := s.now().UTC()
	s.mu.Lock()
	item, ok := s.pending[token]
	delete(s.pending, token)
	s.mu.Unlock()
	if !ok || !item.expiresAt.After(now) {
		return Response{}, ErrPending
	}
	if !accepted {
		return Response{}, ErrDeclined
	}
	if item.kind == KindReadPath {
		if item.readPlan == nil {
			return Response{}, ErrPending
		}
		readGrant := s.root.ApplyReadGrant(*item.readPlan)
		grant := Grant{ID: readGrant.ID, Kind: item.kind, Path: readGrant.Path, Revocable: true}
		s.mu.Lock()
		s.grants[grant.ID] = grant
		response := Response{Status: "granted", Grants: sortedGrants(s.grants)}
		s.mu.Unlock()
		return response, nil
	}
	if item.kind == KindMemoryExport {
		grant := Grant{ID: "runtime_" + item.kind, Kind: item.kind, Path: item.path, Revocable: true}
		s.mu.Lock()
		s.grants[grant.ID] = grant
		response := Response{Status: "granted", Grants: sortedGrants(s.grants)}
		s.mu.Unlock()
		return response, nil
	}
	grant := Grant{ID: "runtime_" + item.kind, Kind: item.kind, Revocable: true}
	s.mu.Lock()
	s.enabled[item.kind] = true
	s.grants[grant.ID] = grant
	response := Response{Status: "granted", Grants: sortedGrants(s.grants)}
	s.mu.Unlock()
	return response, nil
}

func (s *Service) Revoke(id string) (Response, error) {
	if !s.allowed {
		return Response{}, ErrDisabled
	}
	s.mu.Lock()
	grant, ok := s.grants[id]
	if !ok || !grant.Revocable {
		s.mu.Unlock()
		return Response{}, ErrInvalid
	}
	delete(s.grants, id)
	if grant.Kind != KindReadPath && grant.Kind != KindMemoryExport {
		s.enabled[grant.Kind] = s.fixed[grant.Kind]
	}
	response := Response{Status: "revoked", Grants: sortedGrants(s.grants)}
	s.mu.Unlock()
	if grant.Kind == KindReadPath {
		s.root.RevokeRead(id)
	}
	return response, nil
}

func validate(kind, path string) error {
	switch kind {
	case KindReadPath:
		if path == "" || path == "." || filepath.IsAbs(filepath.FromSlash(path)) || len(path) > 4096 {
			return ErrInvalid
		}
	case KindMemoryExport:
		if path == "" || !filepath.IsAbs(path) || len(path) > 4096 {
			return ErrInvalid
		}
	case KindIntentWrite, KindReportImport, KindRunner, KindCache:
		if path != "." && strings.TrimSpace(path) != "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func samePath(left, right string) bool {
	if filepath.VolumeName(left) != "" || filepath.VolumeName(right) != "" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func sortedGrants(values map[string]Grant) []Grant {
	result := make([]Grant, 0, len(values))
	for _, grant := range values {
		result = append(result, grant)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func randomToken() (string, error) {
	data := make([]byte, 24)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "approval_" + base64.RawURLEncoding.EncodeToString(data), nil
}
