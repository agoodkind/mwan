package resolver

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/mdlayher/ndp"
	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/netif"
)

type raSession struct {
	mu      sync.Mutex
	name    string
	index   int
	cancel  context.CancelFunc
	done    chan struct{}
	servers map[netip.Addr]map[netip.Addr]time.Time
	clock   clock.Clock
}

func (module *Module) routerDNS(ctx context.Context, id string, link netif.OwnedLinkResult, log *slog.Logger) []netip.Addr {
	session := module.ra[id]
	start := session == nil
	if session != nil {
		select {
		case <-session.done:
			session.cancel()
			start = true
		default:
		}
	}
	if session != nil && (session.index != link.IfIndex || session.name != link.ActualName) {
		session.cancel()
		session = nil
		start = true
	}
	if session == nil {
		session = &raSession{mu: sync.Mutex{}, name: link.ActualName, index: link.IfIndex, cancel: nil, done: nil, servers: map[netip.Addr]map[netip.Addr]time.Time{}, clock: module.clock}
		module.ra[id] = session
	}
	if start {
		ctx, cancel := context.WithCancel(ctx)
		session.cancel = cancel
		session.done = make(chan struct{})
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					log.ErrorContext(ctx, "resolver RA observer panicked", "iface", session.name, "err", recovered)
				}
			}()
			session.observe(ctx, log, module.Env.RequestReconcile)
		}()
	}
	return session.current(module.clock.Now())
}

func (session *raSession) current(now time.Time) []netip.Addr {
	session.mu.Lock()
	defer session.mu.Unlock()
	servers := []netip.Addr{}
	for _, router := range session.servers {
		for server, until := range router {
			if now.Before(until) && !slices.Contains(servers, server) {
				servers = append(servers, server)
			}
		}
	}
	slices.SortFunc(servers, netip.Addr.Compare)
	return servers
}

func (session *raSession) observe(ctx context.Context, log *slog.Logger, request func(string)) {
	defer close(session.done)
	client, err := netif.NewRAClient(session.name, log)
	if err != nil {
		return
	}
	defer client.Close()
	previous := session.current(session.clock.Now())
	solicit := true
	for ctx.Err() == nil {
		advertisement, router, err := client.ReceiveRA(ctx, time.Second, solicit)
		solicit = false
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if err == nil {
			session.update(router, advertisement, session.clock.Now())
		}
		current := session.current(session.clock.Now())
		if !slices.Equal(previous, current) && request != nil {
			request("router advertisement DNS changed")
		}
		previous = current
	}
}

func (session *raSession) update(router netip.Addr, advertisement *ndp.RouterAdvertisement, now time.Time) {
	session.mu.Lock()
	defer session.mu.Unlock()
	servers := session.servers[router]
	if servers == nil {
		servers = map[netip.Addr]time.Time{}
		session.servers[router] = servers
	}
	for _, option := range advertisement.Options {
		value, ok := option.(*ndp.RecursiveDNSServer)
		if !ok {
			continue
		}
		for _, server := range value.Servers {
			if !server.Is6() || server.IsUnspecified() || server.IsMulticast() {
				continue
			}
			if value.Lifetime == 0 {
				delete(servers, server)
			} else {
				servers[server] = now.Add(value.Lifetime)
			}
		}
	}
	for source, values := range session.servers {
		for server, until := range values {
			if !now.Before(until) {
				delete(values, server)
			}
		}
		if len(values) == 0 {
			delete(session.servers, source)
		}
	}
}
