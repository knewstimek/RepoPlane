// Package workspace enforces RepoPlane's local workspace trust boundary.
package workspace

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var (
	ErrInvalidRoot = errors.New("workspace: invalid root")
	ErrEscape      = errors.New("workspace: path escapes root")
	ErrAbsolute    = errors.New("workspace: request path must be relative")
)

// Root is an already-resolved workspace trust boundary.
type Root struct {
	id       string
	lexical  string
	resolved string
	grantsMu sync.RWMutex
	grants   map[string]readGrant
}

type readGrant struct {
	resolved  string
	directory bool
}

// ReadGrant is a session-local, read-only exception to the primary workspace
// boundary. Paths remain relative to the primary workspace so host absolute
// paths never become part of the public MCP contract.
type ReadGrant struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// ReadGrantPlan binds approval to the canonical target observed before the
// user prompt. Its resolved path remains private to this package.
type ReadGrantPlan struct {
	grant     ReadGrant
	candidate readGrant
}

// Open verifies and resolves a workspace directory.
func Open(path string) (*Root, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRoot, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRoot, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: not a directory", ErrInvalidRoot)
	}
	resolved, err := realPath(abs)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve: %v", ErrInvalidRoot, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, fmt.Errorf("%w: absolute resolved path: %v", ErrInvalidRoot, err)
	}
	resolved = filepath.Clean(resolved)
	return &Root{
		id:       identity(resolved),
		lexical:  filepath.Clean(abs),
		resolved: resolved,
		grants:   make(map[string]readGrant),
	}, nil
}

func (r *Root) ID() string       { return r.id }
func (r *Root) Lexical() string  { return r.lexical }
func (r *Root) Resolved() string { return r.resolved }

// ResolveExisting resolves an existing relative path and rejects symlink or
// junction traversal outside the workspace's resolved root.
func (r *Root) ResolveExisting(relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", ErrAbsolute
	}
	lexical := filepath.Join(r.lexical, filepath.Clean(relative))
	resolved, err := realPath(lexical)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	resolved = filepath.Clean(resolved)
	if !r.readAllowed(resolved) {
		return "", ErrEscape
	}
	return resolved, nil
}

// ResolveForLookup resolves existing paths exactly like ResolveExisting. For a
// missing leaf, it resolves the nearest existing parent first so a symlinked
// parent cannot be used to construct a path outside the workspace.
func (r *Root) ResolveForLookup(relative string) (resolved string, exists bool, err error) {
	if filepath.IsAbs(relative) {
		return "", false, ErrAbsolute
	}
	lexical := filepath.Join(r.lexical, filepath.Clean(relative))
	if _, statErr := os.Lstat(lexical); statErr == nil {
		resolved, err := r.ResolveExisting(relative)
		return resolved, true, err
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", false, statErr
	}

	parent := lexical
	missing := make([]string, 0)
	for {
		if _, statErr := os.Lstat(parent); statErr == nil {
			break
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", false, statErr
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", false, ErrEscape
		}
		missing = append(missing, filepath.Base(parent))
		parent = next
	}
	resolvedParent, err := realPath(parent)
	if err != nil {
		return "", false, err
	}
	if !r.readAllowed(resolvedParent) {
		return "", false, ErrEscape
	}
	resolved = resolvedParent
	for index := len(missing) - 1; index >= 0; index-- {
		resolved = filepath.Join(resolved, missing[index])
	}
	resolved = filepath.Clean(resolved)
	if !r.readAllowed(resolved) {
		return "", false, ErrEscape
	}
	return resolved, false, nil
}

// ResolvePrimaryExisting resolves an existing path only within the immutable
// primary workspace. Mutation, catalog execution, and Runner code use this
// method so runtime read grants never enlarge their authority.
func (r *Root) ResolvePrimaryExisting(relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", ErrAbsolute
	}
	lexical := filepath.Join(r.lexical, filepath.Clean(relative))
	if !within(r.lexical, lexical) {
		return "", ErrEscape
	}
	resolved, err := realPath(lexical)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	resolved = filepath.Clean(resolved)
	if !within(r.resolved, resolved) {
		return "", ErrEscape
	}
	return resolved, nil
}

// GrantRead adds one existing file or directory to the current process's
// ephemeral read boundary. The grant is deliberately not persisted.
func (r *Root) GrantRead(relative string) (ReadGrant, error) {
	plan, err := r.PrepareReadGrant(relative)
	if err != nil {
		return ReadGrant{}, err
	}
	return r.ApplyReadGrant(plan), nil
}

// PrepareReadGrant resolves a proposed grant without changing authority.
func (r *Root) PrepareReadGrant(relative string) (ReadGrantPlan, error) {
	grant, candidate, err := r.readGrantCandidate(relative)
	if err != nil {
		return ReadGrantPlan{}, err
	}
	return ReadGrantPlan{grant: grant, candidate: candidate}, nil
}

// ApplyReadGrant activates the exact canonical target captured before user
// approval. If the relative path is retargeted meanwhile, later reads fail
// closed because they no longer resolve within this target.
func (r *Root) ApplyReadGrant(plan ReadGrantPlan) ReadGrant {
	r.grantsMu.Lock()
	r.grants[plan.grant.ID] = plan.candidate
	r.grantsMu.Unlock()
	return plan.grant
}

// ValidateReadGrant verifies a proposed read grant without changing authority.
func (r *Root) ValidateReadGrant(relative string) (ReadGrant, error) {
	plan, err := r.PrepareReadGrant(relative)
	return plan.grant, err
}

func (r *Root) readGrantCandidate(relative string) (ReadGrant, readGrant, error) {
	if filepath.IsAbs(relative) {
		return ReadGrant{}, readGrant{}, ErrAbsolute
	}
	clean := filepath.Clean(relative)
	if clean == "." || strings.TrimSpace(relative) == "" {
		return ReadGrant{}, readGrant{}, errors.New("workspace: read grant path is required")
	}
	resolved, err := realPath(filepath.Join(r.lexical, clean))
	if err != nil {
		return ReadGrant{}, readGrant{}, err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return ReadGrant{}, readGrant{}, err
	}
	resolved = filepath.Clean(resolved)
	info, err := os.Stat(resolved)
	if err != nil {
		return ReadGrant{}, readGrant{}, err
	}
	id := grantIdentity(resolved)
	return ReadGrant{ID: id, Path: filepath.ToSlash(clean)}, readGrant{resolved: resolved, directory: info.IsDir()}, nil
}

// RevokeRead removes a previously granted read boundary.
func (r *Root) RevokeRead(id string) bool {
	r.grantsMu.Lock()
	defer r.grantsMu.Unlock()
	if _, ok := r.grants[id]; !ok {
		return false
	}
	delete(r.grants, id)
	return true
}

func (r *Root) readAllowed(candidate string) bool {
	if within(r.resolved, candidate) {
		return true
	}
	r.grantsMu.RLock()
	defer r.grantsMu.RUnlock()
	for _, grant := range r.grants {
		if (!grant.directory && samePath(grant.resolved, candidate)) || (grant.directory && within(grant.resolved, candidate)) {
			return true
		}
	}
	return false
}

func samePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func within(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func identity(resolved string) string {
	canonical := filepath.Clean(resolved)
	if runtime.GOOS == "windows" {
		canonical = strings.ToLower(canonical)
	}
	sum := sha256.Sum256([]byte("repoplane-workspace-v1\x00" + canonical))
	return "ws_" + base64.RawURLEncoding.EncodeToString(sum[:18])
}

func grantIdentity(resolved string) string {
	canonical := filepath.Clean(resolved)
	if runtime.GOOS == "windows" {
		canonical = strings.ToLower(canonical)
	}
	sum := sha256.Sum256([]byte("repoplane-read-grant-v1\x00" + canonical))
	return "rg_" + base64.RawURLEncoding.EncodeToString(sum[:18])
}
