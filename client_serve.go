package sendspin

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/echocat/sendspin-player-go/discovery"
)

// RecommendedListenAddress is RC1's suggested server-initiated client port.
// Serve accepts a caller-owned listener and never binds this address implicitly.
const RecommendedListenAddress = ":8928"

// DefaultWebSocketPath is the endpoint mounted and advertised by Serve.
const DefaultWebSocketPath = "/sendspin"

// Serve owns listener until ctx is canceled. It mounts /sendspin and announces
// it over mDNS unless DisableDiscovery is set. A context canceled at shutdown
// also interrupts upgraded WebSocket handlers, which http.Server.Shutdown alone
// cannot do.
func (c *Client) Serve(ctx context.Context, listener net.Listener) error {
	if listener == nil {
		return errors.New("sendspin: nil listener")
	}
	if err := c.enter("inbound"); err != nil {
		return err
	}
	defer c.leave()
	defer func() { _ = listener.Close() }()
	if err := ctx.Err(); err != nil {
		return err
	}
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var discoveryErrors <-chan error
	if !c.cfg.DisableDiscovery {
		service, err := c.serviceForListener(listener)
		if err != nil {
			return err
		}
		ad, err := discovery.AdvertiseClient(service)
		if err != nil {
			return err
		}
		defer ad.Close()
		discoveryErrors = ad.Errors()
	}
	var handlers sync.WaitGroup
	var lifecycle sync.Mutex
	stopping := false
	mux := http.NewServeMux()
	mux.HandleFunc(DefaultWebSocketPath, func(w http.ResponseWriter, r *http.Request) {
		lifecycle.Lock()
		if stopping {
			lifecycle.Unlock()
			http.Error(w, "player is stopping", http.StatusServiceUnavailable)
			return
		}
		handlers.Add(1)
		lifecycle.Unlock()
		defer handlers.Done()
		c.ServeHTTP(w, r)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan struct{})
	watcherDone := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() { lifecycle.Lock(); stopping = true; lifecycle.Unlock(); cancel(); _ = server.Close() })
	}
	go func() {
		defer close(watcherDone)
		for {
			select {
			case <-ctx.Done():
				stop()
				return
			case <-done:
				return
			case err, ok := <-discoveryErrors:
				if !ok {
					discoveryErrors = nil
					continue
				}
				c.emit(Event{Kind: "discovery_error", Err: err})
			}
		}
	}()
	err := server.Serve(listener)
	stop()
	close(done)
	<-watcherDone
	// Wait only for this listener's handlers, not unrelated HTTP servers that
	// happen to expose the same Client through ServeHTTP.
	handlers.Wait()
	if parent.Err() != nil && errors.Is(err, http.ErrServerClosed) {
		return parent.Err()
	}
	return err
}

// DiscoverAndRun chooses the first usable server and reconnects to it. To offer
// an application-specific selection UI, call discovery.Browse and Run instead.
func (c *Client) DiscoverAndRun(ctx context.Context) error {
	if err := c.enter("outbound"); err != nil {
		return err
	}
	defer c.leave()
	browseCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	found := make(chan discovery.Endpoint, 1)
	done := make(chan error, 1)
	go func() {
		done <- discovery.Browse(browseCtx, nil, func(e discovery.Endpoint) {
			select {
			case found <- e:
			default:
			}
		})
	}()
	select {
	case <-ctx.Done():
		cancel()
		<-done // Cleanup: cancellation is primary; still wait for lookup.
		return ctx.Err()
	case err := <-done:
		return err
	case e := <-found:
		cancel()
		<-done // Cleanup: retain the selected endpoint after stopping lookup.
		// Keep the mode reservation; share Run's failure/backoff policy.
		return c.run(ctx, e.URL)
	}
}
