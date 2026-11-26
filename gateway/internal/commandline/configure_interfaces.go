package commandline

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	systemddaemon "github.com/coreos/go-systemd/v22/daemon"

	"goodkind.io/mwan/gateway/connectionid"
	"goodkind.io/mwan/gateway/firewall"
	"goodkind.io/mwan/gateway/internal/commandline/commandconfig"
	"goodkind.io/mwan/gateway/internal/commandline/commandopts"
	"goodkind.io/mwan/gateway/internal/config"
	"goodkind.io/mwan/gateway/internal/ifmgr"
	"goodkind.io/mwan/gateway/internal/linkboot"
	"goodkind.io/mwan/gateway/internal/logging"
	"goodkind.io/mwan/gateway/internal/netif"
	"goodkind.io/mwan/gateway/internal/networkd"
	"goodkind.io/mwan/gateway/internal/notify"
	"goodkind.io/mwan/gateway/internal/tracing"
	"goodkind.io/mwan/gateway/internal/wanstate"
	"goodkind.io/mwan/gateway/networkjson"
	"goodkind.io/mwan/gateway/networkload"
	"goodkind.io/mwan/gateway/version"

	// Each imported module registers with ifmgr at initialization.
	"goodkind.io/mwan/gateway/internal/ifmgr/modules/addresses"
	_ "goodkind.io/mwan/gateway/internal/ifmgr/modules/autoconfiguration"
	"goodkind.io/mwan/gateway/internal/ifmgr/modules/bgpsessions"
	_ "goodkind.io/mwan/gateway/internal/ifmgr/modules/firewall"
	_ "goodkind.io/mwan/gateway/internal/ifmgr/modules/health"
	_ "goodkind.io/mwan/gateway/internal/ifmgr/modules/links"
	_ "goodkind.io/mwan/gateway/internal/ifmgr/modules/npt"
	_ "goodkind.io/mwan/gateway/internal/ifmgr/modules/resolver"
	_ "goodkind.io/mwan/gateway/internal/ifmgr/modules/steering"
	_ "goodkind.io/mwan/gateway/internal/ifmgr/modules/wanroutes"
)

const ifMgrWANRole = "wan"

const (
	defaultIfMgrReconcileInterval = 60 * time.Second
	hairpinAliasRequestTimeout    = 5 * time.Second
)

var errIfMgrRoleRequired = errors.New(
	"ifmgr: role required (set [ifmgr].role in config or pass --role)",
)

var (
	errUnsupportedHTTPTransport = errors.New("HTTP transport is")
	errMissingCertificatePEM    = errors.New("no PEM block found in certificate file")
	errRejectedCertificatePEM   = errors.New("certificate pool rejected PEM data")
	errCertificateHasNoDNSName  = errors.New("certificate has no DNS SAN")
	errIfMgrMultipleInterfaces  = errors.New("ifmgr: multi-iface not supported yet")
	errIfMgrInterfaceRequired   = errors.New("ifmgr: no [ifmgr.iface.<name>] section found")
	errWANFirewallPolicyAbsent  = errors.New("WAN firewall policy is absent")
)

func runIfMgrPath(
	ctx context.Context,
	commandPath string,
	path string,
	options commandopts.Ifmgr,
	output io.Writer,
) error {
	if options.Role == ifMgrWANRole {
		if err := bootstrapWANFirewall(ctx, options.NetworkPath); err != nil {
			slog.Error("ifmgr firewall bootstrap failed", "err", err)

			return fmt.Errorf("ifmgr: %w", err)
		}
	}

	err := commandconfig.WithConfig(
		commandPath,
		path,
		func(cfg *config.Config) error {
			return RunIfMgr(ctx, cfg, options, output, netif.StartDHCPClient)
		},
	)
	if err != nil {
		return fmt.Errorf("%w", err)
	}

	return nil
}

// RunIfMgr expects the caller to load the configuration for the chosen role.
//
// The WAN command installs firewall bootstrap rules before it calls RunIfMgr.
// It passes the production DHCPv4 starter to acquire and renew leases.
//
//	RunIfMgr(ctx, cfg, flags, output, netif.StartDHCPClient)
func RunIfMgr(
	parent context.Context,
	cfg *config.Config,
	flags commandopts.Ifmgr,
	output io.Writer,
	startDHCPv4 addresses.DHCPv4Starter,
) error {
	role, err := ifMgrRole(cfg, flags)
	if err != nil {
		return err
	}

	if flags.NetworkPath == "" {
		flags.NetworkPath = networkjson.DefaultPath
	}

	if flags.SchemaDir == "" {
		flags.SchemaDir = networkjson.DefaultSchemaDir
	}

	logger, closer := buildIfMgrLogger(cfg, flags.Debug, output)
	defer closer.Close()
	runID := tracing.NewID()
	logger = logger.With(slog.String("run_id", runID), slog.String(tracing.ComponentKey, "ifmgr"))
	logger.Info(
		"ifmgr: starting",
		"build",
		version.BuildVersionString(),
		"role",
		role,
		"dry_run",
		flags.DryRun,
		"known_roles",
		ifmgr.KnownRoles(),
	)

	ctx, cancel := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	var aliasSync sync.WaitGroup
	defer func() {
		cancel()
		aliasSync.Wait()
	}()

	daemon, daemonConfig, rejections, err := prepareIfMgrDaemon(
		ctx,
		cfg,
		role,
		flags,
		logger,
		runID,
		startDHCPv4,
		&aliasSync,
	)
	if err != nil {
		return err
	}

	return runIfMgrService(ctx, cfg, role, logger, daemon, daemonConfig, rejections)
}

func prepareIfMgrDaemon(
	ctx context.Context,
	cfg *config.Config,
	role string,
	flags commandopts.Ifmgr,
	logger *slog.Logger,
	runID string,
	startDHCPv4 addresses.DHCPv4Starter,
	aliasSync *sync.WaitGroup,
) (*ifmgr.Daemon, ifmgr.DaemonConfig, []networkjson.Rejection, error) {
	if role == ifMgrWANRole {
		if err := bootstrapWANFirewall(ctx, flags.NetworkPath); err != nil {
			return nil, ifmgr.DaemonConfig{}, nil, err
		}
	}

	loaded, err := parseNetworkConfig(ctx, logger, cfg, role, flags)
	if err != nil {
		logger.Error("ifmgr: network configuration unusable", "err", err)

		return nil, ifmgr.DaemonConfig{}, nil, err
	}

	dcfg, err := buildIfMgrDaemonConfig(cfg, role, cfg.IfMgr.NetworkdTimeout())
	if err != nil {
		logger.Warn("ifmgr: build daemon config failed", "err", err)

		return nil, ifmgr.DaemonConfig{}, nil, fmt.Errorf("build daemon config: %w", err)
	}

	if settings, found := dcfg.ModuleConfigs["addresses"].(addresses.Config); found {
		settings.StartDHCPv4 = startDHCPv4
		dcfg.ModuleConfigs["addresses"] = settings
	}

	dcfg.Notifier = notify.FromConfig(cfg.Email, cfg.NotifyConfig(), logger, "mwan-ifmgr")
	dcfg.LiveState = wanstate.New()
	dcfg.LiveState.SetTransitionLogger(runID, logger)
	if role == ifMgrWANRole && cfg.BGP.Enabled &&
		cfg.BGP.ForwardingReadinessEnabled() {
		dcfg.ForwardingReadySocket = cfg.BGP.ForwardingReadiness.SocketPath
		dcfg.ForwardingReadyTimeout = cfg.BGP.ForwardingReadiness.ReadTimeout()
	}

	daemon, err := ifmgr.NewDaemon(logger, dcfg)
	if err != nil {
		logger.Warn("ifmgr: new daemon failed", "err", err)

		return nil, ifmgr.DaemonConfig{}, nil, fmt.Errorf("new daemon: %w", err)
	}

	rejections, err := writeInterfaceConfig(ctx, logger, loaded)
	if err != nil {
		return nil, ifmgr.DaemonConfig{}, nil, err
	}

	if role == ifMgrWANRole && cfg.OPNsense.NPTv6HairpinAlias != "" {
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			logger.ErrorContext(
				ctx,
				"npt: unsupported HTTP transport",
				"err",
				fmt.Errorf("%w %T", errUnsupportedHTTPTransport, http.DefaultTransport),
			)
		} else {
			startNPTv6HairpinAlias(
				ctx,
				logger,
				cfg.OPNsense,
				dcfg.LiveState,
				aliasSync,
				base,
				hairpinAliasInterval,
			)
		}
	}

	return daemon, dcfg, rejections, nil
}

func runIfMgrService(
	ctx context.Context,
	cfg *config.Config,
	role string,
	logger *slog.Logger,
	daemon *ifmgr.Daemon,
	daemonConfig ifmgr.DaemonConfig,
	rejections []networkjson.Rejection,
) error {
	ctx, cancelObserver := context.WithCancelCause(ctx)
	defer cancelObserver(nil)
	startOwnershipObservers(
		ctx,
		logger,
		daemonConfig.LiveState,
		daemonConfig.Connections,
		cancelObserver,
	)

	// Forwarding readiness uses the live store when management publication is unavailable.
	surface := startWanconfigSurface(
		ctx,
		logger,
		cfg,
		daemonConfig.ModuleConfigs,
		rejections,
		daemonConfig.LiveState,
	)
	if surface != nil {
		defer surface.Close()
	}

	if role == ifMgrWANRole {
		if _, err := systemddaemon.SdNotify(false, systemddaemon.SdNotifyReady); err != nil {
			return fmt.Errorf("notify systemd readiness: %w", err)
		}
	}

	if err := daemon.Run(ctx); err != nil {
		logger.WarnContext(ctx, "ifmgr: daemon run failed", "err", err)

		return fmt.Errorf("ifmgr daemon: %w", err)
	}

	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return fmt.Errorf("ifmgr daemon: %w", cause)
	}

	logShutdownReason(ctx, logger)

	return nil
}

func startNPTv6HairpinAlias(
	ctx context.Context,
	log *slog.Logger,
	cfg config.OPNsenseSection,
	state *wanstate.Store,
	aliasSync *sync.WaitGroup,
	base *http.Transport,
	interval time.Duration,
) {
	certificate, err := os.ReadFile(cfg.NPTv6HairpinCAFile)
	if err != nil {
		log.ErrorContext(ctx, "npt: read OPNsense certificate failed", "err", err)

		return
	}

	roots := x509.NewCertPool()
	block, _ := pem.Decode(certificate)
	if block == nil {
		log.ErrorContext(
			ctx,
			"npt: OPNsense certificate configuration invalid",
			"err",
			errMissingCertificatePEM,
		)

		return
	}

	if !roots.AppendCertsFromPEM(certificate) {
		log.ErrorContext(
			ctx,
			"npt: OPNsense certificate configuration invalid",
			"err",
			errRejectedCertificatePEM,
		)

		return
	}

	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		log.ErrorContext(ctx, "npt: parse OPNsense certificate failed", "err", err)

		return
	}

	if len(parsed.DNSNames) == 0 {
		log.ErrorContext(
			ctx,
			"npt: OPNsense certificate has no DNS name",
			"err",
			errCertificateHasNoDNSName,
		)

		return
	}

	transport := base.Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: parsed.DNSNames[0]}
	client := &http.Client{Transport: transport, Timeout: hairpinAliasRequestTimeout}
	aliasSync.Go(func() {
		syncNPTv6HairpinAlias(ctx, client, cfg, state, interval)
	})
}

func ifMgrRole(cfg *config.Config, flags commandopts.Ifmgr) (string, error) {
	role := cfg.IfMgr.Role
	if flags.Role != "" {
		role = flags.Role
	}

	if role == "" {
		return "", errIfMgrRoleRequired
	}

	return role, nil
}

func buildIfMgrLogger(cfg *config.Config, debug bool, output io.Writer) (*slog.Logger, io.Closer) {
	debugEnabled := debug || cfg.IfMgr.Debug
	// rsyslog copies standard output to the system log. The default level excludes debug
	// records from that log.
	stdoutLevel := slog.LevelInfo
	if debugEnabled {
		stdoutLevel = slog.LevelDebug
	}

	handlers := []slog.Handler{
		slog.NewJSONHandler(output, &slog.HandlerOptions{Level: stdoutLevel}),
	}
	if p := cfg.IfMgr.LogFile; p != "" {
		handlers = append(handlers, logging.FileText(p, "[mwan-ifmgr]"))
	}

	if p := cfg.IfMgr.JSONLogFile; p != "" {
		handlers = append(handlers, logging.FileJSON(p))
	}

	logger, closer := logging.New(logging.Config{
		BuildVersion: version.BuildVersionString(),
		Handlers:     handlers,
	})
	logger.Info(
		"ifmgr: standard output debug logging",
		"enabled",
		debugEnabled,
		"stdout_level",
		stdoutLevel.String(),
	)

	return logger, closer
}

func logShutdownReason(ctx context.Context, log *slog.Logger) {
	if err := ctx.Err(); err != nil {
		log.InfoContext(ctx, "ifmgr: shutdown", "reason", err.Error())
	}
}

func buildIfMgrDaemonConfig(
	cfg *config.Config,
	role string,
	timeout time.Duration,
) (ifmgr.DaemonConfig, error) {
	logger := slog.Default().With("component", "ifmgr")
	interfaceConfig, err := readIfMgrInterfaceConfig(cfg.IfMgr, logger)
	if err != nil {
		return ifmgr.DaemonConfig{}, err
	}

	moduleConfigs, err := buildIfMgrModuleConfigs(cfg.IfMgr, role)
	if err != nil {
		logger.Warn("ifmgr: build module configs failed", "role", role, "err", err)

		return ifmgr.DaemonConfig{}, err
	}

	if settings, found := moduleConfigs["addresses"].(addresses.Config); found {
		settings.NetworkdTimeout = timeout
		moduleConfigs["addresses"] = settings
	}

	if settings, found := moduleConfigs["bgp_sessions"].(bgpsessions.Config); found {
		settings, err = withSessionRouterID(settings, cfg.BGP.RouterID, logger)
		if err != nil {
			return ifmgr.DaemonConfig{}, err
		}

		moduleConfigs["bgp_sessions"] = settings
	}

	return ifmgr.DaemonConfig{
		Role:                   role,
		Iface:                  interfaceConfig.name,
		Connections:            cfg.IfMgr.Connections,
		ReconcileInterval:      interfaceConfig.reconcileInterval,
		LeaseDirectory:         cfg.IfMgr.LeaseDirectory,
		EnableDHCP:             interfaceConfig.enableDHCP,
		DHCPInitial:            interfaceConfig.dhcpInitial,
		DHCPMax:                interfaceConfig.dhcpMax,
		EnableRA:               interfaceConfig.enableRA,
		Notifier:               nil,
		ModuleConfigs:          moduleConfigs,
		LiveState:              nil,
		ForwardingReadySocket:  "",
		ForwardingReadyTimeout: 0,
	}, nil
}

type ifMgrInterfaceConfig struct {
	name              string
	enableDHCP        bool
	enableRA          bool
	dhcpInitial       time.Duration
	dhcpMax           time.Duration
	reconcileInterval time.Duration
}

func readIfMgrInterfaceConfig(
	settings config.IfMgrSection,
	logger *slog.Logger,
) (ifMgrInterfaceConfig, error) {
	result := ifMgrInterfaceConfig{
		name:              "",
		enableDHCP:        false,
		enableRA:          false,
		dhcpInitial:       0,
		dhcpMax:           0,
		reconcileInterval: defaultIfMgrReconcileInterval,
	}

	for name, iface := range settings.Iface {
		if result.name != "" {
			return ifMgrInterfaceConfig{}, fmt.Errorf(
				"%w (saw %q and %q)",
				errIfMgrMultipleInterfaces,
				result.name,
				name,
			)
		}

		result.name = iface.Name
		if result.name == "" {
			result.name = name
		}

		result.enableDHCP = iface.DHCPv4
		result.enableRA = iface.RASolicit
		initial, err := parseIfMgrBackoff(
			logger,
			name,
			"dhcp_initial_backoff",
			iface.DHCPInitialBackoff,
		)
		if err != nil {
			return ifMgrInterfaceConfig{}, err
		}

		result.dhcpInitial = initial

		maximum, err := parseIfMgrBackoff(logger, name, "dhcp_max_backoff", iface.DHCPMaxBackoff)
		if err != nil {
			return ifMgrInterfaceConfig{}, err
		}

		result.dhcpMax = maximum
	}

	if result.name == "" {
		return ifMgrInterfaceConfig{}, errIfMgrInterfaceRequired
	}

	if settings.ReconcileInterval == "" {
		return result, nil
	}

	interval, err := time.ParseDuration(settings.ReconcileInterval)
	if err != nil {
		logger.Warn(
			"ifmgr: invalid reconcile_interval",
			"value",
			settings.ReconcileInterval,
			"err",
			err,
		)

		return ifMgrInterfaceConfig{}, fmt.Errorf("ifmgr.reconcile_interval: %w", err)
	}

	result.reconcileInterval = interval

	return result, nil
}

func parseIfMgrBackoff(
	logger *slog.Logger,
	name string,
	setting string,
	value string,
) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}

	backoff, err := time.ParseDuration(value)
	if err != nil {
		logger.Warn("ifmgr: invalid "+setting, "iface", name, "value", value, "err", err)

		return 0, fmt.Errorf("ifmgr.iface.%s.%s: %w", name, setting, err)
	}

	return backoff, nil
}

// Provider settings must validate before the function writes networkd files.
func parseNetworkConfig(
	ctx context.Context,
	log *slog.Logger,
	cfg *config.Config,
	role string,
	flags commandopts.Ifmgr,
) (*networkjson.Config, error) {
	steers, err := roleSteersProviders(role)
	if err != nil {
		return nil, err
	}

	if !steers {
		return nil, nil
	}

	loaded, err := networkload.Load(flags.NetworkPath, flags.SchemaDir)
	if err != nil {
		log.ErrorContext(
			ctx,
			"ifmgr: load network configuration failed",
			"path",
			flags.NetworkPath,
			"role",
			role,
			"err",
			err,
		)

		return nil, fmt.Errorf("load network configuration: %w", err)
	}

	for _, rejected := range loaded.Rejected {
		log.ErrorContext(
			ctx,
			"ifmgr: provider entry rejected; steering the remaining providers",
			"path",
			flags.NetworkPath,
			"interface",
			rejected.Interface,
			"provider",
			rejected.Provider,
			"err",
			rejected.Err,
		)
	}

	loaded.Apply(cfg)

	return loaded, nil
}

func writeInterfaceConfig(
	ctx context.Context,
	log *slog.Logger,
	loaded *networkjson.Config,
) ([]networkjson.Rejection, error) {
	if loaded == nil {
		return nil, nil
	}

	tables := make(map[connectionid.ID]int, len(loaded.WAN))
	for id, provider := range loaded.WAN {
		tables[connectionid.ID(id)] = provider.TableID
	}

	preserveStatic := networkdNPTPreservation(loaded)
	retiring, err := networkd.ObsoleteLinkFiles(
		networkd.DefaultUnitDir,
		loaded.Connections,
		tables,
		preserveStatic,
	)
	if err != nil {
		log.ErrorContext(ctx, "ifmgr: obsolete networkd name inspection failed", "err", err)

		return nil, fmt.Errorf("inspect obsolete networkd names: %w", err)
	}

	if err := linkboot.ValidateDir(
		networkd.DefaultUnitDir,
		loaded.Connections,
		retiring...,
	); err != nil {
		log.ErrorContext(ctx, "ifmgr: owned link name validation failed", "err", err)

		return nil, fmt.Errorf("validate owned link names: %w", err)
	}

	rejections, err := writeNetworkConfig(ctx, log, loaded, tables, preserveStatic)
	if err != nil {
		return rejections, err
	}

	if _, err := linkboot.WriteDir(networkd.DefaultUnitDir, loaded.Connections); err != nil {
		log.ErrorContext(ctx, "ifmgr: owned link name write failed", "err", err)

		return nil, fmt.Errorf("write owned link names: %w", err)
	}

	return rejections, nil
}

func writeNetworkConfig(
	ctx context.Context,
	log *slog.Logger,
	loaded *networkjson.Config,
	tables map[connectionid.ID]int,
	preserveStatic map[connectionid.ID]bool,
) ([]networkjson.Rejection, error) {
	if loaded == nil {
		return nil, nil
	}

	changed, err := networkd.WriteDir(
		networkd.DefaultUnitDir,
		loaded.Connections,
		tables,
		preserveStatic,
	)
	if err != nil {
		log.ErrorContext(
			ctx,
			"ifmgr: writing networkd unit files failed",
			"dir",
			networkd.DefaultUnitDir,
			"err",
			err,
		)

		return nil, fmt.Errorf("write networkd unit files: %w", err)
	}

	log.InfoContext(
		ctx,
		"ifmgr: networkd unit files written",
		"dir",
		networkd.DefaultUnitDir,
		"changed",
		changed,
	)
	if len(changed) == 0 {
		return loaded.Rejected, nil
	}

	warnRenamedLinks(ctx, log, changed, loaded.Connections)
	if err := networkd.ReloadIfRunning(ctx); err != nil {
		log.ErrorContext(ctx, "ifmgr: reloading systemd-networkd failed", "err", err)

		return nil, fmt.Errorf("reload systemd-networkd: %w", err)
	}

	return loaded.Rejected, nil
}

func bootstrapWANFirewall(ctx context.Context, path string) error {
	baseline, err := networkjson.LoadBaseline(path)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"ifmgr: load protective firewall baseline failed",
			"path",
			path,
			"err",
			err,
		)

		return fmt.Errorf("load protective firewall baseline: %w", err)
	}

	if baseline == nil {
		return fmt.Errorf("%w in %s", errWANFirewallPolicyAbsent, path)
	}

	desired, err := firewall.CompileBaseline(*baseline)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"ifmgr: compile protective firewall baseline failed",
			"path",
			path,
			"err",
			err,
		)

		return fmt.Errorf("compile protective firewall baseline: %w", err)
	}

	if err := firewall.Apply(ctx, desired); err != nil {
		slog.ErrorContext(
			ctx,
			"ifmgr: apply protective firewall baseline failed",
			"path",
			path,
			"err",
			err,
		)

		return fmt.Errorf("apply protective firewall baseline: %w", err)
	}

	if _, err := firewall.Inspect(ctx, desired); err != nil {
		slog.ErrorContext(
			ctx,
			"ifmgr: inspect protective firewall baseline failed",
			"path",
			path,
			"err",
			err,
		)

		return fmt.Errorf("inspect protective firewall baseline: %w", err)
	}

	return nil
}

// The WAN role loads WANCONFIG when its module list includes wan.routes.
func roleSteersProviders(role string) (bool, error) {
	logger := slog.Default().With("component", "ifmgr")
	names, err := ifmgr.ModulesForRole(role)
	if err != nil {
		logger.Warn("ifmgr: modules for role failed", "role", role, "err", err)

		return false, fmt.Errorf("ModulesForRole(%q): %w", role, err)
	}

	return slices.Contains(names, "wan.routes"), nil
}
