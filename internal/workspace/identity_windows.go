//go:build windows

package workspace

import (
	"golang.org/x/sys/windows"
)

func publishIdentity(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	// No REPLACE_EXISTING: competing openers must validate the winner's ID.
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}
