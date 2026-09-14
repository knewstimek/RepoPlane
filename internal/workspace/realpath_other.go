//go:build !windows

package workspace

import "path/filepath"

func realPath(path string) (string, error) { return filepath.EvalSymlinks(path) }
