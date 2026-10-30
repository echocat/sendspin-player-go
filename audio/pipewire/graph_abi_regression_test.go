//go:build linux && (amd64 || arm64)

package pipewire

import (
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

func TestPrivateGraphABILayout(t *testing.T) {
	// Independent offsets from the pinned public C declarations, LP64 ABI.
	for _, check := range []struct {
		name      string
		got, want uintptr
	}{
		{"spa_callbacks.size", unsafe.Sizeof(privateCallbacks{}), 16},
		{"spa_callbacks.data", unsafe.Offsetof(privateCallbacks{}.Data), 8},
		{"spa_interface.size", unsafe.Sizeof(privateInterface{}), 32},
		{"spa_interface.version", unsafe.Offsetof(privateInterface{}.Version), 8},
		{"spa_interface.cb", unsafe.Offsetof(privateInterface{}.Callbacks), 16},
		{"core_methods.size", unsafe.Sizeof(privateCoreMethods{}), 72},
		{"core_methods.add_listener", unsafe.Offsetof(privateCoreMethods{}.AddListener), 8},
		{"core_methods.get_registry", unsafe.Offsetof(privateCoreMethods{}.GetRegistry), 48},
		{"core_methods.create_object", unsafe.Offsetof(privateCoreMethods{}.CreateObject), 56},
		{"core_methods.destroy", unsafe.Offsetof(privateCoreMethods{}.Destroy), 64},
		{"registry_methods.size", unsafe.Sizeof(privateRegistryMethods{}), 32},
		{"registry_methods.add_listener", unsafe.Offsetof(privateRegistryMethods{}.AddListener), 8},
		{"spa_hook.size", unsafe.Sizeof(privateHook{}), 48},
		{"spa_hook.cb", unsafe.Offsetof(privateHook{}.Callbacks), 16},
		{"spa_hook.removed", unsafe.Offsetof(privateHook{}.Removed), 32},
		{"spa_hook.priv", unsafe.Offsetof(privateHook{}.Private), 40},
		{"core_events.size", unsafe.Sizeof(privateCoreEvents{}), 80},
		{"core_events.error", unsafe.Offsetof(privateCoreEvents{}.Error), 32},
		{"core_events.bound_props", unsafe.Offsetof(privateCoreEvents{}.BoundProps), 72},
		{"registry_events.size", unsafe.Sizeof(privateRegistryEvents{}), 24},
		{"registry_events.global", unsafe.Offsetof(privateRegistryEvents{}.Global), 8},
		{"registry_events.global_remove", unsafe.Offsetof(privateRegistryEvents{}.Remove), 16},
		{"spa_dict.size", unsafe.Sizeof(privateDict{}), 16},
		{"spa_dict.items", unsafe.Offsetof(privateDict{}.Items), 8},
		{"pw_properties.size", unsafe.Sizeof(privateProperties{}), 24},
		{"pw_properties.flags", unsafe.Offsetof(privateProperties{}.Flags), 16},
	} {
		if check.got != check.want {
			t.Errorf("%s = %d, want %d", check.name, check.got, check.want)
		}
	}
}

func privateTestInterface(t *testing.T, kind string, methods unsafe.Pointer, data unsafe.Pointer) *privateInterface {
	t.Helper()
	name, err := unix.BytePtrFromString("PipeWire:Interface:" + kind)
	if err != nil {
		t.Fatal(err)
	}
	return &privateInterface{Type: name, Version: 3, Callbacks: privateCallbacks{Funcs: methods, Data: data}}
}

func TestPrivateGraphMethodAndCallbackRouting(t *testing.T) {
	var coreData, registryData, listenerData byte
	corePointer, registryPointer, listenerPointer := unsafe.Pointer(&coreData), unsafe.Pointer(&registryData), unsafe.Pointer(&listenerData)
	var coreHook, registryHook privateHook
	var dictionary privateDict
	var receivedError, receivedGlobal, receivedRemove bool
	message, _ := unix.BytePtrFromString("synthetic native error")
	kind, _ := unix.BytePtrFromString("PipeWire:Interface:Port")
	coreEvents := privateCoreEvents{Error: purego.NewCallback(func(data unsafe.Pointer, id uint32, seq, code int32, text *byte) {
		receivedError = true
		if data != listenerPointer || id != 17 || seq != -23 || code != -5 || unix.BytePtrToString(text) != "synthetic native error" {
			t.Error("core error callback lost data, argument order or signed integers")
		}
	})}
	registryEvents := privateRegistryEvents{
		Global: purego.NewCallback(func(data unsafe.Pointer, id, permissions uint32, typ *byte, version uint32, dict *privateDict) {
			receivedGlobal = true
			if data != listenerPointer || id != 31 || permissions != 0xf0 || unix.BytePtrToString(typ) != "PipeWire:Interface:Port" || version != 3 || dict != &dictionary {
				t.Error("registry global callback lost arguments")
			}
		}),
		Remove: purego.NewCallback(func(data unsafe.Pointer, id uint32) {
			receivedRemove = true
			if data != listenerPointer || id != 31 {
				t.Error("registry remove callback lost arguments")
			}
		}),
	}
	registryMethods := privateRegistryMethods{AddListener: purego.NewCallback(func(object unsafe.Pointer, hook *privateHook, events *privateRegistryEvents, data unsafe.Pointer) int32 {
		if object != registryPointer || hook != &registryHook || events != &registryEvents || events.Version != 0 {
			t.Error("registry listener did not receive cb.data, hook and events v0")
		}
		var global func(unsafe.Pointer, uint32, uint32, *byte, uint32, *privateDict)
		var remove func(unsafe.Pointer, uint32)
		purego.RegisterFunc(&global, events.Global)
		purego.RegisterFunc(&remove, events.Remove)
		global(data, 31, 0xf0, kind, 3, &dictionary)
		remove(data, 31)
		return -19
	})}
	registry := privateTestInterface(t, "Registry", unsafe.Pointer(&registryMethods), registryPointer)
	var registryRequested, objectCreated bool
	coreMethods := privateCoreMethods{
		AddListener: purego.NewCallback(func(object unsafe.Pointer, hook *privateHook, events *privateCoreEvents, data unsafe.Pointer) int32 {
			if object != corePointer || hook != &coreHook || events != &coreEvents || events.Version != 0 || events.Info != 0 || events.BoundProps != 0 {
				t.Error("core listener did not receive cb.data, hook, events v0 with nullable callbacks")
			}
			var callback func(unsafe.Pointer, uint32, int32, int32, *byte)
			purego.RegisterFunc(&callback, events.Error)
			callback(data, 17, -23, -5, message)
			return -13
		}),
		GetRegistry: purego.NewCallback(func(object unsafe.Pointer, version uint32, size uintptr) *privateInterface {
			registryRequested = true
			if object != corePointer || version != 3 || size != 29 {
				t.Error("get_registry did not receive cb.data and arguments")
			}
			return registry
		}),
		CreateObject: purego.NewCallback(func(object unsafe.Pointer, factory, typ *byte, version uint32, props *privateDict, size uintptr) uintptr {
			objectCreated = true
			if object != corePointer || unix.BytePtrToString(factory) != "link-factory" || unix.BytePtrToString(typ) != "PipeWire:Interface:Link" || version != 3 || props != &dictionary || size != 37 {
				t.Error("create_object did not receive cb.data and arguments")
			}
			return 0x1234
		}),
	}
	core := privateTestInterface(t, "Core", unsafe.Pointer(&coreMethods), corePointer)
	// Unused intermediate method slots MUST remain zero, not collapse the ABI.
	if coreMethods.Hello != 0 || coreMethods.Sync != 0 || coreMethods.Pong != 0 || coreMethods.Error != 0 || coreMethods.Destroy != 0 || registryMethods.Bind != 0 || registryMethods.Destroy != 0 {
		t.Fatal("fake method tables must have nullable gaps")
	}
	for _, version := range []uint32{0, 7} {
		coreMethods.Version, registryMethods.Version = version, version
		coreCalls, err := privateBindCore(core)
		if err != nil {
			t.Fatal(err)
		}
		if code := coreCalls.addListener(core.Callbacks.Data, &coreHook, &coreEvents, listenerPointer); code != -13 {
			t.Fatal("core listener return code", code)
		}
		got := coreCalls.getRegistry(core.Callbacks.Data, 3, 29)
		if got != registry {
			t.Fatal("get_registry return pointer", got)
		}
		registryCalls, err := privateBindRegistry(got)
		if err != nil {
			t.Fatal(err)
		}
		if code := registryCalls.addListener(got.Callbacks.Data, &registryHook, &registryEvents, listenerPointer); code != -19 {
			t.Fatal("registry listener return code", code)
		}
		if proxy := coreCalls.createObject(core.Callbacks.Data, "link-factory", "PipeWire:Interface:Link", 3, &dictionary, 37); proxy != 0x1234 {
			t.Fatal("create_object return pointer", proxy)
		}
	}
	if !registryRequested || !objectCreated || !receivedError || !receivedGlobal || !receivedRemove {
		t.Fatal("one or more native methods/callbacks were not actually invoked")
	}
	runtime.KeepAlive(core)
	runtime.KeepAlive(registry)
	runtime.KeepAlive(message)
	runtime.KeepAlive(kind)
}

func TestPrivateGraphRejectsUnavailableABI(t *testing.T) {
	stub := purego.NewCallback(func() int32 { return 0 })
	for _, missing := range []string{"core.add_listener", "core.get_registry", "core.create_object", "registry.add_listener", "core.table", "registry.table", "core.type", "registry.type", "core.version", "registry.version", "core.nil", "registry.nil"} {
		t.Run(missing, func(t *testing.T) {
			cm := privateCoreMethods{AddListener: stub, GetRegistry: stub, CreateObject: stub}
			rm := privateRegistryMethods{AddListener: stub}
			core := privateTestInterface(t, "Core", unsafe.Pointer(&cm), nil)
			registry := privateTestInterface(t, "Registry", unsafe.Pointer(&rm), nil)
			want := ""
			switch missing {
			case "core.add_listener":
				cm.AddListener, want = 0, missing
			case "core.get_registry":
				cm.GetRegistry, want = 0, missing
			case "core.create_object":
				cm.CreateObject, want = 0, missing
			case "registry.add_listener":
				rm.AddListener, want = 0, missing
			case "core.table":
				core.Callbacks.Funcs, want = nil, "missing SPA method table"
			case "registry.table":
				registry.Callbacks.Funcs, want = nil, "missing SPA method table"
			case "core.type":
				core.Type, want = nil, "interface type"
			case "registry.type":
				registry.Type, want = nil, "interface type"
			case "core.version":
				core.Version, want = 2, "interface version"
			case "registry.version":
				registry.Version, want = 2, "interface version"
			case "core.nil":
				core, want = nil, "nil SPA interface"
			case "registry.nil":
				registry, want = nil, "nil SPA interface"
			}
			var err error
			if strings.HasPrefix(missing, "core.") {
				_, err = privateBindCore(core)
			} else {
				_, err = privateBindRegistry(registry)
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("missing ABI must fail explicitly, without fallback: %v, want %q", err, want)
			}
		})
	}
	var method func() int32
	if err := privateBindMethod(&method, stub, 0, 1, "future.method"); err == nil || !strings.Contains(err.Error(), "method table version") || method != nil {
		t.Fatal("unsupported method version must fail before binding", err)
	}
	if err := privateBindMethod(&method, stub, 1, 1, "future.method"); err != nil || method() != 0 {
		t.Fatal("supported method version was not bound", err)
	}
}
