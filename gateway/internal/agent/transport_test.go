package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	transportTestTCPAddress = "MWAN_AGENT_TRANSPORT_TEST_TCP_ADDRESS"
	transportTestSocketPath = "MWAN_AGENT_TRANSPORT_TEST_SOCKET_PATH"
	transportTestDeadline   = 10 * time.Second
)

func TestUnixListenerServesGRPCAndRemovesItsSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "agent.sock")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	listener, closeListener, err := listenUnix(logger, socketPath)
	if err != nil {
		t.Fatal(err)
	}

	grpcServer := grpc.NewServer()
	mwanv1.RegisterMWANAgentServer(grpcServer, NewServer(logger, nil))
	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		closeListener()
	})

	connection, err := grpc.NewClient(
		"passthrough:///agent",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = connection.Close()
	})

	ctx, cancel := context.WithTimeout(t.Context(), transportTestDeadline)
	defer cancel()
	response, err := mwanv1.NewMWANAgentClient(connection).GetSystemInfo(
		ctx,
		&mwanv1.GetSystemInfoRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response == nil {
		t.Fatal("GetSystemInfo returned no response")
	}

	closeListener()
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket path remained after cleanup: %v", err)
	}
	if connection, err := net.Dial("unix", socketPath); err == nil {
		_ = connection.Close()
		t.Fatal("Unix socket accepted a connection after cleanup")
	}
}

func TestAgentRunServesConfiguredTransports(t *testing.T) {
	for _, unixEnabled := range []bool{false, true} {
		name := "tcp"
		if unixEnabled {
			name = "tcp-and-unix"
		}
		t.Run(
			name,
			func(t *testing.T) {
				directory := t.TempDir()
				socketPath := ""
				if unixEnabled {
					socketPath = filepath.Join(directory, "agent.sock")
				}
				tcpAddress := unusedTCPAddress(t)
				ctx, cancel := context.WithTimeout(t.Context(), transportTestDeadline)
				defer cancel()
				command := exec.CommandContext(
					ctx,
					os.Args[0],
					"-test.run=^TestAgentRunTransportChild$",
				)
				command.Dir = directory
				command.Env = append(
					os.Environ(),
					transportTestTCPAddress+"="+tcpAddress,
					transportTestSocketPath+"="+socketPath,
				)
				var diagnostics bytes.Buffer
				command.Stdout, command.Stderr = &diagnostics, &diagnostics
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				waited := false
				t.Cleanup(func() {
					if !waited {
						_ = command.Process.Kill()
						_ = command.Wait()
					}
				})
				requestTransportConfigState(t, ctx, "tcp", tcpAddress)
				if unixEnabled {
					requestTransportConfigState(t, ctx, "unix", socketPath)
				} else {
					assertNoUnixListener(t, command.Process.Pid)
				}
				if err := command.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				err := command.Wait()
				waited = true
				if err != nil {
					t.Fatalf("agent shutdown: %v\n%s", err, diagnostics.String())
				}
				assertTCPAddressReleased(t, tcpAddress)
				entries, err := os.ReadDir(directory)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatalf("agent left socket paths after shutdown: %v", entries)
				}
			},
		)
	}
}

func assertNoUnixListener(t *testing.T, processID int) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Unix listener descriptor check requires Linux procfs")
	}

	descriptorPath := fmt.Sprintf("/proc/%d/fd", processID)
	descriptors, err := os.ReadDir(descriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	inodes := make(map[string]bool)
	for _, descriptor := range descriptors {
		path := filepath.Join(descriptorPath, descriptor.Name())
		target, err := os.Readlink(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			t.Fatal(err)
		}
		inode, found := strings.CutPrefix(target, "socket:[")
		if found {
			inode = strings.TrimSuffix(inode, "]")
			inodes[inode] = true
		}
	}

	entries, err := os.ReadFile("/proc/net/unix")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range strings.Split(strings.TrimSpace(string(entries)), "\n")[1:] {
		fields := strings.Fields(entry)
		if len(fields) < 7 || !inodes[fields[6]] {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil {
			t.Fatalf("parse Unix socket flags %q: %v", fields[3], err)
		}
		if flags&0x10000 != 0 {
			t.Fatalf("agent process %d opened a Unix listening socket: %s", processID, entry)
		}
	}
}

func TestAgentRunTransportChild(t *testing.T) {
	tcpAddress := os.Getenv(transportTestTCPAddress)
	if tcpAddress == "" {
		return
	}
	var cfg config.Config
	options := config.AgentSection{
		SocketPath: os.Getenv(transportTestSocketPath),
		TCPAddr:    tcpAddress,
		LogFile:    "",
		Debug:      false,
	}
	if err := Run(&cfg, options); err != nil {
		t.Fatal(err)
	}
}

func TestAgentRunClosesListenersAfterStartupFailure(t *testing.T) {
	for _, test := range []struct {
		name          string
		existingFile  bool
		missingParent bool
		invalidBGP    bool
		wantError     string
	}{
		{"existing Unix path", true, false, false, "unix listen"},
		{"missing Unix directory", false, true, false, "unix listen"},
		{"BGP failure after both listeners open", false, false, true, "parse BGP dynamic neighbor"},
	} {
		t.Run(
			test.name,
			func(t *testing.T) {
				socketPath := filepath.Join(t.TempDir(), "agent.sock")
				contents := []byte("existing path")
				if test.existingFile {
					if err := os.WriteFile(socketPath, contents, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if test.missingParent {
					socketPath = filepath.Join(t.TempDir(), "missing", "agent.sock")
				}
				var cfg config.Config
				if test.invalidBGP {
					cfg.BGP.Enabled = true
					cfg.BGP.DynamicNeighbors = []string{"invalid-prefix"}
				}
				tcpAddress := unusedTCPAddress(t)
				err := Run(
					&cfg,
					config.AgentSection{
						SocketPath: socketPath,
						TCPAddr:    tcpAddress,
						LogFile:    "",
						Debug:      false,
					},
				)
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("startup error = %v, want %q", err, test.wantError)
				}
				assertTCPAddressReleased(t, tcpAddress)
				if test.existingFile {
					actual, err := os.ReadFile(socketPath)
					if err != nil || !bytes.Equal(actual, contents) {
						t.Fatalf("existing path changed: %q (%v)", actual, err)
					}
					return
				}
				if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("socket path remained after startup failure: %v", err)
				}
			},
		)
	}
}

func unusedTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func assertTCPAddressReleased(t *testing.T, address string) {
	t.Helper()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("TCP listener remained open at %s: %v", address, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}

func requestTransportConfigState(t *testing.T, ctx context.Context, network, address string) {
	t.Helper()
	connection, err := grpc.NewClient(
		"passthrough:///agent",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	response, err := mwanv1.NewMWANAgentClient(connection).GetConfigState(
		ctx,
		&mwanv1.GetConfigStateRequest{},
		grpc.WaitForReady(true),
	)
	if err != nil {
		t.Fatalf("GetConfigState over %s: %v", network, err)
	}
	if response.GetConfigHash() == "" {
		t.Fatalf("GetConfigState over %s omitted the configuration hash", network)
	}
}

func TestUnixListenerPreservesExistingPath(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "agent.sock")
	contents := []byte("existing file")
	if err := os.WriteFile(socketPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, _, err := listenUnix(logger, socketPath); err == nil {
		t.Fatal("listenUnix accepted an existing file")
	}
	actual, err := os.ReadFile(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(contents) {
		t.Fatalf("existing path contents = %q", actual)
	}
}

func TestUnixListenerPreservesReplacedPath(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "agent.sock")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	listener, closeListener, err := listenUnix(logger, socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socketPath); err != nil {
		t.Fatal(err)
	}
	contents := []byte("replacement file")
	if err := os.WriteFile(socketPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	closeListener()
	actual, err := os.ReadFile(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(contents) {
		t.Fatalf("replacement path contents = %q", actual)
	}
}
