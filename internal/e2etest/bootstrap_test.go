package e2etest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
)

func TestPinnedUVBootstrapVerifiesAndExtractsOnlyExecutable(t *testing.T) {
	for _, zipped := range []bool{false, true} {
		t.Run(fmt.Sprintf("zip=%v", zipped), func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "var"), 0700); err != nil {
				t.Fatal(err)
			}
			name := "uv"
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			executable := []byte("pinned executable fixture")
			var archive bytes.Buffer
			ext := ".tar.gz"
			if zipped {
				ext = ".zip"
				writer := zip.NewWriter(&archive)
				entry, err := writer.Create("../outside/" + name)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := entry.Write(executable); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				gz := gzip.NewWriter(&archive)
				writer := tar.NewWriter(gz)
				if err := writer.WriteHeader(&tar.Header{Name: "../outside/" + name, Mode: 0700, Size: int64(len(executable)), Typeflag: tar.TypeReg}); err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write(executable); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				if err := gz.Close(); err != nil {
					t.Fatal(err)
				}
			}
			var downloads atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { downloads.Add(1); _, _ = w.Write(archive.Bytes()) }))
			defer server.Close()
			platform, arch := runtime.GOOS, runtime.GOARCH
			if platform == "darwin" {
				platform = "macos"
			}
			if arch == "amd64" {
				arch = "x64"
			}
			writeLock := func(sum [32]byte) {
				t.Helper()
				data := fmt.Sprintf("[tools.uv.\"platforms.%s-%s\"]\nchecksum = \"sha256:%x\"\nurl = \"%s/uv%s\"\n", platform, arch, sum, server.URL, ext)
				if err := os.WriteFile(filepath.Join(root, "mise.lock"), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeLock(sha256.Sum256([]byte("wrong digest")))
			if _, err := bootstrapUV(context.Background(), root); err == nil {
				t.Fatal("invalid checksum accepted")
			}
			writeLock(sha256.Sum256(archive.Bytes()))
			path, err := bootstrapUV(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, executable) {
				t.Fatal("wrong extracted executable")
			}
			if _, err := os.Stat(filepath.Join(root, "outside")); !os.IsNotExist(err) {
				t.Fatal("archive paths were extracted")
			}
			if _, err := bootstrapUV(context.Background(), root); err != nil {
				t.Fatal(err)
			}
			if downloads.Load() != 2 {
				t.Fatal("verified cached bootstrap was downloaded again")
			}
		})
	}
}
