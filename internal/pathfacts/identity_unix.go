//go:build unix

package pathfacts

import (
	"os"
	"syscall"
)

func platformLinks(_ string, info os.FileInfo) LinkFacts {
	symlink := info.Mode()&os.ModeSymlink != 0
	result := LinkFacts{Symlink: symlink}
	if details, ok := info.Sys().(*syscall.Stat_t); ok {
		count := uint64(details.Nlink)
		result.HardlinkCount = &count
	}
	return result
}
