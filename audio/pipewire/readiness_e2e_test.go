//go:build e2e && linux && (amd64 || arm64)

package pipewire

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestE2EPrivateSocketReadinessRequiresListener(t *testing.T) {
	path := filepath.Join(t.TempDir(), "socket")
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: path}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("bound socket did not create its path", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if privateSocketReady(ctx, path) {
		t.Fatal("bound socket was considered ready before listen")
	}
	if err := unix.Listen(fd, 1); err != nil {
		t.Fatal(err)
	}
	if !privateSocketReady(ctx, path) {
		t.Fatal("listening socket was not considered ready")
	}
	cancel()
	if privateSocketReady(ctx, path) {
		t.Fatal("socket readiness ignored cancellation")
	}
}
