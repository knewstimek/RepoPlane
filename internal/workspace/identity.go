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
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 128))
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if !identityPattern.MatchString(id) {
		return "", errors.New("workspace: invalid private workspace-id; restore the original identity file")
	}
	return id, nil
}

// PinIdentity preserves the old namespace across directory moves. An origin is
// only needed if a legacy workspace was already moved before its first pin.
// It is the old canonical absolute path, which need not still exist.
func (r *Root) PinIdentity(origin string) error {
	wanted := r.id
	if origin != "" {
		if !filepath.IsAbs(origin) {
			return errors.New("workspace: workspace-origin must be an absolute former canonical path")
		}
		wanted = identity(filepath.Clean(origin))
	}
	saved, err := r.savedIdentity()
	if err != nil {
		return err
	}
	if saved != "" {
		if origin != "" && saved != wanted {
			return errors.New("workspace: workspace-origin conflicts with saved identity; refusing to replace it")
		}
		r.id = saved
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
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		// Another opener won. Validate its identity instead of overwriting it.
		saved, readErr := r.savedIdentity()
		if readErr != nil || saved != wanted {
			return errors.New("workspace: concurrent identity initialization; retry after the other opener finishes")
		}
		r.id = saved
		return nil
	}
	if err != nil {
		return fmt.Errorf("workspace: persist identity: %w", err)
	}
	_, writeErr := io.WriteString(f, wanted+"\n")
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(path)
		return err
	}
	r.id = wanted
	return nil
}
