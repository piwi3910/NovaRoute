package frr

import (
	"context"
	"fmt"

	frr "github.com/piwi3910/NovaRoute/api/frr"
	"go.uber.org/zap"
)

// EnableOSPFInterface enables OSPF on the specified interface within the given
// area. Parameters:
//   - ifaceName: the network interface name (e.g. "eth0")
//   - areaID: the OSPF area ID in dotted notation (e.g. "0.0.0.0")
//   - passive: whether the interface should be passive (no OSPF hellos sent)
//   - cost: the OSPF interface cost (0 means use default)
//   - hello: the hello interval in seconds (0 means use default)
//   - dead: the dead interval in seconds (0 means use default)
func (c *Client) EnableOSPFInterface(ctx context.Context, ifaceName, areaID string, passive bool, cost, hello, dead uint32) error {
	c.log.Info("enabling OSPF interface",
		zap.String("interface", ifaceName),
		zap.String("area_id", areaID),
		zap.Bool("passive", passive),
		zap.Uint32("cost", cost),
		zap.Uint32("hello", hello),
		zap.Uint32("dead", dead),
	)

	ifacePath := OSPFInterfacePath(ifaceName, areaID)

	passiveStr := "false"
	if passive {
		passiveStr = "true"
	}

	updates := []*frr.PathValue{
		pv(ifacePath+ospfInterfacePassive, passiveStr),
	}

	if cost > 0 {
		updates = append(updates, pv(ifacePath+ospfInterfaceCost, fmt.Sprintf("%d", cost)))
	}
	if hello > 0 {
		updates = append(updates, pv(ifacePath+ospfInterfaceHelloInterval, fmt.Sprintf("%d", hello)))
	}
	if dead > 0 {
		updates = append(updates, pv(ifacePath+ospfInterfaceDeadInterval, fmt.Sprintf("%d", dead)))
	}

	if err := c.applyChanges(ctx, updates, nil); err != nil {
		return fmt.Errorf("frr: enable OSPF on %s (area=%s): %w", ifaceName, areaID, err)
	}
	return nil
}

// DisableOSPFInterface removes OSPF configuration from the specified interface
// within the given area.
func (c *Client) DisableOSPFInterface(ctx context.Context, ifaceName, areaID string) error {
	c.log.Info("disabling OSPF interface",
		zap.String("interface", ifaceName),
		zap.String("area_id", areaID),
	)

	ifacePath := OSPFInterfacePath(ifaceName, areaID)

	deletes := []*frr.PathValue{
		pvDelete(ifacePath),
	}

	if err := c.applyChanges(ctx, nil, deletes); err != nil {
		return fmt.Errorf("frr: disable OSPF on %s (area=%s): %w", ifaceName, areaID, err)
	}
	return nil
}
