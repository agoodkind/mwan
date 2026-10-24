// Package testsmtp runs a local SMTP receiver for delivery integration tests.
package testsmtp

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/mwan/internal/clock"
)

// Server accepts real SMTP sessions and retains each accepted message for assertions.
type Server struct {
	listener net.Listener
	mu       sync.Mutex
	reject   bool
	Messages chan []byte
	Path     string
}

// Start configures a local mail account for a receiver that runs until test cleanup.
func Start(t *testing.T) *Server {
	t.Helper()
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{listener: listener, mu: sync.Mutex{}, reject: false, Messages: make(chan []byte, 128), Path: filepath.Join(t.TempDir(), "msmtprc")}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("SMTP listener returned address type %T", listener.Addr())
	}
	text := fmt.Sprintf("account local\nhost 127.0.0.1\nport %d\nfrom sender@example.com\nuser test\npassword test\ntls off\ntls_starttls off\naccount default : local\n", address.Port)
	if err := os.WriteFile(server.Path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	slog.Info("SMTP test receiver started", "address", listener.Addr().String())
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("SMTP accept loop panicked", "error", fmt.Sprint(recovered))
				t.Errorf("SMTP accept loop panicked: %v", recovered)
			}
		}()
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						slog.Error("SMTP session panicked", "error", fmt.Sprint(recovered))
						t.Errorf("SMTP session panicked: %v", recovered)
					}
				}()
				if err := server.receive(connection); err != nil && !errors.Is(err, io.EOF) {
					slog.Error("SMTP receiver session failed", "address", connection.RemoteAddr().String(), "error", err)
					t.Errorf("SMTP receiver: %v", err)
				}
			}()
		}
	}()
	return server
}

// Reject controls whether subsequent DATA commands receive a temporary failure.
func (server *Server) Reject(reject bool) {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.reject = reject
}

func (server *Server) receive(connection net.Conn) error {
	defer connection.Close()
	if err := connection.SetDeadline(clock.Real{}.Now().Add(30 * time.Second)); err != nil {
		slog.Error("set SMTP receiver deadline failed", "error", err)
		return fmt.Errorf("set SMTP receiver deadline: %w", err)
	}
	protocol := textproto.NewConn(connection)
	if err := protocol.PrintfLine("220 localhost SMTP"); err != nil {
		return fmt.Errorf("write SMTP greeting: %w", err)
	}
	for {
		line, err := protocol.ReadLine()
		if err != nil {
			return fmt.Errorf("read SMTP command: %w", err)
		}
		switch {
		case strings.HasPrefix(line, "EHLO"):
			err = protocol.PrintfLine("250-localhost\r\n250 AUTH PLAIN")
		case strings.HasPrefix(line, "AUTH"):
			err = protocol.PrintfLine("235 authenticated")
		case line == "DATA":
			if err := protocol.PrintfLine("354 send data"); err != nil {
				return fmt.Errorf("write SMTP DATA reply: %w", err)
			}
			data, readError := io.ReadAll(protocol.DotReader())
			if readError != nil {
				return fmt.Errorf("read SMTP message: %w", readError)
			}
			server.mu.Lock()
			reject := server.reject
			server.mu.Unlock()
			if reject {
				err = protocol.PrintfLine("451 temporary delivery failure")
			} else {
				server.Messages <- data
				err = protocol.PrintfLine("250 accepted")
			}
		case line == "QUIT":
			if err := protocol.PrintfLine("221 closing"); err != nil {
				return fmt.Errorf("write SMTP closing reply: %w", err)
			}
			return nil
		default:
			err = protocol.PrintfLine("250 accepted")
		}
		if err != nil {
			return fmt.Errorf("write SMTP command reply: %w", err)
		}
	}
}

// Next waits for the next accepted message and fails the test after the deadline.
func (server *Server) Next(t *testing.T) []byte {
	t.Helper()
	select {
	case data := <-server.Messages:
		return data
	case <-time.After(30 * time.Second):
		t.Fatal("SMTP did not accept a message")
		return nil
	}
}

// HTML decodes a MIME message and returns its HTML part for content assertions.
func HTML(t *testing.T, data []byte) string {
	t.Helper()
	message, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	text, err := htmlPart(message.Header.Get("Content-Type"), message.Header.Get("Content-Transfer-Encoding"), message.Body)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func htmlPart(contentType, encoding string, body io.Reader) (string, error) {
	mediaType, parameters, err := mime.ParseMediaType(contentType)
	if err != nil {
		slog.Error("parse message content type failed", "content_type", contentType, "error", err)
		return "", fmt.Errorf("parse message content type: %w", err)
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		reader := multipart.NewReader(body, parameters["boundary"])
		for {
			part, err := reader.NextPart()
			if errors.Is(err, io.EOF) {
				return "", nil
			}
			if err != nil {
				slog.Error("read MIME message part failed", "error", err)
				return "", fmt.Errorf("read MIME part: %w", err)
			}
			text, err := htmlPart(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part)
			if err != nil || text != "" {
				return text, err
			}
		}
	}
	if mediaType != "text/html" {
		return "", nil
	}
	if encoding == "quoted-printable" {
		body = quotedprintable.NewReader(body)
	}
	if encoding == "base64" {
		body = base64.NewDecoder(base64.StdEncoding, body)
	}
	data, err := io.ReadAll(body)
	if err != nil {
		slog.Error("decode HTML message failed", "encoding", encoding, "error", err)
		return "", fmt.Errorf("decode HTML message: %w", err)
	}
	return string(data), nil
}
