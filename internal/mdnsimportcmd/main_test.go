package main

import (
	"strings"
	"testing"
)

func TestScopedImportFailsClosedOnMissingPatchAnchors(t *testing.T) {
	for _, name := range []string{"client.go", "server.go", "connection.go"} {
		if _, err := adapt(name, []byte("package zeroconf\n")); err == nil {
			t.Fatal("missing scope patch anchor accepted", name)
		}
	}
	if _, err := adapt("service.go", []byte("package other\n")); err == nil {
		t.Fatal("changed package anchor accepted")
	}
}

func TestImportedDiagnosticsUseRedactedFacadeAndGeneratedProvenance(t *testing.T) {
	input := []byte("package zeroconf\nimport \"log\"\nfunc f(){\n\tlog.Printf(\"SP:SECRET %s\", \"private-name\")\n}\n")
	data, err := adapt("service.go", input)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "SP:SECRET") || strings.Contains(text, `"log"`) || !strings.Contains(text, `logger.Debug("mDNS transport operation unavailable")`) || !strings.Contains(text, revision) || !strings.Contains(text, "DO NOT EDIT") {
		t.Fatal("import lost diagnostic/privacy/provenance boundary", text)
	}
}
