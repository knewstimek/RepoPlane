//go:build !windows

package workspace

import "os"

func publishIdentity(source, target string) error {
	// Link publishes a complete same-directory file atomically and fails if the
	// target already exists. The caller removes its private staging name.
	return os.Link(source, target)
}
