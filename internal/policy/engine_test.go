package policy

import (
	"testing"

	"go.uber.org/zap"
)

// testLogger returns a no-op logger for tests.
func testLogger() *zap.Logger {
	return zap.NewNop()
}

// testConfig returns a standard test configuration with multiple owners.
func testConfig() Config {
	return Config{
		Owners: map[string]OwnerConfig{
			"tenant-a": {
				Token: "token-aaa",
				AllowedPrefixes: PrefixPolicy{
					Type: PrefixTypeHostOnly,
				},
			},
			"tenant-b": {
				Token: "token-bbb",
				AllowedPrefixes: PrefixPolicy{
					Type: PrefixTypeSubnet,
				},
			},
			"tenant-c": {
				Token: "token-ccc",
				AllowedPrefixes: PrefixPolicy{
					Type: PrefixTypeAny,
				},
			},
			"tenant-cidr": {
				Token: "token-cidr",
				AllowedPrefixes: PrefixPolicy{
					Type:         PrefixTypeAny,
					AllowedCIDRs: []string{"10.0.0.0/8", "192.168.0.0/16"},
				},
			},
			"admin": {
				Token: "admin-token",
				AllowedPrefixes: PrefixPolicy{
					Type: PrefixTypeAny,
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Token validation tests
// ---------------------------------------------------------------------------

func TestValidateToken_Valid(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidateToken("tenant-a", "token-aaa"); err != nil {
		t.Fatalf("expected valid token to pass, got error: %v", err)
	}
}

func TestValidateToken_Invalid(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidateToken("tenant-a", "wrong-token"); err == nil {
		t.Fatal("expected invalid token to be rejected")
	}
}

func TestValidateToken_UnknownOwner(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidateToken("unknown-tenant", "some-token"); err == nil {
		t.Fatal("expected unknown owner to be rejected")
	}
}

// ---------------------------------------------------------------------------
// Host-only policy tests
// ---------------------------------------------------------------------------

func TestValidatePrefix_HostOnly_AllowsIPv4Host(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidatePrefix("tenant-a", "10.0.0.1/32"); err != nil {
		t.Fatalf("expected /32 to be allowed under host_only policy, got: %v", err)
	}
}

func TestValidatePrefix_HostOnly_AllowsIPv6Host(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidatePrefix("tenant-a", "fd00::1/128"); err != nil {
		t.Fatalf("expected /128 to be allowed under host_only policy, got: %v", err)
	}
}

func TestValidatePrefix_HostOnly_RejectsIPv4Subnet(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidatePrefix("tenant-a", "10.0.0.0/24"); err == nil {
		t.Fatal("expected /24 to be rejected under host_only policy")
	}
}

func TestValidatePrefix_HostOnly_RejectsIPv6Subnet(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidatePrefix("tenant-a", "fd00::/64"); err == nil {
		t.Fatal("expected /64 to be rejected under host_only policy")
	}
}

// ---------------------------------------------------------------------------
// Subnet policy tests
// ---------------------------------------------------------------------------

func TestValidatePrefix_Subnet_AllowsIPv4Subnet(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidatePrefix("tenant-b", "10.0.0.0/24"); err != nil {
		t.Fatalf("expected /24 to be allowed under subnet policy, got: %v", err)
	}
}

func TestValidatePrefix_Subnet_AllowsIPv4_Slash8(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidatePrefix("tenant-b", "10.0.0.0/8"); err != nil {
		t.Fatalf("expected /8 to be allowed under subnet policy, got: %v", err)
	}
}

func TestValidatePrefix_Subnet_AllowsIPv4_Slash28(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidatePrefix("tenant-b", "10.0.0.0/28"); err != nil {
		t.Fatalf("expected /28 to be allowed under subnet policy, got: %v", err)
	}
}

func TestValidatePrefix_Subnet_RejectsIPv4Host(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidatePrefix("tenant-b", "10.0.0.1/32"); err == nil {
		t.Fatal("expected /32 to be rejected under subnet policy")
	}
}

func TestValidatePrefix_Subnet_RejectsIPv4TooSmall(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidatePrefix("tenant-b", "10.0.0.0/4"); err == nil {
		t.Fatal("expected /4 to be rejected under subnet policy")
	}
}

func TestValidatePrefix_Subnet_RejectsIPv6Host(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidatePrefix("tenant-b", "fd00::1/128"); err == nil {
		t.Fatal("expected IPv6 /128 to be rejected under subnet policy")
	}
}

func TestValidatePrefix_Subnet_AllowsIPv6Subnet(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidatePrefix("tenant-b", "fd00::/48"); err != nil {
		t.Fatalf("expected IPv6 /48 to be allowed under subnet policy, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Any policy tests
// ---------------------------------------------------------------------------

func TestValidatePrefix_Any_AllowsHost(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidatePrefix("tenant-c", "10.0.0.1/32"); err != nil {
		t.Fatalf("expected /32 to be allowed under any policy, got: %v", err)
	}
}

func TestValidatePrefix_Any_AllowsSubnet(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidatePrefix("tenant-c", "10.0.0.0/8"); err != nil {
		t.Fatalf("expected /8 to be allowed under any policy, got: %v", err)
	}
}

func TestValidatePrefix_Any_AllowsIPv6(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidatePrefix("tenant-c", "fd00::/32"); err != nil {
		t.Fatalf("expected IPv6 /32 to be allowed under any policy, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// AllowedCIDRs filtering tests
// ---------------------------------------------------------------------------

func TestValidatePrefix_AllowedCIDRs_WithinRange(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	// 10.1.0.0/16 is within 10.0.0.0/8
	if err := e.ValidatePrefix("tenant-cidr", "10.1.0.0/16"); err != nil {
		t.Fatalf("expected prefix within allowed CIDR to pass, got: %v", err)
	}
}

func TestValidatePrefix_AllowedCIDRs_ExactMatch(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	// 192.168.0.0/16 exactly matches an allowed CIDR
	if err := e.ValidatePrefix("tenant-cidr", "192.168.0.0/16"); err != nil {
		t.Fatalf("expected exact CIDR match to pass, got: %v", err)
	}
}

func TestValidatePrefix_AllowedCIDRs_SubnetOfAllowed(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	// 192.168.1.0/24 is within 192.168.0.0/16
	if err := e.ValidatePrefix("tenant-cidr", "192.168.1.0/24"); err != nil {
		t.Fatalf("expected subnet of allowed CIDR to pass, got: %v", err)
	}
}

func TestValidatePrefix_AllowedCIDRs_OutsideRange(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	// 172.16.0.0/12 is not within 10.0.0.0/8 or 192.168.0.0/16
	if err := e.ValidatePrefix("tenant-cidr", "172.16.0.0/12"); err == nil {
		t.Fatal("expected prefix outside all allowed CIDRs to be rejected")
	}
}

func TestValidatePrefix_AllowedCIDRs_LargerThanAllowed(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	// 10.0.0.0/4 is larger than 10.0.0.0/8 (less specific), should be rejected
	if err := e.ValidatePrefix("tenant-cidr", "10.0.0.0/4"); err == nil {
		t.Fatal("expected prefix larger than allowed CIDR to be rejected")
	}
}

func TestValidatePrefix_AllowedCIDRs_HostWithinRange(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	// 10.0.0.1/32 host route within 10.0.0.0/8
	if err := e.ValidatePrefix("tenant-cidr", "10.0.0.1/32"); err != nil {
		t.Fatalf("expected host route within allowed CIDR to pass, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Unknown owner and invalid prefix tests
// ---------------------------------------------------------------------------

func TestValidatePrefix_UnknownOwner(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidatePrefix("nobody", "10.0.0.1/32"); err == nil {
		t.Fatal("expected unknown owner to be rejected")
	}
}

func TestValidatePrefix_InvalidCIDR(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidatePrefix("tenant-c", "not-a-cidr"); err == nil {
		t.Fatal("expected invalid CIDR to be rejected")
	}
}

// ---------------------------------------------------------------------------
// Conflict detection tests
// ---------------------------------------------------------------------------

func TestCheckConflict_NoExistingOwner(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.CheckConflict("tenant-a", "10.0.0.1/32", "bgp", ""); err != nil {
		t.Fatalf("expected no conflict with empty existing owner, got: %v", err)
	}
}

func TestCheckConflict_SameOwner(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.CheckConflict("tenant-a", "10.0.0.1/32", "bgp", "tenant-a"); err != nil {
		t.Fatalf("expected no conflict when same owner, got: %v", err)
	}
}

func TestCheckConflict_DifferentOwner(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	err := e.CheckConflict("tenant-b", "10.0.0.1/32", "bgp", "tenant-a")
	if err == nil {
		t.Fatal("expected conflict when different owner already has prefix+protocol")
	}
}

func TestCheckConflict_AdminOverride(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.CheckConflict("admin", "10.0.0.1/32", "bgp", "tenant-a"); err != nil {
		t.Fatalf("expected admin to override conflict, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Peer / BFD / OSPF operation tests
// ---------------------------------------------------------------------------

func TestValidatePeerOperation_KnownOwner(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidatePeerOperation("tenant-a"); err != nil {
		t.Fatalf("expected known owner to be allowed peer ops, got: %v", err)
	}
}

func TestValidatePeerOperation_UnknownOwner(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidatePeerOperation("nobody"); err == nil {
		t.Fatal("expected unknown owner to be rejected for peer ops")
	}
}

func TestValidateBFDOperation_KnownOwner(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidateBFDOperation("tenant-b"); err != nil {
		t.Fatalf("expected known owner to be allowed BFD ops, got: %v", err)
	}
}

func TestValidateBFDOperation_UnknownOwner(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidateBFDOperation("nobody"); err == nil {
		t.Fatal("expected unknown owner to be rejected for BFD ops")
	}
}

func TestValidateOSPFOperation_KnownOwner(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidateOSPFOperation("tenant-c"); err != nil {
		t.Fatalf("expected known owner to be allowed OSPF ops, got: %v", err)
	}
}

func TestValidateOSPFOperation_UnknownOwner(t *testing.T) {
	e := NewEngine(testConfig(), testLogger())

	if err := e.ValidateOSPFOperation("nobody"); err == nil {
		t.Fatal("expected unknown owner to be rejected for OSPF ops")
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestNewEngine_NilOwners(t *testing.T) {
	cfg := Config{Owners: nil}
	e := NewEngine(cfg, testLogger())

	// Should not panic; unknown owner should be rejected.
	if err := e.ValidateToken("anyone", "token"); err == nil {
		t.Fatal("expected error for nil owners map")
	}
}

func TestValidatePrefix_UnknownPolicyType(t *testing.T) {
	cfg := Config{
		Owners: map[string]OwnerConfig{
			"bad": {
				Token: "t",
				AllowedPrefixes: PrefixPolicy{
					Type: "invalid_type",
				},
			},
		},
	}
	e := NewEngine(cfg, testLogger())

	if err := e.ValidatePrefix("bad", "10.0.0.0/24"); err == nil {
		t.Fatal("expected error for unknown prefix policy type")
	}
}

func TestCheckConflict_DifferentProtocols(t *testing.T) {
	// This test verifies the conflict is only about what CheckConflict is told.
	// The caller is responsible for passing the correct existing owner per
	// prefix+protocol combination. If existingOwner is empty, no conflict.
	e := NewEngine(testConfig(), testLogger())

	// tenant-a owns 10.0.0.1/32 via bgp, but tenant-b wants it via ospf.
	// If the caller passes empty existingOwner for ospf, it means no conflict
	// on that specific protocol.
	if err := e.CheckConflict("tenant-b", "10.0.0.1/32", "ospf", ""); err != nil {
		t.Fatalf("expected no conflict for different protocol with no existing owner, got: %v", err)
	}
}
