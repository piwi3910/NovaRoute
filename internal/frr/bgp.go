package frr

import (
	"context"
	"fmt"

	"go.uber.org/zap"
)

// ConfigureBGPGlobal creates the BGP instance with the given AS number and
// router ID. This is equivalent to "router bgp <AS>" + "bgp router-id <ID>".
func (c *Client) ConfigureBGPGlobal(ctx context.Context, localAS uint32, routerID string) error {
	c.log.Info("configuring BGP global",
		zap.Uint32("local_as", localAS),
		zap.String("router_id", routerID),
	)

	commands := []string{
		fmt.Sprintf("router bgp %d", localAS),
		fmt.Sprintf("bgp router-id %s", routerID),
		"bgp graceful-restart",
		"bgp graceful-restart restart-time 120",
		"bgp graceful-restart stalepath-time 360",
	}

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
		commands := []string{
			fmt.Sprintf("router bgp %d", newAS),
			fmt.Sprintf("bgp router-id %s", routerID),
			"bgp graceful-restart",
			"bgp graceful-restart restart-time 120",
			"bgp graceful-restart stalepath-time 360",
		}
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
func (c *Client) AddNeighbor(ctx context.Context, addr string, remoteAS uint32, peerType string, keepalive, holdTime uint32) error {
	c.log.Info("adding BGP neighbor",
		zap.String("address", addr),
		zap.Uint32("remote_as", remoteAS),
		zap.String("peer_type", peerType),
		zap.Uint32("keepalive", keepalive),
		zap.Uint32("hold_time", holdTime),
	)

	commands := []string{
		fmt.Sprintf("router bgp %d", c.getLocalAS(ctx)),
		fmt.Sprintf("neighbor %s remote-as %d", addr, remoteAS),
	}

	if keepalive > 0 && holdTime > 0 {
		commands = append(commands, fmt.Sprintf("neighbor %s timers %d %d", addr, keepalive, holdTime))
	}

	// Enable soft-reconfiguration inbound so we can re-evaluate routing policy
	// without tearing down the BGP session.
	commands = append(commands,
		"address-family ipv4 unicast",
		fmt.Sprintf("neighbor %s soft-reconfiguration inbound", addr),
		"exit-address-family",
	)

	commands = append(commands,
		"address-family ipv6 unicast",
		fmt.Sprintf("neighbor %s soft-reconfiguration inbound", addr),
		"exit-address-family",
	)

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

// getLocalAS returns the cached local AS or 0 if not yet known.
func (c *Client) getLocalAS(_ context.Context) uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.localAS
}

// ConfigurePrefixList creates or replaces an IP prefix-list in FRR.
// Each entry is a string like "permit 192.168.100.0/24 ge 32 le 32".
func (c *Client) ConfigurePrefixList(ctx context.Context, name string, entries []string) error {
	c.log.Info("configuring prefix-list",
		zap.String("name", name),
		zap.Int("entries", len(entries)),
	)

	// Remove the old prefix-list first to ensure clean state.
	commands := []string{
		fmt.Sprintf("no ip prefix-list %s", name),
	}

	for i, entry := range entries {
		commands = append(commands, fmt.Sprintf("ip prefix-list %s seq %d %s", name, (i+1)*10, entry))
	}

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: configure prefix-list %s: %w", name, err)
	}
	return nil
}

// ApplyNeighborPrefixList applies an IP prefix-list to a BGP neighbor for the
// given direction ("in" or "out") under the specified address family.
func (c *Client) ApplyNeighborPrefixList(ctx context.Context, addr, prefixListName, direction, afi string) error {
	afiName := resolveAFICLI(afi)

	c.log.Info("applying prefix-list to neighbor",
		zap.String("address", addr),
		zap.String("prefix_list", prefixListName),
		zap.String("direction", direction),
		zap.String("afi", afiName),
	)

	commands := []string{
		fmt.Sprintf("router bgp %d", c.getLocalAS(ctx)),
		fmt.Sprintf("address-family %s", afiName),
		fmt.Sprintf("neighbor %s prefix-list %s %s", addr, prefixListName, direction),
		"exit-address-family",
	}

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: apply prefix-list %s %s to neighbor %s: %w", prefixListName, direction, addr, err)
	}
	return nil
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
