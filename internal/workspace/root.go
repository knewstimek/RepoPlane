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

// ResolveForLookup resolves existing paths exactly like ResolveExisting. For a
// missing leaf, it resolves the nearest existing parent first so a symlinked
// parent cannot be used to construct a path outside the workspace.
func (r *Root) ResolveForLookup(relative string) (resolved string, exists bool, err error) {
	if filepath.IsAbs(relative) {
		return "", false, ErrAbsolute
	}
	lexical := filepath.Join(r.lexical, filepath.Clean(relative))
	if !within(r.lexical, lexical) {
		return "", false, ErrEscape
	}
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
	if !within(r.resolved, resolvedParent) {
		return "", false, ErrEscape
	}
	resolved = resolvedParent
	for index := len(missing) - 1; index >= 0; index-- {
		resolved = filepath.Join(resolved, missing[index])
	}
	resolved = filepath.Clean(resolved)
	if !within(r.resolved, resolved) {
		return "", false, ErrEscape
	}
	return resolved, false, nil
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
