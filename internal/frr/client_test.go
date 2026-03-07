package frr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupFakeVtysh creates a fake vtysh script that records commands to a file.
// For stdin mode (config): writes stdin content to <dir>/stdin.
// For -c mode (show): writes the command arg to <dir>/show_cmd.
// The script echoes the content of <dir>/response if it exists.
func setupFakeVtysh(t *testing.T) (*Client, string) {
	t.Helper()

	dir := t.TempDir()

	script := filepath.Join(dir, "vtysh")
	content := `#!/bin/sh
RECORD_DIR="` + dir + `"

# Parse args: look for -c (show command mode)
SHOW_CMD=""
while [ $# -gt 0 ]; do
  case "$1" in
    -c) shift; SHOW_CMD="$1"; shift ;;
    *) shift ;;
  esac
done

if [ -n "$SHOW_CMD" ]; then
  echo "$SHOW_CMD" >> "$RECORD_DIR/show_cmd"
  if [ -f "$RECORD_DIR/response" ]; then
    cat "$RECORD_DIR/response"
  fi
else
  cat >> "$RECORD_DIR/stdin"
  if [ -f "$RECORD_DIR/response" ]; then
    cat "$RECORD_DIR/response"
  fi
fi
exit 0
`
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil { //nolint:gosec // Test script needs execute permission
		t.Fatalf("write fake vtysh: %v", err)
	}

	client := NewClient(dir, nil)
	client.vtyshPath = script
	client.timeout = 5e9 // 5 seconds

	return client, dir
}

// readRecordedStdin returns the stdin content sent to fake vtysh.
func readRecordedStdin(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "stdin")) //nolint:gosec // Test file path constructed from temp dir
	if err != nil {
		t.Fatalf("read recorded stdin: %v", err)
	}
	return string(data)
}

// readRecordedShowCmd returns the show command sent to fake vtysh.
func readRecordedShowCmd(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "show_cmd")) //nolint:gosec // Test file path constructed from temp dir
	if err != nil {
		t.Fatalf("read recorded show_cmd: %v", err)
	}
	return strings.TrimSpace(string(data))
}

// setFakeResponse writes a response that the fake vtysh will return.
func setFakeResponse(t *testing.T, dir, response string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "response"), []byte(response), 0o600); err != nil {
		t.Fatalf("write fake response: %v", err)
	}
}

// assertStdinContains checks that the recorded stdin contains the expected command line.
func assertStdinContains(t *testing.T, stdin, expected string) {
	t.Helper()
	for _, line := range strings.Split(stdin, "\n") {
		if strings.TrimSpace(line) == expected {
			return
		}
	}
	t.Errorf("command %q not found in stdin:\n%s", expected, stdin)
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
		f, err := os.Create(filepath.Join(dir, name)) //nolint:gosec // Test file path constructed from temp dir
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
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
	client, dir := setupFakeVtysh(t)
	setFakeResponse(t, dir, "FRRouting 10.5.1 (Mock)\n  running on Linux\n")

	ctx := context.Background()
	version, err := client.GetVersion(ctx)
	if err != nil {
		t.Fatalf("GetVersion error: %v", err)
	}
	if version != "10.5.1 (Mock)" {
		t.Errorf("GetVersion = %q, want %q", version, "10.5.1 (Mock)")
	}

	cmd := readRecordedShowCmd(t, dir)
	if cmd != "show version" {
		t.Errorf("show command = %q, want %q", cmd, "show version")
	}
}

// --- BGP tests ---

func TestConfigureBGPGlobal(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	err := client.ConfigureBGPGlobal(ctx, 65000, "10.0.0.1")
	if err != nil {
		t.Fatalf("ConfigureBGPGlobal error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "configure terminal")
	assertStdinContains(t, stdin, "router bgp 65000")
	assertStdinContains(t, stdin, "bgp router-id 10.0.0.1")
	assertStdinContains(t, stdin, "end")

	if client.localAS != 65000 {
		t.Errorf("localAS = %d, want 65000", client.localAS)
	}
}

func TestAddNeighbor(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()
	client.localAS = 65000

	err := client.AddNeighbor(ctx, "192.168.1.1", 65001, "external", 30, 90, nil)
	if err != nil {
		t.Fatalf("AddNeighbor error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "router bgp 65000")
	assertStdinContains(t, stdin, "neighbor 192.168.1.1 remote-as 65001")
	assertStdinContains(t, stdin, "neighbor 192.168.1.1 timers 30 90")
}

func TestAddNeighborDefaultTimers(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()
	client.localAS = 65000

	err := client.AddNeighbor(ctx, "10.0.0.2", 65002, "internal", 0, 0, nil)
	if err != nil {
		t.Fatalf("AddNeighbor error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "neighbor 10.0.0.2 remote-as 65002")

	// Timer command should not be present when 0.
	if strings.Contains(stdin, "timers") {
		t.Errorf("unexpected timers command in stdin:\n%s", stdin)
	}
}

func TestAddNeighborWithConfig(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()
	client.localAS = 65000

	cfg := &NeighborConfig{
		SourceAddress: "10.0.0.1",
		EBGPMultihop:  2,
		Password:      "secret",
		Description:   "test peer",
	}
	err := client.AddNeighbor(ctx, "192.168.1.1", 65001, "external", 30, 90, cfg)
	if err != nil {
		t.Fatalf("AddNeighbor error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "neighbor 192.168.1.1 update-source 10.0.0.1")
	assertStdinContains(t, stdin, "neighbor 192.168.1.1 ebgp-multihop 2")
	assertStdinContains(t, stdin, "neighbor 192.168.1.1 password secret")
	assertStdinContains(t, stdin, "neighbor 192.168.1.1 description test peer")
}

func TestRemoveNeighbor(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()
	client.localAS = 65000

	err := client.RemoveNeighbor(ctx, "192.168.1.1")
	if err != nil {
		t.Fatalf("RemoveNeighbor error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "no neighbor 192.168.1.1")
}

func TestActivateNeighborAFI(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()
	client.localAS = 65000

	err := client.ActivateNeighborAFI(ctx, "192.168.1.1", "ipv4-unicast")
	if err != nil {
		t.Fatalf("ActivateNeighborAFI error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "address-family ipv4 unicast")
	assertStdinContains(t, stdin, "neighbor 192.168.1.1 activate")
	assertStdinContains(t, stdin, "exit-address-family")
}

func TestAdvertiseNetwork(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()
	client.localAS = 65000

	err := client.AdvertiseNetwork(ctx, "10.0.0.0/24", "ipv4")
	if err != nil {
		t.Fatalf("AdvertiseNetwork error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "address-family ipv4 unicast")
	assertStdinContains(t, stdin, "network 10.0.0.0/24")
}

func TestWithdrawNetwork(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()
	client.localAS = 65000

	err := client.WithdrawNetwork(ctx, "10.0.0.0/24", "ipv4")
	if err != nil {
		t.Fatalf("WithdrawNetwork error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "no network 10.0.0.0/24")
}

// --- BFD tests ---

func TestAddBFDPeer(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	err := client.AddBFDPeer(ctx, "192.168.1.1", 300, 300, 3, "eth0")
	if err != nil {
		t.Fatalf("AddBFDPeer error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "bfd")
	assertStdinContains(t, stdin, "peer 192.168.1.1 interface eth0")
	assertStdinContains(t, stdin, "receive-interval 300")
	assertStdinContains(t, stdin, "transmit-interval 300")
	assertStdinContains(t, stdin, "detect-multiplier 3")
}

func TestAddBFDPeerNoInterface(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	err := client.AddBFDPeer(ctx, "10.0.0.1", 200, 200, 5, "")
	if err != nil {
		t.Fatalf("AddBFDPeer error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "peer 10.0.0.1")

	// Should not contain "interface" in the peer command.
	for _, line := range strings.Split(stdin, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "peer ") && strings.Contains(line, "interface") {
			t.Errorf("unexpected interface in peer command: %s", line)
		}
	}
}

func TestRemoveBFDPeer(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	err := client.RemoveBFDPeer(ctx, "192.168.1.1", "")
	if err != nil {
		t.Fatalf("RemoveBFDPeer error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "no peer 192.168.1.1")
}

func TestRemoveBFDPeerWithInterface(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	err := client.RemoveBFDPeer(ctx, "192.168.1.1", "eth0")
	if err != nil {
		t.Fatalf("RemoveBFDPeer with interface error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "no peer 192.168.1.1 interface eth0")
}

func TestSetNeighborBFDEnable(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()
	client.localAS = 65000

	err := client.SetNeighborBFD(ctx, "10.0.0.1", true)
	if err != nil {
		t.Fatalf("SetNeighborBFD(true) error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "neighbor 10.0.0.1 bfd")
}

func TestSetNeighborBFDDisable(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()
	client.localAS = 65000

	err := client.SetNeighborBFD(ctx, "10.0.0.1", false)
	if err != nil {
		t.Fatalf("SetNeighborBFD(false) error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "no neighbor 10.0.0.1 bfd")
}

func TestSetNeighborMaxPrefix(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()
	client.localAS = 65000

	err := client.SetNeighborMaxPrefix(ctx, "10.0.0.1", 500, true, "ipv4-unicast")
	if err != nil {
		t.Fatalf("SetNeighborMaxPrefix error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "neighbor 10.0.0.1 maximum-prefix 500 warning-only")
}

func TestConfigureRouteMap(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	setCmds := []string{"set local-preference 200", "set community 65000:100"}
	err := client.ConfigureRouteMap(ctx, "NR-PFX-10-0-0-0-24", setCmds)
	if err != nil {
		t.Fatalf("ConfigureRouteMap error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "route-map NR-PFX-10-0-0-0-24 permit 10")
	assertStdinContains(t, stdin, "set local-preference 200")
	assertStdinContains(t, stdin, "set community 65000:100")
}

func TestAdvertiseNetworkWithRouteMap(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()
	client.localAS = 65000

	err := client.AdvertiseNetworkWithRouteMap(ctx, "10.0.0.0/24", "ipv4-unicast", "NR-PFX-10-0-0-0-24")
	if err != nil {
		t.Fatalf("AdvertiseNetworkWithRouteMap error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "network 10.0.0.0/24 route-map NR-PFX-10-0-0-0-24")
}

// --- OSPF tests ---

func TestOSPFEnableInterface(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	err := client.EnableOSPFInterface(ctx, "eth0", "0.0.0.0", true, 10, 5, 20)
	if err != nil {
		t.Fatalf("EnableOSPFInterface error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "interface eth0")
	assertStdinContains(t, stdin, "ip ospf area 0.0.0.0")
	assertStdinContains(t, stdin, "ip ospf cost 10")
	assertStdinContains(t, stdin, "ip ospf hello-interval 5")
	assertStdinContains(t, stdin, "ip ospf dead-interval 20")
	assertStdinContains(t, stdin, "passive-interface eth0")
}

func TestOSPFEnableInterfaceDefaultTimers(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	err := client.EnableOSPFInterface(ctx, "eth1", "0.0.0.1", false, 0, 0, 0)
	if err != nil {
		t.Fatalf("EnableOSPFInterface error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "interface eth1")
	assertStdinContains(t, stdin, "ip ospf area 0.0.0.1")

	// Cost, hello, dead should not be present when 0.
	for _, kw := range []string{"cost", "hello-interval", "dead-interval"} {
		if strings.Contains(stdin, kw) {
			t.Errorf("unexpected %q in stdin:\n%s", kw, stdin)
		}
	}

	// Passive should not be present when false.
	if strings.Contains(stdin, "passive") {
		t.Errorf("unexpected passive in stdin:\n%s", stdin)
	}
}

func TestOSPFDisableInterface(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	err := client.DisableOSPFInterface(ctx, "eth0", "0.0.0.0", false)
	if err != nil {
		t.Fatalf("DisableOSPFInterface error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "no ip ospf area 0.0.0.0")
}

// --- OSPFv3 (IPv6) tests ---

func TestOSPFv3EnableInterface(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	err := client.EnableOSPFv3Interface(ctx, "eth0", "0.0.0.0", true, 20)
	if err != nil {
		t.Fatalf("EnableOSPFv3Interface error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "interface eth0")
	assertStdinContains(t, stdin, "ipv6 ospf6 area 0.0.0.0")
	assertStdinContains(t, stdin, "ipv6 ospf6 cost 20")
	assertStdinContains(t, stdin, "router ospf6")
	assertStdinContains(t, stdin, "passive-interface eth0")
}

func TestOSPFv3EnableInterfaceNoCostNotPassive(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	err := client.EnableOSPFv3Interface(ctx, "eth1", "0.0.0.1", false, 0)
	if err != nil {
		t.Fatalf("EnableOSPFv3Interface error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "interface eth1")
	assertStdinContains(t, stdin, "ipv6 ospf6 area 0.0.0.1")

	// Cost should not be present when 0.
	if strings.Contains(stdin, "cost") {
		t.Errorf("unexpected cost in stdin:\n%s", stdin)
	}

	// Passive should not be present when false.
	if strings.Contains(stdin, "passive") {
		t.Errorf("unexpected passive in stdin:\n%s", stdin)
	}

	// Should not contain IPv4 OSPF commands.
	if strings.Contains(stdin, "ip ospf") {
		t.Errorf("unexpected 'ip ospf' (IPv4) command in OSPFv3 config:\n%s", stdin)
	}
}

func TestOSPFv3DisableInterface(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	err := client.DisableOSPFv3Interface(ctx, "eth0", "0.0.0.0", false)
	if err != nil {
		t.Fatalf("DisableOSPFv3Interface error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "interface eth0")
	assertStdinContains(t, stdin, "no ipv6 ospf6 area 0.0.0.0")

	// Should not contain IPv4 OSPF commands.
	if strings.Contains(stdin, "no ip ospf") {
		t.Errorf("unexpected 'no ip ospf' (IPv4) command in OSPFv3 config:\n%s", stdin)
	}
}

func TestOSPFv3DisableInterfaceWithPassive(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	err := client.DisableOSPFv3Interface(ctx, "eth0", "0.0.0.0", true)
	if err != nil {
		t.Fatalf("DisableOSPFv3Interface error: %v", err)
	}

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "no ipv6 ospf6 area 0.0.0.0")
	assertStdinContains(t, stdin, "router ospf6")
	assertStdinContains(t, stdin, "no passive-interface eth0")
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

// --- Error handling tests ---

func TestRunConfigVtyshError(t *testing.T) {
	dir := t.TempDir()

	// Create a fake vtysh that returns an error marker.
	script := filepath.Join(dir, "vtysh")
	content := `#!/bin/sh
echo "% Unknown command"
exit 0
`
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil { //nolint:gosec // Test script needs execute permission
		t.Fatal(err)
	}

	client := NewClient(dir, nil)
	client.vtyshPath = script

	err := client.runConfig(context.Background(), []string{"bad command"})
	if err == nil {
		t.Error("expected error for vtysh error output")
	}
	if !strings.Contains(err.Error(), "% Unknown command") {
		t.Errorf("error = %q, want to contain '%%  Unknown command'", err.Error())
	}
}

func TestRunConfigVtyshExitError(t *testing.T) {
	dir := t.TempDir()

	// Create a fake vtysh that exits with non-zero.
	script := filepath.Join(dir, "vtysh")
	content := `#!/bin/sh
echo "connection refused"
exit 1
`
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil { //nolint:gosec // Test script needs execute permission
		t.Fatal(err)
	}

	client := NewClient(dir, nil)
	client.vtyshPath = script

	err := client.runConfig(context.Background(), []string{"router bgp 65000"})
	if err == nil {
		t.Error("expected error for non-zero exit")
	}
}

func TestRunShowVtyshError(t *testing.T) {
	dir := t.TempDir()

	script := filepath.Join(dir, "vtysh")
	content := `#!/bin/sh
echo "vtysh: cannot connect"
exit 2
`
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil { //nolint:gosec // Test script needs execute permission
		t.Fatal(err)
	}

	client := NewClient(dir, nil)
	client.vtyshPath = script

	_, err := client.runShow(context.Background(), "show version")
	if err == nil {
		t.Error("expected error for non-zero exit")
	}
}

func TestRunConfigBadVtyshPath(t *testing.T) {
	client := NewClient(t.TempDir(), nil)
	client.vtyshPath = "/nonexistent/vtysh"

	err := client.runConfig(context.Background(), []string{"router bgp 65000"})
	if err == nil {
		t.Error("expected error for nonexistent vtysh binary")
	}
}

func TestRunShowBadVtyshPath(t *testing.T) {
	client := NewClient(t.TempDir(), nil)
	client.vtyshPath = "/nonexistent/vtysh"

	_, err := client.runShow(context.Background(), "show version")
	if err == nil {
		t.Error("expected error for nonexistent vtysh binary")
	}
}
