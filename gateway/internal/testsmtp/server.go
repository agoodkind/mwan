// Package testsmtp runs a local SMTP receiver for delivery integration tests.
package testsmtp

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
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
)

type Server struct {
	listener net.Listener
	mu       sync.Mutex
	reject   bool
	Messages chan []byte
	Path     string
}

func Start(t *testing.T) *Server {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{listener: listener, Messages: make(chan []byte, 128), Path: filepath.Join(t.TempDir(), "msmtprc")}
	address := listener.Addr().(*net.TCPAddr)
	text := fmt.Sprintf("account local\nhost 127.0.0.1\nport %d\nfrom sender@example.com\nuser test\npassword test\ntls off\ntls_starttls off\naccount default : local\n", address.Port)
	if err := os.WriteFile(server.Path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				if err := server.receive(connection); err != nil && !errors.Is(err, io.EOF) {
					t.Errorf("SMTP receiver: %v", err)
				}
			}()
		}
	}()
	return server
}

func (server *Server) Reject(reject bool) {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.reject = reject
}

func (server *Server) receive(connection net.Conn) error {
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return err
	}
	protocol := textproto.NewConn(connection)
	if err := protocol.PrintfLine("220 localhost SMTP"); err != nil {
		return err
	}
	for {
		line, err := protocol.ReadLine()
		if err != nil {
			return err
		}
		switch {
		case strings.HasPrefix(line, "EHLO"):
			err = protocol.PrintfLine("250-localhost\r\n250 AUTH PLAIN")
		case strings.HasPrefix(line, "AUTH"):
			err = protocol.PrintfLine("235 authenticated")
		case line == "DATA":
			if err := protocol.PrintfLine("354 send data"); err != nil {
				return err
			}
			data, readError := io.ReadAll(protocol.DotReader())
			if readError != nil {
				return readError
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
			return protocol.PrintfLine("221 closing")
		default:
			err = protocol.PrintfLine("250 accepted")
		}
		if err != nil {
			return err
		}
	}
}

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
		return "", err
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		reader := multipart.NewReader(body, parameters["boundary"])
		for {
			part, err := reader.NextPart()
			if errors.Is(err, io.EOF) {
				return "", nil
			}
			if err != nil {
				return "", err
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
	return string(data), err
}
