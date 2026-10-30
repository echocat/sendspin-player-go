package e2etest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Bootstrap only the executable from the OS archive pinned in mise.lock. No
// remote installation scripts, unpinned package managers or shell are involved.
func bootstrapUV(ctx context.Context, root string) (result string, resultErr error) {
	unlock, err := fileLock(ctx, filepath.Join(root, "var", "uv-bootstrap.lock"))
	if err != nil {
		return "", err
	}
	defer unlock()
	platform := runtime.GOOS
	if platform == "darwin" {
		platform = "macos"
	}
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	section := "[tools.uv.\"platforms." + platform + "-" + arch + "\"]"
	lock, err := os.ReadFile(filepath.Join(root, "mise.lock"))
	if err != nil {
		return "", err
	}
	var url, checksum string
	active := false
	for _, line := range strings.Split(string(lock), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			active = line == section
			continue
		}
		if !active {
			continue
		}
		key, value, ok := strings.Cut(line, " = ")
		if !ok {
			continue
		}
		value = strings.Trim(value, "\"")
		if key == "url" {
			url = value
		}
		if key == "checksum" {
			checksum = strings.TrimPrefix(value, "sha256:")
		}
	}
	if url == "" || len(checksum) != 64 {
		return "", fmt.Errorf("e2etest: uv prerequisite missing; install the mise.lock-pinned uv (no archive for %s/%s)", runtime.GOOS, runtime.GOARCH)
	}
	name := "uv"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dir := filepath.Join(root, "var", "tools", "uv-"+checksum)
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("e2etest: install pinned uv; archive download failed: %w", err)
	}
	// Read/status/hash errors are authoritative; closing a read-only HTTP body
	// must not replace them or turn a verified download into a failed bootstrap.
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("e2etest: pinned uv archive returned %s; install uv from mise.lock", response.Status)
	}
	const maxArchive = 100 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxArchive+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxArchive {
		return "", fmt.Errorf("e2etest: uv archive exceeds limit")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != checksum {
		return "", fmt.Errorf("e2etest: pinned uv archive checksum mismatch")
	}
	var executable []byte
	if strings.HasSuffix(url, ".zip") {
		reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return "", err
		}
		for _, entry := range reader.File {
			if filepath.Base(entry.Name) != name {
				continue
			}
			f, err := entry.Open()
			if err != nil {
				return "", err
			}
			executable, err = io.ReadAll(io.LimitReader(f, maxArchive+1))
			_ = f.Close()
			if err != nil {
				return "", err
			}
			break
		}
	} else {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return "", err
		}
		// This reader wraps memory, not an owned file. Archive integrity was
		// already checked against the pinned SHA256 above.
		defer func() { _ = gz.Close() }()
		reader := tar.NewReader(gz)
		for {
			entry, err := reader.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", err
			}
			if entry.Typeflag != tar.TypeReg || filepath.Base(entry.Name) != name {
				continue
			}
			executable, err = io.ReadAll(io.LimitReader(reader, maxArchive+1))
			if err != nil {
				return "", err
			}
			break
		}
	}
	if len(executable) == 0 || len(executable) > maxArchive {
		return "", fmt.Errorf("e2etest: uv archive has no bounded executable")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".uv-*")
	if err != nil {
		return "", err
	}
	defer func() {
		// Successful rename already removed this name. Otherwise retain both
		// the primary failure and a failure to remove our temporary executable.
		if err := os.Remove(f.Name()); err != nil && !os.IsNotExist(err) {
			resultErr = errors.Join(resultErr, fmt.Errorf("e2etest: remove temporary uv executable: %w", err))
		}
	}()
	_, writeErr := f.Write(executable)
	closeErr := f.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Chmod(f.Name(), 0700); err != nil {
		return "", err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}
