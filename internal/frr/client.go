package frr

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Client configures FRR daemons by invoking vtysh, the integrated FRR CLI
// shell. Each operation runs "vtysh -c <cmd>" (for show commands) or
// "vtysh -f <file>" (for config batches) to configure all relevant daemons.
type Client struct {
	socketDir string
	vtyshPath string
	timeout   time.Duration
	log       *zap.Logger
	mu        sync.Mutex
	localAS   uint32 // Cached after ConfigureBGPGlobal.
}

// NewClient creates a new FRR client that communicates with FRR daemons
// via vtysh. The socketDir (typically /run/frr) is used for readiness checks.
func NewClient(socketDir string, logger *zap.Logger) *Client {
	if logger == nil {
		logger = zap.NewNop()
	}
	vtysh := "vtysh"
	// Check common FRR paths.
	for _, p := range []string{"/usr/bin/vtysh", "/usr/lib/frr/vtysh"} {
		if _, err := os.Stat(p); err == nil {
			vtysh = p
			break
		}
	}
	return &Client{
		socketDir: socketDir,
		vtyshPath: vtysh,
		timeout:   30 * time.Second,
		log:       logger,
	}
}

// Close is a no-op since vtysh is invoked per-operation.
func (c *Client) Close() error {
	return nil
}

// IsReady checks whether the required FRR daemon sockets exist.
func (c *Client) IsReady() bool {
	for _, daemon := range []string{"zebra", "bgpd"} {
		sock := filepath.Join(c.socketDir, daemon+".vty")
		if _, err := os.Stat(sock); err != nil {
			return false
		}
	}
	return true
}

// GetVersion returns the FRR version by running "show version" via vtysh.
func (c *Client) GetVersion(ctx context.Context) (string, error) {
	output, err := c.runShow(ctx, "show version")
	if err != nil {
		return "", fmt.Errorf("frr: get version: %w", err)
	}

	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "FRRouting "); ok {
			return after, nil
		}
	}
	return strings.TrimSpace(strings.Split(output, "\n")[0]), nil
}

// runConfig executes a batch of FRR configuration commands via vtysh.
// Commands are wrapped in "configure terminal" / "end" and piped to vtysh.
func (c *Client) runConfig(ctx context.Context, commands []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Build config block: configure terminal → commands → end.
	lines := make([]string, 0, len(commands)+2)
	lines = append(lines, "configure terminal")
	lines = append(lines, commands...)
	lines = append(lines, "end")

	input := strings.Join(lines, "\n") + "\n"

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.vtyshPath, "--vty_socket", c.socketDir)
	cmd.Stdin = strings.NewReader(input)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("frr: vtysh config failed: %w\noutput: %s", err, strings.TrimSpace(string(out)))
	}

	// Check for error markers in output.
	outStr := string(out)
	if strings.Contains(outStr, "% ") || strings.Contains(outStr, "error reading from") || strings.Contains(outStr, "is not running") {
		return fmt.Errorf("frr: vtysh config error: %s", strings.TrimSpace(outStr))
	}

	for _, cmd := range commands {
		c.log.Debug("VTY command OK", zap.String("cmd", cmd))
	}

	return nil
}

// runShow executes a show command via vtysh and returns the output.
func (c *Client) runShow(ctx context.Context, command string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.vtyshPath, "--vty_socket", c.socketDir, "-c", command)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("frr: vtysh show failed: %w\noutput: %s", err, strings.TrimSpace(string(out)))
	}

	return string(out), nil
}
