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

// DisableOSPFInterface removes OSPF configuration from the specified interface.
func (c *Client) DisableOSPFInterface(ctx context.Context, ifaceName, areaID string) error {
	c.log.Info("disabling OSPF interface",
		zap.String("interface", ifaceName),
		zap.String("area_id", areaID),
	)

	commands := []string{
		fmt.Sprintf("interface %s", ifaceName),
		fmt.Sprintf("no ip ospf area %s", areaID),
		"exit",
		"router ospf",
		fmt.Sprintf("no passive-interface %s", ifaceName),
	}

	if err := c.runConfig(ctx, commands); err != nil {
		return fmt.Errorf("frr: disable OSPF on %s (area=%s): %w", ifaceName, areaID, err)
	}
	return nil
}
