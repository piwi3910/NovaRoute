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
	}

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: configure BGP global (AS=%d, router_id=%s): %w", localAS, routerID, err)
	}

	c.localAS = localAS
	return nil
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
	return c.localAS
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
