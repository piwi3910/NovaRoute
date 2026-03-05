package frr

import (
	"context"
	"fmt"

	"go.uber.org/zap"
)

// AddBFDPeer creates a single-hop BFD session for the given peer address.
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
	}
	// Only set BFD intervals when explicitly specified (non-zero).
	// Zero values mean "use FRR defaults" and are rejected by FRR.
	if minRx > 0 {
		commands = append(commands, fmt.Sprintf("receive-interval %d", minRx))
	}
	if minTx > 0 {
		commands = append(commands, fmt.Sprintf("transmit-interval %d", minTx))
	}
	if detectMult > 0 {
		commands = append(commands, fmt.Sprintf("detect-multiplier %d", detectMult))
	}
	commands = append(commands, "exit", "exit")

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: add BFD peer %s: %w", peerAddr, err)
	}
	return nil
}

// RemoveBFDPeer removes a single-hop BFD session for the given peer address.
// If iface is non-empty, it is appended to the peer command to disambiguate
// multi-interface BFD sessions.
func (c *Client) RemoveBFDPeer(ctx context.Context, peerAddr string, iface string) error {
	c.log.Info("removing BFD peer", zap.String("peer_addr", peerAddr), zap.String("interface", iface))

	peerCmd := fmt.Sprintf("no peer %s", peerAddr)
	if iface != "" {
		peerCmd += fmt.Sprintf(" interface %s", iface)
	}
	commands := []string{
		"bfd",
		peerCmd,
		"exit",
	}

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: remove BFD peer %s: %w", peerAddr, err)
	}
	return nil
}
