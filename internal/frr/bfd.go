package frr

import (
	"context"
	"fmt"

	"go.uber.org/zap"
)

// AddBFDPeer creates a single-hop BFD session for the given peer address.
// Parameters:
//   - peerAddr: the peer's IP address
//   - minRx: minimum receive interval in milliseconds (e.g. 300)
//   - minTx: minimum transmit interval in milliseconds (e.g. 300)
//   - detectMult: detection multiplier (e.g. 3)
//   - iface: the interface to use for BFD (may be empty)
func (c *Client) AddBFDPeer(ctx context.Context, peerAddr string, minRx, minTx, detectMult uint32, iface string) error {
	c.log.Info("adding BFD peer",
		zap.String("peer_addr", peerAddr),
		zap.Uint32("min_rx", minRx),
		zap.Uint32("min_tx", minTx),
		zap.Uint32("detect_mult", detectMult),
		zap.String("interface", iface),
	)

	peerCmd := fmt.Sprintf("peer %s", peerAddr)
	if iface != "" {
		peerCmd += fmt.Sprintf(" interface %s", iface)
	}

	commands := []string{
		"bfd",
		peerCmd,
		fmt.Sprintf("receive-interval %d", minRx),
		fmt.Sprintf("transmit-interval %d", minTx),
		fmt.Sprintf("detect-multiplier %d", detectMult),
		"exit",
		"exit",
	}

	if err := c.runConfig("bfdd", commands); err != nil {
		return fmt.Errorf("frr: add BFD peer %s: %w", peerAddr, err)
	}
	return nil
}

// RemoveBFDPeer removes a single-hop BFD session for the given peer address.
func (c *Client) RemoveBFDPeer(ctx context.Context, peerAddr string) error {
	c.log.Info("removing BFD peer", zap.String("peer_addr", peerAddr))

	commands := []string{
		"bfd",
		fmt.Sprintf("no peer %s", peerAddr),
		"exit",
	}

	if err := c.runConfig("bfdd", commands); err != nil {
		return fmt.Errorf("frr: remove BFD peer %s: %w", peerAddr, err)
	}
	return nil
}
