package policy

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/azrtydxb/NovaRoute/internal/config"
	"go.uber.org/zap"
)

// Sentinel errors for policy engine validation.
var (
	// ErrUnknownOwner is returned when the owner is not found in the policy configuration.
	ErrUnknownOwner = errors.New("unknown owner")

	// ErrInvalidToken is returned when the pre-shared token does not match.
	ErrInvalidToken = errors.New("invalid token for owner")

	// ErrHostOnlyIPv4Policy is returned when a non-/32 prefix violates a host_only policy for IPv4.
	ErrHostOnlyIPv4Policy = errors.New("host_only policy: prefix is not a /32 host route")

	// ErrHostOnlyIPv6Policy is returned when a non-/128 prefix violates a host_only policy for IPv6.
	ErrHostOnlyIPv6Policy = errors.New("host_only policy: prefix is not a /128 host route")

	// ErrSubnetIPv4Policy is returned when an IPv4 prefix is outside the /8-/28 range.
	ErrSubnetIPv4Policy = errors.New("subnet policy: IPv4 prefix must be between /8 and /28")

	// ErrSubnetIPv6Policy is returned when an IPv6 host route is used under a subnet policy.
	ErrSubnetIPv6Policy = errors.New("subnet policy: IPv6 host route not allowed")

	// ErrUnknownPrefixPolicyType is returned for an unrecognised prefix policy type.
	ErrUnknownPrefixPolicyType = errors.New("unknown prefix policy type")

	// ErrPrefixNotInAllowedCIDR is returned when a prefix is not within any configured allowed CIDR.
	ErrPrefixNotInAllowedCIDR = errors.New("prefix is not within any allowed CIDR")

	// ErrOwnershipConflict is returned when another owner already advertises the same prefix+protocol.
	ErrOwnershipConflict = errors.New("ownership conflict")
)

const (
	// PrefixTypeHostOnly allows only /32 (IPv4) and /128 (IPv6) host routes.
	PrefixTypeHostOnly = "host_only"
	// PrefixTypeSubnet allows only /8 through /28 subnet routes.
	PrefixTypeSubnet = "subnet"
	// PrefixTypeAny allows all prefix lengths.
	PrefixTypeAny = "any"

	// adminOwner is the special owner name that can override conflicts.
	adminOwner = "admin"
)

// Engine enforces ownership boundaries for route advertisement and
// protocol operations.
type Engine struct {
	cfg    Config
	logger *zap.Logger
}

// NewEngine creates a new policy engine with the given configuration.
func NewEngine(cfg Config, logger *zap.Logger) *Engine {
	if cfg.Owners == nil {
		cfg.Owners = make(map[string]config.OwnerConfig)
	}
	return &Engine{
		cfg:    cfg,
		logger: logger,
	}
}

// ValidateToken checks the pre-shared token for the given owner.
// Returns an error if the owner is unknown or the token does not match.
func (e *Engine) ValidateToken(owner, token string) error {
	ownerCfg, ok := e.cfg.Owners[owner]
	if !ok {
		e.logger.Warn("token validation failed: unknown owner",
			zap.String("owner", owner),
		)
		return fmt.Errorf("%s: %w", owner, ErrUnknownOwner)
	}
	if ownerCfg.Token != token {
		e.logger.Warn("token validation failed: invalid token",
			zap.String("owner", owner),
		)
		return fmt.Errorf("%s: %w", owner, ErrInvalidToken)
	}
	e.logger.Debug("token validated successfully",
		zap.String("owner", owner),
	)
	return nil
}

// ValidatePrefix checks whether the given prefix is allowed under the
// owner's prefix policy. It validates both the prefix type constraint
// (host_only, subnet, any) and the optional AllowedCIDRs list.
func (e *Engine) ValidatePrefix(owner, prefix string) error {
	ownerCfg, ok := e.cfg.Owners[owner]
	if !ok {
		return fmt.Errorf("%s: %w", owner, ErrUnknownOwner)
	}

	ip, ipNet, err := parseCIDR(prefix)
	if err != nil {
		return fmt.Errorf("invalid prefix %q: %w", prefix, err)
	}

	ones, bits := ipNet.Mask.Size()
	isIPv4 := ip.To4() != nil

	// Validate prefix type policy.
	switch strings.ToLower(ownerCfg.AllowedPrefixes.Type) {
	case PrefixTypeHostOnly:
		if isIPv4 && ones != 32 {
			e.logger.Warn("prefix rejected by host_only policy",
				zap.String("owner", owner),
				zap.String("prefix", prefix),
				zap.Int("prefix_len", ones),
			)
			return fmt.Errorf("owner %s prefix %s: %w", owner, prefix, ErrHostOnlyIPv4Policy)
		}
		if !isIPv4 && ones != 128 {
			e.logger.Warn("prefix rejected by host_only policy",
				zap.String("owner", owner),
				zap.String("prefix", prefix),
				zap.Int("prefix_len", ones),
			)
			return fmt.Errorf("owner %s prefix %s: %w", owner, prefix, ErrHostOnlyIPv6Policy)
		}

	case PrefixTypeSubnet:
		if isIPv4 {
			if ones < 8 || ones > 28 {
				e.logger.Warn("prefix rejected by subnet policy",
					zap.String("owner", owner),
					zap.String("prefix", prefix),
					zap.Int("prefix_len", ones),
				)
				return fmt.Errorf("owner %s prefix %s (/%d): %w", owner, prefix, ones, ErrSubnetIPv4Policy)
			}
		} else {
			// For IPv6 subnet policy, reject host routes (/128)
			// and very small prefixes. Allow /16 through /64 as a
			// reasonable range for IPv6 subnets.
			if ones == bits {
				e.logger.Warn("prefix rejected by subnet policy: host route not allowed",
					zap.String("owner", owner),
					zap.String("prefix", prefix),
					zap.Int("prefix_len", ones),
				)
				return fmt.Errorf("owner %s prefix %s (/%d): %w", owner, prefix, ones, ErrSubnetIPv6Policy)
			}
		}

	case PrefixTypeAny:
		// All prefix lengths are allowed.

	default:
		return fmt.Errorf("type %q for owner %s: %w", ownerCfg.AllowedPrefixes.Type, owner, ErrUnknownPrefixPolicyType)
	}

	// Validate against AllowedCIDRs if configured.
	if len(ownerCfg.AllowedPrefixes.AllowedCIDRs) > 0 {
		if err := e.validateAllowedCIDRs(owner, ip, ipNet, ownerCfg.AllowedPrefixes.AllowedCIDRs); err != nil {
			return err
		}
	}

	e.logger.Debug("prefix validated successfully",
		zap.String("owner", owner),
		zap.String("prefix", prefix),
	)
	return nil
}

// validateAllowedCIDRs checks that the given prefix falls within at least
// one of the allowed CIDR ranges.
func (e *Engine) validateAllowedCIDRs(owner string, ip net.IP, ipNet *net.IPNet, allowedCIDRs []string) error {
	for _, allowedCIDR := range allowedCIDRs {
		_, allowedNet, err := net.ParseCIDR(allowedCIDR)
		if err != nil {
			e.logger.Error("invalid allowed CIDR in policy config",
				zap.String("owner", owner),
				zap.String("cidr", allowedCIDR),
				zap.Error(err),
			)
			return fmt.Errorf("owner %s has invalid allowed CIDR in policy config: %s: %w", owner, allowedCIDR, err)
		}
		// The prefix is allowed if the allowed CIDR contains the network
		// address of the advertised prefix. This means the advertised
		// prefix must be a subnet of (or equal to) the allowed CIDR.
		if allowedNet.Contains(ip) {
			// Also verify the advertised prefix is not larger than the
			// allowed CIDR (i.e., its mask is at least as specific).
			allowedOnes, _ := allowedNet.Mask.Size()
			prefixOnes, _ := ipNet.Mask.Size()
			if prefixOnes >= allowedOnes {
				return nil
			}
		}
	}
	e.logger.Warn("prefix not within any allowed CIDR",
		zap.String("owner", owner),
		zap.String("prefix", ipNet.String()),
		zap.Strings("allowed_cidrs", allowedCIDRs),
	)
	return fmt.Errorf("owner %s prefix %s allowed %v: %w", owner, ipNet.String(), allowedCIDRs, ErrPrefixNotInAllowedCIDR)
}

// ValidatePeerOperation checks whether the owner is allowed to manage
// BGP peers. Currently all known owners can manage peers.
func (e *Engine) ValidatePeerOperation(owner string) error {
	if _, ok := e.cfg.Owners[owner]; !ok {
		return fmt.Errorf("%s: %w", owner, ErrUnknownOwner)
	}
	e.logger.Debug("peer operation validated",
		zap.String("owner", owner),
	)
	return nil
}

// ValidateBFDOperation checks whether the owner is allowed to manage
// BFD sessions. Currently all known owners can manage BFD.
func (e *Engine) ValidateBFDOperation(owner string) error {
	if _, ok := e.cfg.Owners[owner]; !ok {
		return fmt.Errorf("%s: %w", owner, ErrUnknownOwner)
	}
	e.logger.Debug("BFD operation validated",
		zap.String("owner", owner),
	)
	return nil
}

// ValidateOSPFOperation checks whether the owner is allowed to manage
// OSPF areas and interfaces. Currently all known owners can manage OSPF.
func (e *Engine) ValidateOSPFOperation(owner string) error {
	if _, ok := e.cfg.Owners[owner]; !ok {
		return fmt.Errorf("%s: %w", owner, ErrUnknownOwner)
	}
	e.logger.Debug("OSPF operation validated",
		zap.String("owner", owner),
	)
	return nil
}

// CheckConflict checks whether the owner is allowed to advertise the given
// prefix via the given protocol, considering existing ownership. If a
// different owner already advertises the same prefix+protocol combination,
// the operation is rejected unless the requesting owner is "admin".
func (e *Engine) CheckConflict(owner, prefix, protocol string, existingOwner string) error {
	// No conflict if no existing owner, or same owner.
	if existingOwner == "" || existingOwner == owner {
		e.logger.Debug("no ownership conflict",
			zap.String("owner", owner),
			zap.String("prefix", prefix),
			zap.String("protocol", protocol),
		)
		return nil
	}

	// Admin can override any conflict.
	if owner == adminOwner {
		e.logger.Info("admin override: taking ownership from existing owner",
			zap.String("prefix", prefix),
			zap.String("protocol", protocol),
			zap.String("existing_owner", existingOwner),
		)
		return nil
	}

	e.logger.Warn("ownership conflict detected",
		zap.String("owner", owner),
		zap.String("prefix", prefix),
		zap.String("protocol", protocol),
		zap.String("existing_owner", existingOwner),
	)
	return fmt.Errorf(
		"prefix %s via %s is already owned by %s (requesting owner: %s): %w",
		prefix, protocol, existingOwner, owner, ErrOwnershipConflict,
	)
}

// parseCIDR parses a CIDR prefix string and returns the IP, network, and
// any parsing error. It normalises the prefix to ensure it is valid.
func parseCIDR(prefix string) (net.IP, *net.IPNet, error) {
	ip, ipNet, err := net.ParseCIDR(prefix)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse CIDR %q: %w", prefix, err)
	}
	return ip, ipNet, nil
}
