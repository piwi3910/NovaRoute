package frr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Client configures FRR daemons by connecting to their VTY Unix sockets
// and sending CLI commands. Each operation opens a fresh connection to the
// target daemon socket, sends the commands, and disconnects.
type Client struct {
	socketDir string
	timeout   time.Duration
	log       *zap.Logger
	mu        sync.Mutex
	localAS   uint32 // Cached after ConfigureBGPGlobal.
}

// NewClient creates a new FRR VTY client that communicates with FRR daemons
// through their Unix sockets in the given directory (typically /run/frr).
func NewClient(socketDir string, logger *zap.Logger) *Client {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Client{
		socketDir: socketDir,
		timeout:   10 * time.Second,
		log:       logger,
	}
}

// Close is a no-op for the VTY client since connections are per-operation.
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

// GetVersion returns the FRR version by running "show version" on zebra.
func (c *Client) GetVersion(ctx context.Context) (string, error) {
	output, err := c.runShow("zebra", "show version")
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

// runConfig connects to a daemon's VTY socket, enters configure terminal
// mode, executes the given commands, then exits. All commands are sent on
// a single connection to maintain VTY node context.
func (c *Client) runConfig(daemon string, commands []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	sockPath := filepath.Join(c.socketDir, daemon+".vty")
	vc, err := dialVTY(sockPath, c.timeout)
	if err != nil {
		return fmt.Errorf("frr: connect to %s: %w", daemon, err)
	}
	defer vc.close()

	// Enter config mode.
	if _, status, err := vc.execCmd("configure terminal"); err != nil {
		return fmt.Errorf("frr: configure terminal on %s: %w", daemon, err)
	} else if status != cmdSuccess {
		return fmt.Errorf("frr: configure terminal on %s failed (status=%d)", daemon, status)
	}

	// Run each command.
	for _, cmd := range commands {
		output, status, err := vc.execCmd(cmd)
		if err != nil {
			return fmt.Errorf("frr: exec %q on %s: %w", cmd, daemon, err)
		}
		if status != cmdSuccess && status != cmdWarning {
			return fmt.Errorf("frr: %q on %s failed (status=%d): %s", cmd, daemon, status, strings.TrimSpace(output))
		}
		c.log.Debug("VTY command OK",
			zap.String("daemon", daemon),
			zap.String("cmd", cmd),
		)
	}

	// Exit config mode.
	_, _, _ = vc.execCmd("end")

	return nil
}

// runShow connects to a daemon's VTY socket and executes a show command.
func (c *Client) runShow(daemon string, command string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	sockPath := filepath.Join(c.socketDir, daemon+".vty")
	vc, err := dialVTY(sockPath, c.timeout)
	if err != nil {
		return "", fmt.Errorf("frr: connect to %s: %w", daemon, err)
	}
	defer vc.close()

	output, status, err := vc.execCmd(command)
	if err != nil {
		return "", fmt.Errorf("frr: exec %q on %s: %w", command, daemon, err)
	}
	if status != cmdSuccess && status != cmdWarning {
		return output, fmt.Errorf("frr: %q on %s failed (status=%d): %s", command, daemon, status, strings.TrimSpace(output))
	}

	return output, nil
}
