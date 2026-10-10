// Package bgpsessions runs the external BGP sessions of the WAN providers.
package bgpsessions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/bgp"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/forwardingready"
	"goodkind.io/mwan/internal/ifmgr"
)

const moduleName = "bgp_sessions"

// Session is one configured session, and an empty EndpointDest selects an interface without a tunnel.
type Session struct {
	ifmgr.WANRef
	Config        bgp.SessionConfig
	BackupFor     map[netip.Prefix][]connectionid.ID
	EndpointDest  string
	EndpointTable int
}

// Config lists the sessions, the journal file of sessions that installed kernel routes, and the configured IPv6 main-table routes.
type Config struct {
	Sessions         []Session
	StateFile        string
	ConfiguredRoutes []ConfiguredRoute
}

// ConfiguredRoute is one configured IPv6 main-table route, and an empty Via matches every next hop.
type ConfiguredRoute struct {
	Interface string
	Dest      string
	Via       string
	Metric    int
}

// ModuleConfigName returns the registry key that the daemon uses to select this config.
func (Config) ModuleConfigName() string { return moduleName }

type managedSession struct {
	cfg       Session
	session   *bgp.Session
	running   bool
	transport string
}

// Module starts, stops, and advertises through one bgp.Session for each configured session.
type Module struct {
	ifmgr.BaseModule

	cfg Config
	// Init assigns sessions once. Listeners read the slice without the module mutex.
	sessions []*managedSession
	// Reconcile locks the module mutex before Session.Stop calls the listeners.
	// A listener must lock only publishMu.
	publishMu sync.Mutex
	owners    []routeOwner
	stopped   bool
	done      chan struct{}
}

// Init creates the stopped sessions and continues with an empty journal after a journal read error.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := m.InitBase(env, "module", moduleName)
	if m.cfg.StateFile == "" {
		if len(m.cfg.Sessions) > 0 {
			err := errors.New("bgp_sessions: a configured bgp-session requires [ifmgr.modules.bgp_sessions] state_file")
			log.ErrorContext(ctx, "bgp_sessions: state_file is absent", "sessions", len(m.cfg.Sessions), "err", err)
			return err
		}
		log.DebugContext(ctx, "bgp_sessions: module disabled without a session and a state file")
		return fmt.Errorf("%w: bgp_sessions: no session and no state_file", ifmgr.ErrModuleDisabled)
	}
	owners, err := loadJournal(m.cfg.StateFile)
	if err != nil {
		log.ErrorContext(ctx, "bgp_sessions: journal unusable; routes of an earlier process stay in the kernel", "err", err)
		owners = nil
	}
	m.owners = owners
	for _, cfg := range m.cfg.Sessions {
		sessionConfig := cfg.Config
		sessionConfig.Interface = cfg.Iface
		sessionConfig.Tables = []int{unix.RT_TABLE_MAIN}
		session, err := bgp.NewSession(sessionConfig, log)
		if err != nil {
			log.ErrorContext(ctx, "bgp_sessions: session configuration rejected", "connection_id", cfg.Key(), "err", err)
			return fmt.Errorf("bgp_sessions: connection %s: %w", cfg.Key(), err)
		}
		session.OnChange(m.listener(cfg))
		m.sessions = append(m.sessions, &managedSession{cfg: cfg, session: session, running: false, transport: ""})
	}
	for _, entry := range m.sessions {
		m.publish(entry.cfg.Key())
	}
	done := make(chan struct{})
	m.Lock()
	m.done = done
	m.Unlock()
	go func() {
		defer close(done)
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Error("bgp_sessions: shutdown panicked", "err", fmt.Errorf("panic: %v", recovered))
			}
		}()
		m.stopOnShutdown(ctx, log)
	}()
	return nil
}

func (m *Module) stopOnShutdown(ctx context.Context, log *slog.Logger) {
	<-ctx.Done()
	m.Lock()
	defer m.Unlock()
	m.stopped = true
	for _, entry := range m.sessions {
		m.stopLocked(context.WithoutCancel(ctx), log, entry)
	}
}

// Wait returns after the shutdown goroutine stops every session, or at once for a module without Init.
func (m *Module) Wait() {
	m.Lock()
	done := m.done
	m.Unlock()
	if done != nil {
		<-done
	}
}

// ForwardingFailureImpact reports no impact because a session that fails to start already makes its provider's IPv6 family not ready.
func (m *Module) ForwardingFailureImpact() forwardingready.State {
	return forwardingready.State{IPv4: false, IPv6: false}
}

// New returns a module without sessions for a nil config and rejects a config of another type.
func New(cfg ifmgr.ModuleConfig) (ifmgr.Module, error) {
	typed := Config{Sessions: nil, StateFile: "", ConfiguredRoutes: nil}
	if cfg != nil {
		value, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("bgp_sessions: invalid config type %T", cfg)
		}
		typed = value
	}
	return &Module{
		BaseModule: ifmgr.NewBaseModule(moduleName),
		cfg:        typed,
		sessions:   nil,
		publishMu:  sync.Mutex{},
		owners:     nil,
		stopped:    false,
		done:       nil,
	}, nil
}

func init() { ifmgr.Register(moduleName, New) }
