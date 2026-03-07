// Package config handles loading and validating the NovaRoute agent
// configuration. Configuration is stored as JSON and supports environment
// variable expansion in token strings.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Sentinel errors returned by Validate for configuration problems.
var (
	// General field errors.
	ErrListenSocketEmpty        = errors.New("listen_socket must not be empty")
	ErrAtLeastOneOwnerRequired  = errors.New("at least one owner must be configured")
	ErrDisconnectGracePeriodNeg = errors.New("disconnect_grace_period must not be negative")

	// FRR configuration errors.
	ErrFRRSocketDirEmpty         = errors.New("frr.socket_dir must not be empty")
	ErrFRRConnectTimeoutPositive = errors.New("frr.connect_timeout must be positive")
	ErrFRRRetryIntervalPositive  = errors.New("frr.retry_interval must be positive")

	// BGP configuration errors.
	ErrBGPRouterIDInvalid       = errors.New("bgp.router_id is not a valid IP address")
	ErrBGPPeerNeighborEmpty     = errors.New("neighbor_address must not be empty")
	ErrBGPPeerNeighborInvalid   = errors.New("neighbor_address is not a valid IP address")
	ErrBGPPeerRemoteASZero      = errors.New("remote_as must be greater than 0")
	ErrBGPPeerSourceAddrInvalid = errors.New("source_address is not a valid IP address")

	// Owner configuration errors.
	ErrOwnerTokenEmpty        = errors.New("token must not be empty")
	ErrOwnerPrefixTypeEmpty   = errors.New("allowed_prefixes.type must not be empty")
	ErrOwnerPrefixTypeUnknown = errors.New("unknown allowed_prefixes.type (must be host_only, subnet, or any)")

	// Log level errors.
	ErrLogLevelInvalid = errors.New("log_level is not valid (must be debug, info, warn, or error)")
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

	// AsBase enables per-node AS computation: local_as = as_base + last octet
	// of NODE_IP. Requires the NODE_IP environment variable to be set. If
	// both local_as and as_base are set, as_base takes precedence.
	AsBase uint32 `json:"as_base"`

	// RouterID is the BGP router identifier (IPv4 address format).
	// Supports ${VAR} expansion (e.g., "${NODE_IP}").
	RouterID string `json:"router_id"`

	// AutoRouterID, when true, sets router_id to the value of the NODE_IP
	// environment variable if router_id is not already set.
	AutoRouterID bool `json:"auto_router_id"`

	// Peers defines BGP peers that the agent should configure on startup.
	// These peers are applied automatically when FRR becomes ready, using
	// the internal intent→reconciler→FRR pipeline with owner "_config".
	Peers []PeerConfig `json:"peers"`
}

// PeerConfig defines a BGP peer to be configured on agent startup.
type PeerConfig struct {
	// NeighborAddress is the IP address of the BGP neighbor (required).
	NeighborAddress string `json:"neighbor_address"`

	// RemoteAS is the remote autonomous system number (required).
	RemoteAS uint32 `json:"remote_as"`

	// Description is an optional human-readable description for the peer.
	Description string `json:"description"`

	// BFDEnabled enables BFD (Bidirectional Forwarding Detection) for this peer.
	BFDEnabled bool `json:"bfd_enabled"`

	// BFDMinRxMs is the BFD minimum receive interval in milliseconds (default 300).
	BFDMinRxMs uint32 `json:"bfd_min_rx_ms"`

	// BFDMinTxMs is the BFD minimum transmit interval in milliseconds (default 300).
	BFDMinTxMs uint32 `json:"bfd_min_tx_ms"`

	// BFDDetectMultiplier is the BFD detection multiplier (default 3).
	BFDDetectMultiplier uint32 `json:"bfd_detect_multiplier"`

	// Keepalive is the BGP keepalive interval in seconds (0 = FRR default).
	Keepalive uint32 `json:"keepalive"`

	// HoldTime is the BGP hold time in seconds (0 = FRR default).
	HoldTime uint32 `json:"hold_time"`

	// AddressFamilies is the list of address families to activate (default: ["ipv4-unicast"]).
	AddressFamilies []string `json:"address_families"`

	// SourceAddress is the update source address for the peer.
	SourceAddress string `json:"source_address"`

	// EBGPMultihop is the eBGP multihop TTL (0 = disabled).
	EBGPMultihop uint32 `json:"ebgp_multihop"`

	// Password is the BGP session password.
	Password string `json:"password"` //nolint:gosec // BGP session password, not a credential

	// MaxPrefixes is the maximum prefix limit (0 = default 1000).
	MaxPrefixes uint32 `json:"max_prefixes"`
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
		DisconnectGracePeriod: 0,
		Owners:                make(map[string]OwnerConfig),
	}
}

// LoadFromFile reads a JSON configuration file at the given path and returns
// a Config merged with defaults. Fields present in the file override the
// corresponding defaults; fields absent from the file retain their default
// values.
func LoadFromFile(path string) (*Config, error) {
	cleanPath := filepath.Clean(path)
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", cleanPath, err)
	}

	// Start with defaults so absent fields keep sensible values.
	cfg := DefaultConfig()

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", cleanPath, err)
	}

	return cfg, nil
}

// Validate checks that a Config contains all required fields and that the
// values are well-formed. It returns an error describing the first problem
// found, or nil if the configuration is valid.
func Validate(cfg *Config) error {
	if cfg.ListenSocket == "" {
		return ErrListenSocketEmpty
	}

	if cfg.FRR.SocketDir == "" {
		return ErrFRRSocketDirEmpty
	}

	if cfg.FRR.ConnectTimeout <= 0 {
		return fmt.Errorf("frr.connect_timeout must be positive, got %d: %w", cfg.FRR.ConnectTimeout, ErrFRRConnectTimeoutPositive)
	}

	if cfg.FRR.RetryInterval <= 0 {
		return fmt.Errorf("frr.retry_interval must be positive, got %d: %w", cfg.FRR.RetryInterval, ErrFRRRetryIntervalPositive)
	}

	// BGP config is optional — clients can configure it at runtime via ConfigureBGP RPC.
	if cfg.BGP.RouterID != "" {
		if ip := net.ParseIP(cfg.BGP.RouterID); ip == nil {
			return fmt.Errorf("bgp.router_id %q: %w", cfg.BGP.RouterID, ErrBGPRouterIDInvalid)
		}
	}

	// Validate configured BGP peers.
	for i, peer := range cfg.BGP.Peers {
		if err := validateBGPPeer(i, peer); err != nil {
			return err
		}
	}

	if err := validateOwners(cfg.Owners); err != nil {
		return err
	}

	switch strings.ToLower(cfg.LogLevel) {
	case "debug", "info", "warn", "error":
		// valid
	default:
		return fmt.Errorf("log_level %q: %w", cfg.LogLevel, ErrLogLevelInvalid)
	}

	if cfg.DisconnectGracePeriod < 0 {
		return fmt.Errorf("disconnect_grace_period must not be negative, got %d: %w", cfg.DisconnectGracePeriod, ErrDisconnectGracePeriodNeg)
	}

	return nil
}

// validateBGPPeer checks that a single BGP peer configuration entry is valid.
func validateBGPPeer(idx int, peer PeerConfig) error {
	if peer.NeighborAddress == "" {
		return fmt.Errorf("bgp.peers[%d]: %w", idx, ErrBGPPeerNeighborEmpty)
	}
	if ip := net.ParseIP(peer.NeighborAddress); ip == nil {
		return fmt.Errorf("bgp.peers[%d]: neighbor_address %q: %w", idx, peer.NeighborAddress, ErrBGPPeerNeighborInvalid)
	}
	if peer.RemoteAS == 0 {
		return fmt.Errorf("bgp.peers[%d]: %w", idx, ErrBGPPeerRemoteASZero)
	}
	if peer.SourceAddress != "" {
		if ip := net.ParseIP(peer.SourceAddress); ip == nil {
			return fmt.Errorf("bgp.peers[%d]: source_address %q: %w", idx, peer.SourceAddress, ErrBGPPeerSourceAddrInvalid)
		}
	}
	return nil
}

// validateOwners checks that at least one owner is configured and that each
// owner entry has valid fields.
func validateOwners(owners map[string]OwnerConfig) error {
	if len(owners) == 0 {
		return ErrAtLeastOneOwnerRequired
	}

	for name, owner := range owners {
		if owner.Token == "" {
			return fmt.Errorf("owner %q: %w", name, ErrOwnerTokenEmpty)
		}

		switch strings.ToLower(owner.AllowedPrefixes.Type) {
		case "host_only", "subnet", "any":
			// valid
		case "":
			return fmt.Errorf("owner %q: %w", name, ErrOwnerPrefixTypeEmpty)
		default:
			return fmt.Errorf("owner %q: allowed_prefixes.type %q: %w", name, owner.AllowedPrefixes.Type, ErrOwnerPrefixTypeUnknown)
		}

		for i, cidr := range owner.AllowedPrefixes.AllowedCIDRs {
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				return fmt.Errorf("owner %q: allowed_prefixes.allowed_cidrs[%d] %q is not a valid CIDR: %w", name, i, cidr, err)
			}
		}
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

	// Auto router-id: use NODE_IP if router_id is not set.
	if cfg.BGP.AutoRouterID && cfg.BGP.RouterID == "" {
		if nodeIP := os.Getenv("NODE_IP"); nodeIP != "" {
			cfg.BGP.RouterID = nodeIP
		}
	}

	// Per-node AS computation: as_base + last octet of NODE_IP.
	if cfg.BGP.AsBase > 0 {
		if nodeIP := os.Getenv("NODE_IP"); nodeIP != "" {
			parts := strings.Split(nodeIP, ".")
			if len(parts) == 4 {
				if lastOctet, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
					cfg.BGP.LocalAS = cfg.BGP.AsBase + uint32(lastOctet)
				} else {
					fmt.Fprintf(os.Stderr, "WARNING: cannot parse last octet of NODE_IP=%q: %v\n", nodeIP, err)
				}
			} else {
				fmt.Fprintf(os.Stderr, "WARNING: NODE_IP=%q is not a valid IPv4 address for as_base computation\n", nodeIP)
			}
		} else {
			fmt.Fprintf(os.Stderr, "WARNING: bgp.as_base is set but NODE_IP environment variable is not set\n")
		}
	}

	// Explicit env var overrides for BGP fields (take highest precedence).
	if v := os.Getenv("NOVAROUTE_BGP_LOCAL_AS"); v != "" {
		if as, err := strconv.ParseUint(v, 10, 32); err == nil {
			cfg.BGP.LocalAS = uint32(as)
		} else {
			fmt.Fprintf(os.Stderr, "WARNING: NOVAROUTE_BGP_LOCAL_AS=%q is not a valid uint32, ignoring: %v\n", v, err)
		}
	}
	if v := os.Getenv("NOVAROUTE_BGP_ROUTER_ID"); v != "" {
		cfg.BGP.RouterID = v
	}
}
