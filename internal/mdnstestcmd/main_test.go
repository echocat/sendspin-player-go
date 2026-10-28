package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildContextContainsOnlyRequiredSources(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"go.mod", "go.sum", "client.go", "client_test.go", "device.json", "profile.cov", ".private.go", "_scratch/secret.go", ".git/config", ".idea/credentials.go", "_examples/player/main.go", "audio/portaudio/go.mod", "audio/portaudio/output.go", "internal/e2e/server.py", "internal/e2e/media.py", "internal/e2e/mdns.py", "internal/e2e/pairing_observer.py", "internal/e2e/uv.lock", "internal/e2e/.python-version", "internal/e2e/scratch.py", "var/tmp/private.go", "var/python/e2e/private.go", "var/conformance/capture.go"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	output := t.TempDir()
	digest, err := buildContext(root, output)
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) != 16 {
		t.Fatal(digest)
	}
	for _, name := range []string{"client.go", "internal/e2e/mdns.py", "internal/e2e/pairing_observer.py", "internal/e2e/uv.lock", "internal/e2e/.python-version"} {
		if _, err = os.Stat(filepath.Join(output, "src", name)); err != nil {
			t.Fatal("missing fixture asset", name, err)
		}
	}
	for _, name := range []string{"device.json", "profile.cov", ".private.go", "_scratch/secret.go", ".git/config", ".idea/credentials.go", "_examples/player/main.go", "audio/portaudio/go.mod", "audio/portaudio/output.go", "internal/e2e/scratch.py", "var/tmp/private.go", "var/python/e2e/private.go", "var/conformance/capture.go"} {
		if _, err = os.Stat(filepath.Join(output, "src", name)); !os.IsNotExist(err) {
			t.Fatal("copied private/non-fixture asset", name, err)
		}
	}
}
