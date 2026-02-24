package frr

import (
	"context"
	"fmt"

	frr "github.com/piwi3910/NovaRoute/api/frr"
	"go.uber.org/zap"
)

// ConfigureBGPGlobal sets up the BGP instance with the given local AS number
// and router ID. This creates the default BGP instance if it does not already
// exist, and configures its global parameters.
func (c *Client) ConfigureBGPGlobal(ctx context.Context, localAS uint32, routerID string) error {
	c.log.Info("configuring BGP global",
		zap.Uint32("local_as", localAS),
		zap.String("router_id", routerID),
	)

	updates := []*frr.PathValue{
		pv(bgpGlobalAS, fmt.Sprintf("%d", localAS)),
		pv(bgpGlobalRouterID, routerID),
	}

	if err := c.applyChanges(ctx, updates, nil); err != nil {
		return fmt.Errorf("frr: configure BGP global (AS=%d, router_id=%s): %w", localAS, routerID, err)
	}
	return nil
}

// AddNeighbor adds a BGP neighbor with the given parameters. The peerType
// should be "internal" for iBGP or "external" for eBGP. Keepalive and holdTime
// are specified in seconds (typical values: keepalive=30, holdTime=90).
func (c *Client) AddNeighbor(ctx context.Context, addr string, remoteAS uint32, peerType string, keepalive, holdTime uint32) error {
	c.log.Info("adding BGP neighbor",
		zap.String("address", addr),
		zap.Uint32("remote_as", remoteAS),
		zap.String("peer_type", peerType),
		zap.Uint32("keepalive", keepalive),
		zap.Uint32("hold_time", holdTime),
	)

	neighborBase := BGPNeighborPath(addr)

	updates := []*frr.PathValue{
		pv(neighborBase+bgpNeighborRemoteAS, fmt.Sprintf("%d", remoteAS)),
		pv(neighborBase+bgpNeighborPeerType, peerType),
		pv(neighborBase+bgpNeighborEnabled, "true"),
	}

	if keepalive > 0 {
		updates = append(updates, pv(neighborBase+bgpNeighborTimersKeepalive, fmt.Sprintf("%d", keepalive)))
	}
	if holdTime > 0 {
		updates = append(updates, pv(neighborBase+bgpNeighborTimersHoldTime, fmt.Sprintf("%d", holdTime)))
	}

	if err := c.applyChanges(ctx, updates, nil); err != nil {
		return fmt.Errorf("frr: add BGP neighbor %s (AS=%d): %w", addr, remoteAS, err)
	}
	return nil
}

// RemoveNeighbor removes a BGP neighbor by its IP address.
func (c *Client) RemoveNeighbor(ctx context.Context, addr string) error {
	c.log.Info("removing BGP neighbor", zap.String("address", addr))

	neighborBase := BGPNeighborPath(addr)

	deletes := []*frr.PathValue{
		pvDelete(neighborBase),
	}

	if err := c.applyChanges(ctx, nil, deletes); err != nil {
		return fmt.Errorf("frr: remove BGP neighbor %s: %w", addr, err)
	}
	return nil
}

// ActivateNeighborAFI activates an address family for a BGP neighbor.
// The afi parameter accepts friendly names ("ipv4-unicast", "ipv4",
// "ipv6-unicast", "ipv6") or full YANG identities
// (e.g. "frr-routing:ipv4-unicast").
func (c *Client) ActivateNeighborAFI(ctx context.Context, addr string, afi string) error {
	resolvedAFI := resolveAFI(afi)

	c.log.Info("activating BGP neighbor AFI",
		zap.String("address", addr),
		zap.String("afi", resolvedAFI),
	)

	afiPath := BGPNeighborAFIPath(addr, resolvedAFI)

	updates := []*frr.PathValue{
		pv(afiPath+bgpNeighborAFIEnabled, "true"),
	}

	if err := c.applyChanges(ctx, updates, nil); err != nil {
		return fmt.Errorf("frr: activate AFI %s for neighbor %s: %w", resolvedAFI, addr, err)
	}
	return nil
}

// AdvertiseNetwork adds a network prefix to BGP for advertisement.
// The afi parameter accepts the same values as ActivateNeighborAFI.
// The prefix should be in CIDR notation (e.g. "10.0.0.0/24").
func (c *Client) AdvertiseNetwork(ctx context.Context, prefix string, afi string) error {
	resolvedAFI := resolveAFI(afi)

	c.log.Info("advertising BGP network",
		zap.String("prefix", prefix),
		zap.String("afi", resolvedAFI),
	)

	networkPath := BGPNetworkPath(prefix, resolvedAFI)

	updates := []*frr.PathValue{
		pv(networkPath, ""),
	}

	if err := c.applyChanges(ctx, updates, nil); err != nil {
		return fmt.Errorf("frr: advertise network %s (afi=%s): %w", prefix, resolvedAFI, err)
	}
	return nil
}

// WithdrawNetwork removes a network prefix from BGP advertisements.
// The afi parameter accepts the same values as ActivateNeighborAFI.
func (c *Client) WithdrawNetwork(ctx context.Context, prefix string, afi string) error {
	resolvedAFI := resolveAFI(afi)

	c.log.Info("withdrawing BGP network",
		zap.String("prefix", prefix),
		zap.String("afi", resolvedAFI),
	)

	networkPath := BGPNetworkPath(prefix, resolvedAFI)

	deletes := []*frr.PathValue{
		pvDelete(networkPath),
	}

	if err := c.applyChanges(ctx, nil, deletes); err != nil {
		return fmt.Errorf("frr: withdraw network %s (afi=%s): %w", prefix, resolvedAFI, err)
	}
	return nil
}
