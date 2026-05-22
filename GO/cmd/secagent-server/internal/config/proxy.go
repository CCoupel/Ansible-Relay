// Package config provides server configuration helpers.
// This file handles proxy/gateway mode detection and configuration.
package config

import (
	"encoding/json"
	"os"
	"strings"
)

// RelayEndpoint describes a downstream relay reachable in push mode.
// Populated from the PROXY_RELAYS env var (JSON array) or a YAML config file.
type RelayEndpoint struct {
	RelayID     string `json:"relay_id"     yaml:"relay_id"`
	URL         string `json:"url"          yaml:"url"`
	Token       string `json:"token"        yaml:"token"`
	Description string `json:"description"  yaml:"description"`
}

// ProxyConfig holds the proxy/gateway configuration for the relay server.
// When Enabled is false the server runs as a standard relay (no change in behaviour).
type ProxyConfig struct {
	// Enabled is true when the server runs in proxy/gateway mode.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// PushRelays lists downstream relays the proxy initiates connections to
	// (push mode). Empty in pull-only or standalone mode.
	PushRelays []RelayEndpoint `json:"push_relays" yaml:"push_relays"`
}

// global holds the loaded proxy config (set once at startup via LoadProxyConfig).
var global *ProxyConfig

// LoadProxyConfig reads proxy settings from environment variables.
//
// Variables:
//   - PROXY_MODE    : "true" / "1" / "yes" → proxy mode enabled
//   - PROXY_RELAYS  : JSON array of RelayEndpoint objects (optional)
//
// The function is idempotent: calling it multiple times overwrites the previous config.
// Returns the loaded config (never nil).
func LoadProxyConfig() *ProxyConfig {
	cfg := &ProxyConfig{}

	// Detect proxy mode from env var
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("PROXY_MODE")))
	cfg.Enabled = raw == "true" || raw == "1" || raw == "yes"

	// Parse push relay endpoints (optional)
	if relaysJSON := strings.TrimSpace(os.Getenv("PROXY_RELAYS")); relaysJSON != "" {
		var endpoints []RelayEndpoint
		if err := json.Unmarshal([]byte(relaysJSON), &endpoints); err == nil {
			cfg.PushRelays = endpoints
		}
		// Silently ignore malformed JSON — push relays simply stay empty.
	}

	global = cfg
	return cfg
}

// IsProxyMode returns true when the server is running in proxy/gateway mode.
// LoadProxyConfig must have been called first; if not, it falls back to reading
// the PROXY_MODE env var directly.
func IsProxyMode() bool {
	if global != nil {
		return global.Enabled
	}
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("PROXY_MODE")))
	return raw == "true" || raw == "1" || raw == "yes"
}

// Get returns the loaded ProxyConfig, or a zero-value config if LoadProxyConfig
// has not been called yet.
func Get() ProxyConfig {
	if global != nil {
		return *global
	}
	return ProxyConfig{}
}
