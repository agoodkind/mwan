// Command protocolrunner executes the privileged protocol acceptance suite.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

type options struct {
	lane                                 lane
	image, arch, source, binary, results string
	ownedRoleConfigs                     string
	originalBinary, originalSHA256       string
}

const (
	ownedRoleService = "mwan/services/mwan-update-att-pinned-dests.service"
	ownedRoleScript  = "mwan/scripts/update-att-pinned-dests.sh"
)

type lane string

const (
	laneNamespace       lane = "namespace"
	laneSystemd         lane = "systemd"
	laneOriginalUpgrade lane = "original-upgrade"
)

type eventAction string

const (
	actionSkip eventAction = "skip"
	actionFail eventAction = "fail"
	actionPass eventAction = "pass"
)

type testEvent struct {
	Action  eventAction `json:"Action"`
	Package string      `json:"Package"`
	Test    string      `json:"Test"`
	Output  string      `json:"Output"`
}

func requiredTests(selected lane) ([]string, error) {
	switch selected {
	case laneNamespace:
		return []string{
			"TestProtocolRunnerBootstrap",
			"TestOwnedDHCPv4DaemonRuntime", "TestOwnedDHCPv4ClasslessDaemonRuntime",
			"TestOwnedDHCPv4RebindDaemonRuntime", "TestOwnedDHCPv4NAKDaemonRuntime",
			"TestOwnedDHCPv4RejectedRecoveryRuntime", "TestOOBDHCPv4DaemonRuntime",
			"TestFailoverDHCPv4DaemonRuntime", "TestFailoverDHCPv4DisabledRuntime",
			"TestOwnedDHCPv6PDDaemonRuntime", "TestOwnedDHCPv6PDDaemonWaitsForRA",
			"TestOwnedDHCPv6PDRejectsInformationRequest",
			"TestOwnedDHCPv6IAAddressOnlyRuntime", "TestOwnedDHCPv6IACombinedRuntime",
			"TestOwnedDHCPv6IAUnequalLifetimesRuntime", "TestOwnedDHCPv6IADuplicateRuntime",
			"TestOOBDHCPv4DaemonRestartRecovery", "TestOOBDHCPv4DaemonLateInterfaceRecovery",
			"TestOOBDHCPv4DaemonRejectedRecovery", "TestOwnedDHCPv6DaemonRestartRecovery",
			"TestAutoconfigurationDaemonRuntime", "TestRadvdAutoconfigurationDaemonRuntime",
			"TestOwnedMappedDaemonRuntime", "TestSelectionExclusionDaemonRuntime",
			"TestOwnedStaticDaemonRuntime", "TestWANFirewallRuntimePackets",
			"TestStartupPolicyIgnoresUnansweredProbes",
			"TestKernelPolicyDaemonRuntime",
			"TestObservationDaemonRuntime",
			"TestDistributionObservationDaemonRuntime",
		}, nil
	case laneSystemd:
		return []string{"TestNetworkdResolverDaemonRuntime", "TestNetworkdOrderedDaemonStartup", "TestStaticResolverDaemonRuntime", "TestOwnedRolesDaemonRuntime", "TestNetworkdNPTEdgeDaemonRuntime", "TestConnectionReleaseDaemonRuntime", "TestDeployOperationWatchRuntime"}, nil
	case laneOriginalUpgrade:
		return []string{"TestLegacyNPTPreparationUpgrade"}, nil
	default:
		return nil, fmt.Errorf("unknown protocol lane %q", selected)
	}
}

func parseOptions() (result options, failure error) {
	defer func() {
		if failure != nil {
			slog.Error("Protocol arguments rejected", "error", failure)
		}
	}()
	var opts options
	var rawLane string
	flags := flag.NewFlagSet("protocolrunner", flag.ContinueOnError)
	flags.StringVar(&rawLane, "lane", "", "namespace, systemd or original-upgrade")
	flags.StringVar(&opts.image, "image", "", "local Docker image")
	flags.StringVar(&opts.arch, "arch", "", "native Linux architecture")
	flags.StringVar(&opts.source, "source", "", "absolute source directory")
	flags.StringVar(&opts.binary, "binary", "", "optional absolute published executable")
	flags.StringVar(&opts.results, "results", "", "acceptance artifact directory")
	flags.StringVar(&opts.ownedRoleConfigs, "owned-role-configs", "", "absolute Configs checkout for the systemd suite")
	flags.StringVar(&opts.originalBinary, "original-binary", "", "absolute original executable for the upgrade")
	flags.StringVar(&opts.originalSHA256, "original-sha256", "", "verified original executable SHA256")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return opts, fmt.Errorf("parse protocol options: %w", err)
	}
	selectedLane := lane(rawLane)
	if _, err := requiredTests(selectedLane); err != nil {
		return opts, err
	}
	opts.lane = selectedLane
	if !filepath.IsAbs(opts.source) {
		return opts, errors.New("-source must be absolute")
	}
	if opts.results == "" {
		return opts, errors.New("-results is required")
	}
	results, err := filepath.Abs(opts.results)
	if err != nil {
		return opts, fmt.Errorf("resolve results directory: %w", err)
	}
	opts.results = results
	if opts.image == "" || (opts.arch != "arm64" && opts.arch != "amd64") {
		return opts, errors.New("-image and -arch arm64 or amd64 are required for a new container")
	}
	if opts.binary != "" && !filepath.IsAbs(opts.binary) {
		return opts, errors.New("-binary must be absolute")
	}
	if err := validateOriginalUpgrade(opts); err != nil {
		return opts, err
	}
	if opts.lane == laneSystemd {
		if !filepath.IsAbs(opts.ownedRoleConfigs) {
			return opts, errors.New("-owned-role-configs must be absolute for the systemd suite")
		}
		for _, relative := range []string{ownedRoleService, ownedRoleScript} {
			path := filepath.Join(opts.ownedRoleConfigs, relative)
			info, err := os.Stat(path)
			if err != nil {
				return opts, fmt.Errorf("inspect owned-role bootstrap %s: %w", path, err)
			}
			if !info.Mode().IsRegular() {
				return opts, fmt.Errorf("owned-role bootstrap must be a regular file: %s", path)
			}
		}
	}
	return opts, nil
}

func validateOriginalUpgrade(opts options) error {
	if opts.lane != laneOriginalUpgrade {
		return nil
	}
	if opts.arch != "amd64" || opts.binary == "" || !filepath.IsAbs(opts.originalBinary) {
		return errors.New("original-upgrade requires AMD64 and absolute candidate/original executable paths")
	}
	if len(opts.originalSHA256) != sha256.Size*2 || strings.Trim(opts.originalSHA256, "0123456789abcdef") != "" {
		return errors.New("original-upgrade requires a complete lowercase hexadecimal executable SHA256")
	}
	return nil
}

func dockerCommand(ctx context.Context, arguments ...string) error {
	command := exec.CommandContext(ctx, "docker", arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		slog.Error("Docker command failed", "arguments", arguments, "error", err, "output", string(output))
		return fmt.Errorf("docker %v: %w: %s", arguments, err, output)
	}
	return nil
}

func startContainer(ctx context.Context, opts options, name string) (failure error) {
	defer func() {
		if failure != nil {
			slog.Error("Protocol container startup failed", "container", name, "error", failure)
		}
	}()
	arguments := []string{
		"run", "-d", "--name", name, "--privileged", "--platform", "linux/" + opts.arch,
		"--cgroupns", "private", "--security-opt", "label=disable", "--tmpfs", "/run", "--tmpfs", "/run/lock",
		"--mount", "type=bind,src=" + opts.source + ",dst=/src,readonly",
		"-v", "mwan-wanconfig-gomod:/go/pkg/mod",
		"-v", "mwan-wanconfig-cache-" + opts.arch + ":/root/.cache",
		"-v", "mwan-wanconfig-gomk-" + opts.arch + ":/src/.make",
	}
	if opts.lane == laneSystemd {
		for _, relative := range []string{ownedRoleService, ownedRoleScript} {
			arguments = append(arguments, "--mount", "type=bind,src="+filepath.Join(opts.ownedRoleConfigs, relative)+",dst=/mwan-bootstrap/"+filepath.Base(relative)+",readonly")
		}
	}
	if opts.binary != "" {
		info, err := os.Stat(opts.binary)
		if err != nil {
			return fmt.Errorf("inspect published executable: %w", err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return errors.New("published executable must be a regular executable file")
		}
		arguments = append(arguments, "--mount", "type=bind,src="+opts.binary+",dst=/mwan-release/mwan,readonly")
	}
	if opts.lane == laneOriginalUpgrade {
		arguments = append(arguments, "--mount", "type=bind,src="+opts.originalBinary+",dst=/mwan-original/mwan,readonly")
	}
	arguments = append(arguments, opts.image)
	if opts.lane == laneNamespace {
		arguments = append(arguments, "sleep", "infinity")
	}
	return dockerCommand(ctx, arguments...)
}

func waitSystemd(ctx context.Context, container string, selected lane) (failure error) {
	defer func() {
		if failure != nil {
			slog.Error("Systemd bus readiness failed", "container", container, "error", failure)
		}
	}()
	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var lastError error
	for {
		lastError = dockerCommand(deadline, "exec", container, "systemctl", "show", "--property=Version", "--value")
		if lastError == nil && selected == laneOriginalUpgrade {
			lastError = dockerCommand(deadline, "exec", container, "busctl", "--system", "--no-pager", "list")
		}
		if lastError == nil {
			return nil
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("systemd bus did not become available: %w", lastError)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func testArguments(opts options, container string, required []string) []string {
	arguments := []string{
		"exec", "-w", "/src", "-e", "GOWORK=off", "-e", "GOFLAGS=-buildvcs=false",
		"-e", "MWAN_PROTOCOL_ACCEPTANCE=1",
		"-e", "GIT_CONFIG_COUNT=1", "-e", "GIT_CONFIG_KEY_0=safe.directory", "-e", "GIT_CONFIG_VALUE_0=/src",
	}
	if opts.binary != "" {
		arguments = append(arguments, "-e", "MWAN_PROTOCOL_TEST_BINARY=/mwan-release/mwan")
	}
	if opts.lane == laneSystemd {
		arguments = append(arguments, "-e", "MWAN_NETWORKD_RESOLVER_SYSTEMD_TEST=1", "-e", "MWAN_NETWORKD_STARTUP_SYSTEMD_TEST=1", "-e", "MWAN_RESOLVER_SYSTEMD_TEST=1", "-e", "MWAN_OWNED_ROLE_BOOTSTRAP_DIR=/mwan-bootstrap")
	}
	timeout := "20m"
	if opts.lane == laneOriginalUpgrade {
		arguments = append(arguments, "-e", "MWAN_NPT_LEGACY_BINARY=/mwan-original/mwan", "-e", "MWAN_NPT_LEGACY_SHA256="+opts.originalSHA256)
		timeout = "10m"
	}
	patterns := make([]string, len(required))
	for index, name := range required {
		patterns[index] = regexp.QuoteMeta(name)
	}
	return append(arguments, container, "go", "test", "-json", "-count=1", "-timeout="+timeout, "-tags", "netns firewallnetns",
		"-run", "^("+strings.Join(patterns, "|")+")$", "./cmd/mwan")
}

func runTests(ctx context.Context, arguments, required []string, artifact, diagnostics io.Writer) (failure error) {
	slog.Info("Protocol test execution started", "arguments", arguments, "required", required)
	defer func() {
		if failure != nil {
			slog.Error("Protocol acceptance failed", "error", failure)
		}
	}()
	command := exec.CommandContext(ctx, "docker", arguments...)
	output, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open test output: %w", err)
	}
	command.Stderr = io.MultiWriter(os.Stderr, diagnostics)
	if err := command.Start(); err != nil {
		return fmt.Errorf("start protocol tests: %w", err)
	}
	decoder := json.NewDecoder(io.TeeReader(output, artifact))
	passed := make(map[string]bool)
	var verdict error
	for {
		var event testEvent
		if err := decoder.Decode(&event); err != nil {
			if !errors.Is(err, io.EOF) {
				verdict = errors.Join(verdict, fmt.Errorf("decode test event: %w", err))
				if _, err := io.Copy(artifact, output); err != nil {
					verdict = errors.Join(verdict, fmt.Errorf("retain test output: %w", err))
				}
			}
			break
		}
		if _, err := fmt.Fprint(os.Stdout, event.Output); err != nil {
			verdict = errors.Join(verdict, fmt.Errorf("write test output: %w", err))
		}
		switch event.Action {
		case actionSkip, actionFail:
			verdict = errors.Join(verdict, fmt.Errorf("required protocol execution reported %s: %s %s", event.Action, event.Package, event.Test))
		case actionPass:
			if event.Package == "goodkind.io/mwan/cmd/mwan" {
				passed[event.Test] = true
			}
		}
	}
	if err := command.Wait(); err != nil {
		verdict = errors.Join(verdict, fmt.Errorf("protocol test process: %w", err))
	}
	for _, name := range required {
		if !passed[name] {
			verdict = errors.Join(verdict, fmt.Errorf("required protocol case did not pass: %s", name))
		}
	}
	return verdict
}

func run(ctx context.Context, opts options) (result error) {
	defer func() {
		if result != nil {
			slog.Error("Protocol runner failed", "lane", opts.lane, "error", result)
		}
	}()
	required, err := requiredTests(opts.lane)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(opts.results, 0o755); err != nil {
		return fmt.Errorf("create results directory: %w", err)
	}
	directory, err := os.MkdirTemp(opts.results, string(opts.lane)+"-")
	if err != nil {
		return fmt.Errorf("create run directory: %w", err)
	}
	fmt.Fprintln(os.Stderr, "Protocol artifacts:", directory)
	artifact, err := os.Create(filepath.Join(directory, "events.jsonl"))
	if err != nil {
		return fmt.Errorf("create event artifact: %w", err)
	}
	defer func() { result = errors.Join(result, artifact.Close()) }()
	diagnostics, err := os.Create(filepath.Join(directory, "stderr.log"))
	if err != nil {
		return fmt.Errorf("create diagnostic artifact: %w", err)
	}
	defer func() { result = errors.Join(result, diagnostics.Close()) }()
	container := "mwan-protocol-" + filepath.Base(directory)
	// Docker may create the container before the client receives its response.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		err := dockerCommand(cleanup, "rm", "-f", container)
		result = errors.Join(result, err)
	}()
	if err := startContainer(ctx, opts, container); err != nil {
		return err
	}
	if opts.lane != laneNamespace {
		if err := waitSystemd(ctx, container, opts.lane); err != nil {
			return err
		}
	}
	arguments := testArguments(opts, container, required)
	manifest, err := json.MarshalIndent(struct {
		Container string   `json:"container"`
		Arguments []string `json:"arguments"`
		Required  []string `json:"required"`
	}{container, arguments, required}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), manifest, 0o600); err != nil {
		return fmt.Errorf("write run manifest: %w", err)
	}
	return runTests(ctx, arguments, required, artifact, diagnostics)
}

func exitCode() int {
	opts, err := parseOptions()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if ctx.Err() != nil {
			return 130
		}
		return 1
	}
	return 0
}

func main() {
	code := exitCode()
	slog.Info("Protocol acceptance completed", "exit_code", code)
	os.Exit(code)
}
