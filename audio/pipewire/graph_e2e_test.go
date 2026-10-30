//go:build e2e && linux && (amd64 || arm64)

package pipewire

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/echocat/sendspin-player-go/audio/internal/dynlib"
	"golang.org/x/sys/unix"
)

type privateGraph struct {
	api              *api
	libraryVersion   string
	loop, context    uintptr
	core, registry   *privateInterface
	contextNew       func(uintptr, uintptr, uintptr) uintptr
	contextDestroy   func(uintptr)
	connect          func(uintptr, uintptr, uintptr) *privateInterface
	disconnect       func(*privateInterface) int32
	coreCalls        privateCoreCalls
	registryCalls    privateRegistryCalls
	propsNew         func(uintptr) *privateProperties
	propsSet         func(*privateProperties, string, string) int32
	propsFree        func(*privateProperties)
	proxyDestroy     func(uintptr)
	streamDisconnect func(uintptr) int32
	hook, coreHook   privateHook
	events           privateRegistryEvents
	coreEvents       privateCoreEvents
	globals          map[uint32]map[string]string
	links            map[[2]uint32]uintptr
	err              error // guarded by the native loop mutex, just like the maps
	changed          chan struct{}
}

func openPrivateGraph(ctx context.Context) (_ *privateGraph, result error) {
	a, err := loadAPI("")
	if err != nil {
		return nil, err
	}
	lib, err := dynlib.Open("", "libpipewire-0.3.so.0")
	if err != nil {
		return nil, err
	}
	g := &privateGraph{api: a, globals: map[uint32]map[string]string{}, links: map[[2]uint32]uintptr{}, changed: make(chan struct{}, 1)}
	var libraryVersion func() *byte
	for _, binding := range []struct {
		dst  any
		name string
	}{
		{&libraryVersion, "pw_get_library_version"},
		{&g.contextNew, "pw_context_new"}, {&g.contextDestroy, "pw_context_destroy"},
		{&g.connect, "pw_context_connect"}, {&g.disconnect, "pw_core_disconnect"},
		{&g.propsNew, "pw_properties_new"}, {&g.propsSet, "pw_properties_set"}, {&g.propsFree, "pw_properties_free"},
		{&g.proxyDestroy, "pw_proxy_destroy"}, {&g.streamDisconnect, "pw_stream_disconnect"},
	} {
		if err = lib.Bind(binding.dst, binding.name); err != nil {
			return nil, err
		}
	}
	g.libraryVersion = unix.BytePtrToString(libraryVersion())
	defer func() {
		if result != nil {
			g.Close()
		}
	}()
	g.loop = a.loopNew("sendspin-test-graph", 0)
	if g.loop == 0 {
		return nil, fmt.Errorf("private graph loop allocation failed")
	}
	g.context = g.contextNew(a.loopGetLoop(g.loop), 0, 0)
	if g.context == 0 {
		return nil, fmt.Errorf("private graph context allocation failed")
	}
	g.core = g.connect(g.context, 0, 0)
	if g.core == nil {
		return nil, fmt.Errorf("private graph connection failed")
	}
	if g.coreCalls, err = privateBindCore(g.core); err != nil {
		return nil, err
	}
	g.coreEvents.Error = purego.NewCallback(func(_ uintptr, id uint32, seq, code int32, message *byte) {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		g.err = fmt.Errorf("private graph native error id=%d seq=%d code=%d: %s", id, seq, code, unix.BytePtrToString(message))
		g.notify()
	})
	if code := g.coreCalls.addListener(g.core.Callbacks.Data, &g.coreHook, &g.coreEvents, nil); code < 0 {
		return nil, pwError("graph core listener", code)
	}
	g.registry = g.coreCalls.getRegistry(g.core.Callbacks.Data, 3, 0)
	if g.registry == nil {
		return nil, fmt.Errorf("private graph registry allocation failed")
	}
	if g.registryCalls, err = privateBindRegistry(g.registry); err != nil {
		return nil, err
	}
	g.events.Global = purego.NewCallback(func(_ uintptr, id, permissions uint32, kind *byte, version uint32, dict *privateDict) {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		props := map[string]string{"interface": unix.BytePtrToString(kind)}
		if dict != nil && dict.NItems <= 4096 {
			for _, item := range unsafe.Slice(dict.Items, dict.NItems) {
				props[unix.BytePtrToString(item.Key)] = unix.BytePtrToString(item.Value)
			}
		}
		g.globals[id] = props
		g.linkPorts()
		g.notify()
	})
	g.events.Remove = purego.NewCallback(func(_ uintptr, id uint32) {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		delete(g.globals, id)
		for ports, proxy := range g.links {
			if ports[0] == id || ports[1] == id {
				g.proxyDestroy(proxy)
				delete(g.links, ports)
			}
		}
		g.notify()
	})
	if code := g.registryCalls.addListener(g.registry.Callbacks.Data, &g.hook, &g.events, nil); code < 0 {
		return nil, pwError("graph registry listener", code)
	}
	if code := a.loopStart(g.loop); code < 0 {
		return nil, pwError("graph loop start", code)
	}
	for {
		g.lock()
		ready, graphErr := g.sinkReady(), g.err
		g.unlock()
		if graphErr != nil {
			return nil, graphErr
		}
		if ready {
			return g, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("private null graph not ready: %w; registry: %s", ctx.Err(), g.snapshot())
		case <-g.changed:
		}
	}
}

func (g *privateGraph) lock()   { runtime.LockOSThread(); g.api.loopLock(g.loop) }
func (g *privateGraph) unlock() { g.api.loopUnlock(g.loop); runtime.UnlockOSThread() }
func (g *privateGraph) notify() {
	select {
	case g.changed <- struct{}{}:
	default:
	}
}

func (g *privateGraph) nodeName(port map[string]string) string {
	id, _ := strconv.ParseUint(port["node.id"], 10, 32)
	return g.globals[uint32(id)]["node.name"]
}

func (g *privateGraph) sinkReady() bool {
	ports := 0
	for _, p := range g.globals {
		if p["port.direction"] == "in" && g.nodeName(p) == "sendspin-null" {
			ports++
		}
	}
	return ports == 2
}

// Called only by registry callbacks under the native loop's OS-thread-owned
// mutex. Links belong to this controller and disappear with their stream ports.
func (g *privateGraph) linkPorts() {
	for outID, out := range g.globals {
		if out["port.direction"] != "out" || g.nodeName(out) != "sendspin-player" {
			continue
		}
		for inID, in := range g.globals {
			// Production streams intentionally negotiate unpositioned channels
			// (UNK); pair their explicit port indices with the sink's FL/FR ports.
			if in["port.direction"] != "in" || g.nodeName(in) != "sendspin-null" || out["port.id"] == "" || out["port.id"] != in["port.id"] {
				continue
			}
			pair := [2]uint32{outID, inID}
			if g.links[pair] != 0 {
				continue
			}
			props := g.propsNew(0)
			if props == nil {
				g.err = fmt.Errorf("private graph link properties allocation failed")
				return
			}
			for key, value := range map[string]string{
				"link.output.node": out["node.id"], "link.output.port": strconv.FormatUint(uint64(outID), 10),
				"link.input.node": in["node.id"], "link.input.port": strconv.FormatUint(uint64(inID), 10),
				"object.linger": "false",
			} {
				if code := g.propsSet(props, key, value); code < 0 {
					g.err = pwError("graph link properties", code)
				}
			}
			if g.err == nil {
				// pw_properties embeds spa_dict as its first member. The native
				// allocation stays controller-owned; create_object only borrows it.
				proxy := g.coreCalls.createObject(g.core.Callbacks.Data, "link-factory", "PipeWire:Interface:Link", 3, &props.Dict, 0)
				if proxy == 0 {
					g.err = fmt.Errorf("private graph link factory failed for %v", pair)
				} else {
					g.links[pair] = proxy
				}
			}
			g.propsFree(props)
			runtime.KeepAlive(props)
		}
	}
}

func (g *privateGraph) snapshot() string {
	g.lock()
	defer g.unlock()
	var out strings.Builder
	fmt.Fprintf(&out, "error=%v links=%v globals=", g.err, g.links)
	for id, props := range g.globals {
		fmt.Fprintf(&out, "\n%d: %v", id, props)
	}
	return out.String()
}

func (g *privateGraph) failure() error {
	g.lock()
	defer g.unlock()
	return g.err
}

func (g *privateGraph) Close() {
	if g.loop != 0 {
		g.api.loopStop(g.loop)
	}
	if g.core != nil {
		g.disconnect(g.core)
		g.core = nil
		g.registry = nil
	}
	if g.context != 0 {
		g.contextDestroy(g.context)
		g.context = 0
	}
	if g.loop != 0 {
		g.api.loopDestroy(g.loop)
		g.loop = 0
	}
	// Keep callback/event storage alive until native callbacks have joined.
	runtime.KeepAlive(g)
}
