package netfault

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/internal/monotime"
)

func TestLoopbackProxyPreservesBytesPacesAndClosesWorkers(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, e := l.Accept()
		if e == nil {
			defer func() { _ = c.Close() }()
			_, _ = io.Copy(c, c)
		}
	}()
	p, err := New(l.Addr().String(), Profile{Delay: time.Millisecond, Jitter: time.Millisecond, BytesPerSecond: 100_000, ChunkBytes: 7})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c, err := net.Dial("tcp", p.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err = c.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte{0, 255, 17, 42}, 8)
	at := monotime.Now()
	if _, err = c.Write(data); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(data))
	if _, err = io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	p.DropConnections()
	p.Close()
	p.Close()
	if !bytes.Equal(data, got) || monotime.Since(at) < 4*time.Millisecond || p.Bytes() != int64(2*len(data)) {
		t.Fatal("unpaced/lost bytes", p.Bytes())
	}
	if p.Active() != 0 || p.Accepted() != 1 {
		t.Fatal("unjoined workers")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("peer leaked")
	}
}

func TestProxyRejectsNonLoopbackAndInvalidProfiles(t *testing.T) {
	for _, target := range []string{"example.com:80", "0.0.0.0:80", "192.0.2.1:80", "malformed"} {
		if p, e := New(target, Profile{}); e == nil {
			p.Close()
			t.Fatal(target)
		}
	}
	if p, e := New("127.0.0.1:1", Profile{Delay: -1}); e == nil {
		p.Close()
		t.Fatal("negative delay")
	}
}
