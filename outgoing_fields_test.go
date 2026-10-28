package sendspin

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

var outgoingPayloadFields = map[string]string{
	"client/init": "client_id version suite", "noise/handshake": "data",
	"client/hello": "name device_info supported_roles player@v1_support visualizer@v1_support supported_pair_methods unpaired_access",
	"client/state": "available player artwork visualizer", "client/time": "client_transmitted",
	"client/command": "controller", "client/leave": "", "client/goodbye": "reason",
	"client/pair-init": "pairing_index", "client/pair-finalize": "long_term_psk", "pair/abort": "reason",
}

func exactAllowedKeys(raw json.RawMessage, fields string) (map[string]json.RawMessage, error) {
	var p map[string]json.RawMessage
	if json.Unmarshal(raw, &p) != nil || p == nil {
		return nil, errors.New("outgoing object is not an object")
	}
	allowed := map[string]bool{}
	for _, key := range strings.Fields(fields) {
		allowed[key] = true
	}
	for key := range p {
		if !allowed[key] {
			return nil, fmt.Errorf("undefined outgoing key %q", key)
		}
	}
	return p, nil
}

func checkOutgoingPayload(kind string, raw json.RawMessage) error {
	fields, ok := outgoingPayloadFields[kind]
	if !ok {
		return fmt.Errorf("unreviewed outgoing message kind %q", kind)
	}
	p, err := exactAllowedKeys(raw, fields)
	if err != nil {
		return err
	}
	check := func(parent map[string]json.RawMessage, key, fields string) (map[string]json.RawMessage, error) {
		if value, ok := parent[key]; ok {
			return exactAllowedKeys(value, fields)
		}
		return nil, nil
	}
	formatFields := "codec sample_rate channels bit_depth"
	if kind == "client/hello" {
		if _, err = check(p, "device_info", "product_name manufacturer software_version mac_address"); err != nil {
			return err
		}
		if _, err = check(p, "visualizer@v1_support", "buffer_capacity"); err != nil {
			return err
		}
		if _, err = check(p, "unpaired_access", "enabled"); err != nil {
			return err
		}
		if methods, err := check(p, "supported_pair_methods", "pairing_psk"); err != nil {
			return err
		} else if methods != nil {
			if _, err = check(methods, "pairing_psk", "locations"); err != nil {
				return err
			}
		}
		if support, err := check(p, "player@v1_support", "supported_formats buffer_capacity"); err != nil {
			return err
		} else if support != nil {
			var formats []json.RawMessage
			if json.Unmarshal(support["supported_formats"], &formats) != nil {
				return errors.New("invalid outgoing format array")
			}
			for _, f := range formats {
				if _, err = exactAllowedKeys(f, formatFields); err != nil {
					return err
				}
			}
		}
	}
	if kind == "client/state" {
		if player, err := check(p, "player", "volume muted output_delay_ms required_lead_time_ms min_buffer_ms supported_commands format"); err != nil {
			return err
		} else if player != nil {
			if _, err = check(player, "format", formatFields); err != nil {
				return err
			}
		}
		if artwork, err := check(p, "artwork", "channels"); err != nil {
			return err
		} else if artwork != nil {
			var channels []json.RawMessage
			if json.Unmarshal(artwork["channels"], &channels) != nil {
				return errors.New("invalid outgoing artwork array")
			}
			for _, channel := range channels {
				if _, err = exactAllowedKeys(channel, "source format width height"); err != nil {
					return err
				}
			}
		}
		if visual, err := check(p, "visualizer", "types rate_max spectrum"); err != nil {
			return err
		} else if visual != nil {
			if _, err = check(visual, "spectrum", "n_disp_bins scale f_min f_max"); err != nil {
				return err
			}
		}
	}
	if kind == "client/command" {
		if _, err = check(p, "controller", "command volume mute position_ms offset_ms"); err != nil {
			return err
		}
	}
	return nil
}

func TestAllOutgoingMessageCallSitesHaveAnExplicitFieldInventory(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	root := filepath.Dir(here)
	var files []string
	for _, dir := range []string{root, filepath.Join(root, "internal/wire")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go") {
				files = append(files, filepath.Join(dir, entry.Name()))
			}
		}
	}
	seen := map[string]bool{}
	for _, path := range files {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				index := -1
				if sel.Sel.Name == "send" {
					index = 0
				}
				if sel.Sel.Name == "JSON" {
					index = 1
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "protocol" && sel.Sel.Name == "Encode" {
					index = 0
				}
				if index < 0 {
					return true
				}
				if len(call.Args) <= index {
					t.Fatal("outgoing call lost discriminator")
				}
				if literal, ok := call.Args[index].(*ast.BasicLit); ok && literal.Kind == token.STRING {
					kind, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatal(err)
					}
					if _, ok = outgoingPayloadFields[kind]; !ok {
						t.Fatal("new outgoing message has no explicit field inventory", kind)
					}
					seen[kind] = true
				} else {
					// The sole generic bridge forwards session.send to Channel.JSON,
					// and Channel.JSON to protocol.Encode. Public operations never take
					// a caller-supplied message discriminator.
					if !((filepath.Base(path) == "session.go" && fn.Name.Name == "send") || (filepath.Base(path) == "channel.go" && fn.Name.Name == "JSON")) {
						t.Fatal("unreviewed dynamic outgoing discriminator", path, fn.Name.Name)
					}
				}
				return true
			})
		}
	}
	if len(seen) != len(outgoingPayloadFields) {
		t.Fatal("outgoing inventory and actual call sites differ", seen)
	}
}
