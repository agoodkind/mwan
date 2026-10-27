// Package vpp configures a separate VPP process through its local binary API.
package vpp

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"go.fd.io/govpp/adapter/socketclient"
	"go.fd.io/govpp/api"
	"go.fd.io/govpp/binapi/memclnt"
	"go.fd.io/govpp/binapi/vpe"
	"go.fd.io/govpp/core"
)

// BindingVersion must match the generated VPP API bindings.
const BindingVersion = "26.06"

// Endpoints requires explicit sockets and the compiled binary API release.
type Endpoints struct {
	APISocket       string
	StatsSocket     string
	ExpectedVersion string
	Timeout         time.Duration
}

// Validate rejects unusable transport settings before any socket connection.
func (settings Endpoints) Validate() error {
	if !filepath.IsAbs(settings.APISocket) {
		return fmt.Errorf("VPP api_socket must be an absolute path")
	}
	if !filepath.IsAbs(settings.StatsSocket) {
		return fmt.Errorf("VPP stats_socket must be an absolute path")
	}
	if settings.ExpectedVersion != BindingVersion {
		return fmt.Errorf("VPP expected_version must be %s for the compiled binary API bindings", BindingVersion)
	}
	if settings.Timeout <= 0 {
		return fmt.Errorf("VPP request_timeout must be positive")
	}
	return nil
}

// Client uses message bindings verified against the connected VPP process.
type Client struct {
	connection *core.Connection
	settings   Endpoints
}

// Open requires the configured process version and compiled API bindings to agree.
func Open(ctx context.Context, settings Endpoints, messages ...api.Message) (result *Client, failure error) {
	defer func() {
		if failure != nil {
			slog.ErrorContext(ctx, "open VPP binary API connection failed", "socket", settings.APISocket, "error", failure)
		}
	}()
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("connect VPP API %s: %w", settings.APISocket, err)
	}
	socket := socketclient.NewVppClient(settings.APISocket)
	socket.SetConnectTimeout(settings.Timeout)
	connection, err := core.Connect(socket)
	if err != nil {
		return nil, fmt.Errorf("connect VPP API %s: %w", settings.APISocket, err)
	}
	messages = append(messages, &vpe.ShowVersion{}, &vpe.ShowVersionReply{}, &memclnt.ControlPing{}, &memclnt.ControlPingReply{})
	if err := connection.CheckCompatibility(messages...); err != nil {
		connection.Disconnect()
		return nil, fmt.Errorf("VPP API %s is incompatible with %s bindings: %w", settings.APISocket, BindingVersion, err)
	}
	requestContext, cancel := context.WithTimeout(ctx, settings.Timeout)
	defer cancel()
	version, err := vpe.NewServiceClient(connection).ShowVersion(requestContext, &vpe.ShowVersion{})
	if err != nil {
		connection.Disconnect()
		return nil, fmt.Errorf("read VPP version from %s: %w", settings.APISocket, err)
	}
	if version.Version != settings.ExpectedVersion && !strings.HasPrefix(version.Version, settings.ExpectedVersion+"-") {
		connection.Disconnect()
		return nil, fmt.Errorf("VPP API %s reports version %q; expected %s", settings.APISocket, version.Version, settings.ExpectedVersion)
	}
	return &Client{connection: connection, settings: settings}, nil
}

// Close releases the API connection after a reconciliation or measurement.
func (client *Client) Close() {
	client.connection.Disconnect()
}

func (client *Client) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, client.settings.Timeout)
}
