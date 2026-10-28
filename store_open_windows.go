package sendspin

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openCredentialFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	// Load reads an immutable snapshot. Permit an atomic Save to replace its
	// directory entry while this handle still refers to the previous file.
	// os.Open omits FILE_SHARE_DELETE and can otherwise make a concurrent
	// credential update fail with access denied on Windows.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

func replaceCredentialFile(from, to string) error {
	name, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(name, windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	defer func() {
		if handle != windows.InvalidHandle {
			_ = windows.CloseHandle(handle)
		}
	}()
	// Both paths are in the same directory. A simple name avoids NT namespace
	// conversion and also handles long paths in the replacement buffer.
	target, err := windows.UTF16FromString(filepath.Base(to))
	if err != nil {
		return err
	}
	type renameInformation struct {
		Flags          uint32
		RootDirectory  windows.Handle
		FileNameLength uint32
		FileName       [1]uint16
	}
	const replaceIfExists, posixSemantics = 1, 2
	const fileRenameInformationEx = 65
	// FILE_RENAME_POSIX_SEMANTICS retains existing reader snapshots while
	// publishing the replacement name atomically. MoveFileEx (os.Rename)
	// rejects an open target even when its handles share delete access.
	var layout renameInformation
	length := int(unsafe.Offsetof(layout.FileName)) + len(target)*2
	buffer := make([]byte, max(length, int(unsafe.Sizeof(layout))))
	info := (*renameInformation)(unsafe.Pointer(&buffer[0]))
	info.Flags = replaceIfExists | posixSemantics
	info.FileNameLength = uint32((len(target) - 1) * 2)
	copy(unsafe.Slice(&info.FileName[0], len(target)), target)
	var status windows.IO_STATUS_BLOCK
	err = windows.NtSetInformationFile(handle, &status, &buffer[0], uint32(len(buffer)), fileRenameInformationEx)
	if errors.Is(err, windows.STATUS_INVALID_INFO_CLASS) || errors.Is(err, windows.STATUS_NOT_SUPPORTED) || errors.Is(err, windows.STATUS_INVALID_PARAMETER) {
		// Older filesystems may not implement POSIX replacement. Preserve their
		// normal rename behavior, including failure when a reader blocks it.
		_ = windows.CloseHandle(handle)
		handle = windows.InvalidHandle
		return os.Rename(from, to)
	}
	return err
}
