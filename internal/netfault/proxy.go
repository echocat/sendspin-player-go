// Package netfault provides a loopback-only, ordered TCP impairment fixture.
// It never configures host networking and never inspects/decrypts media or keys.
package netfault

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type Profile struct {
	Delay, Jitter  time.Duration
	BytesPerSecond int
	ChunkBytes     int
}

type Proxy struct {
	listener                net.Listener
	target                  string
	profile                 Profile
	ctx                     context.Context
	cancel                  context.CancelFunc
	acceptDone              chan struct{}
	wg                      sync.WaitGroup
	once                    sync.Once
	mu                      sync.Mutex
	connections             map[net.Conn]context.CancelFunc
	accepted, active, bytes atomic.Int64
	blackhole               atomic.Bool
	stalled                 chan struct{}
	stallOnce               sync.Once
}

func New(target string, profile Profile) (*Proxy, error) {
	host, _, err := net.SplitHostPort(target)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return nil, errors.New("netfault: target must be an explicit loopback IP endpoint")
	}
	if profile.Delay < 0 || profile.Jitter < 0 || profile.BytesPerSecond < 0 || profile.ChunkBytes < 0 || profile.ChunkBytes > 32<<10 || profile.Delay > time.Second || profile.Jitter > time.Second {
		return nil, errors.New("netfault: invalid bounded profile")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Proxy{listener: l, target: target, profile: profile, ctx: ctx, cancel: cancel, acceptDone: make(chan struct{}), connections: map[net.Conn]context.CancelFunc{}, stalled: make(chan struct{})}
	go func() {
		defer close(p.acceptDone)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			p.wg.Go(func() { p.forward(c) })
		}
	}()
	return p, nil
}

func (p *Proxy) Address() string          { return p.listener.Addr().String() }
func (p *Proxy) Accepted() int64          { return p.accepted.Load() }
func (p *Proxy) Active() int64            { return p.active.Load() }
func (p *Proxy) Bytes() int64             { return p.bytes.Load() }
func (p *Proxy) Stall()                   { p.blackhole.Store(true) }
func (p *Proxy) Stalled() <-chan struct{} { return p.stalled }
func (p *Proxy) DropConnections() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for c, cancel := range p.connections {
		cancel()
		_ = c.Close()
	}
}
func (p *Proxy) Close() {
	p.once.Do(func() { p.cancel(); _ = p.listener.Close(); <-p.acceptDone; p.DropConnections(); p.wg.Wait() })
}

func (p *Proxy) forward(client net.Conn) {
	ctx, cancel := context.WithCancel(p.ctx)
	defer cancel()
	defer func() { _ = client.Close() }()
	peer, err := (&net.Dialer{Timeout: time.Second}).DialContext(p.ctx, "tcp", p.target)
	if err != nil {
		return
	}
	defer func() { _ = peer.Close() }()
	p.mu.Lock()
	if p.ctx.Err() != nil {
		p.mu.Unlock()
		return
	}
	p.connections[client], p.connections[peer] = cancel, cancel
	p.mu.Unlock()
	p.accepted.Add(1)
	p.active.Add(1)
	defer func() {
		p.active.Add(-1)
		p.mu.Lock()
		delete(p.connections, client)
		delete(p.connections, peer)
		p.mu.Unlock()
	}()
	done := make(chan struct{})
	go func() { p.copy(ctx, peer, client); cancel(); _ = peer.Close(); _ = client.Close(); close(done) }()
	p.copy(ctx, client, peer)
	cancel()
	_ = peer.Close()
	_ = client.Close()
	<-done
}

func (p *Proxy) copy(ctx context.Context, dst, src net.Conn) {
	size := p.profile.ChunkBytes
	if size == 0 {
		size = 4096
	}
	buf := make([]byte, size)
	var step int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if p.blackhole.Load() {
				p.stallOnce.Do(func() { close(p.stalled) })
				<-ctx.Done()
				return
			}
			wait := p.profile.Delay
			// A fixed periodic trace is reproducible without shared random state.
			if p.profile.Jitter > 0 {
				wait += time.Duration(step%5) * p.profile.Jitter / 4
			}
			step++
			if p.profile.BytesPerSecond > 0 {
				wait += time.Duration(n) * time.Second / time.Duration(p.profile.BytesPerSecond)
			}
			if wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			for sent := 0; sent < n; {
				written, e := dst.Write(buf[sent:n])
				p.bytes.Add(int64(written))
				sent += written
				if e != nil || written == 0 {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}
