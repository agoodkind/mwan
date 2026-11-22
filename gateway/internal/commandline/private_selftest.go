package commandline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/commandline/commandopts"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/version"
	"goodkind.io/mwan/internal/wanconfig"
	"goodkind.io/mwan/internal/wanstate"
	"goodkind.io/mwan/internal/yangpub"
	"goodkind.io/mwan/internal/yangpub/schema"
)

const selftestTimeout = 30 * time.Second

// Keep the provider registered while a RESTCONF client reads it.
const selftestHoldTime = 20 * time.Second

const (
	selftestInternalIface      = "eninternal0"
	selftestManagementIface    = "enmgmt0"
	selftestProviderIface      = "enexample0"
	selftestProviderName       = "example"
	selftestInterfaceType      = "iana-if-type:other"
	selftestSSHPort            = 22
	selftestForcedDSCP         = 8
	selftestProbeTimeoutMillis = 2000
	selftestTableID            = 100
	selftestFromPriority       = 55
	selftestPingCount          = 3
	selftestThreshold          = 2
	selftestIntervalSeconds    = 10
	selftestIPv6Family         = "ipv6"
	selftestRepositoryMode     = 0o750
)

const (
	selftestHashModePath = "/ietf-interfaces:interfaces/" +
		"goodkind-mwan-steering:steering-group/goodkind-mwan-steering:hash-mode"
	selftestHashModeValue = "random"
)

// The private selftest reads the address and WAN name through sysrepo.
const selftestOwnedAddress = "203.0.113.3"

// Restore the hash mode after the test context expires.
const restoreSelftestTimeout = 10 * time.Second

const (
	selftestProviderModule = "ietf-interfaces"
	selftestProviderPath   = "/ietf-interfaces:interfaces"
)

func failStep(log *slog.Logger, step string, err error) error {
	log.Error("wanconfig selftest step failed", "step", step, "err", err)

	return fmt.Errorf("%s: %w", step, err)
}

func runWanconfigSelftestOptions(flags commandopts.Selftest, diagnostics io.Writer) int {
	log, closer := logging.New(logging.Config{
		BuildVersion: version.BuildVersionString(),
		Handlers:     []slog.Handler{slog.NewTextHandler(diagnostics, nil)},
	})
	defer closer.Close()
	if flags.Repository != "" {
		return runPrivateSelftest(log, flags, diagnostics)
	}

	return runGatewaySelftest(log, diagnostics)
}

func runGatewaySelftest(log *slog.Logger, diagnostics io.Writer) int {
	pub, err := yangpub.New(log)
	if err != nil {
		fmt.Fprintf(diagnostics, "mwan wanconfig-selftest: %v\n", err)

		return 1
	}

	defer func() {
		if cerr := pub.Close(); cerr != nil {
			log.Error("yangpub close failed", "err", cerr)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), selftestTimeout)
	defer cancel()

	// Save the configured hash mode before publishing the selftest value.
	priorValue, priorFound, err := pub.GetItem(ctx, yangpub.DatastoreRunning, selftestHashModePath)
	if err != nil {
		fmt.Fprintf(diagnostics, "mwan wanconfig-selftest: read current value: %v\n", err)

		return 1
	}
	defer restoreSelftestLeaf(pub, log, priorValue, priorFound)

	publishItems := []yangpub.Item{
		{Path: selftestHashModePath, Value: selftestHashModeValue},
	}
	if err := pub.SetItems(ctx, yangpub.DatastoreRunning, publishItems); err != nil {
		fmt.Fprintf(diagnostics, "mwan wanconfig-selftest: publish: %v\n", err)

		return 1
	}

	log.Info(
		"published selftest leaf",
		"path",
		selftestHashModePath,
		"prior_value_present",
		priorFound,
	)

	providerFn := func(_ context.Context, xpath string) ([]yangpub.Item, error) {
		log.Info("provider read", "xpath", xpath)
		requestTime := clock.Real{}.Now().UTC().Format(time.RFC3339)
		operItems := []yangpub.Item{
			{
				Path:  "/ietf-interfaces:interfaces/interface[name='wanconfig-selftest0']/type",
				Value: selftestInterfaceType,
			},
			{
				Path:  "/ietf-interfaces:interfaces/interface[name='wanconfig-selftest0']/description",
				Value: "computed at " + requestTime,
			},
		}

		return operItems, nil
	}
	if err := pub.RegisterProvider(
		ctx,
		selftestProviderModule,
		selftestProviderPath,
		providerFn,
	); err != nil {
		fmt.Fprintf(diagnostics, "mwan wanconfig-selftest: register provider: %v\n", err)

		return 1
	}

	log.Info(
		"provider registered, holding for external reads",
		"module",
		selftestProviderModule,
		"path",
		selftestProviderPath,
		"hold",
		selftestHoldTime.String(),
	)
	holdTimer := time.NewTimer(selftestHoldTime)
	defer holdTimer.Stop()
	select {
	case <-ctx.Done():
		fmt.Fprintf(
			diagnostics,
			"mwan wanconfig-selftest: wait for RESTCONF reads: %v\n",
			ctx.Err(),
		)

		return 1
	case <-holdTimer.C:
	}

	log.Info("wanconfig selftest complete")

	return 0
}

// Restore the previous hash mode or delete the leaf to use its default.
// Deferred cleanup logs restore errors after the command sets its exit code.
func restoreSelftestLeaf(
	pub yangpub.Publisher,
	log *slog.Logger,
	priorValue string,
	priorFound bool,
) {
	ctx, cancel := context.WithTimeout(context.Background(), restoreSelftestTimeout)
	defer cancel()

	if priorFound {
		items := []yangpub.Item{{Path: selftestHashModePath, Value: priorValue}}
		if err := pub.SetItems(ctx, yangpub.DatastoreRunning, items); err != nil {
			log.Error(
				"selftest leaf restore failed",
				"path",
				selftestHashModePath,
				"value",
				priorValue,
				"err",
				err,
			)

			return
		}

		log.Info("selftest leaf restored", "path", selftestHashModePath, "value", priorValue)

		return
	}

	if err := pub.DeleteItem(ctx, yangpub.DatastoreRunning, selftestHashModePath); err != nil {
		log.Error("selftest leaf removal failed", "path", selftestHashModePath, "err", err)

		return
	}

	log.Info("selftest leaf removed", "path", selftestHashModePath)
}

// Use the binary's steering revision for its model.
// Use host revisions for other models.
var selftestModels = []struct {
	pattern  string
	features []string
}{
	{pattern: "ietf-yang-types@*.yang", features: nil},
	{pattern: "ietf-inet-types@*.yang", features: nil},
	{pattern: "ietf-interfaces@*.yang", features: nil},
	{pattern: "iana-if-type@*.yang", features: nil},
	{pattern: "ietf-ip@*.yang", features: nil},
	{pattern: "ietf-nat@*.yang", features: []string{"basic-nat44", "napt44", "dst-nat", "nptv6"}},
	{pattern: schema.SteeringFile, features: nil},
}

func resolveSelftestModels(log *slog.Logger, dir string) ([]yangpub.Model, error) {
	models := make([]yangpub.Model, 0, len(selftestModels))
	for _, entry := range selftestModels {
		matches, err := filepath.Glob(filepath.Join(dir, entry.pattern))
		if err != nil {
			return nil, failStep(log, "match "+entry.pattern, err)
		}

		if len(matches) != 1 {
			return nil, selftestInvariant(
				fmt.Sprintf(
					"want exactly one file matching %s in %s, found %d",
					entry.pattern,
					dir,
					len(matches),
				),
			)
		}

		models = append(
			models,
			yangpub.Model{Path: matches[0], Features: entry.features, Update: false},
		)
	}

	return models, nil
}

func selftestConnections() []interfaceintent.Connection {
	return []interfaceintent.Connection{
		{
			ID:         connectionid.ID(selftestInternalIface),
			Name:       selftestInternalIface,
			Type:       selftestInterfaceType,
			Enabled:    nil,
			Roles:      interfaceintent.RoleInternal,
			Owner:      interfaceintent.OwnerExternal,
			Link:       nil,
			IPv4:       nil,
			IPv6:       nil,
			LeaseStore: "",
			Networkd:   nil,
		},
		{
			ID:         connectionid.ID(selftestManagementIface),
			Name:       selftestManagementIface,
			Type:       selftestInterfaceType,
			Enabled:    nil,
			Roles:      interfaceintent.RoleManagement,
			Owner:      interfaceintent.OwnerExternal,
			Link:       nil,
			IPv4:       nil,
			IPv6:       nil,
			LeaseStore: "",
			Networkd:   nil,
		},
		{
			ID:         connectionid.ID(selftestProviderName),
			Name:       selftestProviderIface,
			Type:       selftestInterfaceType,
			Enabled:    nil,
			Roles:      interfaceintent.RoleProvider,
			Owner:      interfaceintent.OwnerExternal,
			Link:       nil,
			IPv4:       nil,
			IPv6:       nil,
			LeaseStore: "",
			Networkd:   nil,
		},
	}
}

func selftestFirewallConfig() firewall.Config {
	var firewallConfig firewall.Config
	firewallConfig.Enabled, firewallConfig.InternalInterface = true, selftestInternalIface
	firewallConfig.InternalNetworkIPv4 = netip.MustParsePrefix("192.0.2.0/29")
	firewallConfig.ManagementInterface = selftestManagementIface
	firewallConfig.ManagementServices = []firewall.Service{
		{Protocol: "tcp", Port: selftestSSHPort, Sources: nil},
	}
	firewallConfig.KnownInterfaces = []string{
		selftestInternalIface,
		selftestManagementIface,
		selftestProviderIface,
	}
	firewallConfig.Providers = []firewall.Provider{
		{
			Interface:      selftestProviderIface,
			Mark:           1,
			ForcedDSCP:     selftestForcedDSCP,
			MasqueradeIPv4: true,
			StaticMappings: nil,
			ReducedMTU:     false,
		},
	}
	firewallConfig.Paths = []firewall.ForwardingPath{
		{
			InternalInterface: selftestInternalIface,
			ExternalInterface: selftestProviderIface,
			IPv4:              true,
			IPv6:              true,
		},
	}
	firewallConfig.PinnedSetV4Name, firewallConfig.PinnedSetV6Name = "selftest_pinned_v4", "selftest_pinned_v6"

	return firewallConfig
}

func selftestGateway() wanconfig.Gateway {
	return wanconfig.Gateway{
		PinnedConnectionID: "",
		ConnectionIDs:      nil,
		Connections:        selftestConnections(),
		InternalIface:      selftestInternalIface,
		Firewall:           selftestFirewallConfig(),
		HashMode:           "random",
		Group: wanconfig.GroupSettings{
			ReservedTables:     []uint32{400},
			InternalPrefix:     netip.MustParsePrefix("3d06:bad:b01:210::/60"),
			OpnsenseEdgeV6:     netip.MustParseAddr("2001:db8:fe::2"),
			MwanbrEdgeV6:       netip.MustParseAddr("2001:db8:fe::3"),
			InternalNetV4:      netip.MustParsePrefix("192.0.2.0/29"),
			ProbeTimeoutMillis: selftestProbeTimeoutMillis,
		},
		Members: []wanconfig.Member{
			{
				SelectionEnabled: nil,
				Name:             selftestProviderName,
				ProviderName:     selftestProviderName,
				Iface:            selftestProviderIface,
				Tier:             0,
				Weight:           1,
				ProbePolicy:      selftestProviderName,
				TranslationV6: &config.IPv6Translation{
					Mode: config.TranslationNPTv6,
					NPT: &config.NPTv6Translation{
						InternalPrefix: netip.MustParsePrefix("3d06:bad:b01:210::/60"),
						ExternalSource: config.PrefixDelegated,
						ExternalPrefix: netip.Prefix{},
						ExpectedPrefix: netip.MustParsePrefix("2001:db8:a::/60"),
					},
				},
				TranslationIDV4: wanconfig.TranslationInstanceID(selftestProviderName, "ipv4"),
				TranslationIDV6: wanconfig.TranslationInstanceID(
					selftestProviderName,
					selftestIPv6Family,
				),
				TableID:    selftestTableID,
				FwMark:     1,
				FwMarkPrio: selftestTableID,
				FromPrio:   selftestFromPriority,
				V4Source:   "",
				ForcedDSCP: selftestForcedDSCP,
				TranslationV4: &config.IPv4Translation{
					Mode: config.TranslationNAPT44,
					StaticMappings: []config.StaticMapping{
						{
							External: netip.MustParseAddr(selftestOwnedAddress),
							Internal: netip.MustParseAddr("192.0.2.3"),
							Delivery: "",
						},
					},
				},
				Health: &wanconfig.ProbeSettings{
					Enabled:              true,
					PingCount:            new(uint8(selftestPingCount)),
					SuccessThreshold:     new(uint8(selftestThreshold)),
					FailureThreshold:     new(uint8(selftestThreshold)),
					RecoveryThreshold:    new(uint8(selftestThreshold)),
					CheckIntervalSeconds: new(uint32(selftestIntervalSeconds)),
					TargetsV4:            []netip.Addr{netip.MustParseAddr("192.0.2.10")},
					TargetsV6:            []netip.Addr{netip.MustParseAddr("2001:db8:53::1")},
					HTTPURLs:             []string{"https://example.test/ip"},
				},
				// The private selftest validates publication. It does not render networkd link files.
				LinkFiles:   "",
				BGPSessions: nil,
			},
		},
	}
}

func selftestStore() *wanstate.Store {
	store := wanstate.New()
	store.SetHealth(map[string]wanstate.MemberHealth{
		selftestProviderName: {
			Verdict:             wanstate.HealthHealthy,
			ConsecutiveFailures: 0,
			LastTransition:      time.Time{},
			V4:                  wanstate.ProbePass,
			V4Verdict:           wanstate.HealthHealthy,
			V6:                  wanstate.ProbePass,
			V6Verdict:           wanstate.HealthHealthy,
		},
	})
	store.SetRouting(
		0,
		map[string]wanstate.MemberRouting{
			selftestProviderName: {
				Carrying:         true,
				SelectionEnabled: true,
				V4Ready:          false,
				V6Ready:          false,
				V4Dependencies:   nil,
				V6Dependencies:   nil,
				V4Reason:         "",
				V6Reason:         "",
				OwnedAddresses:   []netip.Addr{netip.MustParseAddr(selftestOwnedAddress)},
			},
		},
	)
	store.SetTranslation(map[string]wanstate.MemberTranslation{
		selftestProviderName: {
			V4: wanstate.FamilyTranslation{
				Mode:           string(config.TranslationNAPT44),
				Ready:          true,
				Reason:         "",
				InternalPrefix: netip.Prefix{},
				ExternalPrefix: netip.Prefix{},
			},
			V6: wanstate.FamilyTranslation{
				Mode:           string(config.TranslationNPTv6),
				Ready:          true,
				Reason:         "",
				InternalPrefix: netip.MustParsePrefix("3d06:bad:b01:210::/60"),
				ExternalPrefix: netip.MustParsePrefix("2001:db8:b::/60"),
			},
		},
	})
	store.SetIntendedRuleset(
		"chain prerouting:\n" +
			"  iif \"enexample0\" ip6 daddr 2001:db8:a::/60 " +
			"dnat prefix to 3d06:bad:b01:210::/60\n" +
			"chain postrouting:\n",
	)

	return store
}

func selftestRejections() []networkjson.Rejection {
	return []networkjson.Rejection{
		{
			Interface: "enrejected0",
			Provider:  "rejected-example",
			Err:       selftestInvariant("ipv6/dhcp is required"),
		},
	}
}

// Verify that a second connection reads published and live state.
// The daemon owns the modules during this read.
func runPrivateSelftest(log *slog.Logger, flags commandopts.Selftest, diagnostics io.Writer) int {
	if err := runPrivateSelftestSteps(log, flags); err != nil {
		fmt.Fprintf(diagnostics, "mwan wanconfig-selftest: %v\n", err)

		return 1
	}

	log.Info("wanconfig private selftest complete", "repository", flags.Repository)

	return 0
}

// Remove only shared memory segments that use this test's prefix.
const sysrepoSHMDir = "/dev/shm"

func runPrivateSelftestSteps(log *slog.Logger, flags commandopts.Selftest) error {
	ctx, cancel := context.WithTimeout(context.Background(), selftestTimeout)
	defer cancel()

	var runError error
	if err := withPrivateRepository(
		ctx,
		log,
		flags,
		func(reader yangpub.Publisher) {
			runError = runPrivateSelftestWithReader(ctx, log, reader)
		},
	); err != nil {
		return err
	}

	return runError
}

func runPrivateSelftestWithReader(
	ctx context.Context,
	log *slog.Logger,
	reader yangpub.Publisher,
) error {
	daemon, err := yangpub.New(log)
	if err != nil {
		return failStep(log, "daemon connection", err)
	}

	defer func() { _ = daemon.Close() }()

	gateway := selftestGateway()
	if err := wanconfig.Publish(ctx, log, runningReplacer{pub: daemon}, gateway); err != nil {
		return failStep(log, "publish configuration", err)
	}

	for _, module := range publishedModules {
		if err := daemon.OwnModule(ctx, module); err != nil {
			return failStep(log, "own "+module, err)
		}
	}

	if err := registerLiveStateProviders(
		ctx,
		log,
		daemon,
		selftestStore(),
		gateway,
		selftestRejections(),
	); err != nil {
		return failStep(log, "register providers", err)
	}

	if err := checkSelftestTree(ctx, log, reader); err != nil {
		return err
	}

	if err := checkSelftestNotifications(ctx, log, reader, daemon); err != nil {
		return err
	}

	if err := wanconfig.Publish(ctx, log, runningReplacer{pub: daemon}, gateway); err != nil {
		return failStep(log, "publish configuration with ownership alive", err)
	}

	if err := daemon.Close(); err != nil {
		return failStep(log, "close daemon connection", err)
	}

	after, found, err := reader.ExportJSON(ctx, yangpub.DatastoreOperational, "/ietf-interfaces:*")
	if err != nil {
		return failStep(log, "operational read after close", err)
	}

	if !found {
		return nil
	}

	// The datastore may still return the empty container after the daemon releases its
	// claims.
	if err := checkSelftestInterfacesBare(log, json.RawMessage(after)); err != nil {
		return failStep(log, "interfaces tree after close: "+after, err)
	}

	return nil
}

// withPrivateRepository closes the datastore and removes shared memory.
func withPrivateRepository(
	ctx context.Context,
	log *slog.Logger,
	flags commandopts.Selftest,
	run func(yangpub.Publisher),
) error {
	if err := os.MkdirAll(flags.Repository, selftestRepositoryMode); err != nil {
		return failStep(log, "create repository", err)
	}

	// Require an empty repository. Opening a connection can change sysrepo's files.
	entries, err := os.ReadDir(flags.Repository)
	if err != nil {
		return failStep(log, "read repository", err)
	}

	if len(entries) > 0 {
		return selftestInvariant(
			fmt.Sprintf(
				"repository %s is not empty; the private selftest needs a fresh directory",
				flags.Repository,
			),
		)
	}

	// Sysrepo reads these environment variables when it connects. Use a unique prefix to
	// isolate the private repository.
	shmPrefix := fmt.Sprintf("mwanselftest%d", os.Getpid())
	restoreEnv := setSelftestEnv([]envSetting{
		{name: "SYSREPO_REPOSITORY_PATH", value: flags.Repository},
		{name: "SYSREPO_SHM_PREFIX", value: shmPrefix},
		// Skip sysrepo group ownership changes for the private repository.
		{name: "SR_ENV_RUN_TESTS", value: "1"},
	})

	models, err := resolveSelftestModels(log, flags.ModelsDir)
	if err != nil {
		restoreEnv()

		return err
	}

	reader, err := yangpub.New(log)
	if err != nil {
		removeSelftestSHM(log, shmPrefix)
		restoreEnv()

		return failStep(log, "reader connection", err)
	}

	// Restore the environment after disconnecting and deleting shared memory.
	closeRepository := func() {
		_ = reader.Close()
		removeSelftestSHM(log, shmPrefix)
		restoreEnv()
	}
	defer closeRepository()

	if _, err := reader.InstallModules(ctx, models, flags.ModelsDir); err != nil {
		return failStep(log, "install models", err)
	}

	run(reader)

	return nil
}

type envSetting struct {
	name  string
	value string
}

func setSelftestEnv(settings []envSetting) func() {
	restores := make([]func(), 0, len(settings))
	for _, setting := range settings {
		prior, present := os.LookupEnv(setting.name)
		os.Setenv(setting.name, setting.value)
		if present {
			restores = append(restores, func() { os.Setenv(setting.name, prior) })
		} else {
			restores = append(restores, func() { os.Unsetenv(setting.name) })
		}
	}

	return func() {
		for _, restore := range restores {
			restore()
		}
	}
}

const (
	selftestNotifTimeout           = 10 * time.Second
	selftestNotificationBufferSize = 4
)

type receivedNotification struct {
	xpath   string
	payload string
}

func checkSelftestNotifications(
	ctx context.Context,
	log *slog.Logger,
	reader yangpub.Publisher,
	daemon yangpub.Publisher,
) error {
	received := make(chan receivedNotification, selftestNotificationBufferSize)
	subscriber := func(xpath string, payload string) {
		select {
		case received <- receivedNotification{xpath: xpath, payload: payload}:
		default:
		}
	}
	if err := reader.SubscribeNotifications(ctx, "goodkind-mwan-steering", subscriber); err != nil {
		return failStep(log, "subscribe notifications", err)
	}

	store := selftestStore()
	notifier := newSurfaceNotifier(log, selftestGateway())
	store.Observe(notifier)
	notifierCtx, stopNotifier := context.WithCancel(ctx)
	senderDone := startNotifierSender(notifierCtx, log, notifier, daemon)
	// Wait for the notification sender before closing its sysrepo connection.
	defer func() {
		stopNotifier()
		<-senderDone
	}()

	store.NotifyHealthTransition(
		selftestProviderName,
		wanstate.HealthHealthy,
		wanstate.HealthUnhealthy,
	)
	store.SetRouting(
		1,
		map[string]wanstate.MemberRouting{
			selftestProviderName: {
				Carrying:         false,
				SelectionEnabled: true,
				V4Ready:          false,
				V6Ready:          false,
				V4Reason:         "",
				V6Reason:         "",
				V4Dependencies:   nil,
				V6Dependencies:   nil,
				OwnedAddresses:   nil,
			},
		},
	)

	byPath := map[string]string{}
	for len(byPath) < 2 {
		select {
		case notification := <-received:
			byPath[notification.xpath] = notification.payload
		case <-time.After(selftestNotifTimeout):
			return failStep(
				log,
				"await notifications",
				selftestInvariant(
					fmt.Sprintf("received %v, want health-transition and tier-change", byPath),
				),
			)
		}
	}

	if err := checkSelftestHealthNotification(
		log,
		byPath["/goodkind-mwan-steering:health-transition"],
	); err != nil {
		return failStep(log, "health-transition notification", err)
	}

	if err := checkSelftestTierNotification(
		log,
		byPath["/goodkind-mwan-steering:tier-change"],
	); err != nil {
		return failStep(log, "tier-change notification", err)
	}

	return nil
}

func checkSelftestHealthNotification(log *slog.Logger, payload string) error {
	if payload == "" {
		return selftestInvariant("not received")
	}

	root, err := unmarshalObject(log, json.RawMessage(payload), "health-transition payload")
	if err != nil {
		return err
	}

	body, err := unmarshalObject(
		log,
		root["goodkind-mwan-steering:health-transition"],
		"health-transition body",
	)
	if err != nil {
		return err
	}

	if err := expectLeaf(
		body,
		"interface",
		strconv.Quote(selftestProviderIface),
		payload,
	); err != nil {
		return err
	}

	if err := expectLeaf(body, "health", `"unhealthy"`, payload); err != nil {
		return err
	}

	return expectLeaf(body, "previous-health", `"healthy"`, payload)
}

func checkSelftestTierNotification(log *slog.Logger, payload string) error {
	if payload == "" {
		return selftestInvariant("not received")
	}

	root, err := unmarshalObject(log, json.RawMessage(payload), "tier-change payload")
	if err != nil {
		return err
	}

	body, err := unmarshalObject(
		log,
		root["goodkind-mwan-steering:tier-change"],
		"tier-change body",
	)
	if err != nil {
		return err
	}

	if err := expectLeaf(body, "active-tier", "1", payload); err != nil {
		return err
	}

	return expectLeaf(body, "previous-tier", "0", payload)
}

// Log cleanup errors after the exit code is set.
func removeSelftestSHM(log *slog.Logger, prefix string) {
	segments, err := filepath.Glob(filepath.Join(sysrepoSHMDir, prefix+"*"))
	if err != nil {
		log.Warn("selftest shared memory listing failed", "prefix", prefix, "err", err)

		return
	}

	for _, segment := range segments {
		if err := os.Remove(segment); err != nil {
			log.Warn("selftest shared memory removal failed", "segment", segment, "err", err)
		}
	}
}
