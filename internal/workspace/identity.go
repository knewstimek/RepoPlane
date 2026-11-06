package workspace

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Keep the legacy ID, rather than rewriting records, when pinning a workspace.
// This private file must travel with the directory and must never be published.
const identityFile = ".repoplane/workspace-id"

var identityPattern = regexp.MustCompile(`^ws_[A-Za-z0-9_-]{24}$`)

func (r *Root) savedIdentity() (string, error) {
	path, exists, err := r.ResolveForLookup(identityFile)
	if err != nil || !exists {
		return "", err
	}
	// Grants must never allow an identity file outside the primary workspace.
	path, err = r.ResolvePrimaryExisting(identityFile)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 128 {
		return "", errors.New("workspace: private workspace-id must be a regular file of at most 128 bytes; restore the original identity file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 129))
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if len(data) > 128 || !identityPattern.MatchString(id) {
		return "", errors.New("workspace: invalid private workspace-id; restore the original identity file")
	}
	return id, nil
}

// PrepareIdentity binds services to a namespace without publishing a marker.
// Call PinIdentity only after service initialization succeeds.
func (r *Root) PrepareIdentity(origin string) error {
	wanted := r.id
	if origin != "" {
		if !filepath.IsAbs(origin) {
			return errors.New("workspace: workspace-origin must be an absolute former canonical path")
		}
		// A missing old leaf can still have an aliased existing parent (Windows
		// 8.3 names, junctions, or symlinks). Match Open's canonical namespace.
		canonical, err := RealPathAllowMissing(origin)
		if err != nil {
			return fmt.Errorf("workspace: resolve former workspace path: %w", err)
		}
		wanted = identity(canonical)
	}
	saved, err := r.savedIdentity()
	if err != nil {
		return err
	}
	if saved != "" && saved != wanted {
		return errors.New("workspace: workspace-origin or prepared identity conflicts with saved identity; refusing to replace it")
	}
	r.id = wanted
	return nil
}

// PinIdentity preserves the old namespace across directory moves. An origin is
// only needed if a legacy workspace was already moved before its first pin.
// It is the old canonical absolute path, which need not still exist.
func (r *Root) PinIdentity(origin string) error {
	if err := r.PrepareIdentity(origin); err != nil {
		return err
	}
	wanted := r.id
	if saved, err := r.savedIdentity(); err != nil {
		return err
	} else if saved != "" {
		if saved != wanted {
			return errors.New("workspace: saved identity changed during initialization")
		}
		return nil
	}
	parent, _, err := r.ResolveForLookup(".repoplane")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("workspace: create private identity directory: %w", err)
	}
	if _, err := r.ResolvePrimaryExisting(".repoplane"); err != nil {
		return err
	}
	path := filepath.Join(parent, "workspace-id")
	f, err := os.CreateTemp(parent, ".workspace-id-")
	if err != nil {
		return fmt.Errorf("workspace: persist identity: %w", err)
	}
	defer os.Remove(f.Name())
	_, writeErr := io.WriteString(f, wanted+"\n")
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	// Publish complete bytes without replacing another opener's marker. A crash
	// before publication may leave a private temp file, never a partial marker.
	if err := publishIdentity(f.Name(), path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("workspace: publish identity: %w", err)
		}
		saved, readErr := r.savedIdentity()
		if readErr != nil || saved != wanted {
			return errors.New("workspace: concurrent identity initialization selected another identity; refusing to replace it")
		}
	}
	r.id = wanted
	return nil
}
