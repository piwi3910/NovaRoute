package frr

import (
	"context"
	"fmt"

	frr "github.com/piwi3910/NovaRoute/api/frr"
	"go.uber.org/zap"
)

// AddBFDPeer creates a single-hop BFD session for the given peer address.
// Parameters:
//   - peerAddr: the peer's IP address
//   - minRx: minimum receive interval in milliseconds (e.g. 300)
//   - minTx: minimum transmit interval in milliseconds (e.g. 300)
//   - detectMult: detection multiplier (e.g. 3)
//   - iface: the interface to use for BFD (may be empty for non-interface-specific peers)
func (c *Client) AddBFDPeer(ctx context.Context, peerAddr string, minRx, minTx, detectMult uint32, iface string) error {
	c.log.Info("adding BFD peer",
		zap.String("peer_addr", peerAddr),
		zap.Uint32("min_rx", minRx),
		zap.Uint32("min_tx", minTx),
		zap.Uint32("detect_mult", detectMult),
		zap.String("interface", iface),
	)

	peerBase := BFDPeerPath(peerAddr)

	updates := []*frr.PathValue{
		pv(peerBase+bfdMinRxInterval, fmt.Sprintf("%d", minRx)),
		pv(peerBase+bfdMinTxInterval, fmt.Sprintf("%d", minTx)),
		pv(peerBase+bfdDetectMultiplier, fmt.Sprintf("%d", detectMult)),
	}

	if iface != "" {
		updates = append(updates, pv(peerBase+bfdInterface, iface))
	}

	if err := c.applyChanges(ctx, updates, nil); err != nil {
		return fmt.Errorf("frr: add BFD peer %s: %w", peerAddr, err)
	}
	return nil
}

// RemoveBFDPeer removes a single-hop BFD session for the given peer address.
func (c *Client) RemoveBFDPeer(ctx context.Context, peerAddr string) error {
	c.log.Info("removing BFD peer", zap.String("peer_addr", peerAddr))

	peerBase := BFDPeerPath(peerAddr)

	deletes := []*frr.PathValue{
		pvDelete(peerBase),
	}

	if err := c.applyChanges(ctx, nil, deletes); err != nil {
		return fmt.Errorf("frr: remove BFD peer %s: %w", peerAddr, err)
	}
	return nil
}
