package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/bgp"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/tracing"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

var pingReceivedRE = regexp.MustCompile(`(\d+)\s+received`)

type Server struct {
	mwanv1.UnimplementedMWANAgentServer
	deployFilePath string
	deployExpected bool
	log            *slog.Logger
	bgp            *bgp.Speaker // nil when BGP is disabled
	clock          clock
	notifier       notify.Notifier

	// These are injectable in tests to avoid reading real /proc files.
	// Production code leaves them nil and uses the real /proc paths.
	uptimePath  string
	loadAvgPath string
	meminfoPath string
	statfsPath  string
}

func NewServer(
	deployFilePath string,
	log *slog.Logger,
	bgpSpeaker *bgp.Speaker,
	notifier notify.Notifier,
) *Server {
	if notifier == nil {
		notifier = notify.NullNotifier{}
	}
	return &Server{
		deployFilePath: deployFilePath,
		// deployExpected defaults to true so production and testbed-deployed
		// hosts keep their historical alerting behaviour. Fresh hosts that
		// have never been Ansible-deployed flip this to false via
		// SetDeployExpected so the missing file is steady state.
		deployExpected: true,
		log:            log,
		bgp:            bgpSpeaker,
		clock:          realClock{},
		notifier:       notifier,
		uptimePath:     "/proc/uptime",
		loadAvgPath:    "/proc/loadavg",
		meminfoPath:    "/proc/meminfo",
		statfsPath:     "/",
	}
}

// SetDeployExpected toggles the deploy-file-missing notify gate. When
// false, GetConfigState treats a missing deploy file as steady state
// and skips the Notify call. The Resolve call still fires on a
// successful read so a host that flips from fresh to deployed clears
// any stale alert.
func (a *Server) SetDeployExpected(expected bool) {
	a.deployExpected = expected
}

func (a *Server) enrichRPCContext(
	ctx context.Context,
	peerInfo *peer.Peer,
	metadataMap metadata.MD,
) context.Context {
	attrs := make([]slog.Attr, 0, 4)
	if peerInfo != nil && peerInfo.Addr != nil {
		attrs = append(attrs,
			slog.String("peer_addr", peerInfo.Addr.String()),
			slog.String("transport", peerInfo.Addr.Network()),
		)
	}
	if authorityValues := metadataMap.Get(":authority"); len(authorityValues) > 0 {
		attrs = append(attrs, slog.String("grpc_authority", authorityValues[0]))
	}
	return tracing.WithAttrs(ctx, attrs...)
}

func (a *Server) GetHealth(
	ctx context.Context,
	_ *mwanv1.GetHealthRequest,
) (*mwanv1.GetHealthResponse, error) {
	peerInfo, _ := peer.FromContext(ctx)
	metadataMap, _ := metadata.FromIncomingContext(ctx)
	ctx = a.enrichRPCContext(ctx, peerInfo, metadataMap)
	resp := &mwanv1.GetHealthResponse{}

	ipv4OK := a.pingExitZero(ctx, "ping", "-c", "1", "-W", "2", "1.1.1.1")
	resp.Ipv4Ok = ipv4OK

	// No -s flag: relies on Linux ping6's 56-byte default. Webpass drops
	// ICMPv6 with payload <= 8 bytes; if this ever runs on FreeBSD or with
	// a smaller default, add "-s", "16" to keep the Webpass path measurable.
	ipv6OK := a.pingExitZero(ctx, "ping6", "-c", "1", "-W", "2", "2606:4700:4700::1111")
	resp.Ipv6Ok = ipv6OK

	ifaces, err := net.Interfaces()
	if err != nil {
		a.log.ErrorContext(ctx, "list network interfaces", "error", err)
	} else {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			st := &mwanv1.WANStatus{Name: iface.Name}
			st.LinkUp = iface.Flags&net.FlagUp != 0
			if st.LinkUp {
				st.Ipv4Reachable = a.pingExitZero(ctx, "ping", "-I", iface.Name,
					"-c", "1", "-W", "2", "1.1.1.1")
				st.Ipv6Reachable = a.pingExitZero(ctx, "ping6", "-I", iface.Name,
					"-c", "1", "-W", "2", "2606:4700:4700::1111")
			}
			resp.WanInterfaces = append(resp.WanInterfaces, st)
		}
	}

	failed, ferr := a.listFailedSystemdUnits(ctx)
	if ferr != nil {
		a.log.WarnContext(ctx, "list failed systemd units", "error", ferr)
	} else {
		resp.FailedUnits = failed
	}

	if a.bgp != nil {
		st := a.bgp.Status()
		resp.BgpAnnouncing = st.Announcing
		resp.BgpAllEstablished = a.bgp.IsEstablished()
	}

	return resp, nil
}

func (a *Server) pingExitZero(ctx context.Context, name string, args ...string) bool {
	slog.DebugContext(ctx, "agent: pingExitZero", "name", name, "args", args)
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, name, args...)
	err := cmd.Run()
	return err == nil
}

func (a *Server) listFailedSystemdUnits(ctx context.Context) ([]string, error) {
	slog.DebugContext(ctx, "agent: listFailedSystemdUnits")
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "systemctl", "list-units",
		"--state=failed", "--no-legend", "--no-pager")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var names []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "0 loaded") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		unit := strings.TrimPrefix(fields[0], "●")
		unit = strings.TrimSpace(unit)
		if unit == "" || !strings.Contains(unit, ".") {
			continue
		}
		names = append(names, unit)
	}
	if err := sc.Err(); err != nil {
		return names, err
	}
	return names, nil
}

func (a *Server) Ping(
	ctx context.Context,
	req *mwanv1.PingRequest,
) (*mwanv1.PingResponse, error) {
	peerInfo, _ := peer.FromContext(ctx)
	metadataMap, _ := metadata.FromIncomingContext(ctx)
	ctx = a.enrichRPCContext(ctx, peerInfo, metadataMap)
	target := strings.TrimSpace(req.GetTarget())
	if target == "" {
		return nil, status.Error(codes.InvalidArgument, "target is required")
	}

	count := req.GetCount()
	if count <= 0 {
		count = 2
	}
	timeoutSec := req.GetTimeoutSeconds()
	if timeoutSec <= 0 {
		timeoutSec = 3
	}

	bin := "ping"
	if strings.Contains(target, ":") {
		bin = "ping6"
	}

	args := []string{
		"-c", strconv.FormatInt(int64(count), 10),
		"-W", strconv.FormatInt(int64(timeoutSec), 10),
	}
	if bind := strings.TrimSpace(req.GetBindInterface()); bind != "" {
		args = append(args, "-I", bind)
	}
	args = append(args, target)

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin, args...)
	out, err := cmd.Output()
	stdout := string(out)
	packets := parsePingReceivedCount(stdout)

	resp := &mwanv1.PingResponse{
		PacketsReceived: packets,
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			resp.Success = exitErr.ExitCode() == 0
			return resp, nil
		}
		a.log.ErrorContext(ctx, "ping exec", "binary", bin, "error", err)
		return nil, status.Errorf(codes.Internal, "ping: %v", err)
	}
	resp.Success = true
	return resp, nil
}

func parsePingReceivedCount(stdout string) int32 {
	m := pingReceivedRE.FindStringSubmatch(stdout)
	if len(m) < 2 {
		return 0
	}
	// m[1] is guaranteed to be \d+ by the regex; ParseInt cannot fail here.
	n, _ := strconv.ParseInt(m[1], 10, 32)
	return int32(n)
}

func (a *Server) GetConfigState(
	ctx context.Context,
	_ *mwanv1.GetConfigStateRequest,
) (*mwanv1.GetConfigStateResponse, error) {
	peerInfo, _ := peer.FromContext(ctx)
	metadataMap, _ := metadata.FromIncomingContext(ctx)
	ctx = a.enrichRPCContext(ctx, peerInfo, metadataMap)
	composite := sha256.New()
	var manifest strings.Builder
	for _, p := range criticalPaths() {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		fileSum := hex.EncodeToString(sum[:])
		manifest.WriteString(fileSum)
		manifest.WriteString("  ")
		manifest.WriteString(p)
		manifest.WriteByte('\n')
		_, _ = composite.Write([]byte(p))
		_, _ = composite.Write([]byte{0})
		_, _ = composite.Write(data)
	}

	raw, err := os.ReadFile(a.deployFilePath)
	if err != nil {
		// Skip the notify route on hosts that have never been deployed.
		// The watchdog's deploy-staleness check still observes a zero
		// epoch; this gate suppresses the WARN flood on fresh hosts.
		if a.deployExpected {
			a.notifier.Notify(ctx, notify.Event{
				Level:   slog.LevelWarn,
				Kind:    "deploy-file-missing",
				Key:     a.deployFilePath,
				Message: "read deploy file",
				Fields: []slog.Attr{
					slog.String("path", a.deployFilePath),
					slog.String("error", err.Error()),
				},
			})
		}
	} else {
		a.notifier.Resolve(ctx, "deploy-file-missing", a.deployFilePath,
			"deploy file readable",
			slog.String("path", a.deployFilePath),
		)
	}
	deployEpoch, _ := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)

	return &mwanv1.GetConfigStateResponse{
		ConfigHash:      hex.EncodeToString(composite.Sum(nil)),
		ConfigManifest:  manifest.String(),
		LastDeployEpoch: deployEpoch,
		LastChangeEpoch: a.clock.Now().Unix(),
	}, nil
}

// criticalPaths returns the sorted list of files whose contents contribute to
// the composite config hash. Glob patterns are expanded at call time so newly
// created files under watched directories are automatically included.
func criticalPaths() []string {
	var paths []string
	add := func(p string) { paths = append(paths, p) }
	add("/etc/mwan/mwan.env")
	add("/etc/nftables.conf")
	add("/etc/iproute2/rt_tables")
	add("/etc/sysctl.d/99-mwan.conf")
	add("/etc/wpa_supplicant/wpa_supplicant.conf")
	for _, g := range []string{
		"/etc/systemd/network/*",
		"/etc/networkd-dispatcher/routable.d/*",
	} {
		matches, err := filepath.Glob(g)
		if err != nil {
			continue
		}
		for _, m := range matches {
			st, err := os.Stat(m)
			if err != nil || st.IsDir() {
				continue
			}
			add(m)
		}
	}
	sort.Strings(paths)
	return paths
}

func (a *Server) GetSystemInfo(
	ctx context.Context,
	_ *mwanv1.GetSystemInfoRequest,
) (*mwanv1.GetSystemInfoResponse, error) {
	peerInfo, _ := peer.FromContext(ctx)
	metadataMap, _ := metadata.FromIncomingContext(ctx)
	ctx = a.enrichRPCContext(ctx, peerInfo, metadataMap)
	host, err := os.Hostname()
	if err != nil {
		a.log.ErrorContext(ctx, "hostname", "error", err)
		return nil, status.Errorf(codes.Internal, "hostname: %v", err)
	}

	uptimeSec, err := readUptimeSecondsFrom(a.uptimePath)
	if err != nil {
		a.log.ErrorContext(ctx, "read uptime", "error", err)
		return nil, status.Errorf(codes.Internal, "uptime: %v", err)
	}

	loadAvg, err := readLoadAverageFrom(a.loadAvgPath)
	if err != nil {
		a.log.ErrorContext(ctx, "read loadavg", "error", err)
		return nil, status.Errorf(codes.Internal, "loadavg: %v", err)
	}

	memTotal, memAvail, err := readMeminfoKBFrom(a.meminfoPath)
	if err != nil {
		a.log.ErrorContext(ctx, "read meminfo", "error", err)
		return nil, status.Errorf(codes.Internal, "meminfo: %v", err)
	}
	memTotalBytes := memTotal * 1024
	memAvailBytes := memAvail * 1024
	memUsedBytes := memTotalBytes - memAvailBytes

	diskTotal, diskAvail, err := statfsDiskBytes(a.statfsPath)
	if err != nil {
		a.log.ErrorContext(ctx, "statfs root", "error", err)
		return nil, status.Errorf(codes.Internal, "disk: %v", err)
	}
	diskUsed := diskTotal - diskAvail

	return &mwanv1.GetSystemInfoResponse{
		Hostname:         host,
		UptimeSeconds:    uptimeSec,
		LoadAverage:      loadAvg,
		MemoryUsedBytes:  memUsedBytes,
		MemoryTotalBytes: memTotalBytes,
		DiskUsedBytes:    diskUsed,
		DiskTotalBytes:   diskTotal,
	}, nil
}

func readUptimeSeconds() (int64, error) {
	return readUptimeSecondsFrom("/proc/uptime")
}

func readUptimeSecondsFrom(path string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 1 {
		return 0, fmt.Errorf("unexpected /proc/uptime format")
	}
	secFloat, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, err
	}
	return int64(secFloat), nil
}

func readLoadAverage() (string, error) {
	return readLoadAverageFrom("/proc/loadavg")
}

func readLoadAverageFrom(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		return "", fmt.Errorf("unexpected /proc/loadavg format")
	}
	return strings.Join(fields[:3], " "), nil
}

func readMeminfoKB() (totalKB int64, availKB int64, err error) {
	return readMeminfoKBFrom("/proc/meminfo")
}

func readMeminfoKBFrom(path string) (totalKB int64, availKB int64, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	var gotTotal, gotAvail bool
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			v, ok := parseMeminfoLineKB(line)
			if !ok {
				return 0, 0, fmt.Errorf("parse MemTotal")
			}
			totalKB = v
			gotTotal = true
		}
		if strings.HasPrefix(line, "MemAvailable:") {
			v, ok := parseMeminfoLineKB(line)
			if !ok {
				return 0, 0, fmt.Errorf("parse MemAvailable")
			}
			availKB = v
			gotAvail = true
		}
		if gotTotal && gotAvail {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	if !gotTotal || !gotAvail {
		return 0, 0, fmt.Errorf("meminfo missing MemTotal or MemAvailable")
	}
	return totalKB, availKB, nil
}

func parseMeminfoLineKB(line string) (int64, bool) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return 0, false
	}
	rest := strings.TrimSpace(line[idx+1:])
	fields := strings.Fields(rest)
	if len(fields) < 1 {
		return 0, false
	}
	kb, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, false
	}
	return kb, true
}

func statfsDiskBytes(path string) (total int64, avail int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := int64(st.Bsize)
	total = int64(st.Blocks) * bs
	avail = int64(st.Bavail) * bs
	return total, avail, nil
}

func (a *Server) GetBGPStatus(
	ctx context.Context,
	_ *mwanv1.GetBGPStatusRequest,
) (*mwanv1.GetBGPStatusResponse, error) {
	if a.bgp == nil {
		return nil, status.Error(codes.Unavailable, "BGP not enabled")
	}
	st := a.bgp.Status()
	resp := &mwanv1.GetBGPStatusResponse{
		Announcing:     st.Announcing,
		AllEstablished: a.bgp.IsEstablished(),
	}
	for _, p := range st.Peers {
		resp.Peers = append(resp.Peers, &mwanv1.BGPPeerStatus{
			Address:      p.Address,
			Afi:          p.AFI,
			SessionState: p.State,
			Established:  p.Established,
			UpSinceEpoch: p.UpSince,
		})
	}
	return resp, nil
}

func (a *Server) AnnounceRoutes(
	ctx context.Context,
	_ *mwanv1.AnnounceRoutesRequest,
) (*mwanv1.AnnounceRoutesResponse, error) {
	if a.bgp == nil {
		return nil, status.Error(codes.Unavailable, "BGP not enabled")
	}
	if err := a.bgp.AnnounceDefault(); err != nil {
		return &mwanv1.AnnounceRoutesResponse{Success: false, Error: err.Error()}, nil
	}
	return &mwanv1.AnnounceRoutesResponse{Success: true}, nil
}

func (a *Server) WithdrawRoutes(
	ctx context.Context,
	_ *mwanv1.WithdrawRoutesRequest,
) (*mwanv1.WithdrawRoutesResponse, error) {
	if a.bgp == nil {
		return nil, status.Error(codes.Unavailable, "BGP not enabled")
	}
	if err := a.bgp.WithdrawDefault(); err != nil {
		return &mwanv1.WithdrawRoutesResponse{Success: false, Error: err.Error()}, nil
	}
	return &mwanv1.WithdrawRoutesResponse{Success: true}, nil
}

func (a *Server) WatchEvents(
	_ *mwanv1.WatchEventsRequest,
	stream mwanv1.MWANAgent_WatchEventsServer,
) error {
	<-stream.Context().Done()
	return stream.Context().Err()
}
