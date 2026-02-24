package reconciler

import (
	"testing"

	v1 "github.com/piwi3910/NovaRoute/api/v1"
	"github.com/piwi3910/NovaRoute/internal/intent"
	"go.uber.org/zap"
)

func TestNewReconciler(t *testing.T) {
	logger := zap.NewNop()
	store := intent.NewStore(logger)

	r := NewReconciler(store, nil, logger, nil)
	if r == nil {
		t.Fatal("expected non-nil Reconciler")
	}
	if r.intentStore != store {
		t.Error("intent store not set correctly")
	}
	if r.frrClient != nil {
		t.Error("expected nil frrClient")
	}
	if r.appliedPeers == nil {
		t.Error("appliedPeers map not initialized")
	}
	if r.appliedPrefixes == nil {
		t.Error("appliedPrefixes map not initialized")
	}
	if r.appliedBFD == nil {
		t.Error("appliedBFD map not initialized")
	}
	if r.appliedOSPF == nil {
		t.Error("appliedOSPF map not initialized")
	}
	if r.triggerCh == nil {
		t.Error("triggerCh not initialized")
	}
}

func TestNewReconcilerNilLogger(t *testing.T) {
	store := intent.NewStore(nil)

	r := NewReconciler(store, nil, nil, nil)
	if r == nil {
		t.Fatal("expected non-nil Reconciler")
	}
	if r.logger == nil {
		t.Error("expected non-nil logger (should default to nop)")
	}
}

func TestPeerKey(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{"10.0.0.1", "peer:10.0.0.1"},
		{"192.168.1.100", "peer:192.168.1.100"},
		{"2001:db8::1", "peer:2001:db8::1"},
	}

	for _, tt := range tests {
		got := peerKey(tt.addr)
		if got != tt.want {
			t.Errorf("peerKey(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}

func TestPrefixKey(t *testing.T) {
	tests := []struct {
		protocol v1.Protocol
		prefix   string
		want     string
	}{
		{v1.Protocol_PROTOCOL_BGP, "10.0.0.0/24", "prefix:bgp:10.0.0.0/24"},
		{v1.Protocol_PROTOCOL_OSPF, "172.16.0.0/16", "prefix:ospf:172.16.0.0/16"},
		{v1.Protocol_PROTOCOL_BGP, "2001:db8::/32", "prefix:bgp:2001:db8::/32"},
		{v1.Protocol_PROTOCOL_UNSPECIFIED, "10.0.0.0/8", "prefix:unknown:10.0.0.0/8"},
	}

	for _, tt := range tests {
		got := prefixKey(tt.protocol, tt.prefix)
		if got != tt.want {
			t.Errorf("prefixKey(%v, %q) = %q, want %q", tt.protocol, tt.prefix, got, tt.want)
		}
	}
}

func TestBFDKey(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{"10.0.0.1", "bfd:10.0.0.1"},
		{"2001:db8::1", "bfd:2001:db8::1"},
	}

	for _, tt := range tests {
		got := bfdKey(tt.addr)
		if got != tt.want {
			t.Errorf("bfdKey(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}

func TestOSPFKey(t *testing.T) {
	tests := []struct {
		iface string
		want  string
	}{
		{"eth0", "ospf:eth0"},
		{"ens192", "ospf:ens192"},
	}

	for _, tt := range tests {
		got := ospfKey(tt.iface)
		if got != tt.want {
			t.Errorf("ospfKey(%q) = %q, want %q", tt.iface, got, tt.want)
		}
	}
}

func TestKeyConsistency(t *testing.T) {
	// Verify that generating the same key twice yields the same result.
	addr := "10.0.0.1"
	k1 := peerKey(addr)
	k2 := peerKey(addr)
	if k1 != k2 {
		t.Errorf("peerKey not consistent: %q != %q", k1, k2)
	}

	prefix := "10.0.0.0/24"
	pk1 := prefixKey(v1.Protocol_PROTOCOL_BGP, prefix)
	pk2 := prefixKey(v1.Protocol_PROTOCOL_BGP, prefix)
	if pk1 != pk2 {
		t.Errorf("prefixKey not consistent: %q != %q", pk1, pk2)
	}

	bk1 := bfdKey(addr)
	bk2 := bfdKey(addr)
	if bk1 != bk2 {
		t.Errorf("bfdKey not consistent: %q != %q", bk1, bk2)
	}

	iface := "eth0"
	ok1 := ospfKey(iface)
	ok2 := ospfKey(iface)
	if ok1 != ok2 {
		t.Errorf("ospfKey not consistent: %q != %q", ok1, ok2)
	}
}

func TestResolvePeerType(t *testing.T) {
	tests := []struct {
		pt   v1.PeerType
		want string
	}{
		{v1.PeerType_PEER_TYPE_INTERNAL, "internal"},
		{v1.PeerType_PEER_TYPE_EXTERNAL, "external"},
		{v1.PeerType_PEER_TYPE_UNSPECIFIED, "external"},
	}

	for _, tt := range tests {
		got := resolvePeerType(tt.pt)
		if got != tt.want {
			t.Errorf("resolvePeerType(%v) = %q, want %q", tt.pt, got, tt.want)
		}
	}
}

func TestResolveAddressFamily(t *testing.T) {
	tests := []struct {
		af   v1.AddressFamily
		want string
	}{
		{v1.AddressFamily_ADDRESS_FAMILY_IPV4_UNICAST, "ipv4-unicast"},
		{v1.AddressFamily_ADDRESS_FAMILY_IPV6_UNICAST, "ipv6-unicast"},
		{v1.AddressFamily_ADDRESS_FAMILY_UNSPECIFIED, ""},
	}

	for _, tt := range tests {
		got := resolveAddressFamily(tt.af)
		if got != tt.want {
			t.Errorf("resolveAddressFamily(%v) = %q, want %q", tt.af, got, tt.want)
		}
	}
}

func TestProtocolString(t *testing.T) {
	tests := []struct {
		p    v1.Protocol
		want string
	}{
		{v1.Protocol_PROTOCOL_BGP, "bgp"},
		{v1.Protocol_PROTOCOL_OSPF, "ospf"},
		{v1.Protocol_PROTOCOL_UNSPECIFIED, "unknown"},
	}

	for _, tt := range tests {
		got := protocolString(tt.p)
		if got != tt.want {
			t.Errorf("protocolString(%v) = %q, want %q", tt.p, got, tt.want)
		}
	}
}

func TestDetectAFI(t *testing.T) {
	tests := []struct {
		prefix string
		want   string
	}{
		{"10.0.0.0/24", "ipv4-unicast"},
		{"192.168.1.0/24", "ipv4-unicast"},
		{"2001:db8::/32", "ipv6-unicast"},
		{"::1/128", "ipv6-unicast"},
	}

	for _, tt := range tests {
		got := detectAFI(tt.prefix)
		if got != tt.want {
			t.Errorf("detectAFI(%q) = %q, want %q", tt.prefix, got, tt.want)
		}
	}
}

func TestPeerEqual(t *testing.T) {
	base := &intent.PeerIntent{
		NeighborAddress: "10.0.0.1",
		RemoteAS:        65001,
		PeerType:        v1.PeerType_PEER_TYPE_EXTERNAL,
		Keepalive:       30,
		HoldTime:        90,
		AddressFamilies: []v1.AddressFamily{v1.AddressFamily_ADDRESS_FAMILY_IPV4_UNICAST},
	}

	same := &intent.PeerIntent{
		NeighborAddress: "10.0.0.1",
		RemoteAS:        65001,
		PeerType:        v1.PeerType_PEER_TYPE_EXTERNAL,
		Keepalive:       30,
		HoldTime:        90,
		AddressFamilies: []v1.AddressFamily{v1.AddressFamily_ADDRESS_FAMILY_IPV4_UNICAST},
	}

	if !peerEqual(base, same) {
		t.Error("expected equal peers to be equal")
	}

	diffAS := &intent.PeerIntent{
		NeighborAddress: "10.0.0.1",
		RemoteAS:        65002,
		PeerType:        v1.PeerType_PEER_TYPE_EXTERNAL,
		Keepalive:       30,
		HoldTime:        90,
		AddressFamilies: []v1.AddressFamily{v1.AddressFamily_ADDRESS_FAMILY_IPV4_UNICAST},
	}
	if peerEqual(base, diffAS) {
		t.Error("expected peers with different AS to be unequal")
	}

	diffAF := &intent.PeerIntent{
		NeighborAddress: "10.0.0.1",
		RemoteAS:        65001,
		PeerType:        v1.PeerType_PEER_TYPE_EXTERNAL,
		Keepalive:       30,
		HoldTime:        90,
		AddressFamilies: []v1.AddressFamily{
			v1.AddressFamily_ADDRESS_FAMILY_IPV4_UNICAST,
			v1.AddressFamily_ADDRESS_FAMILY_IPV6_UNICAST,
		},
	}
	if peerEqual(base, diffAF) {
		t.Error("expected peers with different address families to be unequal")
	}
}

func TestPrefixEqual(t *testing.T) {
	base := &intent.PrefixIntent{
		Prefix:          "10.0.0.0/24",
		Protocol:        v1.Protocol_PROTOCOL_BGP,
		LocalPreference: 100,
		Communities:     []string{"65001:100"},
	}

	same := &intent.PrefixIntent{
		Prefix:          "10.0.0.0/24",
		Protocol:        v1.Protocol_PROTOCOL_BGP,
		LocalPreference: 100,
		Communities:     []string{"65001:100"},
	}

	if !prefixEqual(base, same) {
		t.Error("expected equal prefixes to be equal")
	}

	diffLP := &intent.PrefixIntent{
		Prefix:          "10.0.0.0/24",
		Protocol:        v1.Protocol_PROTOCOL_BGP,
		LocalPreference: 200,
		Communities:     []string{"65001:100"},
	}
	if prefixEqual(base, diffLP) {
		t.Error("expected prefixes with different local preference to be unequal")
	}
}

func TestBFDEqual(t *testing.T) {
	base := &intent.BFDIntent{
		PeerAddress:      "10.0.0.1",
		MinRxMs:          300,
		MinTxMs:          300,
		DetectMultiplier: 3,
		InterfaceName:    "eth0",
	}

	same := &intent.BFDIntent{
		PeerAddress:      "10.0.0.1",
		MinRxMs:          300,
		MinTxMs:          300,
		DetectMultiplier: 3,
		InterfaceName:    "eth0",
	}

	if !bfdEqual(base, same) {
		t.Error("expected equal BFD intents to be equal")
	}

	diff := &intent.BFDIntent{
		PeerAddress:      "10.0.0.1",
		MinRxMs:          500,
		MinTxMs:          300,
		DetectMultiplier: 3,
		InterfaceName:    "eth0",
	}
	if bfdEqual(base, diff) {
		t.Error("expected BFD intents with different MinRxMs to be unequal")
	}
}

func TestOSPFEqual(t *testing.T) {
	base := &intent.OSPFIntent{
		InterfaceName: "eth0",
		AreaID:        "0.0.0.0",
		Passive:       false,
		Cost:          10,
		HelloInterval: 10,
		DeadInterval:  40,
	}

	same := &intent.OSPFIntent{
		InterfaceName: "eth0",
		AreaID:        "0.0.0.0",
		Passive:       false,
		Cost:          10,
		HelloInterval: 10,
		DeadInterval:  40,
	}

	if !ospfEqual(base, same) {
		t.Error("expected equal OSPF intents to be equal")
	}

	diff := &intent.OSPFIntent{
		InterfaceName: "eth0",
		AreaID:        "0.0.0.1",
		Passive:       false,
		Cost:          10,
		HelloInterval: 10,
		DeadInterval:  40,
	}
	if ospfEqual(base, diff) {
		t.Error("expected OSPF intents with different AreaID to be unequal")
	}
}

func TestTriggerReconcile(t *testing.T) {
	logger := zap.NewNop()
	store := intent.NewStore(logger)
	r := NewReconciler(store, nil, logger, nil)

	// First trigger should succeed (buffered channel of size 1).
	r.TriggerReconcile()

	// Second trigger should not block (coalesced).
	r.TriggerReconcile()

	// Drain the channel.
	select {
	case <-r.triggerCh:
		// expected
	default:
		t.Error("expected trigger signal on channel")
	}

	// Channel should now be empty.
	select {
	case <-r.triggerCh:
		t.Error("expected empty channel after drain")
	default:
		// expected
	}
}

func TestApplyIntentUnknownType(t *testing.T) {
	logger := zap.NewNop()
	store := intent.NewStore(logger)
	r := NewReconciler(store, nil, logger, nil)

	err := r.ApplyIntent(nil, "unknown", nil)
	if err == nil {
		t.Error("expected error for unknown intent type")
	}
}

func TestApplyIntentWrongType(t *testing.T) {
	logger := zap.NewNop()
	store := intent.NewStore(logger)
	r := NewReconciler(store, nil, logger, nil)

	// Pass a string instead of *intent.PeerIntent.
	err := r.ApplyIntent(nil, "peer", "not-a-peer")
	if err == nil {
		t.Error("expected error for wrong value type")
	}

	err = r.ApplyIntent(nil, "prefix", "not-a-prefix")
	if err == nil {
		t.Error("expected error for wrong value type")
	}

	err = r.ApplyIntent(nil, "bfd", "not-a-bfd")
	if err == nil {
		t.Error("expected error for wrong value type")
	}

	err = r.ApplyIntent(nil, "ospf", "not-an-ospf")
	if err == nil {
		t.Error("expected error for wrong value type")
	}
}

func TestRemoveIntentUnknownType(t *testing.T) {
	logger := zap.NewNop()
	store := intent.NewStore(logger)
	r := NewReconciler(store, nil, logger, nil)

	err := r.RemoveIntent(nil, "unknown", "key")
	if err == nil {
		t.Error("expected error for unknown intent type")
	}
}

func TestRemoveIntentNotFound(t *testing.T) {
	logger := zap.NewNop()
	store := intent.NewStore(logger)
	r := NewReconciler(store, nil, logger, nil)

	err := r.RemoveIntent(nil, "peer", "10.0.0.99")
	if err == nil {
		t.Error("expected error when removing non-existent peer")
	}

	err = r.RemoveIntent(nil, "prefix", "bgp:10.0.0.0/24")
	if err == nil {
		t.Error("expected error when removing non-existent prefix")
	}

	err = r.RemoveIntent(nil, "bfd", "10.0.0.99")
	if err == nil {
		t.Error("expected error when removing non-existent BFD")
	}

	err = r.RemoveIntent(nil, "ospf", "eth99")
	if err == nil {
		t.Error("expected error when removing non-existent OSPF")
	}
}
