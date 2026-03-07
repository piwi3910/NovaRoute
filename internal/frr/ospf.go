package frr

import (
	"context"
	"fmt"

	"go.uber.org/zap"
)

// EnableOSPFInterface enables OSPF on the specified interface within the given area.
func (c *Client) EnableOSPFInterface(ctx context.Context, ifaceName, areaID string, passive bool, cost, hello, dead uint32) error {
	c.log.Info("enabling OSPF interface",
		zap.String("interface", ifaceName),
		zap.String("area_id", areaID),
		zap.Bool("passive", passive),
		zap.Uint32("cost", cost),
		zap.Uint32("hello", hello),
		zap.Uint32("dead", dead),
	)

	commands := []string{
		fmt.Sprintf("interface %s", ifaceName),
		fmt.Sprintf("ip ospf area %s", areaID),
	}

	if cost > 0 {
		commands = append(commands, fmt.Sprintf("ip ospf cost %d", cost))
	}
	if hello > 0 {
		commands = append(commands, fmt.Sprintf("ip ospf hello-interval %d", hello))
	}
	if dead > 0 {
		commands = append(commands, fmt.Sprintf("ip ospf dead-interval %d", dead))
	}

	commands = append(commands, "exit")

	if passive {
		commands = append(commands,
			"router ospf",
			fmt.Sprintf("passive-interface %s", ifaceName),
		)
	}

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: enable OSPF on %s (area=%s): %w", ifaceName, areaID, err)
	}
	return nil
}

// EnableOSPFv3Interface enables OSPFv3 (IPv6) on the specified interface within the given area.
// OSPFv3 uses "ipv6 ospf6 area" and "ipv6 ospf6 cost" commands under the interface context.
// Passive interfaces are configured under "router ospf6" with "passive-interface <iface>".
// Unlike IPv4 OSPF, hello/dead intervals are not configured per-interface in FRR OSPFv3.
func (c *Client) EnableOSPFv3Interface(ctx context.Context, ifaceName, areaID string, passive bool, cost uint32) error {
	c.log.Info("enabling OSPFv3 interface",
		zap.String("interface", ifaceName),
		zap.String("area_id", areaID),
		zap.Bool("passive", passive),
		zap.Uint32("cost", cost),
	)

	commands := []string{
		fmt.Sprintf("interface %s", ifaceName),
		fmt.Sprintf("ipv6 ospf6 area %s", areaID),
	}

	if cost > 0 {
		commands = append(commands, fmt.Sprintf("ipv6 ospf6 cost %d", cost))
	}

	commands = append(commands, "exit")

	if passive {
		commands = append(commands,
			"router ospf6",
			fmt.Sprintf("passive-interface %s", ifaceName),
		)
	}

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: enable OSPFv3 on %s (area=%s): %w", ifaceName, areaID, err)
	}
	return nil
}

// DisableOSPFv3Interface removes OSPFv3 (IPv6) configuration from the specified interface.
// If passive is true, the passive-interface setting is also removed from router ospf6.
func (c *Client) DisableOSPFv3Interface(ctx context.Context, ifaceName, areaID string, passive bool) error {
	c.log.Info("disabling OSPFv3 interface",
		zap.String("interface", ifaceName),
		zap.String("area_id", areaID),
	)

	commands := []string{
		fmt.Sprintf("interface %s", ifaceName),
		fmt.Sprintf("no ipv6 ospf6 area %s", areaID),
		"exit",
	}

	if passive {
		commands = append(commands,
			"router ospf6",
			fmt.Sprintf("no passive-interface %s", ifaceName),
		)
	}

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: disable OSPFv3 on %s (area=%s): %w", ifaceName, areaID, err)
	}
	return nil
}

// DisableOSPFInterface removes OSPF configuration from the specified interface.
// If passive is true, the passive-interface setting is also removed.
func (c *Client) DisableOSPFInterface(ctx context.Context, ifaceName, areaID string, passive bool) error {
	c.log.Info("disabling OSPF interface",
		zap.String("interface", ifaceName),
		zap.String("area_id", areaID),
	)

	commands := []string{
		fmt.Sprintf("interface %s", ifaceName),
		fmt.Sprintf("no ip ospf area %s", areaID),
		"exit",
	}

	if passive {
		commands = append(commands,
			"router ospf",
			fmt.Sprintf("no passive-interface %s", ifaceName),
		)
	}

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: disable OSPF on %s (area=%s): %w", ifaceName, areaID, err)
	}
	return nil
}
