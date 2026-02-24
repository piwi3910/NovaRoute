package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.ListenSocket != "/run/novaroute/novaroute.sock" {
		t.Errorf("ListenSocket = %q, want %q", cfg.ListenSocket, "/run/novaroute/novaroute.sock")
	}
	if cfg.FRR.SocketDir != "/run/frr" {
		t.Errorf("FRR.SocketDir = %q, want %q", cfg.FRR.SocketDir, "/run/frr")
	}
	if cfg.FRR.ConnectTimeout != 10 {
		t.Errorf("FRR.ConnectTimeout = %d, want %d", cfg.FRR.ConnectTimeout, 10)
	}
	if cfg.FRR.RetryInterval != 5 {
		t.Errorf("FRR.RetryInterval = %d, want %d", cfg.FRR.RetryInterval, 5)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.MetricsAddress != ":9100" {
		t.Errorf("MetricsAddress = %q, want %q", cfg.MetricsAddress, ":9100")
	}
	if cfg.DisconnectGracePeriod != 30 {
		t.Errorf("DisconnectGracePeriod = %d, want %d", cfg.DisconnectGracePeriod, 30)
	}
	if cfg.Owners == nil {
		t.Error("Owners map should be initialized, got nil")
	}
	if len(cfg.Owners) != 0 {
		t.Errorf("Owners should be empty, got %d entries", len(cfg.Owners))
	}
	// BGP defaults should be zero values (require explicit configuration).
	if cfg.BGP.LocalAS != 0 {
		t.Errorf("BGP.LocalAS = %d, want 0 (no default)", cfg.BGP.LocalAS)
	}
	if cfg.BGP.RouterID != "" {
		t.Errorf("BGP.RouterID = %q, want empty (no default)", cfg.BGP.RouterID)
	}
}

// writeTestConfig writes a Config as JSON to a temporary file and returns the path.
func writeTestConfig(t *testing.T, cfg *Config) string {
	t.Helper()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshalling test config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

// validConfig returns a minimal valid configuration for testing.
func validConfig() *Config {
	return &Config{
		ListenSocket: "/run/novaroute/novaroute.sock",
		FRR: FRRConfig{
			SocketDir:         "/run/frr",
			ConnectTimeout: 10,
			RetryInterval:  5,
		},
		BGP: BGPConfig{
			LocalAS:  65001,
			RouterID: "10.0.0.1",
		},
		Owners: map[string]OwnerConfig{
			"novaedge": {
				Token: "secret-token-123",
				AllowedPrefixes: PrefixPolicy{
					Type:         "host_only",
					AllowedCIDRs: []string{"10.0.0.0/8"},
				},
			},
		},
		LogLevel:              "info",
		MetricsAddress:        ":9100",
		DisconnectGracePeriod: 30,
	}
}

func TestLoadFromFile(t *testing.T) {
	cfg := validConfig()
	path := writeTestConfig(t, cfg)

	loaded, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile(%q) returned error: %v", path, err)
	}

	if loaded.BGP.LocalAS != 65001 {
		t.Errorf("BGP.LocalAS = %d, want %d", loaded.BGP.LocalAS, 65001)
	}
	if loaded.BGP.RouterID != "10.0.0.1" {
		t.Errorf("BGP.RouterID = %q, want %q", loaded.BGP.RouterID, "10.0.0.1")
	}
	if loaded.ListenSocket != "/run/novaroute/novaroute.sock" {
		t.Errorf("ListenSocket = %q, want %q", loaded.ListenSocket, "/run/novaroute/novaroute.sock")
	}
	owner, ok := loaded.Owners["novaedge"]
	if !ok {
		t.Fatal("expected owner 'novaedge' in loaded config")
	}
	if owner.Token != "secret-token-123" {
		t.Errorf("owner token = %q, want %q", owner.Token, "secret-token-123")
	}
	if owner.AllowedPrefixes.Type != "host_only" {
		t.Errorf("owner prefix type = %q, want %q", owner.AllowedPrefixes.Type, "host_only")
	}
}

func TestLoadFromFile_MergesWithDefaults(t *testing.T) {
	// Write a partial config that only sets BGP and owners.
	partial := `{
		"bgp": {
			"local_as": 65002,
			"router_id": "10.0.0.2"
		},
		"owners": {
			"test": {
				"token": "tok",
				"allowed_prefixes": {"type": "any"}
			}
		}
	}`
	path := filepath.Join(t.TempDir(), "partial.json")
	if err := os.WriteFile(path, []byte(partial), 0o644); err != nil {
		t.Fatalf("writing partial config: %v", err)
	}

	loaded, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile returned error: %v", err)
	}

	// BGP values should come from the file.
	if loaded.BGP.LocalAS != 65002 {
		t.Errorf("BGP.LocalAS = %d, want %d", loaded.BGP.LocalAS, 65002)
	}

	// Defaults should fill in absent fields.
	if loaded.ListenSocket != "/run/novaroute/novaroute.sock" {
		t.Errorf("ListenSocket = %q, want default %q", loaded.ListenSocket, "/run/novaroute/novaroute.sock")
	}
	if loaded.FRR.SocketDir != "/run/frr" {
		t.Errorf("FRR.SocketDir = %q, want default %q", loaded.FRR.SocketDir, "/run/frr")
	}
	if loaded.FRR.ConnectTimeout != 10 {
		t.Errorf("FRR.ConnectTimeout = %d, want default %d", loaded.FRR.ConnectTimeout, 10)
	}
	if loaded.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want default %q", loaded.LogLevel, "info")
	}
	if loaded.DisconnectGracePeriod != 30 {
		t.Errorf("DisconnectGracePeriod = %d, want default %d", loaded.DisconnectGracePeriod, 30)
	}
}

func TestLoadFromFile_FileNotFound(t *testing.T) {
	_, err := LoadFromFile("/nonexistent/path/config.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestLoadFromFile_InvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not valid json}"), 0o644); err != nil {
		t.Fatalf("writing bad config: %v", err)
	}

	_, err := LoadFromFile(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestValidate_ValidConfig(t *testing.T) {
	cfg := validConfig()
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate returned error for valid config: %v", err)
	}
}

func TestValidate_MissingLocalAS(t *testing.T) {
	cfg := validConfig()
	cfg.BGP.LocalAS = 0

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for LocalAS=0, got nil")
	}
	if got := err.Error(); got != "bgp.local_as must be greater than 0" {
		t.Errorf("error = %q, want %q", got, "bgp.local_as must be greater than 0")
	}
}

func TestValidate_MissingRouterID(t *testing.T) {
	cfg := validConfig()
	cfg.BGP.RouterID = ""

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for empty RouterID, got nil")
	}
	if got := err.Error(); got != "bgp.router_id must not be empty" {
		t.Errorf("error = %q, want %q", got, "bgp.router_id must not be empty")
	}
}

func TestValidate_InvalidRouterID(t *testing.T) {
	cfg := validConfig()
	cfg.BGP.RouterID = "not-an-ip"

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for invalid RouterID, got nil")
	}
}

func TestValidate_MissingOwners(t *testing.T) {
	cfg := validConfig()
	cfg.Owners = map[string]OwnerConfig{}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for empty owners, got nil")
	}
	if got := err.Error(); got != "at least one owner must be configured" {
		t.Errorf("error = %q, want %q", got, "at least one owner must be configured")
	}
}

func TestValidate_NilOwners(t *testing.T) {
	cfg := validConfig()
	cfg.Owners = nil

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for nil owners, got nil")
	}
}

func TestValidate_OwnerEmptyToken(t *testing.T) {
	cfg := validConfig()
	cfg.Owners["bad"] = OwnerConfig{
		Token:           "",
		AllowedPrefixes: PrefixPolicy{Type: "any"},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for empty token, got nil")
	}
}

func TestValidate_OwnerEmptyPrefixType(t *testing.T) {
	cfg := validConfig()
	cfg.Owners["bad"] = OwnerConfig{
		Token:           "tok",
		AllowedPrefixes: PrefixPolicy{Type: ""},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for empty prefix type, got nil")
	}
}

func TestValidate_OwnerInvalidPrefixType(t *testing.T) {
	cfg := validConfig()
	cfg.Owners["bad"] = OwnerConfig{
		Token:           "tok",
		AllowedPrefixes: PrefixPolicy{Type: "unknown_type"},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for unknown prefix type, got nil")
	}
}

func TestValidate_OwnerInvalidCIDR(t *testing.T) {
	cfg := validConfig()
	cfg.Owners["bad"] = OwnerConfig{
		Token: "tok",
		AllowedPrefixes: PrefixPolicy{
			Type:         "any",
			AllowedCIDRs: []string{"not-a-cidr"},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for invalid CIDR, got nil")
	}
}

func TestValidate_EmptyListenSocket(t *testing.T) {
	cfg := validConfig()
	cfg.ListenSocket = ""

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for empty ListenSocket, got nil")
	}
}

func TestValidate_EmptyFRRSocketDir(t *testing.T) {
	cfg := validConfig()
	cfg.FRR.SocketDir = ""

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for empty FRR socket_dir, got nil")
	}
}

func TestValidate_InvalidLogLevel(t *testing.T) {
	cfg := validConfig()
	cfg.LogLevel = "verbose"

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for invalid log level, got nil")
	}
}

func TestValidate_NegativeGracePeriod(t *testing.T) {
	cfg := validConfig()
	cfg.DisconnectGracePeriod = -1

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for negative grace period, got nil")
	}
}

func TestValidate_ZeroConnectTimeout(t *testing.T) {
	cfg := validConfig()
	cfg.FRR.ConnectTimeout = 0

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for zero connect timeout, got nil")
	}
}

func TestValidate_ZeroRetryInterval(t *testing.T) {
	cfg := validConfig()
	cfg.FRR.RetryInterval = 0

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for zero retry interval, got nil")
	}
}

func TestExpandEnvVars_ReplacesTokens(t *testing.T) {
	t.Setenv("NOVAROUTE_TEST_TOKEN", "expanded-secret")

	cfg := validConfig()
	cfg.Owners["novaedge"] = OwnerConfig{
		Token:           "${NOVAROUTE_TEST_TOKEN}",
		AllowedPrefixes: PrefixPolicy{Type: "any"},
	}

	ExpandEnvVars(cfg)

	owner := cfg.Owners["novaedge"]
	if owner.Token != "expanded-secret" {
		t.Errorf("token = %q, want %q", owner.Token, "expanded-secret")
	}
}

func TestExpandEnvVars_UnsetVarBecomesEmpty(t *testing.T) {
	// Ensure the variable is not set.
	t.Setenv("NOVAROUTE_UNSET_VAR_TEST", "")
	os.Unsetenv("NOVAROUTE_UNSET_VAR_TEST")

	cfg := validConfig()
	cfg.Owners["novaedge"] = OwnerConfig{
		Token:           "${NOVAROUTE_UNSET_VAR_TEST}",
		AllowedPrefixes: PrefixPolicy{Type: "any"},
	}

	ExpandEnvVars(cfg)

	owner := cfg.Owners["novaedge"]
	if owner.Token != "" {
		t.Errorf("token = %q, want empty string for unset var", owner.Token)
	}
}

func TestExpandEnvVars_LiteralTokenUnchanged(t *testing.T) {
	cfg := validConfig()
	cfg.Owners["novaedge"] = OwnerConfig{
		Token:           "literal-token-no-vars",
		AllowedPrefixes: PrefixPolicy{Type: "any"},
	}

	ExpandEnvVars(cfg)

	owner := cfg.Owners["novaedge"]
	if owner.Token != "literal-token-no-vars" {
		t.Errorf("token = %q, want %q", owner.Token, "literal-token-no-vars")
	}
}

func TestExpandEnvVars_MultipleOwners(t *testing.T) {
	t.Setenv("TOK_A", "alpha")
	t.Setenv("TOK_B", "bravo")

	cfg := validConfig()
	cfg.Owners = map[string]OwnerConfig{
		"a": {
			Token:           "${TOK_A}",
			AllowedPrefixes: PrefixPolicy{Type: "any"},
		},
		"b": {
			Token:           "${TOK_B}",
			AllowedPrefixes: PrefixPolicy{Type: "host_only"},
		},
	}

	ExpandEnvVars(cfg)

	if cfg.Owners["a"].Token != "alpha" {
		t.Errorf("owner 'a' token = %q, want %q", cfg.Owners["a"].Token, "alpha")
	}
	if cfg.Owners["b"].Token != "bravo" {
		t.Errorf("owner 'b' token = %q, want %q", cfg.Owners["b"].Token, "bravo")
	}
}

func TestExpandEnvVars_MixedLiteralAndVar(t *testing.T) {
	t.Setenv("PART", "world")

	cfg := validConfig()
	cfg.Owners["novaedge"] = OwnerConfig{
		Token:           "hello-${PART}-suffix",
		AllowedPrefixes: PrefixPolicy{Type: "any"},
	}

	ExpandEnvVars(cfg)

	owner := cfg.Owners["novaedge"]
	if owner.Token != "hello-world-suffix" {
		t.Errorf("token = %q, want %q", owner.Token, "hello-world-suffix")
	}
}
