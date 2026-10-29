//go:build linux || darwin

package dynlib

import "github.com/ebitengine/purego"

func load(name string, _ bool) (uintptr, error) {
	return purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_LOCAL)
}
func symbol(handle uintptr, name string) (uintptr, error) { return purego.Dlsym(handle, name) }
