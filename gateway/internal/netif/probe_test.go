package netif

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

func TestMatchesEchoReply4(t *testing.T) {
	t.Parallel()

	const (
		id  = 1234
		seq = 42
	)
	message := icmp.Message{
		Type: ipv4.ICMPTypeEchoReply,
		Code: 0,
		Body: &icmp.Echo{
			ID:   id,
			Seq:  seq,
			Data: []byte("mwan-v4probe"),
		},
	}
	packet, err := message.Marshal(nil)
	if err != nil {
		t.Fatalf("marshal echo reply: %v", err)
	}

	parsedMessage, err := icmp.ParseMessage(icmpV4Protocol, packet)
	if err != nil {
		t.Fatalf("parse echo reply: %v", err)
	}
	if !matchesEchoReply4(parsedMessage, id, seq) {
		t.Fatal("matching echo reply was rejected")
	}

	if matchesEchoReply4(parsedMessage, id, seq+1) {
		t.Fatal("echo reply with wrong sequence was accepted")
	}
}

func TestPeerMatchesTarget(t *testing.T) {
	t.Parallel()

	target := netip.MustParseAddr("1.1.1.1")
	if !peerMatchesTarget(&net.IPAddr{IP: net.ParseIP("1.1.1.1")}, target) {
		t.Fatal("reply from the target was rejected")
	}
	if peerMatchesTarget(&net.IPAddr{IP: net.ParseIP("8.8.8.8")}, target) {
		t.Fatal("reply from a different target was accepted")
	}
	if peerMatchesTarget(&net.UDPAddr{IP: net.ParseIP("1.1.1.1")}, target) {
		t.Fatal("reply with an unexpected peer type was accepted")
	}
}

func TestHTTPCheckReturnsStatusCode(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter, _ *http.Request,
	) {
		response.WriteHeader(http.StatusTeapot)
	}))
	defer server.Close()

	statusCode, err := HTTPCheck(context.Background(), "", server.URL, time.Second)
	if err != nil {
		t.Fatalf("HTTPCheck: %v", err)
	}
	if statusCode != http.StatusTeapot {
		t.Fatalf("HTTPCheck status = %d, want %d", statusCode, http.StatusTeapot)
	}
}

func TestHTTPGetReturnsStatusAndTrimmedBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter, _ *http.Request,
	) {
		response.WriteHeader(http.StatusTeapot)
		if _, err := response.Write([]byte("  192.0.2.10\n")); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	result, err := HTTPGet(
		context.Background(),
		"",
		"inet",
		server.URL,
		time.Second,
	)
	if err != nil {
		t.Fatalf("HTTPGet: %v", err)
	}
	if result.StatusCode != http.StatusTeapot {
		t.Fatalf(
			"HTTPGet status = %d, want %d",
			result.StatusCode,
			http.StatusTeapot,
		)
	}
	if result.Body != "192.0.2.10" {
		t.Fatalf("HTTPGet body = %q, want %q", result.Body, "192.0.2.10")
	}
}

func TestHTTPGetRejectsResponseBodyOverLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter, _ *http.Request,
	) {
		if _, err := response.Write([]byte(strings.Repeat("x", 4097))); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	_, err := HTTPGet(context.Background(), "", "", server.URL, time.Second)
	if err == nil {
		t.Fatal("HTTPGet accepted a response body larger than 4 KiB")
	}
}

func TestHTTPCheckDoesNotApplyHTTPGetBodyLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter, _ *http.Request,
	) {
		if _, err := response.Write([]byte(strings.Repeat("x", 4097))); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	statusCode, err := HTTPCheck(context.Background(), "", server.URL, time.Second)
	if err != nil {
		t.Fatalf("HTTPCheck: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("HTTPCheck status = %d, want %d", statusCode, http.StatusOK)
	}
}
