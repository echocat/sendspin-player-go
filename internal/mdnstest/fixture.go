// Package mdnstest contains the isolated real-multicast E2E fixture.
package mdnstest

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed Containerfile
var containerfile []byte

func fixtureFile(path string) bool {
	path = filepath.ToSlash(path)
	if path == "internal/e2e/.python-version" {
		return true
	}
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") || strings.HasPrefix(base, "_") {
		return false
	}
	if strings.HasSuffix(path, ".go") {
		return true
	}
	if path == "go.mod" || path == "go.sum" || path == "LICENSE" || path == "NOTICES.md" || path == "LICENSES/Apache-2.0.txt" || path == "internal/mdns/LICENSE" {
		return true
	}
	if path == "internal/mdnstest/Containerfile" {
		return true
	}
	if strings.HasPrefix(path, "internal/e2e/") {
		name := strings.TrimPrefix(path, "internal/e2e/")
		switch name {
		case "server.py", "media.py", "pairing_observer.py", "weak_playback.py", "source_controls.py", "pyproject.toml", "uv.lock", ".python-version", "go.mod":
			return true
		}
	}
	return false
}

func buildContext(root, destination string) (string, error) {
	hash := sha256.New()
	_, _ = hash.Write(containerfile)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if filepath.ToSlash(relative) == "audio/native" {
				return filepath.SkipDir // Unused selector source for this fixture.
			}
			if relative != "." && (strings.HasPrefix(entry.Name(), ".") || strings.HasPrefix(entry.Name(), "_") || entry.Name() == "var" || entry.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !fixtureFile(relative) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(hash, "\x00%s\x00", filepath.ToSlash(relative)) // hash.Hash.Write cannot fail.
		_, _ = hash.Write(data)
		output := filepath.Join(destination, "src", relative)
		if err = os.MkdirAll(filepath.Dir(output), 0700); err != nil {
			return err
		}
		return os.WriteFile(output, data, 0600)
	})
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(filepath.Join(destination, "Containerfile"), containerfile, 0600); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil))[:16], nil
}
