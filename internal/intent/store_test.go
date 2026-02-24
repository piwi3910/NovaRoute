package intent

import (
	"sync"
	"testing"

	v1 "github.com/piwi3910/NovaRoute/api/v1"
	"go.uber.org/zap"
)

func newTestStore() *Store {
	logger, _ := zap.NewDevelopment()
	return NewStore(logger)
}

// --- Peer intent tests ---

func TestSetAndGetPeerIntent(t *testing.T) {
	s := newTestStore()

	intent := &PeerIntent{
		NeighborAddress: "192.168.1.1",
		RemoteAS:        65001,
		PeerType:        v1.PeerType_PEER_TYPE_EXTERNAL,
		Keepalive:       30,
		HoldTime:        90,
		BFDEnabled:      true,
		Description:     "test peer",
		AddressFamilies: []v1.AddressFamily{v1.AddressFamily_ADDRESS_FAMILY_IPV4_UNICAST},
		SourceAddress:   "10.0.0.1",
		EBGPMultihop:    2,
		Password:        "secret",
	}

	err := s.SetPeerIntent("novaedge", intent)
	if err != nil {
		t.Fatalf("SetPeerIntent failed: %v", err)
	}

	peers := s.GetPeerIntents()
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer intent, got %d", len(peers))
	}

	p := peers[0]
	if p.Owner != "novaedge" {
		t.Errorf("expected owner 'novaedge', got %q", p.Owner)
	}
	if p.NeighborAddress != "192.168.1.1" {
		t.Errorf("expected neighbor '192.168.1.1', got %q", p.NeighborAddress)
	}
	if p.RemoteAS != 65001 {
		t.Errorf("expected remote AS 65001, got %d", p.RemoteAS)
	}
	if p.PeerType != v1.PeerType_PEER_TYPE_EXTERNAL {
		t.Errorf("expected peer type EXTERNAL, got %v", p.PeerType)
	}
	if p.Keepalive != 30 {
		t.Errorf("expected keepalive 30, got %d", p.Keepalive)
	}
	if p.HoldTime != 90 {
		t.Errorf("expected hold time 90, got %d", p.HoldTime)
	}
	if !p.BFDEnabled {
		t.Error("expected BFD enabled")
	}
	if p.Description != "test peer" {
		t.Errorf("expected description 'test peer', got %q", p.Description)
	}
	if len(p.AddressFamilies) != 1 || p.AddressFamilies[0] != v1.AddressFamily_ADDRESS_FAMILY_IPV4_UNICAST {
		t.Errorf("unexpected address families: %v", p.AddressFamilies)
	}
	if p.SourceAddress != "10.0.0.1" {
		t.Errorf("expected source address '10.0.0.1', got %q", p.SourceAddress)
	}
	if p.EBGPMultihop != 2 {
		t.Errorf("expected eBGP multihop 2, got %d", p.EBGPMultihop)
	}
	if p.Password != "secret" {
		t.Errorf("expected password 'secret', got %q", p.Password)
	}
	if p.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
	if p.UpdatedAt.IsZero() {
		t.Error("expected non-zero UpdatedAt")
	}
}

func TestUpdatePeerIntentPreservesCreatedAt(t *testing.T) {
	s := newTestStore()

	err := s.SetPeerIntent("novaedge", &PeerIntent{
		NeighborAddress: "192.168.1.1",
		RemoteAS:        65001,
	})
	if err != nil {
		t.Fatalf("SetPeerIntent failed: %v", err)
	}

	first := s.GetPeerIntents()[0]
	createdAt := first.CreatedAt

	err = s.SetPeerIntent("novaedge", &PeerIntent{
		NeighborAddress: "192.168.1.1",
		RemoteAS:        65002,
	})
	if err != nil {
		t.Fatalf("SetPeerIntent update failed: %v", err)
	}

	updated := s.GetPeerIntents()[0]
	if !updated.CreatedAt.Equal(createdAt) {
		t.Error("CreatedAt should be preserved on update")
	}
	if updated.RemoteAS != 65002 {
		t.Errorf("expected updated remote AS 65002, got %d", updated.RemoteAS)
	}
}

func TestRemovePeerIntent(t *testing.T) {
	s := newTestStore()

	err := s.SetPeerIntent("novaedge", &PeerIntent{
		NeighborAddress: "192.168.1.1",
		RemoteAS:        65001,
	})
	if err != nil {
		t.Fatalf("SetPeerIntent failed: %v", err)
	}

	err = s.RemovePeerIntent("novaedge", "192.168.1.1")
	if err != nil {
		t.Fatalf("RemovePeerIntent failed: %v", err)
	}

	peers := s.GetPeerIntents()
	if len(peers) != 0 {
		t.Fatalf("expected 0 peer intents after removal, got %d", len(peers))
	}
}

func TestRemovePeerIntentNotFound(t *testing.T) {
	s := newTestStore()

	err := s.RemovePeerIntent("novaedge", "192.168.1.1")
	if err == nil {
		t.Fatal("expected error removing non-existent peer intent")
	}
}

func TestRemovePeerIntentWrongOwner(t *testing.T) {
	s := newTestStore()

	err := s.SetPeerIntent("novaedge", &PeerIntent{
		NeighborAddress: "192.168.1.1",
		RemoteAS:        65001,
	})
	if err != nil {
		t.Fatalf("SetPeerIntent failed: %v", err)
	}

	err = s.RemovePeerIntent("novanet", "192.168.1.1")
	if err == nil {
		t.Fatal("expected error removing peer intent belonging to different owner")
	}

	// The original peer should still exist.
	peers := s.GetPeerIntents()
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer intent, got %d", len(peers))
	}
}

func TestSetPeerIntentValidation(t *testing.T) {
	s := newTestStore()

	err := s.SetPeerIntent("", &PeerIntent{NeighborAddress: "1.2.3.4"})
	if err == nil {
		t.Error("expected error for empty owner")
	}

	err = s.SetPeerIntent("novaedge", nil)
	if err == nil {
		t.Error("expected error for nil intent")
	}

	err = s.SetPeerIntent("novaedge", &PeerIntent{NeighborAddress: ""})
	if err == nil {
		t.Error("expected error for empty neighbor address")
	}
}

// --- Prefix intent tests ---

func TestSetAndGetPrefixIntent(t *testing.T) {
	s := newTestStore()

	intent := &PrefixIntent{
		Prefix:          "10.0.0.0/24",
		Protocol:        v1.Protocol_PROTOCOL_BGP,
		LocalPreference: 100,
		Communities:     []string{"65001:100", "65001:200"},
		MED:             50,
		NextHop:         "192.168.1.1",
	}

	err := s.SetPrefixIntent("novaedge", intent)
	if err != nil {
		t.Fatalf("SetPrefixIntent failed: %v", err)
	}

	prefixes := s.GetPrefixIntents()
	if len(prefixes) != 1 {
		t.Fatalf("expected 1 prefix intent, got %d", len(prefixes))
	}

	p := prefixes[0]
	if p.Owner != "novaedge" {
		t.Errorf("expected owner 'novaedge', got %q", p.Owner)
	}
	if p.Prefix != "10.0.0.0/24" {
		t.Errorf("expected prefix '10.0.0.0/24', got %q", p.Prefix)
	}
	if p.Protocol != v1.Protocol_PROTOCOL_BGP {
		t.Errorf("expected protocol BGP, got %v", p.Protocol)
	}
	if p.LocalPreference != 100 {
		t.Errorf("expected local preference 100, got %d", p.LocalPreference)
	}
	if len(p.Communities) != 2 {
		t.Errorf("expected 2 communities, got %d", len(p.Communities))
	}
	if p.MED != 50 {
		t.Errorf("expected MED 50, got %d", p.MED)
	}
	if p.NextHop != "192.168.1.1" {
		t.Errorf("expected next hop '192.168.1.1', got %q", p.NextHop)
	}
	if p.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
}

func TestRemovePrefixIntent(t *testing.T) {
	s := newTestStore()

	err := s.SetPrefixIntent("novaedge", &PrefixIntent{
		Prefix:   "10.0.0.0/24",
		Protocol: v1.Protocol_PROTOCOL_BGP,
	})
	if err != nil {
		t.Fatalf("SetPrefixIntent failed: %v", err)
	}

	err = s.RemovePrefixIntent("novaedge", "10.0.0.0/24", "bgp")
	if err != nil {
		t.Fatalf("RemovePrefixIntent failed: %v", err)
	}

	prefixes := s.GetPrefixIntents()
	if len(prefixes) != 0 {
		t.Fatalf("expected 0 prefix intents after removal, got %d", len(prefixes))
	}
}

func TestRemovePrefixIntentWrongProtocol(t *testing.T) {
	s := newTestStore()

	err := s.SetPrefixIntent("novaedge", &PrefixIntent{
		Prefix:   "10.0.0.0/24",
		Protocol: v1.Protocol_PROTOCOL_BGP,
	})
	if err != nil {
		t.Fatalf("SetPrefixIntent failed: %v", err)
	}

	err = s.RemovePrefixIntent("novaedge", "10.0.0.0/24", "ospf")
	if err == nil {
		t.Fatal("expected error removing prefix with wrong protocol")
	}

	prefixes := s.GetPrefixIntents()
	if len(prefixes) != 1 {
		t.Fatalf("expected 1 prefix intent to remain, got %d", len(prefixes))
	}
}

func TestSamePrefixDifferentProtocols(t *testing.T) {
	s := newTestStore()

	err := s.SetPrefixIntent("novaedge", &PrefixIntent{
		Prefix:   "10.0.0.0/24",
		Protocol: v1.Protocol_PROTOCOL_BGP,
	})
	if err != nil {
		t.Fatalf("SetPrefixIntent BGP failed: %v", err)
	}

	err = s.SetPrefixIntent("novaedge", &PrefixIntent{
		Prefix:   "10.0.0.0/24",
		Protocol: v1.Protocol_PROTOCOL_OSPF,
	})
	if err != nil {
		t.Fatalf("SetPrefixIntent OSPF failed: %v", err)
	}

	prefixes := s.GetPrefixIntents()
	if len(prefixes) != 2 {
		t.Fatalf("expected 2 prefix intents (one per protocol), got %d", len(prefixes))
	}
}

func TestSetPrefixIntentValidation(t *testing.T) {
	s := newTestStore()

	err := s.SetPrefixIntent("", &PrefixIntent{Prefix: "10.0.0.0/24"})
	if err == nil {
		t.Error("expected error for empty owner")
	}

	err = s.SetPrefixIntent("novaedge", nil)
	if err == nil {
		t.Error("expected error for nil intent")
	}

	err = s.SetPrefixIntent("novaedge", &PrefixIntent{Prefix: ""})
	if err == nil {
		t.Error("expected error for empty prefix")
	}
}

// --- BFD intent tests ---

func TestSetAndGetBFDIntent(t *testing.T) {
	s := newTestStore()

	intent := &BFDIntent{
		PeerAddress:      "192.168.1.1",
		MinRxMs:          300,
		MinTxMs:          300,
		DetectMultiplier: 3,
		InterfaceName:    "eth0",
	}

	err := s.SetBFDIntent("novaedge", intent)
	if err != nil {
		t.Fatalf("SetBFDIntent failed: %v", err)
	}

	bfds := s.GetBFDIntents()
	if len(bfds) != 1 {
		t.Fatalf("expected 1 BFD intent, got %d", len(bfds))
	}

	b := bfds[0]
	if b.Owner != "novaedge" {
		t.Errorf("expected owner 'novaedge', got %q", b.Owner)
	}
	if b.PeerAddress != "192.168.1.1" {
		t.Errorf("expected peer '192.168.1.1', got %q", b.PeerAddress)
	}
	if b.MinRxMs != 300 {
		t.Errorf("expected MinRxMs 300, got %d", b.MinRxMs)
	}
	if b.MinTxMs != 300 {
		t.Errorf("expected MinTxMs 300, got %d", b.MinTxMs)
	}
	if b.DetectMultiplier != 3 {
		t.Errorf("expected DetectMultiplier 3, got %d", b.DetectMultiplier)
	}
	if b.InterfaceName != "eth0" {
		t.Errorf("expected interface 'eth0', got %q", b.InterfaceName)
	}
}

func TestRemoveBFDIntent(t *testing.T) {
	s := newTestStore()

	err := s.SetBFDIntent("novaedge", &BFDIntent{
		PeerAddress: "192.168.1.1",
		MinRxMs:     300,
		MinTxMs:     300,
	})
	if err != nil {
		t.Fatalf("SetBFDIntent failed: %v", err)
	}

	err = s.RemoveBFDIntent("novaedge", "192.168.1.1")
	if err != nil {
		t.Fatalf("RemoveBFDIntent failed: %v", err)
	}

	bfds := s.GetBFDIntents()
	if len(bfds) != 0 {
		t.Fatalf("expected 0 BFD intents after removal, got %d", len(bfds))
	}
}

func TestRemoveBFDIntentNotFound(t *testing.T) {
	s := newTestStore()

	err := s.RemoveBFDIntent("novaedge", "192.168.1.1")
	if err == nil {
		t.Fatal("expected error removing non-existent BFD intent")
	}
}

func TestSetBFDIntentValidation(t *testing.T) {
	s := newTestStore()

	err := s.SetBFDIntent("", &BFDIntent{PeerAddress: "1.2.3.4"})
	if err == nil {
		t.Error("expected error for empty owner")
	}

	err = s.SetBFDIntent("novaedge", nil)
	if err == nil {
		t.Error("expected error for nil intent")
	}

	err = s.SetBFDIntent("novaedge", &BFDIntent{PeerAddress: ""})
	if err == nil {
		t.Error("expected error for empty peer address")
	}
}

// --- OSPF intent tests ---

func TestSetAndGetOSPFIntent(t *testing.T) {
	s := newTestStore()

	intent := &OSPFIntent{
		InterfaceName: "eth0",
		AreaID:        "0.0.0.0",
		Passive:       false,
		Cost:          10,
		HelloInterval: 10,
		DeadInterval:  40,
	}

	err := s.SetOSPFIntent("novaedge", intent)
	if err != nil {
		t.Fatalf("SetOSPFIntent failed: %v", err)
	}

	ospfs := s.GetOSPFIntents()
	if len(ospfs) != 1 {
		t.Fatalf("expected 1 OSPF intent, got %d", len(ospfs))
	}

	o := ospfs[0]
	if o.Owner != "novaedge" {
		t.Errorf("expected owner 'novaedge', got %q", o.Owner)
	}
	if o.InterfaceName != "eth0" {
		t.Errorf("expected interface 'eth0', got %q", o.InterfaceName)
	}
	if o.AreaID != "0.0.0.0" {
		t.Errorf("expected area '0.0.0.0', got %q", o.AreaID)
	}
	if o.Passive {
		t.Error("expected passive=false")
	}
	if o.Cost != 10 {
		t.Errorf("expected cost 10, got %d", o.Cost)
	}
	if o.HelloInterval != 10 {
		t.Errorf("expected hello interval 10, got %d", o.HelloInterval)
	}
	if o.DeadInterval != 40 {
		t.Errorf("expected dead interval 40, got %d", o.DeadInterval)
	}
}

func TestRemoveOSPFIntent(t *testing.T) {
	s := newTestStore()

	err := s.SetOSPFIntent("novaedge", &OSPFIntent{
		InterfaceName: "eth0",
		AreaID:        "0.0.0.0",
	})
	if err != nil {
		t.Fatalf("SetOSPFIntent failed: %v", err)
	}

	err = s.RemoveOSPFIntent("novaedge", "eth0")
	if err != nil {
		t.Fatalf("RemoveOSPFIntent failed: %v", err)
	}

	ospfs := s.GetOSPFIntents()
	if len(ospfs) != 0 {
		t.Fatalf("expected 0 OSPF intents after removal, got %d", len(ospfs))
	}
}

func TestRemoveOSPFIntentNotFound(t *testing.T) {
	s := newTestStore()

	err := s.RemoveOSPFIntent("novaedge", "eth0")
	if err == nil {
		t.Fatal("expected error removing non-existent OSPF intent")
	}
}

func TestSetOSPFIntentValidation(t *testing.T) {
	s := newTestStore()

	err := s.SetOSPFIntent("", &OSPFIntent{InterfaceName: "eth0"})
	if err == nil {
		t.Error("expected error for empty owner")
	}

	err = s.SetOSPFIntent("novaedge", nil)
	if err == nil {
		t.Error("expected error for nil intent")
	}

	err = s.SetOSPFIntent("novaedge", &OSPFIntent{InterfaceName: ""})
	if err == nil {
		t.Error("expected error for empty interface name")
	}
}

// --- Owner isolation tests ---

func TestOwnerIsolation(t *testing.T) {
	s := newTestStore()

	// Two owners set peer intents for the same neighbor address.
	err := s.SetPeerIntent("novaedge", &PeerIntent{
		NeighborAddress: "192.168.1.1",
		RemoteAS:        65001,
	})
	if err != nil {
		t.Fatalf("SetPeerIntent novaedge failed: %v", err)
	}

	err = s.SetPeerIntent("novanet", &PeerIntent{
		NeighborAddress: "192.168.1.1",
		RemoteAS:        65002,
	})
	if err != nil {
		t.Fatalf("SetPeerIntent novanet failed: %v", err)
	}

	// Both should be visible globally.
	allPeers := s.GetPeerIntents()
	if len(allPeers) != 2 {
		t.Fatalf("expected 2 total peer intents, got %d", len(allPeers))
	}

	// Remove from novaedge should not affect novanet.
	err = s.RemovePeerIntent("novaedge", "192.168.1.1")
	if err != nil {
		t.Fatalf("RemovePeerIntent failed: %v", err)
	}

	allPeers = s.GetPeerIntents()
	if len(allPeers) != 1 {
		t.Fatalf("expected 1 peer intent after removal, got %d", len(allPeers))
	}
	if allPeers[0].Owner != "novanet" {
		t.Errorf("expected remaining peer owned by 'novanet', got %q", allPeers[0].Owner)
	}
	if allPeers[0].RemoteAS != 65002 {
		t.Errorf("expected remaining peer RemoteAS 65002, got %d", allPeers[0].RemoteAS)
	}
}

func TestOwnerIsolationPrefixes(t *testing.T) {
	s := newTestStore()

	err := s.SetPrefixIntent("novaedge", &PrefixIntent{
		Prefix:   "10.0.0.0/24",
		Protocol: v1.Protocol_PROTOCOL_BGP,
	})
	if err != nil {
		t.Fatalf("SetPrefixIntent novaedge failed: %v", err)
	}

	err = s.SetPrefixIntent("admin", &PrefixIntent{
		Prefix:   "10.0.0.0/24",
		Protocol: v1.Protocol_PROTOCOL_BGP,
	})
	if err != nil {
		t.Fatalf("SetPrefixIntent admin failed: %v", err)
	}

	// Removing from admin should not affect novaedge.
	err = s.RemovePrefixIntent("admin", "10.0.0.0/24", "bgp")
	if err != nil {
		t.Fatalf("RemovePrefixIntent failed: %v", err)
	}

	prefixes := s.GetPrefixIntents()
	if len(prefixes) != 1 {
		t.Fatalf("expected 1 prefix intent after removal, got %d", len(prefixes))
	}
	if prefixes[0].Owner != "novaedge" {
		t.Errorf("expected remaining prefix owned by 'novaedge', got %q", prefixes[0].Owner)
	}
}

// --- RemoveAllByOwner tests ---

func TestRemoveAllByOwner(t *testing.T) {
	s := newTestStore()

	owner := "novaedge"

	// Add one of each intent type.
	err := s.SetPeerIntent(owner, &PeerIntent{
		NeighborAddress: "192.168.1.1",
		RemoteAS:        65001,
	})
	if err != nil {
		t.Fatalf("SetPeerIntent failed: %v", err)
	}

	err = s.SetPeerIntent(owner, &PeerIntent{
		NeighborAddress: "192.168.1.2",
		RemoteAS:        65002,
	})
	if err != nil {
		t.Fatalf("SetPeerIntent failed: %v", err)
	}

	err = s.SetPrefixIntent(owner, &PrefixIntent{
		Prefix:   "10.0.0.0/24",
		Protocol: v1.Protocol_PROTOCOL_BGP,
	})
	if err != nil {
		t.Fatalf("SetPrefixIntent failed: %v", err)
	}

	err = s.SetBFDIntent(owner, &BFDIntent{
		PeerAddress: "192.168.1.1",
	})
	if err != nil {
		t.Fatalf("SetBFDIntent failed: %v", err)
	}

	err = s.SetOSPFIntent(owner, &OSPFIntent{
		InterfaceName: "eth0",
		AreaID:        "0.0.0.0",
	})
	if err != nil {
		t.Fatalf("SetOSPFIntent failed: %v", err)
	}

	// Add a different owner's intent to verify isolation.
	err = s.SetPeerIntent("novanet", &PeerIntent{
		NeighborAddress: "10.0.0.1",
		RemoteAS:        65100,
	})
	if err != nil {
		t.Fatalf("SetPeerIntent novanet failed: %v", err)
	}

	// Remove all for the first owner.
	s.RemoveAllByOwner(owner)

	// Verify all novaedge intents are gone.
	oi := s.GetOwnerIntents(owner)
	if oi != nil {
		t.Fatalf("expected nil OwnerIntents after RemoveAllByOwner, got %+v", oi)
	}

	if len(s.GetPeerIntents()) != 1 {
		t.Fatalf("expected 1 peer intent (novanet), got %d", len(s.GetPeerIntents()))
	}
	if len(s.GetPrefixIntents()) != 0 {
		t.Fatalf("expected 0 prefix intents, got %d", len(s.GetPrefixIntents()))
	}
	if len(s.GetBFDIntents()) != 0 {
		t.Fatalf("expected 0 BFD intents, got %d", len(s.GetBFDIntents()))
	}
	if len(s.GetOSPFIntents()) != 0 {
		t.Fatalf("expected 0 OSPF intents, got %d", len(s.GetOSPFIntents()))
	}

	// novanet should be unaffected.
	remaining := s.GetPeerIntents()
	if remaining[0].Owner != "novanet" {
		t.Errorf("expected novanet peer to remain, got owner %q", remaining[0].Owner)
	}
}

func TestRemoveAllByOwnerNonExistent(t *testing.T) {
	s := newTestStore()

	// Should not panic on non-existent owner.
	s.RemoveAllByOwner("nonexistent")
}

// --- GetOwnerIntents / GetAllIntents tests ---

func TestGetOwnerIntents(t *testing.T) {
	s := newTestStore()

	err := s.SetPeerIntent("novaedge", &PeerIntent{
		NeighborAddress: "192.168.1.1",
		RemoteAS:        65001,
	})
	if err != nil {
		t.Fatalf("SetPeerIntent failed: %v", err)
	}

	err = s.SetPrefixIntent("novaedge", &PrefixIntent{
		Prefix:   "10.0.0.0/24",
		Protocol: v1.Protocol_PROTOCOL_BGP,
	})
	if err != nil {
		t.Fatalf("SetPrefixIntent failed: %v", err)
	}

	oi := s.GetOwnerIntents("novaedge")
	if oi == nil {
		t.Fatal("expected non-nil OwnerIntents")
	}
	if len(oi.Peers) != 1 {
		t.Errorf("expected 1 peer, got %d", len(oi.Peers))
	}
	if len(oi.Prefixes) != 1 {
		t.Errorf("expected 1 prefix, got %d", len(oi.Prefixes))
	}
	if len(oi.BFD) != 0 {
		t.Errorf("expected 0 BFD, got %d", len(oi.BFD))
	}
	if len(oi.OSPF) != 0 {
		t.Errorf("expected 0 OSPF, got %d", len(oi.OSPF))
	}
}

func TestGetOwnerIntentsNonExistent(t *testing.T) {
	s := newTestStore()

	oi := s.GetOwnerIntents("nonexistent")
	if oi != nil {
		t.Fatal("expected nil for non-existent owner")
	}
}

func TestGetAllIntents(t *testing.T) {
	s := newTestStore()

	err := s.SetPeerIntent("novaedge", &PeerIntent{
		NeighborAddress: "192.168.1.1",
		RemoteAS:        65001,
	})
	if err != nil {
		t.Fatalf("SetPeerIntent failed: %v", err)
	}

	err = s.SetPeerIntent("novanet", &PeerIntent{
		NeighborAddress: "10.0.0.1",
		RemoteAS:        65100,
	})
	if err != nil {
		t.Fatalf("SetPeerIntent failed: %v", err)
	}

	all := s.GetAllIntents()
	if len(all) != 2 {
		t.Fatalf("expected 2 owners, got %d", len(all))
	}
	if _, ok := all["novaedge"]; !ok {
		t.Error("expected 'novaedge' in GetAllIntents")
	}
	if _, ok := all["novanet"]; !ok {
		t.Error("expected 'novanet' in GetAllIntents")
	}
}

// --- GetOwnerPrefixes / GetOwnerPeers tests ---

func TestGetOwnerPrefixes(t *testing.T) {
	s := newTestStore()

	err := s.SetPrefixIntent("novaedge", &PrefixIntent{
		Prefix:   "10.0.0.0/24",
		Protocol: v1.Protocol_PROTOCOL_BGP,
	})
	if err != nil {
		t.Fatalf("SetPrefixIntent failed: %v", err)
	}
	err = s.SetPrefixIntent("novaedge", &PrefixIntent{
		Prefix:   "172.16.0.0/16",
		Protocol: v1.Protocol_PROTOCOL_OSPF,
	})
	if err != nil {
		t.Fatalf("SetPrefixIntent failed: %v", err)
	}

	prefixes := s.GetOwnerPrefixes("novaedge")
	if len(prefixes) != 2 {
		t.Fatalf("expected 2 prefixes, got %d", len(prefixes))
	}

	found := make(map[string]bool)
	for _, p := range prefixes {
		found[p] = true
	}
	if !found["10.0.0.0/24"] {
		t.Error("expected '10.0.0.0/24' in owner prefixes")
	}
	if !found["172.16.0.0/16"] {
		t.Error("expected '172.16.0.0/16' in owner prefixes")
	}
}

func TestGetOwnerPrefixesNonExistent(t *testing.T) {
	s := newTestStore()

	prefixes := s.GetOwnerPrefixes("nonexistent")
	if prefixes != nil {
		t.Fatalf("expected nil for non-existent owner, got %v", prefixes)
	}
}

func TestGetOwnerPeers(t *testing.T) {
	s := newTestStore()

	err := s.SetPeerIntent("novaedge", &PeerIntent{
		NeighborAddress: "192.168.1.1",
		RemoteAS:        65001,
	})
	if err != nil {
		t.Fatalf("SetPeerIntent failed: %v", err)
	}
	err = s.SetPeerIntent("novaedge", &PeerIntent{
		NeighborAddress: "192.168.1.2",
		RemoteAS:        65002,
	})
	if err != nil {
		t.Fatalf("SetPeerIntent failed: %v", err)
	}

	peers := s.GetOwnerPeers("novaedge")
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}

	found := make(map[string]bool)
	for _, p := range peers {
		found[p] = true
	}
	if !found["192.168.1.1"] {
		t.Error("expected '192.168.1.1' in owner peers")
	}
	if !found["192.168.1.2"] {
		t.Error("expected '192.168.1.2' in owner peers")
	}
}

func TestGetOwnerPeersNonExistent(t *testing.T) {
	s := newTestStore()

	peers := s.GetOwnerPeers("nonexistent")
	if peers != nil {
		t.Fatalf("expected nil for non-existent owner, got %v", peers)
	}
}

// --- Deep copy safety tests ---

func TestGetAllIntentsReturnsCopy(t *testing.T) {
	s := newTestStore()

	err := s.SetPeerIntent("novaedge", &PeerIntent{
		NeighborAddress: "192.168.1.1",
		RemoteAS:        65001,
	})
	if err != nil {
		t.Fatalf("SetPeerIntent failed: %v", err)
	}

	snapshot := s.GetAllIntents()

	// Mutate the snapshot.
	for _, pi := range snapshot["novaedge"].Peers {
		pi.RemoteAS = 99999
	}

	// The store should not be affected.
	fresh := s.GetPeerIntents()
	if fresh[0].RemoteAS != 65001 {
		t.Errorf("store was mutated through snapshot; expected RemoteAS 65001, got %d", fresh[0].RemoteAS)
	}
}

func TestGetPeerIntentsReturnsCopy(t *testing.T) {
	s := newTestStore()

	err := s.SetPeerIntent("novaedge", &PeerIntent{
		NeighborAddress: "192.168.1.1",
		RemoteAS:        65001,
		AddressFamilies: []v1.AddressFamily{v1.AddressFamily_ADDRESS_FAMILY_IPV4_UNICAST},
	})
	if err != nil {
		t.Fatalf("SetPeerIntent failed: %v", err)
	}

	peers := s.GetPeerIntents()
	peers[0].AddressFamilies = append(peers[0].AddressFamilies, v1.AddressFamily_ADDRESS_FAMILY_IPV6_UNICAST)

	fresh := s.GetPeerIntents()
	if len(fresh[0].AddressFamilies) != 1 {
		t.Errorf("store was mutated through returned slice; expected 1 address family, got %d", len(fresh[0].AddressFamilies))
	}
}

// --- Concurrent access tests ---

func TestConcurrentAccess(t *testing.T) {
	s := newTestStore()

	const numGoroutines = 50
	const numOps = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		go func(id int) {
			defer wg.Done()

			owner := "owner"
			if id%2 == 0 {
				owner = "owner-a"
			} else {
				owner = "owner-b"
			}

			for i := 0; i < numOps; i++ {
				addr := "10.0.0." + itoa(i%10)
				prefix := "10." + itoa(id%256) + ".0.0/24"

				// Mix of operations to stress the lock.
				switch i % 8 {
				case 0:
					_ = s.SetPeerIntent(owner, &PeerIntent{
						NeighborAddress: addr,
						RemoteAS:        uint32(65000 + id),
					})
				case 1:
					_ = s.RemovePeerIntent(owner, addr)
				case 2:
					_ = s.SetPrefixIntent(owner, &PrefixIntent{
						Prefix:   prefix,
						Protocol: v1.Protocol_PROTOCOL_BGP,
					})
				case 3:
					_ = s.RemovePrefixIntent(owner, prefix, "bgp")
				case 4:
					_ = s.SetBFDIntent(owner, &BFDIntent{
						PeerAddress: addr,
						MinRxMs:     300,
						MinTxMs:     300,
					})
				case 5:
					_ = s.RemoveBFDIntent(owner, addr)
				case 6:
					_ = s.SetOSPFIntent(owner, &OSPFIntent{
						InterfaceName: "eth" + itoa(i%5),
						AreaID:        "0.0.0.0",
					})
				case 7:
					_ = s.RemoveOSPFIntent(owner, "eth"+itoa(i%5))
				}

				// Intersperse reads.
				_ = s.GetAllIntents()
				_ = s.GetPeerIntents()
				_ = s.GetPrefixIntents()
				_ = s.GetBFDIntents()
				_ = s.GetOSPFIntents()
				_ = s.GetOwnerPrefixes(owner)
				_ = s.GetOwnerPeers(owner)
				_ = s.GetOwnerIntents(owner)
			}
		}(g)
	}

	wg.Wait()

	// If we get here without a race detector panic, the test passes.
	// Do a final sanity read.
	all := s.GetAllIntents()
	t.Logf("Final state: %d owners", len(all))
	for owner, oi := range all {
		t.Logf("  %s: %d peers, %d prefixes, %d bfd, %d ospf",
			owner, len(oi.Peers), len(oi.Prefixes), len(oi.BFD), len(oi.OSPF))
	}
}

func TestConcurrentRemoveAllByOwner(t *testing.T) {
	s := newTestStore()

	const numGoroutines = 20

	// Populate with intents from multiple owners.
	for i := 0; i < numGoroutines; i++ {
		owner := "owner-" + itoa(i)
		_ = s.SetPeerIntent(owner, &PeerIntent{
			NeighborAddress: "10.0.0." + itoa(i),
			RemoteAS:        uint32(65000 + i),
		})
		_ = s.SetPrefixIntent(owner, &PrefixIntent{
			Prefix:   "10." + itoa(i) + ".0.0/24",
			Protocol: v1.Protocol_PROTOCOL_BGP,
		})
	}

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Each goroutine removes its owner's intents while others read.
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			owner := "owner-" + itoa(id)

			// Read, then remove, then read again.
			_ = s.GetOwnerIntents(owner)
			_ = s.GetAllIntents()
			s.RemoveAllByOwner(owner)
			_ = s.GetPeerIntents()
			_ = s.GetOwnerIntents(owner)
		}(i)
	}

	wg.Wait()

	all := s.GetAllIntents()
	if len(all) != 0 {
		t.Errorf("expected 0 owners after concurrent removal, got %d", len(all))
	}
}

// --- NewStore with nil logger ---

func TestNewStoreWithNilLogger(t *testing.T) {
	s := NewStore(nil)
	if s == nil {
		t.Fatal("expected non-nil store")
	}

	// Should work fine with the nop logger.
	err := s.SetPeerIntent("test", &PeerIntent{
		NeighborAddress: "1.2.3.4",
		RemoteAS:        65001,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// itoa is a simple integer-to-string helper to avoid importing strconv in tests.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	result := ""
	negative := false
	if i < 0 {
		negative = true
		i = -i
	}
	for i > 0 {
		result = string(rune('0'+i%10)) + result
		i /= 10
	}
	if negative {
		result = "-" + result
	}
	return result
}
