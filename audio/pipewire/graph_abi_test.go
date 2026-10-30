//go:build linux && (amd64 || arm64)

package pipewire

import (
	"fmt"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

// Public 64-bit ABI, pinned to PipeWire 1.2.7 (also used by older 0.3 clients):
// https://github.com/PipeWire/pipewire/blob/1.2.7/src/pipewire/core.h
// https://github.com/PipeWire/pipewire/blob/1.2.7/spa/include/spa/utils/hook.h
// These layouts were also checked against the runtime's upstream 1.6.9 headers.
// The four core/registry helpers are inline/macros, NOT portable dlsym names.
// spa_interface_call_res dispatches through cb.funcs, passing cb.data (not the
// interface address). Methods version 0 and events version 0 suffice here.
type privateCallbacks struct {
	Funcs unsafe.Pointer
	Data  unsafe.Pointer
}

type privateInterface struct {
	Type      *byte
	Version   uint32
	Callbacks privateCallbacks
}

type privateCoreMethods struct {
	Version                               uint32
	AddListener, Hello, Sync, Pong, Error uintptr
	GetRegistry, CreateObject, Destroy    uintptr
}

type privateRegistryMethods struct {
	Version                    uint32
	AddListener, Bind, Destroy uintptr
}

type privateDictItem struct{ Key, Value *byte }
type privateDict struct {
	Flags, NItems uint32
	Items         *privateDictItem
}
type privateProperties struct {
	Dict  privateDict
	Flags uint32
}
type privateHook struct {
	Next, Prev *privateHook
	Callbacks  privateCallbacks
	Removed    uintptr
	Private    unsafe.Pointer
}
type privateRegistryEvents struct {
	Version        uint32
	Global, Remove uintptr
}
type privateCoreEvents struct {
	Version                                          uint32
	Info, Done, Ping, Error                          uintptr
	RemoveID, BoundID, AddMem, RemoveMem, BoundProps uintptr
}

// Callables borrow the interface, table and cb.data. The native context owns
// them until disconnect; privateGraph retains hooks/events until loopStop joins
// all callbacks, then disconnects and finally destroys the context.
type privateCoreCalls struct {
	addListener  func(unsafe.Pointer, *privateHook, *privateCoreEvents, unsafe.Pointer) int32
	getRegistry  func(unsafe.Pointer, uint32, uintptr) *privateInterface
	createObject func(unsafe.Pointer, string, string, uint32, *privateDict, uintptr) uintptr
}

type privateRegistryCalls struct {
	addListener func(unsafe.Pointer, *privateHook, *privateRegistryEvents, unsafe.Pointer) int32
}

func (i *privateInterface) require(kind string, version uint32) error {
	if i == nil {
		return fmt.Errorf("private graph %s: nil SPA interface", kind)
	}
	if actual := unix.BytePtrToString(i.Type); actual != kind {
		return fmt.Errorf("private graph SPA interface type %q, need %q", actual, kind)
	}
	if i.Version < version {
		return fmt.Errorf("private graph %s interface version %d, need >= %d", kind, i.Version, version)
	}
	if i.Callbacks.Funcs == nil {
		return fmt.Errorf("private graph %s: missing SPA method table", kind)
	}
	return nil
}

func privateBindMethod(dst any, method uintptr, version, required uint32, name string) error {
	// SPA_CALLBACK_CHECK accepts newer tables, and version 0 is unconditional.
	if version < required {
		return fmt.Errorf("private graph %s method table version %d, need >= %d", name, version, required)
	}
	if method == 0 {
		return fmt.Errorf("private graph %s: missing required SPA method", name)
	}
	purego.RegisterFunc(dst, method)
	return nil
}

func privateBindCore(i *privateInterface) (calls privateCoreCalls, err error) {
	if err = i.require("PipeWire:Interface:Core", 3); err != nil {
		return calls, err
	}
	m := (*privateCoreMethods)(i.Callbacks.Funcs)
	for _, binding := range []struct {
		dst    any
		method uintptr
		name   string
	}{
		{&calls.addListener, m.AddListener, "core.add_listener"},
		{&calls.getRegistry, m.GetRegistry, "core.get_registry"},
		{&calls.createObject, m.CreateObject, "core.create_object"},
	} {
		if err = privateBindMethod(binding.dst, binding.method, m.Version, 0, binding.name); err != nil {
			return privateCoreCalls{}, err
		}
	}
	return calls, nil
}

func privateBindRegistry(i *privateInterface) (calls privateRegistryCalls, err error) {
	if err = i.require("PipeWire:Interface:Registry", 3); err != nil {
		return calls, err
	}
	m := (*privateRegistryMethods)(i.Callbacks.Funcs)
	err = privateBindMethod(&calls.addListener, m.AddListener, m.Version, 0, "registry.add_listener")
	return calls, err
}
