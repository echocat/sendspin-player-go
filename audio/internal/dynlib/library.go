// Package dynlib owns lazy, explicit native library binding. Successful handles
// remain loaded for the process lifetime: callbacks/function pointers must never
// outlive dlclose/FreeLibrary. Importing this package loads no audio library.
package dynlib

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ebitengine/purego"
)

type Library struct {
	handle uintptr
	Path   string
}

var cache = struct {
	sync.Mutex
	entries map[string]*Library
}{entries: make(map[string]*Library)}

func Open(explicit, fallback string) (*Library, error) {
	name := fallback
	if explicit != "" {
		if len(explicit) > 4096 || strings.ContainsRune(explicit, 0) {
			return nil, errors.New("invalid shared library path")
		}
		var err error
		name, err = filepath.Abs(explicit)
		if err != nil {
			return nil, err
		}
	}
	cache.Lock()
	defer cache.Unlock()
	if lib := cache.entries[name]; lib != nil {
		return lib, nil
	}
	h, err := load(name, explicit != "")
	if err != nil {
		return nil, fmt.Errorf("load shared library %q: %w", name, err)
	}
	lib := &Library{handle: h, Path: name}
	cache.entries[name] = lib
	return lib, nil
}
func (l *Library) Bind(target any, name string) error {
	p, err := symbol(l.handle, name)
	if err != nil {
		return fmt.Errorf("bind %s from %q: %w", name, l.Path, err)
	}
	purego.RegisterFunc(target, p)
	return nil
}
