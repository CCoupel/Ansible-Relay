// Package proxy implements the proxy-side HTTP client for push-mode relay connections.
// In push mode, the proxy initiates REST calls to downstream relays instead of waiting
// for relays to connect via /ws/relay.
//
// The RelayClient reuses the relay's existing REST API endpoints:
//   - GET  /api/inventory  — get connected agents (Ansible inventory format)
//   - POST /api/exec/{hostname}   — execute a command on an agent
//   - POST /api/upload/{hostname} — upload a file to an agent
//   - POST /api/fetch/{hostname}  — fetch a file from an agent
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ── Anti-loop hop counting ────────────────────────────────────────────────────

// RelayHopsHeader is the HTTP header used to prevent routing loops in proxy chains.
// Its value is decremented at each proxy node that forwards the request.
// A value of 0 received at a proxy node triggers an HTTP 508 (Loop Detected) response.
const RelayHopsHeader = "X-Relay-Hops"

// DefaultMaxHops is the initial hop budget for proxy chains.
// Supports up to 8 consecutive proxy hops before loop detection fires.
const DefaultMaxHops = 8

// hopCountKey is the unexported context key for the remaining hop budget.
type hopCountKey struct{}

// WithRelayHops stores the remaining hop count in ctx.
// Call this after decrementing the value received in the incoming X-Relay-Hops header.
func WithRelayHops(ctx context.Context, hops int) context.Context {
	return context.WithValue(ctx, hopCountKey{}, hops)
}

// RelayHopsFromContext retrieves the hop count from ctx.
// Returns DefaultMaxHops when no value has been stored (first node in the chain).
func RelayHopsFromContext(ctx context.Context) int {
	if v, ok := ctx.Value(hopCountKey{}).(int); ok {
		return v
	}
	return DefaultMaxHops
}

// ── Request / Response types ─────────────────────────────────────────────────

// InventoryAgent is an agent discovered from a relay's /api/inventory endpoint.
type InventoryAgent struct {
	Hostname string
	Status   string // "connected" | "disconnected" (from secagent_status hostvars)
	LastSeen string
}

// ExecRequest mirrors the relay's POST /api/exec/{hostname} body.
type ExecRequest struct {
	Cmd          string `json:"cmd"`
	Stdin        string `json:"stdin,omitempty"`
	Timeout      int    `json:"timeout,omitempty"`
	Become       bool   `json:"become,omitempty"`
	BecomeMethod string `json:"become_method,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
}

// ExecResponse mirrors the relay's exec response.
type ExecResponse struct {
	TaskID    string `json:"task_id"`
	RC        int    `json:"rc"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Truncated bool   `json:"truncated"`
}

// UploadRequest mirrors the relay's POST /api/upload/{hostname} body.
type UploadRequest struct {
	Dest string `json:"dest"`
	Data string `json:"data"` // base64-encoded content
	Mode string `json:"mode,omitempty"`
}

// FetchRequest mirrors the relay's POST /api/fetch/{hostname} body.
type FetchRequest struct {
	Src string `json:"src"`
}

// FetchResponse mirrors the relay's fetch response.
type FetchResponse struct {
	Data string `json:"data"` // base64-encoded content
	RC   int    `json:"rc"`
}

// ansibleInventory is the parsed structure of GET /api/inventory.
type ansibleInventory struct {
	All  ansibleGroup                       `json:"all"`
	Meta ansibleMeta                        `json:"_meta"`
}

type ansibleGroup struct {
	Hosts []string `json:"hosts"`
}

type ansibleMeta struct {
	Hostvars map[string]map[string]interface{} `json:"hostvars"`
}

// ── RelayClient ─────────────────────────────────────────────────────────────

// RelayClient is an HTTP client for communicating with a downstream relay in push mode.
type RelayClient struct {
	RelayID    string
	BaseURL    string // e.g. "https://dmz1.example.com:7770"
	Token      string // Bearer token for authenticating to the relay
	httpClient *http.Client
}

// NewRelayClient creates a RelayClient with TLS and a reasonable timeout.
func NewRelayClient(relayID, baseURL, token string) *RelayClient {
	return &RelayClient{
		RelayID: relayID,
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// GetInventory fetches the agent inventory from the relay's /api/inventory endpoint.
// Returns the list of agents with hostname and status.
func (c *RelayClient) GetInventory(ctx context.Context) ([]InventoryAgent, error) {
	data, status, err := c.get(ctx, "/api/inventory")
	if err != nil {
		return nil, fmt.Errorf("RelayClient.GetInventory %s: %w", c.RelayID, err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("RelayClient.GetInventory %s: HTTP %d: %s", c.RelayID, status, string(data))
	}

	var inv ansibleInventory
	if err := json.Unmarshal(data, &inv); err != nil {
		return nil, fmt.Errorf("RelayClient.GetInventory parse %s: %w", c.RelayID, err)
	}

	agents := make([]InventoryAgent, 0, len(inv.All.Hosts))
	for _, h := range inv.All.Hosts {
		a := InventoryAgent{Hostname: h}
		if hvars, ok := inv.Meta.Hostvars[h]; ok {
			if s, ok := hvars["secagent_status"].(string); ok {
				a.Status = s
			}
			if ls, ok := hvars["secagent_last_seen"].(string); ok {
				a.LastSeen = ls
			}
		}
		agents = append(agents, a)
	}
	return agents, nil
}

// Exec sends an exec request to the relay for the given hostname.
// This is a blocking call; the relay runs the command synchronously.
func (c *RelayClient) Exec(ctx context.Context, hostname string, req ExecRequest) (*ExecResponse, error) {
	data, status, err := c.post(ctx, "/api/exec/"+hostname, req)
	if err != nil {
		return nil, fmt.Errorf("RelayClient.Exec %s/%s: %w", c.RelayID, hostname, err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("RelayClient.Exec %s/%s: HTTP %d: %s", c.RelayID, hostname, status, string(data))
	}
	var resp ExecResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("RelayClient.Exec parse: %w", err)
	}
	return &resp, nil
}

// Upload sends a file upload request to the relay for the given hostname.
func (c *RelayClient) Upload(ctx context.Context, hostname string, req UploadRequest) error {
	data, status, err := c.post(ctx, "/api/upload/"+hostname, req)
	if err != nil {
		return fmt.Errorf("RelayClient.Upload %s/%s: %w", c.RelayID, hostname, err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("RelayClient.Upload %s/%s: HTTP %d: %s", c.RelayID, hostname, status, string(data))
	}
	return nil
}

// Fetch sends a file fetch request to the relay for the given hostname.
func (c *RelayClient) Fetch(ctx context.Context, hostname string, req FetchRequest) (*FetchResponse, error) {
	data, status, err := c.post(ctx, "/api/fetch/"+hostname, req)
	if err != nil {
		return nil, fmt.Errorf("RelayClient.Fetch %s/%s: %w", c.RelayID, hostname, err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("RelayClient.Fetch %s/%s: HTTP %d: %s", c.RelayID, hostname, status, string(data))
	}
	var resp FetchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("RelayClient.Fetch parse: %w", err)
	}
	return &resp, nil
}

// ── Internal HTTP helpers ─────────────────────────────────────────────────────

func (c *RelayClient) get(ctx context.Context, path string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set(RelayHopsHeader, strconv.Itoa(RelayHopsFromContext(ctx)))
	return c.do(req)
}

func (c *RelayClient) post(ctx context.Context, path string, body interface{}) ([]byte, int, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(RelayHopsHeader, strconv.Itoa(RelayHopsFromContext(ctx)))
	return c.do(req)
}

func (c *RelayClient) do(req *http.Request) ([]byte, int, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read body: %w", err)
	}
	return data, resp.StatusCode, nil
}
