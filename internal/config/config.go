// Package config handles loading and validating the NovaRoute agent
// configuration. Configuration is stored as JSON and supports environment
// variable expansion in token strings.
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// Config holds the complete NovaRoute agent configuration.
type Config struct {
	// ListenSocket is the Unix socket path for the NovaRoute gRPC API.
	ListenSocket string `json:"listen_socket"`

	// FRR holds connection settings for the FRR northbound daemon.
	FRR FRRConfig `json:"frr"`

	// BGP holds BGP global settings (AS number, router ID).
	BGP BGPConfig `json:"bgp"`

	// Owners maps owner names to their authentication and prefix policies.
	Owners map[string]OwnerConfig `json:"owners"`

	// LogLevel sets the logging verbosity (debug, info, warn, error).
	LogLevel string `json:"log_level"`

	// MetricsAddress is the listen address for the Prometheus metrics endpoint.
	MetricsAddress string `json:"metrics_address"`

	// DisconnectGracePeriod is the number of seconds to wait before
	// withdrawing routes after a client disconnects.
	DisconnectGracePeriod int `json:"disconnect_grace_period"`
}

// FRRConfig holds connection settings for the FRR VTY daemon sockets.
type FRRConfig struct {
	// SocketDir is the directory containing FRR daemon VTY sockets
	// (e.g., "/run/frr"). Each daemon creates a <daemon>.vty socket.
	SocketDir string `json:"socket_dir"`

	// ConnectTimeout is the connection timeout in seconds.
	ConnectTimeout int `json:"connect_timeout"`

	// RetryInterval is the retry interval in seconds after a failed connection.
	RetryInterval int `json:"retry_interval"`
}

// BGPConfig holds global BGP settings.
type BGPConfig struct {
	// LocalAS is the local autonomous system number.
	LocalAS uint32 `json:"local_as"`

	// RouterID is the BGP router identifier (IPv4 address format).
	RouterID string `json:"router_id"`
}

// OwnerConfig defines the authentication and prefix policy for a single owner.
type OwnerConfig struct {
	// Token is the pre-shared authentication token for this owner.
	Token string `json:"token"`

	// AllowedPrefixes defines what prefixes this owner may advertise.
	AllowedPrefixes PrefixPolicy `json:"allowed_prefixes"`
}

// PrefixPolicy defines what kinds of prefixes an owner is allowed to advertise.
type PrefixPolicy struct {
	// Type controls the category of allowed prefixes:
	//   "host_only" - only /32 (IPv4) and /128 (IPv6) host routes
	//   "subnet"    - only /8 through /28 subnet routes, no host routes
	//   "any"       - all prefix lengths are allowed
	Type string `json:"type"`

	// AllowedCIDRs is an optional list of CIDR ranges that further restrict
	// which prefixes can be advertised. If non-empty, the advertised prefix
	// must fall within at least one of these CIDRs.
	AllowedCIDRs []string `json:"allowed_cidrs"`
}

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		ListenSocket: "/run/novaroute/novaroute.sock",
		FRR: FRRConfig{
			SocketDir:      "/run/frr",
			ConnectTimeout: 10,
			RetryInterval:  5,
		},
		LogLevel:              "info",
		MetricsAddress:        ":9100",
		DisconnectGracePeriod: 30,
		Owners:                make(map[string]OwnerConfig),
	}
}

// LoadFromFile reads a JSON configuration file at the given path and returns
// a Config merged with defaults. Fields present in the file override the
// corresponding defaults; fields absent from the file retain their default
// values.
func LoadFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

	// Start with defaults so absent fields keep sensible values.
	cfg := DefaultConfig()

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}

	return cfg, nil
}

// Validate checks that a Config contains all required fields and that the
// values are well-formed. It returns an error describing the first problem
// found, or nil if the configuration is valid.
func Validate(cfg *Config) error {
	if cfg.ListenSocket == "" {
		return fmt.Errorf("listen_socket must not be empty")
	}

	if cfg.FRR.SocketDir == "" {
		return fmt.Errorf("frr.socket_dir must not be empty")
	}

	if cfg.FRR.ConnectTimeout <= 0 {
		return fmt.Errorf("frr.connect_timeout must be positive, got %d", cfg.FRR.ConnectTimeout)
	}

	if cfg.FRR.RetryInterval <= 0 {
		return fmt.Errorf("frr.retry_interval must be positive, got %d", cfg.FRR.RetryInterval)
	}

	if cfg.BGP.LocalAS == 0 {
		return fmt.Errorf("bgp.local_as must be greater than 0")
	}

	if cfg.BGP.RouterID == "" {
		return fmt.Errorf("bgp.router_id must not be empty")
	}

	if ip := net.ParseIP(cfg.BGP.RouterID); ip == nil {
		return fmt.Errorf("bgp.router_id %q is not a valid IP address", cfg.BGP.RouterID)
	}

	if len(cfg.Owners) == 0 {
		return fmt.Errorf("at least one owner must be configured")
	}

	for name, owner := range cfg.Owners {
		if owner.Token == "" {
			return fmt.Errorf("owner %q: token must not be empty", name)
		}

		switch strings.ToLower(owner.AllowedPrefixes.Type) {
		case "host_only", "subnet", "any":
			// valid
		case "":
			return fmt.Errorf("owner %q: allowed_prefixes.type must not be empty", name)
		default:
			return fmt.Errorf("owner %q: unknown allowed_prefixes.type %q (must be host_only, subnet, or any)", name, owner.AllowedPrefixes.Type)
		}

		for i, cidr := range owner.AllowedPrefixes.AllowedCIDRs {
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				return fmt.Errorf("owner %q: allowed_prefixes.allowed_cidrs[%d] %q is not a valid CIDR: %w", name, i, cidr, err)
			}
		}
	}

	switch strings.ToLower(cfg.LogLevel) {
	case "debug", "info", "warn", "error":
		// valid
	default:
		return fmt.Errorf("log_level %q is not valid (must be debug, info, warn, or error)", cfg.LogLevel)
	}

	if cfg.DisconnectGracePeriod < 0 {
		return fmt.Errorf("disconnect_grace_period must not be negative, got %d", cfg.DisconnectGracePeriod)
	}

	return nil
}

// ExpandEnvVars replaces ${VAR} placeholders in configuration strings with
// the corresponding environment variable values. If a referenced variable
// is not set, the placeholder is replaced with an empty string.
//
// Supported fields:
//   - Owner tokens (e.g., "token": "${NOVAEDGE_TOKEN}")
//   - bgp.router_id (e.g., "router_id": "${NODE_IP}")
//
// Additionally, the following environment variables override config values
// when set (regardless of what the config file contains):
//   - NOVAROUTE_BGP_LOCAL_AS  → bgp.local_as (must be a valid uint32)
//   - NOVAROUTE_BGP_ROUTER_ID → bgp.router_id
func ExpandEnvVars(cfg *Config) {
	for name, owner := range cfg.Owners {
		owner.Token = os.ExpandEnv(owner.Token)
		cfg.Owners[name] = owner
	}

	// Expand env vars in router_id string.
	cfg.BGP.RouterID = os.ExpandEnv(cfg.BGP.RouterID)

	// Explicit env var overrides for BGP fields.
	if v := os.Getenv("NOVAROUTE_BGP_LOCAL_AS"); v != "" {
		if as, err := strconv.ParseUint(v, 10, 32); err == nil {
			cfg.BGP.LocalAS = uint32(as)
		}
	}
	if v := os.Getenv("NOVAROUTE_BGP_ROUTER_ID"); v != "" {
		cfg.BGP.RouterID = v
	}
}
