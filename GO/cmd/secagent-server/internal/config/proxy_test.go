package config

import (
	"os"
	"testing"
)

// resetGlobal clears the package-level global between test cases.
func resetGlobal() { global = nil }

func TestLoadProxyConfig_Disabled_Default(t *testing.T) {
	resetGlobal()
	os.Unsetenv("PROXY_MODE")
	os.Unsetenv("PROXY_RELAYS")

	cfg := LoadProxyConfig()
	if cfg.Enabled {
		t.Errorf("expected Enabled=false when PROXY_MODE is unset, got true")
	}
	if len(cfg.PushRelays) != 0 {
		t.Errorf("expected 0 push relays, got %d", len(cfg.PushRelays))
	}
}

func TestLoadProxyConfig_Enabled_True(t *testing.T) {
	resetGlobal()
	os.Setenv("PROXY_MODE", "true")
	defer os.Unsetenv("PROXY_MODE")

	cfg := LoadProxyConfig()
	if !cfg.Enabled {
		t.Errorf("expected Enabled=true when PROXY_MODE=true")
	}
}

func TestLoadProxyConfig_Enabled_1(t *testing.T) {
	resetGlobal()
	os.Setenv("PROXY_MODE", "1")
	defer os.Unsetenv("PROXY_MODE")

	cfg := LoadProxyConfig()
	if !cfg.Enabled {
		t.Errorf("expected Enabled=true when PROXY_MODE=1")
	}
}

func TestLoadProxyConfig_Enabled_Yes(t *testing.T) {
	resetGlobal()
	os.Setenv("PROXY_MODE", "yes")
	defer os.Unsetenv("PROXY_MODE")

	cfg := LoadProxyConfig()
	if !cfg.Enabled {
		t.Errorf("expected Enabled=true when PROXY_MODE=yes")
	}
}

func TestLoadProxyConfig_Disabled_False(t *testing.T) {
	resetGlobal()
	os.Setenv("PROXY_MODE", "false")
	defer os.Unsetenv("PROXY_MODE")

	cfg := LoadProxyConfig()
	if cfg.Enabled {
		t.Errorf("expected Enabled=false when PROXY_MODE=false")
	}
}

func TestLoadProxyConfig_Disabled_Empty(t *testing.T) {
	resetGlobal()
	os.Setenv("PROXY_MODE", "")
	defer os.Unsetenv("PROXY_MODE")

	cfg := LoadProxyConfig()
	if cfg.Enabled {
		t.Errorf("expected Enabled=false when PROXY_MODE is empty string")
	}
}

func TestLoadProxyConfig_CaseInsensitive(t *testing.T) {
	resetGlobal()
	os.Setenv("PROXY_MODE", "TRUE")
	defer os.Unsetenv("PROXY_MODE")

	cfg := LoadProxyConfig()
	if !cfg.Enabled {
		t.Errorf("expected Enabled=true when PROXY_MODE=TRUE (case-insensitive)")
	}
}

func TestLoadProxyConfig_PushRelays_Valid(t *testing.T) {
	resetGlobal()
	os.Setenv("PROXY_MODE", "true")
	os.Setenv("PROXY_RELAYS", `[{"relay_id":"dmz1","url":"https://dmz1:7770","token":"tok1","description":"Zone DMZ1"},{"relay_id":"dmz2","url":"https://dmz2:7770","token":"tok2","description":""}]`)
	defer os.Unsetenv("PROXY_MODE")
	defer os.Unsetenv("PROXY_RELAYS")

	cfg := LoadProxyConfig()
	if len(cfg.PushRelays) != 2 {
		t.Fatalf("expected 2 push relays, got %d", len(cfg.PushRelays))
	}
	if cfg.PushRelays[0].RelayID != "dmz1" {
		t.Errorf("expected relay_id=dmz1, got %s", cfg.PushRelays[0].RelayID)
	}
	if cfg.PushRelays[0].URL != "https://dmz1:7770" {
		t.Errorf("unexpected URL: %s", cfg.PushRelays[0].URL)
	}
	if cfg.PushRelays[1].RelayID != "dmz2" {
		t.Errorf("expected relay_id=dmz2, got %s", cfg.PushRelays[1].RelayID)
	}
}

func TestLoadProxyConfig_PushRelays_InvalidJSON(t *testing.T) {
	resetGlobal()
	os.Setenv("PROXY_MODE", "true")
	os.Setenv("PROXY_RELAYS", `not-valid-json`)
	defer os.Unsetenv("PROXY_MODE")
	defer os.Unsetenv("PROXY_RELAYS")

	cfg := LoadProxyConfig()
	// Malformed JSON → push relays silently ignored (no panic)
	if len(cfg.PushRelays) != 0 {
		t.Errorf("expected 0 push relays on invalid JSON, got %d", len(cfg.PushRelays))
	}
}

func TestIsProxyMode_WithGlobal(t *testing.T) {
	global = &ProxyConfig{Enabled: true}
	defer resetGlobal()

	if !IsProxyMode() {
		t.Error("expected IsProxyMode()=true after LoadProxyConfig with Enabled=true")
	}
}

func TestIsProxyMode_WithoutGlobal_Fallback(t *testing.T) {
	resetGlobal()
	os.Setenv("PROXY_MODE", "true")
	defer os.Unsetenv("PROXY_MODE")
	defer resetGlobal()

	if !IsProxyMode() {
		t.Error("expected IsProxyMode() fallback to env var when global is nil")
	}
}

func TestIsProxyMode_WithoutGlobal_FallbackDisabled(t *testing.T) {
	resetGlobal()
	os.Unsetenv("PROXY_MODE")

	if IsProxyMode() {
		t.Error("expected IsProxyMode()=false when global is nil and PROXY_MODE unset")
	}
}

func TestGet_ReturnsZeroValueWhenNotLoaded(t *testing.T) {
	resetGlobal()
	cfg := Get()
	if cfg.Enabled {
		t.Error("expected Enabled=false from zero-value Get()")
	}
	if len(cfg.PushRelays) != 0 {
		t.Error("expected empty PushRelays from zero-value Get()")
	}
}

func TestGet_ReturnsLoadedConfig(t *testing.T) {
	resetGlobal()
	os.Setenv("PROXY_MODE", "true")
	defer os.Unsetenv("PROXY_MODE")

	LoadProxyConfig()
	cfg := Get()
	if !cfg.Enabled {
		t.Error("expected Get() to return Enabled=true after LoadProxyConfig")
	}
}
