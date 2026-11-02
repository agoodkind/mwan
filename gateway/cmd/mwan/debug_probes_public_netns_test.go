//go:build linux && firewallnetns

package main_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/netif"
)

const (
	debugProbeNamespaceChild  = "MWAN_DEBUG_PROBE_TEST_CHILD"
	debugProbeNamespaceBinary = "MWAN_DEBUG_PROBE_TEST_BINARY"
	debugProbeResponseBody    = "address fixture\n<html>exact response</html>"
)

func runDebugProbeNamespaceTest(t *testing.T, run func(*testing.T, string)) {
	t.Helper()
	runDebugProbeIsolatedTest(t, run, uintptr(unix.CLONE_NEWNET|unix.CLONE_NEWNS))
}

func runDebugProbeIsolatedTest(t *testing.T, run func(*testing.T, string), cloneFlags uintptr) {
	t.Helper()
	if os.Getenv(debugProbeNamespaceChild) == t.Name() {
		if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
			t.Fatal(err)
		}
		run(t, os.Getenv(debugProbeNamespaceBinary))
		return
	}
	binary, supplied := os.LookupEnv("MWAN_PROTOCOL_TEST_BINARY")
	if supplied {
		if !filepath.IsAbs(binary) {
			t.Fatalf("MWAN_PROTOCOL_TEST_BINARY must be an absolute path: %q", binary)
		}
		info, err := os.Stat(binary)
		if err != nil {
			t.Fatalf("inspect MWAN_PROTOCOL_TEST_BINARY: %v", err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("MWAN_PROTOCOL_TEST_BINARY must be a regular executable file: %q", binary)
		}
	}
	if os.Geteuid() != 0 {
		if supplied || os.Getenv("MWAN_PROTOCOL_ACCEPTANCE") == "1" {
			t.Fatal("explicit debug validation requires root for network and mount namespaces")
		}
		t.Skip("network and mount namespaces require root")
	}
	if !supplied {
		binary = buildReportCommand(t)
	}
	t.Logf("public debug executable: %s", binary)
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	child.Env = append(os.Environ(), debugProbeNamespaceChild+"="+t.Name(), debugProbeNamespaceBinary+"="+binary)
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: cloneFlags}
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated public debug command: %v: %s", err, output)
	}
}

func bindDebugProbePath(t *testing.T, source, target string) {
	t.Helper()
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() {
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
}

func TestCLIActiveProbeNamespaceRuntime(t *testing.T) {
	runDebugProbeNamespaceTest(t, runCLIActiveProbeNamespaceRuntime)
}

func TestCLIConnectivityAbsentPrefixSystemd(t *testing.T) {
	if os.Getenv("MWAN_NETWORKD_RESOLVER_SYSTEMD_TEST") != "1" {
		t.Skip("requires the isolated systemd protocol runner")
	}
	if os.Getenv(debugProbeNamespaceChild) != t.Name() {
		ensureDebugNetworkd(t)
	}
	runDebugProbeIsolatedTest(t, runCLIConnectivityAbsentPrefix, uintptr(unix.CLONE_NEWNS))
}

func ensureDebugNetworkd(t *testing.T) {
	t.Helper()
	output, err := exec.CommandContext(t.Context(), "systemctl", "show", "systemd-networkd.service", "--property=ActiveState", "--value").CombinedOutput()
	if err != nil {
		t.Fatalf("read systemd-networkd state: %v: %s", err, output)
	}
	state := strings.TrimSpace(string(output))
	if state == "active" {
		return
	}
	if state != "inactive" {
		t.Fatalf("systemd-networkd state %q cannot be restored", state)
	}
	t.Cleanup(func() {
		if output, err := exec.Command("systemctl", "stop", "systemd-networkd.service").CombinedOutput(); err != nil {
			t.Errorf("restore systemd-networkd state: %v: %s", err, output)
		}
	})
	if output, err := exec.CommandContext(t.Context(), "systemctl", "start", "systemd-networkd.service").CombinedOutput(); err != nil {
		t.Fatalf("start systemd-networkd: %v: %s", err, output)
	}
}

func runCLIConnectivityAbsentPrefix(t *testing.T, binary string) {
	config := installDebugProbeNetwork(t)
	loopback, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if loopback.Attrs().Flags&net.FlagUp == 0 {
		t.Fatal("loopback must be up in the isolated systemd container")
	}
	for _, cidr := range []string{"1.1.1.1/32", "2606:4700:4700::1111/128"} {
		address, err := netlink.ParseAddr(cidr)
		if err != nil {
			t.Fatal(err)
		}
		address.Flags = unix.IFA_F_NODAD
		if err := netlink.AddrAdd(loopback, address); err != nil {
			t.Fatalf("add local probe target %s: %v", cidr, err)
		}
		t.Cleanup(func() {
			if err := netlink.AddrDel(loopback, address); err != nil {
				t.Errorf("remove local probe target %s: %v", cidr, err)
			}
		})
	}
	output, diagnostics, code := runCLIProcess(t, binary, "debug", "connectivity", "--config", config, "--output-format", "json")
	var results []struct {
		Prefix        string `json:"prefix"`
		PrefixPresent bool   `json:"prefix_present"`
		Ping4Status   string `json:"ping4_status"`
		Ping6Status   string `json:"ping6_status"`
	}
	if code != 0 || json.Unmarshal([]byte(output), &results) != nil || len(results) != 1 || results[0].Prefix != "" || results[0].PrefixPresent || results[0].Ping4Status != "OK" || results[0].Ping6Status != "OK" {
		t.Fatalf("absent prefix exit=%d output=%s diagnostics=%s", code, output, diagnostics)
	}
	text, diagnostics, code := runCLIProcess(t, binary, "debug", "connectivity", "--config", config, "--output-format", "text")
	if code != 0 || !strings.Contains(text, "  prefix: \n  prefix present: false\n") || strings.Contains(text, "invalid Prefix") {
		t.Fatalf("absent prefix text exit=%d output=%s diagnostics=%s", code, text, diagnostics)
	}
}

func runCLIActiveProbeNamespaceRuntime(t *testing.T, binary string) {
	config := installDebugProbeNetwork(t)
	loopback, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(loopback); err != nil {
		t.Fatal(err)
	}
	for _, cidr := range []string{"192.0.2.10/24", "2001:db8:1::10/64", "1.1.1.1/32", "8.8.8.8/32", "2606:4700:4700::1111/128", "2620:fe::9/128"} {
		address, err := netlink.ParseAddr(cidr)
		if err != nil {
			t.Fatal(err)
		}
		address.Flags = unix.IFA_F_NODAD
		if err := netlink.AddrAdd(loopback, address); err != nil {
			t.Fatal(err)
		}
	}
	_, prefix, err := net.ParseCIDR("2001:db8:100::/56")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{LinkIndex: loopback.Attrs().Index, Dst: prefix, Protocol: unix.RTPROT_DHCP}); err != nil {
		t.Fatal(err)
	}
	certificate := startDebugProbeHTTPS(t)
	for _, view := range []string{"curl4", "curl6", "lb4", "lb6", "lb4-ifaces", "lb6-ifaces"} {
		t.Run(view, func(t *testing.T) {
			output, diagnostics := runDebugProbeFixtureCommand(t, binary, config, certificate, view, "json", nil)
			var results []struct {
				netif.HTTPAttempt
				WAN *struct {
					ID    string `json:"id"`
					Name  string `json:"name"`
					Iface string `json:"interface"`
				} `json:"wan"`
			}
			if err := json.Unmarshal([]byte(output), &results); err != nil {
				t.Fatalf("decode=%v output=%s diagnostics=%s", err, output, diagnostics)
			}
			count := 6
			if strings.HasPrefix(view, "curl") {
				count = 2
			}
			if len(results) != count || strings.Count(output, "\n") != 1 || diagnostics != "" {
				t.Fatalf("results=%+v diagnostics=%s", results, diagnostics)
			}
			for index, result := range results {
				family := "inet"
				if strings.Contains(view, "6") {
					family = "inet6"
				}
				iface := ""
				if strings.HasPrefix(view, "curl") || strings.HasSuffix(view, "-ifaces") {
					iface = "lo"
				}
				if result.Iteration != index+1 || result.Timeout != 5*time.Second || result.Family != family || result.Interface != iface {
					t.Fatalf("request=%+v", result)
				}
				if index%2 == 0 {
					if result.Target != "https://ifconfig.co" || result.StatusCode != http.StatusServiceUnavailable || result.Body != debugProbeResponseBody || result.Error != "" {
						t.Fatalf("response=%+v", result)
					}
				} else if result.Target != "https://api.ipify.org" || result.Error != "HTTPGet: HTTP GET: Get \"https://api.ipify.org\": EOF" || result.Body != "" || result.StatusCode != 0 {
					t.Fatalf("failed response=%+v", result)
				}
				if strings.HasSuffix(view, "-ifaces") && (result.WAN == nil || result.WAN.Name != "att" || result.WAN.Iface != "lo") {
					t.Fatalf("WAN=%+v", result.WAN)
				}
			}
			text, diagnostics := runDebugProbeFixtureCommand(t, binary, config, certificate, view, "text", nil)
			if diagnostics != "" || strings.Count(text, debugProbeResponseBody) != count/2 || strings.Count(text, results[1].Error) != count/2 {
				t.Fatalf("text=%s diagnostics=%s", text, diagnostics)
			}
			for _, result := range results {
				record := fmt.Sprintf("  interface: %s\n  family: %s\n  target: %s\n  iteration: %d\n  timeout: %s\n  status code: %d\n  body: %s\n", result.Interface, result.Family, result.Target, result.Iteration, result.Timeout, result.StatusCode, result.Body)
				if !strings.Contains(text, record) {
					t.Fatalf("%s omitted response %+v: %s", view, result, text)
				}
				if result.WAN != nil && (!strings.Contains(text, "  WAN id: "+result.WAN.ID+"\n") || !strings.Contains(text, "  WAN name: "+result.WAN.Name+"\n") || !strings.Contains(text, "  WAN interface: "+result.WAN.Iface+"\n")) {
					t.Fatalf("%s omitted WAN details: %s", view, text)
				}
			}
			for _, format := range []string{"text", "json"} {
				full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, diagnostics = runDebugProbeFixtureCommand(t, binary, config, certificate, view, format, full)
				if err := full.Close(); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(diagnostics, "no space left on device") {
					t.Fatalf("write error diagnostics=%s", diagnostics)
				}
			}
		})
	}
	for _, view := range []string{"ping4", "ping6"} {
		output, diagnostics := runDebugProbeFixtureCommand(t, binary, config, certificate, view, "json", nil)
		var attempts []netif.PingAttempt
		if err := json.Unmarshal([]byte(output), &attempts); err != nil || len(attempts) != 6 || diagnostics != "" {
			t.Fatalf("%s: decode=%v results=%+v diagnostics=%s", view, err, attempts, diagnostics)
		}
		for _, attempt := range attempts {
			if attempt.Error != "" || attempt.RTT <= 0 || attempt.Interface != "lo" {
				t.Fatalf("real echo result=%+v", attempt)
			}
		}
		text, diagnostics := runDebugProbeFixtureCommand(t, binary, config, certificate, view, "text", nil)
		if diagnostics != "" {
			t.Fatalf("%s diagnostics=%s", view, diagnostics)
		}
		assertDebugPingDetails(t, text, attempts)
	}
	output, diagnostics := runDebugProbeFixtureCommand(t, binary, config, certificate, "connectivity", "json", nil)
	var connectivity []struct {
		Addresses     []netif.CurrentAddr `json:"addresses"`
		IPv4Present   bool                `json:"ipv4_present"`
		IPv6Present   bool                `json:"ipv6_present"`
		Prefix        string              `json:"prefix"`
		PrefixPresent bool                `json:"prefix_present"`
		NPTPresent    bool                `json:"npt_present"`
		NPTStatus     string              `json:"npt_status"`
		Ping4Status   string              `json:"ping4_status"`
		Ping6Status   string              `json:"ping6_status"`
		Ping4         []netif.PingAttempt `json:"ping4"`
		Ping6         []netif.PingAttempt `json:"ping6"`
	}
	if err := json.Unmarshal([]byte(output), &connectivity); err != nil || len(connectivity) != 1 || diagnostics != "" {
		t.Fatalf("decode=%v result=%s diagnostics=%s", err, output, diagnostics)
	}
	result := connectivity[0]
	if !result.IPv4Present || !result.IPv6Present || !result.PrefixPresent || result.Prefix != "2001:db8:100::/56" || result.NPTPresent || result.NPTStatus != "FAIL" || result.Ping4Status != "OK" || result.Ping6Status != "OK" || len(result.Addresses) < 6 || len(result.Ping4) != 2 || len(result.Ping6) != 2 {
		t.Fatalf("connectivity=%+v", result)
	}
	for _, attempt := range append(result.Ping4, result.Ping6...) {
		if attempt.Error != "" || attempt.RTT <= 0 || attempt.Timeout != 2*time.Second {
			t.Fatalf("connectivity attempt=%+v", attempt)
		}
	}
	text, diagnostics := runDebugProbeFixtureCommand(t, binary, config, certificate, "connectivity", "text", nil)
	if diagnostics != "" || !strings.Contains(text, "  prefix: "+result.Prefix+"\n") || !strings.Contains(text, "  prefix present: true\n") || !strings.Contains(text, "  NPT present: false\n") {
		t.Fatalf("connectivity text=%s diagnostics=%s", text, diagnostics)
	}
	for _, address := range result.Addresses {
		if !strings.Contains(text, "  address: "+address.CIDR+"\n") || !strings.Contains(text, "    origin: "+address.Origin+"\n") {
			t.Fatalf("connectivity omitted address %+v: %s", address, text)
		}
	}
	assertDebugPingDetails(t, text, append(result.Ping4, result.Ping6...))
	for _, format := range []string{"text", "json"} {
		full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, diagnostics := runDebugProbeFixtureCommand(t, binary, config, certificate, "connectivity", format, full)
		if err := full.Close(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(diagnostics, "no space left on device") {
			t.Fatalf("connectivity output write diagnostics=%s", diagnostics)
		}
	}
}

func assertDebugPingDetails(t *testing.T, text string, attempts []netif.PingAttempt) {
	t.Helper()
	if strings.Count(text, "  attempt ") != len(attempts) {
		t.Fatalf("ping details=%s", text)
	}
	for _, attempt := range attempts {
		fields := fmt.Sprintf("\n    interface: %s\n    family: %s\n    target: %s\n    timeout: %s\n    error: \n", attempt.Interface, attempt.Family, attempt.Target, attempt.Timeout)
		found := false
		for _, record := range strings.Split(text, "  attempt ")[1:] {
			value, rest, ok := strings.Cut(record, "\n")
			if !ok || !strings.HasPrefix(value, fmt.Sprintf("%d: ", attempt.Attempt)) || !strings.HasPrefix("\n"+rest, fields) {
				continue
			}
			rtt, err := time.ParseDuration(strings.TrimPrefix(value, fmt.Sprintf("%d: ", attempt.Attempt)))
			if err != nil || rtt <= 0 {
				t.Fatalf("invalid ping RTT %q: %v", value, err)
			}
			found = true
		}
		if !found {
			t.Fatalf("ping omitted request %+v: %s", attempt, text)
		}
	}
	if strings.Count(text, "    error: \n") != len(attempts) {
		t.Fatalf("ping results=%s", text)
	}
}

func installDebugProbeNetwork(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	fixture, err := os.ReadFile(filepath.Join("..", "..", "yang", "instances", "network-min.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(fixture, &document); err != nil {
		t.Fatal(err)
	}
	var interfaces map[string]json.RawMessage
	if err := json.Unmarshal(document["ietf-interfaces:interfaces"], &interfaces); err != nil {
		t.Fatal(err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(interfaces["interface"], &entries); err != nil {
		t.Fatal(err)
	}
	retained := make([]json.RawMessage, 0, len(entries))
	for _, entry := range entries {
		if bytes.Contains(entry, []byte(`"goodkind-mwan-steering:wan"`)) && !bytes.Contains(entry, []byte(`"name": "att"`)) {
			continue
		}
		retained = append(retained, bytes.ReplaceAll(entry, []byte("enatt0"), []byte("lo")))
	}
	interfaces["interface"], err = json.Marshal(retained)
	if err != nil {
		t.Fatal(err)
	}
	document["ietf-interfaces:interfaces"], err = json.Marshal(interfaces)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "network.json"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	bindDebugProbePath(t, root, "/etc/mwan")
	schema, err := filepath.Abs(filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	bindDebugProbePath(t, schema, "/usr/local/share/wanconfig/yang")
	config := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(config, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return config
}

func startDebugProbeHTTPS(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	hosts := filepath.Join(root, "hosts")
	if err := os.WriteFile(hosts, []byte("127.0.0.1 ifconfig.co api.ipify.org\n::1 ifconfig.co api.ipify.org\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bindDebugProbePath(t, hosts, "/etc/hosts")
	public, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "debug probe fixture"}, DNSNames: []string{"ifconfig.co", "api.ipify.org"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, signingKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(signingKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	tlsCertificate, err := tls.X509KeyPair(certificatePEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	certificate := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(certificate, certificatePEM, 0o600); err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Host == "api.ipify.org" {
			hijacker, ok := writer.(http.Hijacker)
			if !ok {
				t.Error("TLS response writer does not support hijacking")
				return
			}
			connection, _, err := hijacker.Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			if err := connection.Close(); err != nil {
				t.Error(err)
			}
			return
		}
		writer.WriteHeader(http.StatusServiceUnavailable)
		if _, err := fmt.Fprint(writer, " \n", debugProbeResponseBody, "\n "); err != nil {
			t.Error(err)
		}
	})
	for _, address := range []string{"127.0.0.1:443", "[::1]:443"} {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
		listener = tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{tlsCertificate}, MinVersion: tls.VersionTLS12})
		t.Cleanup(func() {
			if err := server.Close(); err != nil {
				t.Error(err)
			}
		})
		go func() {
			if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
				t.Error(err)
			}
		}()
	}
	return certificate
}

func runDebugProbeFixtureCommand(t *testing.T, binary, config, certificate, view, format string, destination io.Writer) (string, string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), binary, "debug", view, "--config", config, "--output-format", format)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.Contains(strings.ToLower(key), "proxy") || key == "SSL_CERT_FILE" {
			continue
		}
		command.Env = append(command.Env, entry)
	}
	command.Env = append(command.Env, "SSL_CERT_FILE="+certificate)
	var output, diagnostics bytes.Buffer
	command.Stdout, command.Stderr = &output, &diagnostics
	if destination != nil {
		command.Stdout = destination
	}
	err := command.Run()
	if destination == nil && err != nil {
		t.Fatalf("%s %s: %v output=%s diagnostics=%s", view, format, err, output.String(), diagnostics.String())
	}
	if destination != nil && err == nil {
		t.Fatal("command accepted the failed output write")
	}
	return output.String(), diagnostics.String()
}
