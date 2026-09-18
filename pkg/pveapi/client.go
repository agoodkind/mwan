// Package pveapi is a minimal Proxmox VE API client for qemu guest agent exec.
package pveapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Client calls PVE /api2/json over HTTPS (localhost uses InsecureSkipVerify).
type Client struct {
	httpClient *http.Client
	baseURL    string
	tokenID    string
	secret     string
}

// NewClient builds a client. baseURL is e.g. "https://127.0.0.1:8006/api2/json".
func NewClient(baseURL, tokenID, secret string) *Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true,
		},
	}
	return &Client{
		httpClient: &http.Client{
			Timeout:   60 * time.Second,
			Transport: tr,
		},
		baseURL: strings.TrimRight(baseURL, "/"),
		tokenID: tokenID,
		secret:  secret,
	}
}

func (c *Client) authHeader() string {
	return fmt.Sprintf("PVEAPIToken=%s=%s", c.tokenID, c.secret)
}

type guestExecRequest struct {
	Command []string `json:"command"`
}

type guestExecResponse struct {
	Data struct {
		PID int `json:"pid"`
	} `json:"data"`
}

// GuestExec starts a command in the guest; returns QEMU agent PID.
func (c *Client) GuestExec(
	ctx context.Context,
	node, vmid string,
	command []string,
) (int, error) {
	u := fmt.Sprintf(
		"%s/nodes/%s/qemu/%s/agent/exec",
		c.baseURL,
		node,
		vmid,
	)
	body, err := json.Marshal(guestExecRequest{Command: command})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", c.authHeader())
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("pve guest exec: HTTP %d: %s", resp.StatusCode, raw)
	}
	var parsed guestExecResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		slog.WarnContext(
			ctx,
			"failed to decode pve guest exec response",
			"err",
			err,
			"node",
			node,
			"vmid",
			vmid,
		)
		return 0, fmt.Errorf("pve guest exec decode: %w", err)
	}
	if parsed.Data.PID == 0 {
		return 0, fmt.Errorf("pve guest exec: missing pid in %s", raw)
	}
	return parsed.Data.PID, nil
}

type execStatusResponse struct {
	Data struct {
		Exited   int    `json:"exited"`
		ExitCode int    `json:"exitcode"`
		OutData  string `json:"out-data"`
		ErrData  string `json:"err-data"`
	} `json:"data"`
}

// GuestExecStatus waits for completion and returns exit code and decoded stdout.
func (c *Client) GuestExecStatus(
	ctx context.Context,
	node, vmid string,
	pid int,
) (exitCode int, stdout string, stderr string, err error) {
	u := fmt.Sprintf(
		"%s/nodes/%s/qemu/%s/agent/exec-status?pid=%d",
		c.baseURL,
		node,
		vmid,
		pid,
	)
	for {
		if err := ctx.Err(); err != nil {
			return 0, "", "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return 0, "", "", err
		}
		req.Header.Set("Authorization", c.authHeader())
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return 0, "", "", err
		}
		raw, rerr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if rerr != nil {
			return 0, "", "", rerr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return 0, "", "", fmt.Errorf(
				"pve exec-status: HTTP %d: %s",
				resp.StatusCode,
				raw,
			)
		}
		var st execStatusResponse
		if err := json.Unmarshal(raw, &st); err != nil {
			return 0, "", "", err
		}
		if st.Data.Exited == 1 {
			out, oerr := decodeAgentB64(st.Data.OutData)
			if oerr != nil {
				return 0, "", "", oerr
			}
			errOut, eerr := decodeAgentB64(st.Data.ErrData)
			if eerr != nil {
				return 0, "", "", eerr
			}
			return st.Data.ExitCode, out, errOut, nil
		}
		select {
		case <-ctx.Done():
			return 0, "", "", ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// decodeAgentB64 decodes PVE guest-exec out-data / err-data.
// The PVE API returns these fields as base64 when output is present, but
// some Proxmox versions (or certain error paths) return plain text. If
// the value is already printable text (all bytes < 0x80), return it as-is
// without attempting a base64 decode, since a valid base64 decode of plain
// ASCII content (e.g. a hex hash string) produces incorrect binary garbage.
// Only attempt base64 decode when the input contains characters outside the
// printable ASCII range, which would indicate actual base64-encoded binary.
func decodeAgentB64(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	// If every byte is printable ASCII (0x20-0x7e) or a common whitespace
	// character, the value is already plain text. Return it directly.
	plainText := true
	for i := range len(s) {
		b := s[i]
		if b >= 0x80 || (b < 0x20 && b != '\n' && b != '\r' && b != '\t') {
			plainText = false
			break
		}
	}
	if plainText {
		return s, nil
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		// Input had non-printable bytes but isn't valid base64 either,
		// so we cannot decode it. Surface the error rather than masking it
		// with the original (binary) input.
		slog.Warn("pveapi: decode base64 agent output failed", "err", err)
		return "", fmt.Errorf("decode base64 agent output: %w", err)
	}
	return string(raw), nil
}
