package frr

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestResolveAFICLI_BGP(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"ipv4-unicast", "ipv4 unicast"},
		{"ipv4", "ipv4 unicast"},
		{"ipv6-unicast", "ipv6 unicast"},
		{"ipv6", "ipv6 unicast"},
		{"unknown", "unknown"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := resolveAFICLI(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestErrBGPNotConfigured(t *testing.T) {
	assert.Error(t, ErrBGPNotConfigured)
	assert.Equal(t, "BGP local AS not configured", ErrBGPNotConfigured.Error())
}

func TestNeighborConfig(t *testing.T) {
	cfg := &NeighborConfig{
		SourceAddress: "192.168.1.1",
		EBGPMultihop:  5,
		Password:      "secret",
		Description:   "Test neighbor",
	}

	assert.Equal(t, "192.168.1.1", cfg.SourceAddress)
	assert.Equal(t, uint32(5), cfg.EBGPMultihop)
	assert.Equal(t, "secret", cfg.Password)
	assert.Equal(t, "Test neighbor", cfg.Description)
}

func TestNeighborConfig_Empty(t *testing.T) {
	cfg := &NeighborConfig{}

	assert.Equal(t, "", cfg.SourceAddress)
	assert.Equal(t, uint32(0), cfg.EBGPMultihop)
	assert.Equal(t, "", cfg.Password)
	assert.Equal(t, "", cfg.Description)
}

func TestBGPGracefulRestartCommands(t *testing.T) {
	commands := bgpGracefulRestartCommands()

	assert.NotEmpty(t, commands)
	assert.Contains(t, commands[0], "bgp graceful-restart")
	assert.Contains(t, commands[1], "bgp graceful-restart restart-time")
	assert.Contains(t, commands[2], "bgp graceful-restart stalepath-time")
}

func TestClient_GetLocalAS(t *testing.T) {
	logger := zap.NewNop()
	client := NewClient("/tmp", logger)

	// Initially returns 0
	localAS := client.GetLocalAS()
	assert.Equal(t, uint32(0), localAS)
}

func TestClient_GetLocalAS_AfterConfig(t *testing.T) {
	client, _ := setupFakeVtysh(t)
	ctx := context.Background()

	// Configure BGP global
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Now GetLocalAS should return the configured AS
	localAS := client.GetLocalAS()
	assert.Equal(t, uint32(65001), localAS)
}

func TestClient_AddNeighbor_NoBGPConfig(t *testing.T) {
	logger := zap.NewNop()
	client := NewClient("/tmp", logger)
	ctx := context.Background()

	// Should fail because BGP is not configured
	err := client.AddNeighbor(ctx, "192.168.1.2", 65002, "external", 10, 30, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "BGP local AS not configured")
}

func TestClient_ConfigureBGPGlobal(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Verify commands were sent
	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "router bgp 65001")
	assertStdinContains(t, stdin, "bgp router-id router-1")
}

func TestClient_ConfigureBGPGlobal_InvalidAS(t *testing.T) {
	client, _ := setupFakeVtysh(t)
	ctx := context.Background()

	// AS 0 should still work (FRR will handle validation)
	err := client.ConfigureBGPGlobal(ctx, 0, "router-1")
	// The fake vtysh always succeeds, so we just verify it doesn't panic
	assert.NoError(t, err)
}

func TestClient_ReconfigureBGPGlobal_SameAS(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	// First configuration
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Clear recorded stdin
	_ = os.Remove(filepath.Join(dir, "stdin"))

	// Same AS - should just update router-id
	err = client.ConfigureBGPGlobal(ctx, 65001, "router-2")
	require.NoError(t, err)

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "router bgp 65001")
	assertStdinContains(t, stdin, "bgp router-id router-2")
}

func TestClient_ReconfigureBGPGlobal_DifferentAS(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	// First configuration
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Clear recorded stdin
	_ = os.Remove(filepath.Join(dir, "stdin"))

	// Different AS - should remove old and create new
	err = client.ReconfigureBGPGlobal(ctx, 65001, 65002, "router-1")
	require.NoError(t, err)

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "no router bgp 65001")
	assertStdinContains(t, stdin, "router bgp 65002")
}

func TestClient_AddNeighbor(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	// Configure BGP first
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Clear recorded stdin
	_ = os.Remove(filepath.Join(dir, "stdin"))

	// Add neighbor
	err = client.AddNeighbor(ctx, "192.168.1.2", 65002, "external", 10, 30, nil)
	require.NoError(t, err)

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "router bgp 65001")
	assertStdinContains(t, stdin, "neighbor 192.168.1.2 remote-as 65002")
	assertStdinContains(t, stdin, "neighbor 192.168.1.2 timers 10 30")
}

func TestClient_AddNeighbor_WithConfig(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	// Configure BGP first
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Clear recorded stdin
	_ = os.Remove(filepath.Join(dir, "stdin"))

	// Add neighbor with config
	cfg := &NeighborConfig{
		SourceAddress: "192.168.1.1",
		EBGPMultihop:  5,
		Password:      "secret123",
		Description:   "Test peer",
	}
	err = client.AddNeighbor(ctx, "192.168.1.2", 65002, "external", 10, 30, cfg)
	require.NoError(t, err)

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "neighbor 192.168.1.2 update-source 192.168.1.1")
	assertStdinContains(t, stdin, "neighbor 192.168.1.2 ebgp-multihop 5")
	assertStdinContains(t, stdin, "neighbor 192.168.1.2 password secret123")
	assertStdinContains(t, stdin, "neighbor 192.168.1.2 description Test peer")
}

func TestClient_AddNeighbor_NoTimers(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	// Configure BGP first
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Clear recorded stdin
	_ = os.Remove(filepath.Join(dir, "stdin"))

	// Add neighbor without timers
	err = client.AddNeighbor(ctx, "192.168.1.2", 65002, "external", 0, 0, nil)
	require.NoError(t, err)

	stdin := readRecordedStdin(t, dir)
	// Timers should not be configured
	assert.NotContains(t, stdin, "timers")
}

func TestClient_RemoveNeighbor(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	// Configure BGP first
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Clear recorded stdin
	_ = os.Remove(filepath.Join(dir, "stdin"))

	// Remove neighbor
	err = client.RemoveNeighbor(ctx, "192.168.1.2")
	require.NoError(t, err)

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "router bgp 65001")
	assertStdinContains(t, stdin, "no neighbor 192.168.1.2")
}

func TestClient_ActivateNeighborAFI(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	// Configure BGP first
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Clear recorded stdin
	_ = os.Remove(filepath.Join(dir, "stdin"))

	// Activate AFI
	err = client.ActivateNeighborAFI(ctx, "192.168.1.2", "ipv4")
	require.NoError(t, err)

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "address-family ipv4 unicast")
	assertStdinContains(t, stdin, "neighbor 192.168.1.2 activate")
	assertStdinContains(t, stdin, "neighbor 192.168.1.2 soft-reconfiguration inbound")
}

func TestClient_ActivateNeighborAFI_IPv6(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	// Configure BGP first
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Clear recorded stdin
	_ = os.Remove(filepath.Join(dir, "stdin"))

	// Activate IPv6 AFI
	err = client.ActivateNeighborAFI(ctx, "2001:db8::2", "ipv6-unicast")
	require.NoError(t, err)

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "address-family ipv6 unicast")
}

func TestClient_AdvertiseNetwork(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	// Configure BGP first
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Clear recorded stdin
	_ = os.Remove(filepath.Join(dir, "stdin"))

	// Advertise network
	err = client.AdvertiseNetwork(ctx, "10.0.0.0/24", "ipv4")
	require.NoError(t, err)

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "address-family ipv4 unicast")
	assertStdinContains(t, stdin, "network 10.0.0.0/24")
}

func TestClient_WithdrawNetwork(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	// Configure BGP first
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Clear recorded stdin
	_ = os.Remove(filepath.Join(dir, "stdin"))

	// Withdraw network
	err = client.WithdrawNetwork(ctx, "10.0.0.0/24", "ipv4")
	require.NoError(t, err)

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "address-family ipv4 unicast")
	assertStdinContains(t, stdin, "no network 10.0.0.0/24")
}

func TestClient_SetNeighborMaxPrefix(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	// Configure BGP first
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Clear recorded stdin
	_ = os.Remove(filepath.Join(dir, "stdin"))

	// Set max prefix
	err = client.SetNeighborMaxPrefix(ctx, "192.168.1.2", 1000, false, "ipv4")
	require.NoError(t, err)

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "neighbor 192.168.1.2 maximum-prefix 1000")
	assert.NotContains(t, stdin, "warning-only")
}

func TestClient_SetNeighborMaxPrefix_WarningOnly(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	// Configure BGP first
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Clear recorded stdin
	_ = os.Remove(filepath.Join(dir, "stdin"))

	// Set max prefix with warning only
	err = client.SetNeighborMaxPrefix(ctx, "192.168.1.2", 1000, true, "ipv4")
	require.NoError(t, err)

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "neighbor 192.168.1.2 maximum-prefix 1000 warning-only")
}

func TestClient_ConfigureRouteMap(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	// Configure BGP first
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Clear recorded stdin
	_ = os.Remove(filepath.Join(dir, "stdin"))

	// Configure route-map
	setCmds := []string{"set local-preference 200", "set community 65001:100"}
	err = client.ConfigureRouteMap(ctx, "test-map", setCmds)
	require.NoError(t, err)

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "no route-map test-map")
	assertStdinContains(t, stdin, "route-map test-map permit 10")
	assertStdinContains(t, stdin, "set local-preference 200")
	assertStdinContains(t, stdin, "set community 65001:100")
	assertStdinContains(t, stdin, "exit")
}

func TestClient_ConfigureRouteMap_Empty(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	// Configure BGP first
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Clear recorded stdin
	_ = os.Remove(filepath.Join(dir, "stdin"))

	// Configure route-map with no set commands
	err = client.ConfigureRouteMap(ctx, "empty-map", nil)
	require.NoError(t, err)

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "no route-map empty-map")
	assertStdinContains(t, stdin, "route-map empty-map permit 10")
}

func TestClient_AdvertiseNetworkWithRouteMap(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	// Configure BGP first
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Clear recorded stdin
	_ = os.Remove(filepath.Join(dir, "stdin"))

	// Advertise network with route-map
	err = client.AdvertiseNetworkWithRouteMap(ctx, "10.0.0.0/24", "ipv4", "test-map")
	require.NoError(t, err)

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "address-family ipv4 unicast")
	assertStdinContains(t, stdin, "network 10.0.0.0/24 route-map test-map")
}

func TestClient_RemoveRouteMap(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	// Configure BGP first
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Clear recorded stdin
	_ = os.Remove(filepath.Join(dir, "stdin"))

	// Remove route-map
	err = client.RemoveRouteMap(ctx, "test-map")
	require.NoError(t, err)

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "no route-map test-map")
}

func TestClient_SetNeighborBFD_Enable(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	// Configure BGP first
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Clear recorded stdin
	_ = os.Remove(filepath.Join(dir, "stdin"))

	// Enable BFD
	err = client.SetNeighborBFD(ctx, "192.168.1.2", true)
	require.NoError(t, err)

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "neighbor 192.168.1.2 bfd")
}

func TestClient_SetNeighborBFD_Disable(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	ctx := context.Background()

	// Configure BGP first
	err := client.ConfigureBGPGlobal(ctx, 65001, "router-1")
	require.NoError(t, err)

	// Clear recorded stdin
	_ = os.Remove(filepath.Join(dir, "stdin"))

	// Disable BFD
	err = client.SetNeighborBFD(ctx, "192.168.1.2", false)
	require.NoError(t, err)

	stdin := readRecordedStdin(t, dir)
	assertStdinContains(t, stdin, "no neighbor 192.168.1.2 bfd")
}

// Benchmark tests
func BenchmarkResolveAFICLI(b *testing.B) {
	inputs := []string{"ipv4", "ipv6", "ipv4-unicast", "ipv6-unicast"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resolveAFICLI(inputs[i%len(inputs)])
	}
}
