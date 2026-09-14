//go:build windows

package pathfacts

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func platformLinks(path string, info os.FileInfo) LinkFacts {
	symlink := info.Mode()&os.ModeSymlink != 0
	result := LinkFacts{Symlink: symlink}
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return result
	}
	attributes, err := windows.GetFileAttributes(pointer)
	if err == nil {
		reparse := attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
		result.ReparsePoint = &reparse
		junction := false
		if reparse {
			if tag, ok := reparseTag(pointer); ok {
				junction = tag == windows.IO_REPARSE_TAG_MOUNT_POINT
				result.Junction = &junction
			}
		} else {
			result.Junction = &junction
		}
	}
	handle, err := windows.CreateFile(
		pointer, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0,
	)
	if err != nil {
		return result
	}
	defer windows.CloseHandle(handle)
	var details windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &details); err == nil {
		count := uint64(details.NumberOfLinks)
		result.HardlinkCount = &count
	}
	return result
}

type fileAttributeTagInfo struct {
	FileAttributes uint32
	ReparseTag     uint32
}

func reparseTag(path *uint16) (uint32, bool) {
	handle, err := windows.CreateFile(
		path, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0,
	)
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(handle)
	var info fileAttributeTagInfo
	err = windows.GetFileInformationByHandleEx(
		handle, windows.FileAttributeTagInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)),
	)
	return info.ReparseTag, err == nil
}
