package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"goodkind.io/mwan/internal/wanconfig"
	"goodkind.io/mwan/internal/wanstate"
	"goodkind.io/mwan/internal/yangpub"
)

// selftestTimeout bounds the whole publish-and-provide exercise.
const selftestTimeout = 30 * time.Second

// selftestHoldTime keeps the provider registration alive long enough for
// an external RESTCONF read to hit it during testbed validation.
const selftestHoldTime = 20 * time.Second

// selftestHashModePath is the one config leaf the gateway selftest
// publishes, the steering group's hash mode, and selftestHashModeValue is
// what it writes there for the duration of the run.
const (
	selftestHashModePath  = "/ietf-interfaces:interfaces/goodkind-mwan-steering:steering-group/goodkind-mwan-steering:hash-mode"
	selftestHashModeValue = "random"
)

// selftestOwnedAddress is the mapped address the private selftest's store
// reports the member's link holding, so the read proves the owned-address
// leaf-list and the wan name beside it serve through real sysrepo.
const selftestOwnedAddress = "203.0.113.3"

// restoreSelftestTimeout bounds the restore, which runs after the main
// context has already expired.
const restoreSelftestTimeout = 10 * time.Second

// selftestProviderModule and selftestProviderPath place a throwaway
// operational provider on the interfaces list, so an external read
// proves values reach the daemon at request time.
const (
	selftestProviderModule = "ietf-interfaces"
	selftestProviderPath   = "/ietf-interfaces:interfaces"
)

// selftestFlags selects the exercise. Without a repository the selftest
// runs against the host's datastore the way the testbed validation does;
// with one it stands up a private repository from the model files in
// modelsDir and proves the whole serving contract against it, touching
// nothing the host serves.
type selftestFlags struct {
	repository string
	modelsDir  string
}

// failStep logs a failed selftest step and returns it wrapped under the
// step's name, so the operator reads the step both in the journal and in
// the exit message.
func failStep(log *slog.Logger, step string, err error) error {
	log.Error("wanconfig selftest step failed", "step", step, "err", err)
	return fmt.Errorf("%s: %w", step, err)
}

func parseSelftestFlags(log *slog.Logger, args []string) (selftestFlags, error) {
	flags := selftestFlags{repository: "", modelsDir: ""}
	set := flag.NewFlagSet("wanconfig-selftest", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	set.StringVar(&flags.repository, "repository", "",
		"private sysrepo repository directory to create and test against, instead of the host's")
	set.StringVar(&flags.modelsDir, "models-dir", "",
		"directory holding the gateway's model files, installed into the private repository")
	if err := set.Parse(args); err != nil {
		return flags, failStep(log, "parse flags", err)
	}
	if (flags.repository == "") != (flags.modelsDir == "") {
		return flags, errors.New("--repository and --models-dir go together")
	}
	return flags, nil
}

// runWanconfigSelftest exercises the publishing binding end to end. On
// builds without the binding it reports unavailability and exits nonzero
// without touching anything.
func runWanconfigSelftest(args []string) int {
	log := slog.Default()
	flags, err := parseSelftestFlags(log, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mwan wanconfig-selftest: %v\n", err)
		return 2
	}
	if flags.repository != "" {
		return runPrivateSelftest(log, flags)
	}
	return runGatewaySelftest(log)
}

// runGatewaySelftest publishes one marker leaf into the host's running
// datastore, registers an operational provider, and holds the
// registration open for an external RESTCONF read.
func runGatewaySelftest(log *slog.Logger) int {
	pub, err := yangpub.New(log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mwan wanconfig-selftest: %v\n", err)
		return 1
	}
	defer func() {
		if cerr := pub.Close(); cerr != nil {
			log.Error("yangpub close failed", "err", cerr)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), selftestTimeout)
	defer cancel()

	// The selftest writes a real config leaf, so it snapshots the current
	// value first and puts it back on the way out. Without that, a run
	// against a gateway whose operator chose a non-default hash mode would
	// silently change how traffic is spread.
	priorValue, priorFound, err := pub.GetItem(ctx, yangpub.DatastoreRunning, selftestHashModePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mwan wanconfig-selftest: read current value: %v\n", err)
		return 1
	}
	defer restoreSelftestLeaf(pub, log, priorValue, priorFound)

	publishItems := []yangpub.Item{
		{Path: selftestHashModePath, Value: selftestHashModeValue},
	}
	if err := pub.SetItems(ctx, yangpub.DatastoreRunning, publishItems); err != nil {
		fmt.Fprintf(os.Stderr, "mwan wanconfig-selftest: publish: %v\n", err)
		return 1
	}
	log.Info("published selftest leaf",
		"path", selftestHashModePath, "prior_value_present", priorFound)

	providerFn := func(_ context.Context, xpath string) ([]yangpub.Item, error) {
		log.Info("provider read", "xpath", xpath)
		requestTime := time.Now().UTC().Format(time.RFC3339)
		operItems := []yangpub.Item{
			{
				Path:  "/ietf-interfaces:interfaces/interface[name='wanconfig-selftest0']/type",
				Value: "iana-if-type:other",
			},
			{
				Path:  "/ietf-interfaces:interfaces/interface[name='wanconfig-selftest0']/description",
				Value: "computed at " + requestTime,
			},
		}
		return operItems, nil
	}
	if err := pub.RegisterProvider(ctx, selftestProviderModule, selftestProviderPath, providerFn); err != nil {
		fmt.Fprintf(os.Stderr, "mwan wanconfig-selftest: register provider: %v\n", err)
		return 1
	}
	log.Info("provider registered, holding for external reads",
		"module", selftestProviderModule,
		"path", selftestProviderPath,
		"hold", selftestHoldTime.String())
	time.Sleep(selftestHoldTime)
	log.Info("wanconfig selftest complete")
	return 0
}

// restoreSelftestLeaf puts the hash mode back the way the selftest found
// it: the prior value when one was set, and no value at all when the leaf
// was absent and the schema default applied. A restore failure is logged
// rather than returned, because it runs from a deferred call after the
// exit code is decided.
func restoreSelftestLeaf(pub yangpub.Publisher, log *slog.Logger, priorValue string, priorFound bool) {
	ctx, cancel := context.WithTimeout(context.Background(), restoreSelftestTimeout)
	defer cancel()

	if priorFound {
		items := []yangpub.Item{{Path: selftestHashModePath, Value: priorValue}}
		if err := pub.SetItems(ctx, yangpub.DatastoreRunning, items); err != nil {
			log.Error("selftest leaf restore failed",
				"path", selftestHashModePath, "value", priorValue, "err", err)
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

// selftestModels lists the gateway's model files in install order, the
// order the deploy installs them, each matched by pattern because the
// file name carries the revision and a models directory may carry a
// different revision of the interface-type registry than the deploy ships.
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
	{pattern: "goodkind-mwan-steering@*.yang", features: nil},
}

// resolveSelftestModels finds exactly one file per model in dir.
func resolveSelftestModels(log *slog.Logger, dir string) ([]yangpub.Model, error) {
	models := make([]yangpub.Model, 0, len(selftestModels))
	for _, entry := range selftestModels {
		matches, err := filepath.Glob(filepath.Join(dir, entry.pattern))
		if err != nil {
			return nil, failStep(log, "match "+entry.pattern, err)
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("want exactly one file matching %s in %s, found %d",
				entry.pattern, dir, len(matches))
		}
		models = append(models, yangpub.Model{Path: matches[0], Features: entry.features})
	}
	return models, nil
}

// selftestGateway is the shape the private selftest publishes: one
// translating member on tier 0 and the internal link, the smallest
// configuration that exercises every owned subtree.
func selftestGateway() wanconfig.Gateway {
	return wanconfig.Gateway{
		InternalIface: "eninternal0",
		HashMode:      "random",
		Group: wanconfig.GroupSettings{
			ReservedTables:     []uint32{400, 500},
			InternalPrefix:     netip.MustParsePrefix("3d06:bad:b01:210::/60"),
			OpnsenseEdgeV6:     netip.MustParseAddr("2001:db8:fe::2"),
			MwanbrEdgeV6:       netip.MustParseAddr("2001:db8:fe::3"),
			InternalNetV4:      netip.MustParsePrefix("192.0.2.0/29"),
			ProbeTimeoutMillis: 2000,
		},
		Members: []wanconfig.Member{{
			Name:        "att",
			Iface:       "enatt0",
			Tier:        0,
			Weight:      1,
			ProbePolicy: "att",
			NPTInternal: netip.MustParsePrefix("3d06:bad:b01:210::/60"),
			NPTExternal: netip.MustParsePrefix("2001:db8:a::/60"),
			TableID:     100,
			FwMark:      1,
			FwMarkPrio:  100,
			FromPrio:    55,
			V4Source:    "",
			ForcedDSCP:  8,
			// The mapping's external address is the one selftestStore reports
			// the link owning, so the read carries the configured mapping and
			// the live owned address under one wan container.
			StaticMappings: []wanconfig.StaticMapping{{
				External: netip.MustParseAddr(selftestOwnedAddress),
				Internal: netip.MustParseAddr("192.0.2.3"),
			}},
			Health: &wanconfig.ProbeSettings{
				Enabled:              true,
				PingCount:            new(uint8(3)),
				SuccessThreshold:     new(uint8(2)),
				FailureThreshold:     new(uint8(2)),
				RecoveryThreshold:    new(uint8(2)),
				CheckIntervalSeconds: new(uint32(10)),
				TargetsV4:            []netip.Addr{netip.MustParseAddr("192.0.2.10")},
				TargetsV6:            []netip.Addr{netip.MustParseAddr("2001:db8:53::1")},
				HTTPURLs:             []string{"https://example.test/ip"},
			},
		}},
		Daemon: wanconfig.DaemonSettings{
			Watchdog: wanconfig.WatchdogSettings{
				Present:                      true,
				DeployWindowMinutes:          30,
				ConnectivityTimeoutSeconds:   60,
				CheckIntervalHealthySeconds:  30,
				CheckIntervalDegradedSeconds: 10,
				PostRollbackGraceSeconds:     120,
				AlertCooldownSeconds:         300,
				DeployGracePeriodSeconds:     60,
				MaxRollbackAttempts:          3,
				SnapshotHealthyThreshold:     20,
				MaxKnownGoodSnapshots:        3,
				PingTargets: []netip.Addr{
					netip.MustParseAddr("2606:4700:4700::1111"),
					netip.MustParseAddr("1.1.1.1"),
				},
			},
			OOB: wanconfig.OOBSettings{
				V6Present:         true,
				V6Iface:           "enoob0",
				V6Addr:            netip.MustParseAddr("2001:db8:ff::2"),
				V6TableID:         500,
				ManageSLAACRule:   true,
				SLAACRulePriority: 7,
				V4Present:         false,
				V4Iface:           "",
				V4TableID:         0,
			},
			Tap: wanconfig.TapSettings{
				Present:           true,
				Unit:              "cloudflared-oob.service",
				DowngradePatterns: []string{"failed to sufficiently increase receive buffer size"},
			},
		},
	}
}

// selftestStore is the live state the private selftest serves for that
// gateway, written the way the modules write it.
func selftestStore() *wanstate.Store {
	store := wanstate.New()
	store.SetHealth(map[string]wanstate.MemberHealth{
		"att": {
			Verdict:             wanstate.HealthHealthy,
			ConsecutiveFailures: 0,
			LastTransition:      time.Time{},
			V4:                  wanstate.ProbePass,
			V6:                  wanstate.ProbePass,
		},
	})
	store.SetRouting(0, map[string]wanstate.MemberRouting{"att": {
		Carrying:       true,
		OwnedAddresses: []netip.Addr{netip.MustParseAddr(selftestOwnedAddress)},
	}})
	store.SetTranslation(map[string]wanstate.MemberTranslation{
		"att": {Delegated: netip.MustParsePrefix("2001:db8:a::/60"), KernelPresent: true},
	})
	store.SetIntendedRuleset("chain prerouting:\n  iif \"enatt0\" ip6 daddr 2001:db8:a::/60 dnat prefix to 3d06:bad:b01:210::/60\nchain postrouting:\n")
	return store
}

// runPrivateSelftest proves the serving contract against a private
// repository: install the models, publish the configuration, own the
// modules, register the providers, and read the operational datastore
// over a second connection the way the RESTCONF server does. The read
// must carry the configuration and the live state together, a later
// publish must succeed with the ownership alive, and Close must release
// everything. Each failure names the step.
func runPrivateSelftest(log *slog.Logger, flags selftestFlags) int {
	if err := runPrivateSelftestSteps(log, flags); err != nil {
		fmt.Fprintf(os.Stderr, "mwan wanconfig-selftest: %v\n", err)
		return 1
	}
	log.Info("wanconfig private selftest complete", "repository", flags.repository)
	return 0
}

// sysrepoSHMDir is where sysrepo keeps its shared-memory segments. A
// disconnect leaves the main and per-module segments behind, so the
// private selftest removes its own prefixed set on the way out.
const sysrepoSHMDir = "/dev/shm"

func runPrivateSelftestSteps(log *slog.Logger, flags selftestFlags) error {
	ctx, cancel := context.WithTimeout(context.Background(), selftestTimeout)
	defer cancel()

	reader, closeRepository, err := openPrivateRepository(ctx, log, flags)
	if err != nil {
		return err
	}
	// Deferred before the daemon connection's own close, so it runs after
	// both connections have disconnected.
	defer closeRepository()

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
	if err := registerLiveStateProviders(ctx, log, daemon, selftestStore(), gateway); err != nil {
		return failStep(log, "register providers", err)
	}

	if err := checkSelftestTree(ctx, log, reader); err != nil {
		return err
	}
	if err := checkSelftestDaemon(ctx, log, reader); err != nil {
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
	// With nothing owned and nothing provided the datastore may still
	// print the bare container; what must be gone is the member's
	// configuration and its state.
	if err := checkSelftestInterfacesBare(log, json.RawMessage(after)); err != nil {
		return failStep(log, "interfaces tree after close: "+after, err)
	}
	return nil
}

// openPrivateRepository points this process's datastore at a fresh private
// repository, installs the gateway's models into it over a reader connection,
// and returns that connection with a cleanup that closes it and removes the
// run's shared memory. The caller defers the cleanup before opening any other
// connection, so the shared memory goes only after every connection has
// disconnected.
func openPrivateRepository(
	ctx context.Context,
	log *slog.Logger,
	flags selftestFlags,
) (yangpub.Publisher, func(), error) {
	if err := os.MkdirAll(flags.repository, 0o750); err != nil {
		return nil, nil, failStep(log, "create repository", err)
	}
	// The repository must be this run's own. Pointing the selftest at a
	// populated one, the host's included, would leave connection records
	// in it and test a datastore the host may be serving.
	entries, err := os.ReadDir(flags.repository)
	if err != nil {
		return nil, nil, failStep(log, "read repository", err)
	}
	if len(entries) > 0 {
		return nil, nil, fmt.Errorf("repository %s is not empty; the private selftest needs a fresh directory", flags.repository)
	}
	// sysrepo reads its repository path and shared-memory prefix from the
	// environment at connect time; a distinct prefix keeps this run apart
	// from any datastore the host serves.
	shmPrefix := fmt.Sprintf("mwanselftest%d", os.Getpid())
	restoreEnv := setSelftestEnv([]envSetting{
		{name: "SYSREPO_REPOSITORY_PATH", value: flags.repository},
		{name: "SYSREPO_SHM_PREFIX", value: shmPrefix},
		// The static sysrepo compiles with the upstream group policy
		// (SYSREPO_GROUP=sysrepo, MWAN-435), and sr_is_prod_env() gates every
		// group chown on this variable being absent. The private repository is
		// this run's own, so the group policy has nothing to protect here, and
		// without this the selftest needs a sysrepo group with the running
		// user in it on every machine that runs it.
		{name: "SR_ENV_RUN_TESTS", value: "1"},
	})

	models, err := resolveSelftestModels(log, flags.modelsDir)
	if err != nil {
		restoreEnv()
		return nil, nil, err
	}
	reader, err := yangpub.New(log)
	if err != nil {
		removeSelftestSHM(log, shmPrefix)
		restoreEnv()
		return nil, nil, failStep(log, "reader connection", err)
	}
	// The environment goes back last, after the connection has disconnected
	// and the shared memory is gone, so a later connection in this process
	// reaches the datastore it reached before the selftest ran.
	closeRepository := func() {
		_ = reader.Close()
		removeSelftestSHM(log, shmPrefix)
		restoreEnv()
	}
	if err := reader.InstallModules(ctx, models, flags.modelsDir); err != nil {
		closeRepository()
		return nil, nil, failStep(log, "install models", err)
	}
	return reader, closeRepository, nil
}

// envSetting is one process environment variable and the value to give it.
type envSetting struct {
	name  string
	value string
}

// setSelftestEnv sets each variable and returns a function that puts every
// one back: the prior value when the variable was set, and no variable at
// all when it was absent.
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

// selftestNotifTimeout bounds the wait for each notification to reach the
// subscriber.
const selftestNotifTimeout = 10 * time.Second

// receivedNotification is one notification as the subscriber saw it.
type receivedNotification struct {
	xpath   string
	payload string
}

// checkSelftestNotifications proves the streaming seam end to end: the
// reader subscribes the way the stack's servers do, the daemon side is
// driven through the same store transitions the modules commit, and both
// notifications arrive with their leaves.
func checkSelftestNotifications(
	ctx context.Context,
	log *slog.Logger,
	reader yangpub.Publisher,
	daemon yangpub.Publisher,
) error {
	received := make(chan receivedNotification, 4)
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
	// The sender must have left the binding before the caller closes the
	// daemon connection, the same contract the surface's Close keeps.
	defer func() {
		stopNotifier()
		<-senderDone
	}()

	// The two transitions the acceptance names: a committed health
	// transition, and a routing pass that installs a different tier than
	// the baseline selftestStore wrote.
	store.NotifyHealthTransition("att", wanstate.HealthHealthy, wanstate.HealthUnhealthy)
	store.SetRouting(1, map[string]wanstate.MemberRouting{"att": {Carrying: false, OwnedAddresses: nil}})

	byPath := map[string]string{}
	for len(byPath) < 2 {
		select {
		case notification := <-received:
			byPath[notification.xpath] = notification.payload
		case <-time.After(selftestNotifTimeout):
			return failStep(log, "await notifications",
				fmt.Errorf("received %v, want health-transition and tier-change", byPath))
		}
	}
	if err := checkSelftestHealthNotification(log, byPath["/goodkind-mwan-steering:health-transition"]); err != nil {
		return failStep(log, "health-transition notification", err)
	}
	if err := checkSelftestTierNotification(log, byPath["/goodkind-mwan-steering:tier-change"]); err != nil {
		return failStep(log, "tier-change notification", err)
	}
	return nil
}

func checkSelftestHealthNotification(log *slog.Logger, payload string) error {
	if payload == "" {
		return errors.New("not received")
	}
	root, err := unmarshalObject(log, json.RawMessage(payload), "health-transition payload")
	if err != nil {
		return err
	}
	body, err := unmarshalObject(log, root["goodkind-mwan-steering:health-transition"], "health-transition body")
	if err != nil {
		return err
	}
	if err := expectLeaf(body, "interface", `"enatt0"`, payload); err != nil {
		return err
	}
	if err := expectLeaf(body, "health", `"unhealthy"`, payload); err != nil {
		return err
	}
	return expectLeaf(body, "previous-health", `"healthy"`, payload)
}

func checkSelftestTierNotification(log *slog.Logger, payload string) error {
	if payload == "" {
		return errors.New("not received")
	}
	root, err := unmarshalObject(log, json.RawMessage(payload), "tier-change payload")
	if err != nil {
		return err
	}
	body, err := unmarshalObject(log, root["goodkind-mwan-steering:tier-change"], "tier-change body")
	if err != nil {
		return err
	}
	if err := expectLeaf(body, "active-tier", "1", payload); err != nil {
		return err
	}
	return expectLeaf(body, "previous-tier", "0", payload)
}

// removeSelftestSHM deletes the shared-memory segments this run's prefix
// created. A failure is logged, not returned: the verdict is already
// decided when this runs.
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

// unmarshalObject decodes one JSON object level, so a check reaches into
// the tree without committing to its full shape.
func unmarshalObject(log *slog.Logger, raw json.RawMessage, what string) (map[string]json.RawMessage, error) {
	object := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, failStep(log, "decode "+what, err)
	}
	return object, nil
}

func unmarshalArray(log *slog.Logger, raw json.RawMessage, what string) ([]json.RawMessage, error) {
	var array []json.RawMessage
	if err := json.Unmarshal(raw, &array); err != nil {
		return nil, failStep(log, "decode "+what, err)
	}
	return array, nil
}

// expectLeaf compares one leaf's JSON encoding against the expected one.
func expectLeaf(object map[string]json.RawMessage, name string, want string, where string) error {
	got, present := object[name]
	if !present {
		return fmt.Errorf("%s: %s is absent", where, name)
	}
	if string(got) != want {
		return fmt.Errorf("%s: %s = %s, want %s", where, name, got, want)
	}
	return nil
}

// checkSelftestTree reads both owned subtrees from the operational
// datastore and checks that the configuration and the live state arrive
// together.
func checkSelftestTree(ctx context.Context, log *slog.Logger, reader yangpub.Publisher) error {
	interfacesJSON, found, err := reader.ExportJSON(ctx, yangpub.DatastoreOperational, "/ietf-interfaces:*")
	if err != nil {
		return failStep(log, "operational interfaces read", err)
	}
	if !found {
		return errors.New("operational interfaces read: nothing served")
	}
	if err := checkSelftestInterfaces(log, json.RawMessage(interfacesJSON)); err != nil {
		return failStep(log, "interfaces tree: "+interfacesJSON, err)
	}
	natJSON, found, err := reader.ExportJSON(ctx, yangpub.DatastoreOperational, "/ietf-nat:*")
	if err != nil {
		return failStep(log, "operational nat read", err)
	}
	if !found {
		return errors.New("operational nat read: nothing served")
	}
	if err := checkSelftestNAT(log, json.RawMessage(natJSON)); err != nil {
		return failStep(log, "nat tree: "+natJSON, err)
	}
	return nil
}

func checkSelftestInterfaces(log *slog.Logger, tree json.RawMessage) error {
	root, err := unmarshalObject(log, tree, "interfaces tree")
	if err != nil {
		return err
	}
	interfaces, err := unmarshalObject(log, root["ietf-interfaces:interfaces"], "interfaces container")
	if err != nil {
		return err
	}
	entries, err := unmarshalArray(log, interfaces["interface"], "interface list")
	if err != nil {
		return err
	}
	var member map[string]json.RawMessage
	for _, entry := range entries {
		decoded, err := unmarshalObject(log, entry, "interface entry")
		if err != nil {
			return err
		}
		if string(decoded["name"]) == `"enatt0"` {
			member = decoded
		}
	}
	if member == nil {
		return errors.New("interface enatt0 is absent")
	}
	steering, err := unmarshalObject(log, member["goodkind-mwan-steering:steering"], "steering container")
	if err != nil {
		return err
	}
	if err := expectLeaf(steering, "tier", "0", "configuration"); err != nil {
		return err
	}
	if err := expectLeaf(steering, "probe-policy", `"att"`, "configuration"); err != nil {
		return err
	}
	state, err := unmarshalObject(log, steering["state"], "steering state")
	if err != nil {
		return err
	}
	if err := expectLeaf(state, "health", `"healthy"`, "live state"); err != nil {
		return err
	}
	if err := expectLeaf(state, "carrying", "true", "live state"); err != nil {
		return err
	}
	if err := checkSelftestOwnedAddresses(log, member); err != nil {
		return err
	}
	group, err := unmarshalObject(log, interfaces["goodkind-mwan-steering:steering-group"], "steering group")
	if err != nil {
		return err
	}
	groupState, err := unmarshalObject(log, group["state"], "steering group state")
	if err != nil {
		return err
	}
	if err := expectLeaf(groupState, "active-tier", "0", "live state"); err != nil {
		return err
	}
	if _, present := groupState["intended-ruleset"]; !present {
		return errors.New("live state: intended-ruleset is absent")
	}
	return nil
}

// checkSelftestOwnedAddresses checks the member's wan container carries the
// provider's name, the static mapping the configuration publish wrote, and
// exactly the one owned address the store reports. The configuration and the
// live state both write into this container, so one read proves they merge
// rather than one replacing the other.
func checkSelftestOwnedAddresses(log *slog.Logger, member map[string]json.RawMessage) error {
	wan, err := unmarshalObject(log, member["goodkind-mwan-steering:wan"], "wan container")
	if err != nil {
		return err
	}
	if err := expectLeaf(wan, "name", `"att"`, "live state"); err != nil {
		return err
	}
	mappings, err := unmarshalArray(log, wan["static-mapping"], "static-mapping list")
	if err != nil {
		return err
	}
	if len(mappings) != 1 {
		return fmt.Errorf("configuration: static-mapping = %s, want one entry", wan["static-mapping"])
	}
	mapping, err := unmarshalObject(log, mappings[0], "static-mapping entry")
	if err != nil {
		return err
	}
	if err := expectLeaf(mapping, "external", `"`+selftestOwnedAddress+`"`, "configuration"); err != nil {
		return err
	}
	owned, err := unmarshalArray(log, wan["owned-address"], "owned-address leaf-list")
	if err != nil {
		return err
	}
	want := `"` + selftestOwnedAddress + `"`
	if len(owned) != 1 || string(owned[0]) != want {
		return fmt.Errorf("live state: owned-address = %s, want [%s]", wan["owned-address"], want)
	}
	return nil
}

// checkSelftestDaemon reads the steering module's own subtree from the
// operational datastore and checks the daemon settings arrived: the
// watchdog policy with its probe targets, the out-of-band policy, and
// the tap.
func checkSelftestDaemon(ctx context.Context, log *slog.Logger, reader yangpub.Publisher) error {
	daemonJSON, found, err := reader.ExportJSON(ctx, yangpub.DatastoreOperational, "/goodkind-mwan-steering:*")
	if err != nil {
		return failStep(log, "operational daemon read", err)
	}
	if !found {
		return errors.New("operational daemon read: nothing served")
	}
	root, err := unmarshalObject(log, json.RawMessage(daemonJSON), "daemon tree")
	if err != nil {
		return failStep(log, "daemon tree: "+daemonJSON, err)
	}
	daemon, err := unmarshalObject(log, root["goodkind-mwan-steering:daemon"], "daemon container")
	if err != nil {
		return failStep(log, "daemon tree: "+daemonJSON, err)
	}
	watchdog, err := unmarshalObject(log, daemon["watchdog"], "watchdog container")
	if err != nil {
		return err
	}
	if err := expectLeaf(watchdog, "deploy-window-minutes", "30", "watchdog"); err != nil {
		return err
	}
	if err := expectLeaf(watchdog, "max-rollback-attempts", "3", "watchdog"); err != nil {
		return err
	}
	targets, err := unmarshalObject(log, watchdog["probe-targets"], "probe targets")
	if err != nil {
		return err
	}
	pings, err := unmarshalArray(log, targets["ping"], "ping targets")
	if err != nil {
		return err
	}
	if len(pings) != 2 {
		return fmt.Errorf("watchdog ping targets = %d, want 2", len(pings))
	}
	oob, err := unmarshalObject(log, daemon["oob"], "oob container")
	if err != nil {
		return err
	}
	oobV6, err := unmarshalObject(log, oob["ipv6"], "oob ipv6")
	if err != nil {
		return err
	}
	if err := expectLeaf(oobV6, "interface", `"enoob0"`, "oob"); err != nil {
		return err
	}
	if err := expectLeaf(oobV6, "table-id", "500", "oob"); err != nil {
		return err
	}
	if _, present := oob["ipv4"]; present {
		return errors.New("oob ipv4 published without a config carrying it")
	}
	tap, err := unmarshalObject(log, daemon["tap"], "tap container")
	if err != nil {
		return err
	}
	return expectLeaf(tap, "unit", `"cloudflared-oob.service"`, "tap")
}

func checkSelftestNAT(log *slog.Logger, tree json.RawMessage) error {
	root, err := unmarshalObject(log, tree, "nat tree")
	if err != nil {
		return err
	}
	nat, err := unmarshalObject(log, root["ietf-nat:nat"], "nat container")
	if err != nil {
		return err
	}
	instances, err := unmarshalObject(log, nat["instances"], "instances container")
	if err != nil {
		return err
	}
	entries, err := unmarshalArray(log, instances["instance"], "instance list")
	if err != nil {
		return err
	}
	if len(entries) != 1 {
		return fmt.Errorf("nat instances = %d, want 1", len(entries))
	}
	instance, err := unmarshalObject(log, entries[0], "nat instance")
	if err != nil {
		return err
	}
	if err := expectLeaf(instance, "name", `"att"`, "configuration"); err != nil {
		return err
	}
	// The type is an identity of the instance's own module, which the JSON
	// encoding prints without a module prefix.
	if err := expectLeaf(instance, "type", `"nptv6"`, "configuration"); err != nil {
		return err
	}
	return expectLeaf(instance, "goodkind-mwan-steering:kernel-present", "true", "live state")
}

// checkSelftestInterfacesBare fails when the interfaces tree still carries
// the member's steering configuration or state.
func checkSelftestInterfacesBare(log *slog.Logger, tree json.RawMessage) error {
	root, err := unmarshalObject(log, tree, "interfaces tree")
	if err != nil {
		return err
	}
	raw, present := root["ietf-interfaces:interfaces"]
	if !present {
		return nil
	}
	interfaces, err := unmarshalObject(log, raw, "interfaces container")
	if err != nil {
		return err
	}
	rawList, present := interfaces["interface"]
	if !present {
		return nil
	}
	entries, err := unmarshalArray(log, rawList, "interface list")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		decoded, err := unmarshalObject(log, entry, "interface entry")
		if err != nil {
			return err
		}
		rawSteering, present := decoded["goodkind-mwan-steering:steering"]
		if !present {
			continue
		}
		steering, err := unmarshalObject(log, rawSteering, "steering container")
		if err != nil {
			return err
		}
		if _, present := steering["tier"]; present {
			return errors.New("configuration still enabled after close")
		}
		if _, present := steering["state"]; present {
			return errors.New("live state still served after close")
		}
	}
	return nil
}
