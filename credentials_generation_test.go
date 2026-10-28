package sendspin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCredentialGenerationUsesCSPRNGAndDistinctDeviceSecrets(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(here), "credentials.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var private, pairing bool
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "NewCredentials" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			if sel.Sel.Name == "GenerateKey" {
				arg, ok := call.Args[0].(*ast.SelectorExpr)
				if ok {
					pkg, ok := arg.X.(*ast.Ident)
					private = ok && pkg.Name == "rand" && arg.Sel.Name == "Reader"
				}
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "rand" && sel.Sel.Name == "Read" {
				if slice, ok := call.Args[0].(*ast.SliceExpr); ok {
					if field, ok := slice.X.(*ast.SelectorExpr); ok && field.Sel.Name == "PairingPSK" {
						pairing = true
					}
				}
			}
			return true
		})
	}
	var imported bool
	for _, imp := range f.Imports {
		if imp.Path.Value == `"crypto/rand"` && imp.Name == nil {
			imported = true
		}
	}
	if !private || !pairing || !imported {
		t.Fatal("credential generation no longer draws both secrets from crypto/rand")
	}
	keys, psks, ids := map[Secret]bool{}, map[Secret]bool{}, map[string]bool{}
	for range 64 {
		c, err := NewCredentials()
		if err != nil || c.PrivateKey == (Secret{}) || c.PairingPSK == (Secret{}) || c.PairingPSK == sentinelPSK || c.PrivateKey == c.PairingPSK || keys[c.PrivateKey] || psks[c.PairingPSK] || ids[c.ClientID()] {
			t.Fatal("empty/shared/reused device credentials", err)
		}
		keys[c.PrivateKey], psks[c.PairingPSK], ids[c.ClientID()] = true, true, true
	}
	// Source-policy and duplicate/default regressions, not statistical entropy
	// estimation or independent proof of the operating system CSPRNG.
}
