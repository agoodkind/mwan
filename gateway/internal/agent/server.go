package agent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	mwanv1 "goodkind.io/mwan/gateway/gen/mwan/v1"
	"goodkind.io/mwan/gateway/internal/bgp"
	"goodkind.io/mwan/gateway/internal/configstate"
	"goodkind.io/mwan/gateway/internal/tracing"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

var pingReceivedRE = regexp.MustCompile(`(\d+)\s+received`)

type Server struct {
	mwanv1.UnimplementedMWANAgentServer
	log                    *slog.Logger
	bgp                    *bgp.Speaker // nil when BGP is disabled
	clock                  clock
	forwardingMu           sync.Mutex
	forwardingPath         string
	forwardingPollInterval time.Duration
	forwardingReadTimeout  time.Duration
	announceIPv4           bool
	announceIPv6           bool
	requireAllFamilies     bool
	manualWithdraw         bool
	lastReadError          string

	// The constructor sets these paths to local system files. Tests can replace them.
	uptimePath  string
	loadAvgPath string
	meminfoPath string
	statfsPath  string
}

func NewServer(log *slog.Logger, bgpSpeaker *bgp.Speaker) *Server {
	return &Server{
		log:         log,
		bgp:         bgpSpeaker,
		clock:       realClock{},
		uptimePath:  "/proc/uptime",
		loadAvgPath: "/proc/loadavg",
		meminfoPath: "/proc/meminfo",
		statfsPath:  "/",
	}
}

func (a *Server) enrichRPCContext(
	ctx context.Context,
	peerInfo *peer.Peer,
	metadataMap metadata.MD,
) context.Context {
	attrs := make([]slog.Attr, 0, 4)
	if peerInfo != nil && peerInfo.Addr != nil {
		attrs = append(
			attrs,
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

	// Linux ping6 sends a 56-byte payload by default. Webpass drops ICMPv6
	// packets with payloads of 8 bytes or less.
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
				st.Ipv4Reachable = a.pingExitZero(
					ctx,
					"ping",
					"-I",
					iface.Name,
					"-c",
					"1",
					"-W",
					"2",
					"1.1.1.1",
				)
				st.Ipv6Reachable = a.pingExitZero(
					ctx,
					"ping6",
					"-I",
					iface.Name,
					"-c",
					"1",
					"-W",
					"2",
					"2606:4700:4700::1111",
				)
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
		st := a.bgp.Status(ctx)
		resp.BgpAnnouncing = st.Announcing
		resp.BgpAllEstablished = a.bgp.IsEstablished(ctx)
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
	cmd := exec.CommandContext(
		runCtx,
		"systemctl",
		"list-units",
		"--state=failed",
		"--no-legend",
		"--no-pager",
	)
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
		"-c",
		strconv.FormatInt(int64(count), 10),
		"-W",
		strconv.FormatInt(int64(timeoutSec), 10),
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
	_ context.Context,
	_ *mwanv1.GetConfigStateRequest,
) (*mwanv1.GetConfigStateResponse, error) {
	state := configstate.Read("")
	return &mwanv1.GetConfigStateResponse{
		ConfigHash:      state.ConfigHash,
		ConfigManifest:  state.ConfigManifest,
		LastChangeEpoch: a.clock.Now().Unix(),
	}, nil
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
	st := a.bgp.Status(ctx)
	resp := &mwanv1.GetBGPStatusResponse{
		Announcing:     st.Announcing,
		AnnouncingIpv4: st.AnnouncingV4,
		AnnouncingIpv6: st.AnnouncingV6,
		AllEstablished: a.bgp.IsEstablished(ctx),
	}
	for _, p := range st.Peers {
		resp.Peers = append(
			resp.Peers,
			&mwanv1.BGPPeerStatus{
				Address:      p.Address,
				Afi:          p.AFI,
				SessionState: p.State,
				Established:  p.Established,
				UpSinceEpoch: p.UpSince,
			},
		)
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
	if a.forwardingPath != "" {
		a.forwardingMu.Lock()
		defer a.forwardingMu.Unlock()
		state, err := a.readForwardingReady(ctx)
		if err != nil {
			return &mwanv1.AnnounceRoutesResponse{Success: false, Error: err.Error()}, nil
		}
		if a.requireAllFamilies && ((a.announceIPv4 && !state.IPv4) || (a.announceIPv6 && !state.IPv6)) {
			return &mwanv1.AnnounceRoutesResponse{
				Success: false,
				Error:   "primary forwarding is not ready",
			}, nil
		}
		// Require at least one configured announcement family when requireAllFamilies is false.
		if !a.requireAllFamilies && !(a.announceIPv4 && state.IPv4) && !(a.announceIPv6 && state.IPv6) {
			return &mwanv1.AnnounceRoutesResponse{
				Success: false,
				Error:   "no announced address families are ready",
			}, nil
		}
		if err := a.bgp.SetDefaultAnnouncements(state.IPv4, state.IPv6); err != nil {
			return &mwanv1.AnnounceRoutesResponse{Success: false, Error: err.Error()}, nil
		}
		a.manualWithdraw = false
		return &mwanv1.AnnounceRoutesResponse{Success: true}, nil
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
	if a.forwardingPath != "" {
		a.forwardingMu.Lock()
		defer a.forwardingMu.Unlock()
		a.manualWithdraw = true
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
