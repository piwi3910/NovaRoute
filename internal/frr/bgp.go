package frr

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"
)

// ErrBGPNotConfigured is returned when a BGP operation is attempted before
// configuring the local AS number via ConfigureBGPGlobal.
var ErrBGPNotConfigured = errors.New("BGP local AS not configured")

// NeighborConfig holds optional BGP neighbor configuration fields.
type NeighborConfig struct {
	SourceAddress string
	EBGPMultihop  uint32
	Password      string //nolint:gosec // BGP neighbor password field, not a credential
	Description   string
}

// bgpGracefulRestartCommands returns the standard graceful-restart commands.
func bgpGracefulRestartCommands() []string {
	return []string{
		"bgp graceful-restart",
		"bgp graceful-restart restart-time 120",
		"bgp graceful-restart stalepath-time 360",
	}
}

// ConfigureBGPGlobal creates the BGP instance with the given AS number and
// router ID. This is equivalent to "router bgp <AS>" + "bgp router-id <ID>".
func (c *Client) ConfigureBGPGlobal(ctx context.Context, localAS uint32, routerID string) error {
	c.log.Info("configuring BGP global",
		zap.Uint32("local_as", localAS),
		zap.String("router_id", routerID),
	)

	commands := make([]string, 0, 5)
	commands = append(commands,
		fmt.Sprintf("router bgp %d", localAS),
		fmt.Sprintf("bgp router-id %s", routerID),
	)
	commands = append(commands, bgpGracefulRestartCommands()...)

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: configure BGP global (AS=%d, router_id=%s): %w", localAS, routerID, err)
	}

	c.mu.Lock()
	c.localAS = localAS
	c.mu.Unlock()
	return nil
}

// ReconfigureBGPGlobal changes the BGP AS number and/or router-id at runtime.
// If the AS has changed from oldAS, it first removes the old BGP instance before
// creating the new one. All BGP sessions will be torn down and must be
// re-established by the reconciler.
func (c *Client) ReconfigureBGPGlobal(ctx context.Context, oldAS, newAS uint32, routerID string) error {
	if oldAS == newAS {
		// AS unchanged — just update router-id in place.
		c.log.Info("updating BGP router-id (AS unchanged)",
			zap.Uint32("local_as", newAS),
			zap.String("router_id", routerID),
		)
		commands := make([]string, 0, 5)
		commands = append(commands,
			fmt.Sprintf("router bgp %d", newAS),
			fmt.Sprintf("bgp router-id %s", routerID),
		)
		commands = append(commands, bgpGracefulRestartCommands()...)
		if err := c.runConfig(ctx, commands); err != nil {
			return fmt.Errorf("frr: update router-id (AS=%d): %w", newAS, err)
		}
		return nil
	}

	c.log.Info("reconfiguring BGP global (AS change)",
		zap.Uint32("old_as", oldAS),
		zap.Uint32("new_as", newAS),
		zap.String("router_id", routerID),
	)

	// Remove old BGP instance if it exists.
	if oldAS != 0 {
		rmCommands := []string{
			fmt.Sprintf("no router bgp %d", oldAS),
		}
		if err := c.runConfig(ctx, rmCommands); err != nil {
			return fmt.Errorf("frr: remove old BGP instance (AS=%d): %w", oldAS, err)
		}
	}

	// Create new BGP instance.
	return c.ConfigureBGPGlobal(ctx, newAS, routerID)
}

// GetLocalAS returns the cached local AS number.
func (c *Client) GetLocalAS() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.localAS
}

// AddNeighbor adds a BGP neighbor. The peerType is "internal" or "external".
// Keepalive and holdTime are in seconds (0 means use FRR defaults).
// Note: FRR infers iBGP vs eBGP from whether remoteAS equals the local AS,
// so peerType is used for informational/logging purposes only.
func (c *Client) AddNeighbor(ctx context.Context, addr string, remoteAS uint32, peerType string, keepalive, holdTime uint32, cfg *NeighborConfig) error {
	c.log.Info("adding BGP neighbor",
		zap.String("address", addr),
		zap.Uint32("remote_as", remoteAS),
		zap.String("peer_type", peerType),
		zap.Uint32("keepalive", keepalive),
		zap.Uint32("hold_time", holdTime),
	)

	localAS := c.getLocalAS(ctx)
	if localAS == 0 {
		return fmt.Errorf("frr: cannot add neighbor %s: %w", addr, ErrBGPNotConfigured)
	}

	commands := []string{
		fmt.Sprintf("router bgp %d", localAS),
		fmt.Sprintf("neighbor %s remote-as %d", addr, remoteAS),
	}

	if keepalive > 0 && holdTime > 0 {
		commands = append(commands, fmt.Sprintf("neighbor %s timers %d %d", addr, keepalive, holdTime))
	}

	if cfg != nil {
		if cfg.Description != "" {
			commands = append(commands, fmt.Sprintf("neighbor %s description %s", addr, cfg.Description))
		}
		if cfg.SourceAddress != "" {
			commands = append(commands, fmt.Sprintf("neighbor %s update-source %s", addr, cfg.SourceAddress))
		}
		if cfg.EBGPMultihop > 0 {
			commands = append(commands, fmt.Sprintf("neighbor %s ebgp-multihop %d", addr, cfg.EBGPMultihop))
		}
		if cfg.Password != "" {
			commands = append(commands, fmt.Sprintf("neighbor %s password %s", addr, cfg.Password))
		}
	}

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: add BGP neighbor %s (AS=%d): %w", addr, remoteAS, err)
	}
	return nil
}

// RemoveNeighbor removes a BGP neighbor by its IP address.
func (c *Client) RemoveNeighbor(ctx context.Context, addr string) error {
	c.log.Info("removing BGP neighbor", zap.String("address", addr))

	commands := []string{
		fmt.Sprintf("router bgp %d", c.getLocalAS(ctx)),
		fmt.Sprintf("no neighbor %s", addr),
	}

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: remove BGP neighbor %s: %w", addr, err)
	}
	return nil
}

// ActivateNeighborAFI activates an address family for a BGP neighbor.
// The afi parameter accepts "ipv4-unicast", "ipv4", "ipv6-unicast", "ipv6".
func (c *Client) ActivateNeighborAFI(ctx context.Context, addr string, afi string) error {
	afiName := resolveAFICLI(afi)

	c.log.Info("activating BGP neighbor AFI",
		zap.String("address", addr),
		zap.String("afi", afiName),
	)

	commands := []string{
		fmt.Sprintf("router bgp %d", c.getLocalAS(ctx)),
		fmt.Sprintf("address-family %s", afiName),
		fmt.Sprintf("neighbor %s activate", addr),
		// Enable soft-reconfiguration inbound so we can re-evaluate routing
		// policy without tearing down the BGP session.
		fmt.Sprintf("neighbor %s soft-reconfiguration inbound", addr),
		"exit-address-family",
	}

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: activate AFI %s for neighbor %s: %w", afiName, addr, err)
	}
	return nil
}

// AdvertiseNetwork adds a network prefix to BGP for advertisement.
// The afi parameter accepts the same values as ActivateNeighborAFI.
func (c *Client) AdvertiseNetwork(ctx context.Context, prefix string, afi string) error {
	afiName := resolveAFICLI(afi)

	c.log.Info("advertising BGP network",
		zap.String("prefix", prefix),
		zap.String("afi", afiName),
	)

	commands := []string{
		fmt.Sprintf("router bgp %d", c.getLocalAS(ctx)),
		fmt.Sprintf("address-family %s", afiName),
		fmt.Sprintf("network %s", prefix),
		"exit-address-family",
	}

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: advertise network %s (afi=%s): %w", prefix, afiName, err)
	}
	return nil
}

// WithdrawNetwork removes a network prefix from BGP advertisements.
func (c *Client) WithdrawNetwork(ctx context.Context, prefix string, afi string) error {
	afiName := resolveAFICLI(afi)

	c.log.Info("withdrawing BGP network",
		zap.String("prefix", prefix),
		zap.String("afi", afiName),
	)

	commands := []string{
		fmt.Sprintf("router bgp %d", c.getLocalAS(ctx)),
		fmt.Sprintf("address-family %s", afiName),
		fmt.Sprintf("no network %s", prefix),
		"exit-address-family",
	}

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: withdraw network %s (afi=%s): %w", prefix, afiName, err)
	}
	return nil
}

// getLocalAS returns the cached local AS. It logs a warning if the local AS
// has not been configured (returns 0), which would produce invalid FRR commands.
func (c *Client) getLocalAS(_ context.Context) uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.localAS == 0 {
		c.log.Warn("getLocalAS: BGP local AS not configured, returning 0")
	}
	return c.localAS
}

// SetNeighborMaxPrefix configures the maximum number of prefixes accepted
// from a BGP neighbor. If warningOnly is true, FRR logs a warning instead
// of tearing down the session when the limit is exceeded.
func (c *Client) SetNeighborMaxPrefix(ctx context.Context, addr string, maxPrefixes uint32, warningOnly bool, afi string) error {
	afiName := resolveAFICLI(afi)

	c.log.Info("setting neighbor maximum-prefix",
		zap.String("address", addr),
		zap.Uint32("max_prefixes", maxPrefixes),
		zap.Bool("warning_only", warningOnly),
		zap.String("afi", afiName),
	)

	cmd := fmt.Sprintf("neighbor %s maximum-prefix %d", addr, maxPrefixes)
	if warningOnly {
		cmd += " warning-only"
	}

	commands := []string{
		fmt.Sprintf("router bgp %d", c.getLocalAS(ctx)),
		fmt.Sprintf("address-family %s", afiName),
		cmd,
		"exit-address-family",
	}

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: set max-prefix %d for neighbor %s: %w", maxPrefixes, addr, err)
	}
	return nil
}

// ConfigureRouteMap creates or replaces a route-map in FRR with the given
// set commands. Each setCmd is a complete "set ..." line.
func (c *Client) ConfigureRouteMap(ctx context.Context, name string, setCmds []string) error {
	c.log.Info("configuring route-map",
		zap.String("name", name),
		zap.Int("set_commands", len(setCmds)),
	)

	// Remove old route-map first for clean state.
	commands := make([]string, 0, 3+len(setCmds))
	commands = append(commands,
		fmt.Sprintf("no route-map %s", name),
		fmt.Sprintf("route-map %s permit 10", name),
	)
	commands = append(commands, setCmds...)
	commands = append(commands, "exit")

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: configure route-map %s: %w", name, err)
	}
	return nil
}

// AdvertiseNetworkWithRouteMap adds a network prefix with an associated route-map.
func (c *Client) AdvertiseNetworkWithRouteMap(ctx context.Context, prefix, afi, routeMap string) error {
	afiName := resolveAFICLI(afi)

	c.log.Info("advertising BGP network with route-map",
		zap.String("prefix", prefix),
		zap.String("afi", afiName),
		zap.String("route_map", routeMap),
	)

	commands := []string{
		fmt.Sprintf("router bgp %d", c.getLocalAS(ctx)),
		fmt.Sprintf("address-family %s", afiName),
		fmt.Sprintf("network %s route-map %s", prefix, routeMap),
		"exit-address-family",
	}

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: advertise network %s with route-map %s: %w", prefix, routeMap, err)
	}
	return nil
}

// RemoveRouteMap removes a route-map from FRR.
func (c *Client) RemoveRouteMap(ctx context.Context, name string) error {
	c.log.Info("removing route-map", zap.String("name", name))

	commands := []string{
		fmt.Sprintf("no route-map %s", name),
	}

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: remove route-map %s: %w", name, err)
	}
	return nil
}

// SetNeighborBFD enables or disables BFD for a BGP neighbor.
func (c *Client) SetNeighborBFD(ctx context.Context, addr string, enabled bool) error {
	action := "enabling"
	cmd := fmt.Sprintf("neighbor %s bfd", addr)
	if !enabled {
		action = "disabling"
		cmd = fmt.Sprintf("no neighbor %s bfd", addr)
	}

	c.log.Info(action+" BFD for neighbor",
		zap.String("address", addr),
		zap.Bool("enabled", enabled),
	)

	commands := []string{
		fmt.Sprintf("router bgp %d", c.getLocalAS(ctx)),
		cmd,
	}

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: set neighbor %s bfd=%v: %w", addr, enabled, err)
	}
	return nil
}

// resolveAFICLI maps AFI identifiers to FRR CLI address-family names.
func resolveAFICLI(afi string) string {
	switch afi {
	case "ipv4-unicast", "ipv4":
		return "ipv4 unicast"
	case "ipv6-unicast", "ipv6":
		return "ipv6 unicast"
	default:
		return afi
	}
}
