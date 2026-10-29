//go:build windows

package dynlib

import "golang.org/x/sys/windows"

func load(name string, explicit bool) (uintptr, error) {
	flags := uint32(windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
	if explicit {
		flags = windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR | windows.LOAD_LIBRARY_SEARCH_DEFAULT_DIRS
	}
	h, err := windows.LoadLibraryEx(name, 0, uintptr(flags))
	return uintptr(h), err
}
func symbol(handle uintptr, name string) (uintptr, error) {
	return windows.GetProcAddress(windows.Handle(handle), name)
}
