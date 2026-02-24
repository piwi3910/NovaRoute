package frr

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// mockVTYDaemon simulates an FRR daemon's VTY Unix socket.
// It accepts one connection, reads commands, and responds with the VTY protocol.
type mockVTYDaemon struct {
	mu       sync.Mutex
	commands []string // All commands received.
	listener net.Listener
}

// newMockVTYDaemon creates a mock VTY daemon listening on a Unix socket.
func newMockVTYDaemon(t *testing.T, dir, name string) *mockVTYDaemon {
	t.Helper()

	sockPath := filepath.Join(dir, name+".vty")
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to listen on %s: %v", sockPath, err)
	}

	m := &mockVTYDaemon{listener: listener}
	go m.serve(t)

	t.Cleanup(func() {
		listener.Close()
	})

	return m
}

func (m *mockVTYDaemon) serve(t *testing.T) {
	t.Helper()

	for {
		conn, err := m.listener.Accept()
		if err != nil {
			return // Listener closed.
		}
		go m.handleConn(conn)
	}
}

func (m *mockVTYDaemon) handleConn(conn net.Conn) {
	defer conn.Close()

	// Unix VTY sockets do NOT send a banner — the daemon just waits for commands.
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return
		}

		// Commands are null-terminated.
		cmd := string(buf[:n-1]) // Strip null terminator.

		m.mu.Lock()
		m.commands = append(m.commands, cmd)
		m.mu.Unlock()

		// Respond with success marker.
		sendMarker(conn, "", cmdSuccess)
	}
}

func (m *mockVTYDaemon) getCommands() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.commands))
	copy(result, m.commands)
	return result
}

// sendMarker sends output followed by the 4-byte VTY status marker.
func sendMarker(conn net.Conn, output string, status byte) {
	data := append([]byte(output), 0, status, 0, 0)
	conn.Write(data)
}

// setupVTYTest creates a temp directory with mock VTY daemons and returns a Client.
// Uses a short base path to avoid Unix socket path length limits (104 bytes on macOS).
func setupVTYTest(t *testing.T, daemons ...string) (*Client, map[string]*mockVTYDaemon) {
	t.Helper()

	dir, err := os.MkdirTemp("/tmp", "vty")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	mocks := make(map[string]*mockVTYDaemon, len(daemons))

	for _, d := range daemons {
		mocks[d] = newMockVTYDaemon(t, dir, d)
	}

	client := NewClient(dir, nil)
	// Use short timeout for tests.
	client.timeout = 2 * 1e9 // 2 seconds

	return client, mocks
}

// --- Client tests ---

func TestNewClient(t *testing.T) {
	dir := t.TempDir()
	client := NewClient(dir, nil)
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.socketDir != dir {
		t.Errorf("socketDir = %q, want %q", client.socketDir, dir)
	}
}

func TestIsReady(t *testing.T) {
	dir := t.TempDir()
	client := NewClient(dir, nil)

	// No sockets yet.
	if client.IsReady() {
		t.Error("expected not ready when no sockets")
	}

	// Create fake socket files.
	for _, name := range []string{"zebra.vty", "bgpd.vty"} {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}

	if !client.IsReady() {
		t.Error("expected ready when socket files exist")
	}
}

func TestCloseNoOp(t *testing.T) {
	client := NewClient(t.TempDir(), nil)
	if err := client.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

func TestGetVersion(t *testing.T) {
	client, mocks := setupVTYTest(t, "zebra")

	// Override the mock to return version in the show command response.
	// We need to close the default mock and create a custom one.
	mocks["zebra"].listener.Close()

	dir := client.socketDir
	sockPath := filepath.Join(dir, "zebra.vty")
	os.Remove(sockPath)

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Unix VTY: no banner, no enable. First command is "show version".
		buf := make([]byte, 4096)
		conn.Read(buf)
		sendMarker(conn, "FRRouting 10.5.1 (Mock)\n  running on Linux\n", cmdSuccess)
	}()

	ctx := context.Background()
	version, err := client.GetVersion(ctx)
	if err != nil {
		t.Fatalf("GetVersion error: %v", err)
	}
	if version != "10.5.1 (Mock)" {
		t.Errorf("GetVersion = %q, want %q", version, "10.5.1 (Mock)")
	}
}

// --- BGP tests ---

func TestConfigureBGPGlobal(t *testing.T) {
	client, mocks := setupVTYTest(t, "bgpd")
	ctx := context.Background()

	err := client.ConfigureBGPGlobal(ctx, 65000, "10.0.0.1")
	if err != nil {
		t.Fatalf("ConfigureBGPGlobal error: %v", err)
	}

	cmds := mocks["bgpd"].getCommands()
	assertContainsCmd(t, cmds, "configure terminal")
	assertContainsCmd(t, cmds, "router bgp 65000")
	assertContainsCmd(t, cmds, "bgp router-id 10.0.0.1")

	if client.localAS != 65000 {
		t.Errorf("localAS = %d, want 65000", client.localAS)
	}
}

func TestAddNeighbor(t *testing.T) {
	client, mocks := setupVTYTest(t, "bgpd")
	ctx := context.Background()
	client.localAS = 65000

	err := client.AddNeighbor(ctx, "192.168.1.1", 65001, "external", 30, 90)
	if err != nil {
		t.Fatalf("AddNeighbor error: %v", err)
	}

	cmds := mocks["bgpd"].getCommands()
	assertContainsCmd(t, cmds, "router bgp 65000")
	assertContainsCmd(t, cmds, "neighbor 192.168.1.1 remote-as 65001")
	assertContainsCmd(t, cmds, "neighbor 192.168.1.1 timers 30 90")
}

func TestAddNeighborDefaultTimers(t *testing.T) {
	client, mocks := setupVTYTest(t, "bgpd")
	ctx := context.Background()
	client.localAS = 65000

	err := client.AddNeighbor(ctx, "10.0.0.2", 65002, "internal", 0, 0)
	if err != nil {
		t.Fatalf("AddNeighbor error: %v", err)
	}

	cmds := mocks["bgpd"].getCommands()
	assertContainsCmd(t, cmds, "neighbor 10.0.0.2 remote-as 65002")

	// Timer command should not be present when 0.
	for _, cmd := range cmds {
		if strings.Contains(cmd, "timers") {
			t.Errorf("unexpected timers command: %s", cmd)
		}
	}
}

func TestRemoveNeighbor(t *testing.T) {
	client, mocks := setupVTYTest(t, "bgpd")
	ctx := context.Background()
	client.localAS = 65000

	err := client.RemoveNeighbor(ctx, "192.168.1.1")
	if err != nil {
		t.Fatalf("RemoveNeighbor error: %v", err)
	}

	cmds := mocks["bgpd"].getCommands()
	assertContainsCmd(t, cmds, "no neighbor 192.168.1.1")
}

func TestActivateNeighborAFI(t *testing.T) {
	client, mocks := setupVTYTest(t, "bgpd")
	ctx := context.Background()
	client.localAS = 65000

	err := client.ActivateNeighborAFI(ctx, "192.168.1.1", "ipv4-unicast")
	if err != nil {
		t.Fatalf("ActivateNeighborAFI error: %v", err)
	}

	cmds := mocks["bgpd"].getCommands()
	assertContainsCmd(t, cmds, "address-family ipv4 unicast")
	assertContainsCmd(t, cmds, "neighbor 192.168.1.1 activate")
	assertContainsCmd(t, cmds, "exit-address-family")
}

func TestAdvertiseNetwork(t *testing.T) {
	client, mocks := setupVTYTest(t, "bgpd")
	ctx := context.Background()
	client.localAS = 65000

	err := client.AdvertiseNetwork(ctx, "10.0.0.0/24", "ipv4")
	if err != nil {
		t.Fatalf("AdvertiseNetwork error: %v", err)
	}

	cmds := mocks["bgpd"].getCommands()
	assertContainsCmd(t, cmds, "address-family ipv4 unicast")
	assertContainsCmd(t, cmds, "network 10.0.0.0/24")
}

func TestWithdrawNetwork(t *testing.T) {
	client, mocks := setupVTYTest(t, "bgpd")
	ctx := context.Background()
	client.localAS = 65000

	err := client.WithdrawNetwork(ctx, "10.0.0.0/24", "ipv4")
	if err != nil {
		t.Fatalf("WithdrawNetwork error: %v", err)
	}

	cmds := mocks["bgpd"].getCommands()
	assertContainsCmd(t, cmds, "no network 10.0.0.0/24")
}

// --- BFD tests ---

func TestAddBFDPeer(t *testing.T) {
	client, mocks := setupVTYTest(t, "bfdd")
	ctx := context.Background()

	err := client.AddBFDPeer(ctx, "192.168.1.1", 300, 300, 3, "eth0")
	if err != nil {
		t.Fatalf("AddBFDPeer error: %v", err)
	}

	cmds := mocks["bfdd"].getCommands()
	assertContainsCmd(t, cmds, "bfd")
	assertContainsCmd(t, cmds, "peer 192.168.1.1 interface eth0")
	assertContainsCmd(t, cmds, "receive-interval 300")
	assertContainsCmd(t, cmds, "transmit-interval 300")
	assertContainsCmd(t, cmds, "detect-multiplier 3")
}

func TestAddBFDPeerNoInterface(t *testing.T) {
	client, mocks := setupVTYTest(t, "bfdd")
	ctx := context.Background()

	err := client.AddBFDPeer(ctx, "10.0.0.1", 200, 200, 5, "")
	if err != nil {
		t.Fatalf("AddBFDPeer error: %v", err)
	}

	cmds := mocks["bfdd"].getCommands()
	assertContainsCmd(t, cmds, "peer 10.0.0.1")

	// Should not contain "interface" in the peer command.
	for _, cmd := range cmds {
		if strings.HasPrefix(cmd, "peer ") && strings.Contains(cmd, "interface") {
			t.Errorf("unexpected interface in peer command: %s", cmd)
		}
	}
}

func TestRemoveBFDPeer(t *testing.T) {
	client, mocks := setupVTYTest(t, "bfdd")
	ctx := context.Background()

	err := client.RemoveBFDPeer(ctx, "192.168.1.1")
	if err != nil {
		t.Fatalf("RemoveBFDPeer error: %v", err)
	}

	cmds := mocks["bfdd"].getCommands()
	assertContainsCmd(t, cmds, "no peer 192.168.1.1")
}

// --- OSPF tests ---

func TestOSPFEnableInterface(t *testing.T) {
	client, mocks := setupVTYTest(t, "ospfd")
	ctx := context.Background()

	err := client.EnableOSPFInterface(ctx, "eth0", "0.0.0.0", true, 10, 5, 20)
	if err != nil {
		t.Fatalf("EnableOSPFInterface error: %v", err)
	}

	cmds := mocks["ospfd"].getCommands()
	assertContainsCmd(t, cmds, "interface eth0")
	assertContainsCmd(t, cmds, "ip ospf area 0.0.0.0")
	assertContainsCmd(t, cmds, "ip ospf cost 10")
	assertContainsCmd(t, cmds, "ip ospf hello-interval 5")
	assertContainsCmd(t, cmds, "ip ospf dead-interval 20")
	assertContainsCmd(t, cmds, "passive-interface eth0")
}

func TestOSPFEnableInterfaceDefaultTimers(t *testing.T) {
	client, mocks := setupVTYTest(t, "ospfd")
	ctx := context.Background()

	err := client.EnableOSPFInterface(ctx, "eth1", "0.0.0.1", false, 0, 0, 0)
	if err != nil {
		t.Fatalf("EnableOSPFInterface error: %v", err)
	}

	cmds := mocks["ospfd"].getCommands()
	assertContainsCmd(t, cmds, "interface eth1")
	assertContainsCmd(t, cmds, "ip ospf area 0.0.0.1")

	// Cost, hello, dead should not be present when 0.
	for _, cmd := range cmds {
		if strings.Contains(cmd, "cost") || strings.Contains(cmd, "hello-interval") || strings.Contains(cmd, "dead-interval") {
			t.Errorf("unexpected timer command: %s", cmd)
		}
	}

	// Passive should not be present when false.
	for _, cmd := range cmds {
		if strings.Contains(cmd, "passive") {
			t.Errorf("unexpected passive command: %s", cmd)
		}
	}
}

func TestOSPFDisableInterface(t *testing.T) {
	client, mocks := setupVTYTest(t, "ospfd")
	ctx := context.Background()

	err := client.DisableOSPFInterface(ctx, "eth0", "0.0.0.0")
	if err != nil {
		t.Fatalf("DisableOSPFInterface error: %v", err)
	}

	cmds := mocks["ospfd"].getCommands()
	assertContainsCmd(t, cmds, "no ip ospf area 0.0.0.0")
}

// --- AFI resolution tests ---

func TestResolveAFICLI(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"ipv4", "ipv4 unicast"},
		{"ipv4-unicast", "ipv4 unicast"},
		{"ipv6", "ipv6 unicast"},
		{"ipv6-unicast", "ipv6 unicast"},
		{"custom-afi", "custom-afi"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := resolveAFICLI(tt.input)
			if result != tt.expected {
				t.Errorf("resolveAFICLI(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// --- Connection error tests ---

func TestRunConfigBadSocket(t *testing.T) {
	client := NewClient(t.TempDir(), nil)
	err := client.runConfig("bgpd", []string{"router bgp 65000"})
	if err == nil {
		t.Error("expected error connecting to nonexistent socket")
	}
}

func TestRunShowBadSocket(t *testing.T) {
	client := NewClient(t.TempDir(), nil)
	_, err := client.runShow("zebra", "show version")
	if err == nil {
		t.Error("expected error connecting to nonexistent socket")
	}
}

// --- Helpers ---

func assertContainsCmd(t *testing.T, cmds []string, expected string) {
	t.Helper()
	if !slices.Contains(cmds, expected) {
		t.Errorf("command %q not found in: %s", expected, fmt.Sprintf("%v", cmds))
	}
}
