//go:build !windows && !unix

package pathfacts

import "os"

func platformLinks(_ string, info os.FileInfo) LinkFacts {
	return LinkFacts{Symlink: info.Mode()&os.ModeSymlink != 0}
}
