package wire

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHandshakeRandomnessAndResponderPolicyAreExplicit(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(here), "channel.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	imported := false
	for _, i := range f.Imports {
		if i.Path.Value == `"crypto/rand"` && i.Name == nil {
			imported = true
		}
	}
	configs := 0
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		typeName, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || typeName.Sel.Name != "Config" {
			return true
		}
		pkg, ok := typeName.X.(*ast.Ident)
		if !ok || pkg.Name != "noise" {
			return true
		}
		configs++
		var random, responder, pattern, placement bool
		for _, element := range lit.Elts {
			kv, ok := element.(*ast.KeyValueExpr)
			if !ok {
				t.Fatal("unkeyed crypto configuration")
			}
			key := kv.Key.(*ast.Ident).Name
			switch key {
			case "Random":
				v, ok := kv.Value.(*ast.SelectorExpr)
				if ok {
					p, ok := v.X.(*ast.Ident)
					random = ok && p.Name == "rand" && v.Sel.Name == "Reader"
				}
			case "Initiator":
				v, ok := kv.Value.(*ast.Ident)
				responder = ok && v.Name == "false"
			case "Pattern":
				v, ok := kv.Value.(*ast.SelectorExpr)
				pattern = ok && v.Sel.Name == "HandshakeKK"
			case "PresharedKeyPlacement":
				v, ok := kv.Value.(*ast.BasicLit)
				placement = ok && v.Value == "2"
			case "EphemeralKeypair", "PeerEphemeral":
				t.Fatal("configured/reused ephemeral material")
			}
		}
		if !random || !responder || !pattern || !placement {
			t.Fatal("changed KKpsk2 responder/randomness policy")
		}
		return true
	})
	if !imported || configs != 1 {
		t.Fatal("unreviewed crypto construction", configs)
	}
}
