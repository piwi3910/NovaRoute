package server

import (
	"context"
	"testing"
	"time"

	v1 "github.com/azrtydxb/NovaRoute/api/v1"
	"github.com/azrtydxb/NovaRoute/internal/config"
	"github.com/azrtydxb/NovaRoute/internal/intent"
	"github.com/azrtydxb/NovaRoute/internal/policy"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mockReconciler implements ReconcilerInterface for testing.
type mockReconciler struct {
	triggerCount int
	lastAS       uint32
	lastRouterID string
}

func (m *mockReconciler) TriggerReconcile() {
	m.triggerCount++
}

func (m *mockReconciler) UpdateBGPGlobal(localAS uint32, routerID string) (uint32, string) {
	prevAS := m.lastAS
	prevRouterID := m.lastRouterID
	m.lastAS = localAS
	m.lastRouterID = routerID
	return prevAS, prevRouterID
}

// testPolicyConfig returns a policy configuration with a known test owner.
func testPolicyConfig() policy.Config {
	return policy.Config{
		Owners: map[string]config.OwnerConfig{
			"test-owner": {
				Token: "test-token",
				AllowedPrefixes: config.PrefixPolicy{
					Type: "any",
				},
			},
			"other-owner": {
				Token: "other-token",
				AllowedPrefixes: config.PrefixPolicy{
					Type: "any",
				},
			},
		},
	}
}

// newTestServer creates a Server with test dependencies.
func newTestServer() (*Server, *mockReconciler) {
	logger := zap.NewNop()
	store := intent.NewStore(logger)
	pol := policy.NewEngine(testPolicyConfig(), logger)
	rec := &mockReconciler{}
	srv := NewServer(store, pol, rec, logger)
	return srv, rec
}

// --------------------------------------------------------------------------
// Register / Deregister
// --------------------------------------------------------------------------

func TestRegister(t *testing.T) {
	tests := []struct {
		name     string
		req      *v1.RegisterRequest
		wantCode codes.Code
	}{
		{
			name:     "success",
			req:      &v1.RegisterRequest{Owner: "test-owner", Token: "test-token"},
			wantCode: codes.OK,
		},
		{
			name:     "empty owner",
			req:      &v1.RegisterRequest{Owner: "", Token: "test-token"},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "empty token",
			req:      &v1.RegisterRequest{Owner: "test-owner", Token: ""},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "invalid token",
			req:      &v1.RegisterRequest{Owner: "test-owner", Token: "wrong"},
			wantCode: codes.Unauthenticated,
		},
		{
			name:     "unknown owner",
			req:      &v1.RegisterRequest{Owner: "nobody", Token: "test-token"},
			wantCode: codes.Unauthenticated,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServer()
			resp, err := srv.Register(context.Background(), tc.req)
			assertGRPCCode(t, err, tc.wantCode)
			if tc.wantCode == codes.OK {
				if resp.GetSessionId() == "" {
					t.Error("expected non-empty session_id")
				}
			}
		})
	}
}

func TestRegisterReassertTriggersReconcile(t *testing.T) {
	srv, rec := newTestServer()
	ctx := context.Background()

	// First, add a peer so there's existing state.
	peer := &intent.PeerIntent{NeighborAddress: "10.0.0.1", RemoteAS: 65001}
	if err := srv.intentStore.SetPeerIntent("test-owner", peer); err != nil {
		t.Fatalf("SetPeerIntent: %v", err)
	}
	rec.triggerCount = 0

	_, err := srv.Register(ctx, &v1.RegisterRequest{
		Owner:           "test-owner",
		Token:           "test-token",
		ReassertIntents: true,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if rec.triggerCount != 1 {
		t.Errorf("expected 1 reconcile trigger, got %d", rec.triggerCount)
	}
}

func TestDeregister(t *testing.T) {
	tests := []struct {
		name     string
		req      *v1.DeregisterRequest
		wantCode codes.Code
	}{
		{
			name:     "success",
			req:      &v1.DeregisterRequest{Owner: "test-owner", Token: "test-token"},
			wantCode: codes.OK,
		},
		{
			name:     "empty owner",
			req:      &v1.DeregisterRequest{Owner: "", Token: "test-token"},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "empty token",
			req:      &v1.DeregisterRequest{Owner: "test-owner", Token: ""},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "invalid token",
			req:      &v1.DeregisterRequest{Owner: "test-owner", Token: "wrong"},
			wantCode: codes.Unauthenticated,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServer()
			// Register first.
			_, _ = srv.Register(context.Background(), &v1.RegisterRequest{Owner: "test-owner", Token: "test-token"})

			_, err := srv.Deregister(context.Background(), tc.req)
			assertGRPCCode(t, err, tc.wantCode)
		})
	}
}

func TestDeregisterWithdrawAll(t *testing.T) {
	srv, rec := newTestServer()
	ctx := context.Background()

	// Register and set a peer intent.
	_, _ = srv.Register(ctx, &v1.RegisterRequest{Owner: "test-owner", Token: "test-token"})
	peer := &intent.PeerIntent{NeighborAddress: "10.0.0.1", RemoteAS: 65001}
	if err := srv.intentStore.SetPeerIntent("test-owner", peer); err != nil {
		t.Fatalf("SetPeerIntent: %v", err)
	}
	rec.triggerCount = 0

	_, err := srv.Deregister(ctx, &v1.DeregisterRequest{
		Owner:       "test-owner",
		Token:       "test-token",
		WithdrawAll: true,
	})
	if err != nil {
		t.Fatalf("Deregister: %v", err)
	}

	// Verify intents were removed.
	oi := srv.intentStore.GetOwnerIntents("test-owner")
	if oi != nil {
		t.Error("expected nil owner intents after withdraw_all")
	}

	// Verify reconciliation was triggered.
	if rec.triggerCount < 1 {
		t.Errorf("expected at least 1 reconcile trigger, got %d", rec.triggerCount)
	}
}

// --------------------------------------------------------------------------
// ApplyPeer / RemovePeer
// --------------------------------------------------------------------------

func TestApplyPeer(t *testing.T) {
	tests := []struct {
		name     string
		req      *v1.ApplyPeerRequest
		wantCode codes.Code
	}{
		{
			name: "success",
			req: &v1.ApplyPeerRequest{
				Owner: "test-owner",
				Token: "test-token",
				Peer: &v1.BGPPeer{
					NeighborAddress: "10.0.0.1",
					RemoteAs:        65001,
				},
			},
			wantCode: codes.OK,
		},
		{
			name: "empty owner",
			req: &v1.ApplyPeerRequest{
				Owner: "",
				Token: "test-token",
				Peer:  &v1.BGPPeer{NeighborAddress: "10.0.0.1", RemoteAs: 65001},
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "nil peer",
			req: &v1.ApplyPeerRequest{
				Owner: "test-owner",
				Token: "test-token",
				Peer:  nil,
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "empty neighbor address",
			req: &v1.ApplyPeerRequest{
				Owner: "test-owner",
				Token: "test-token",
				Peer:  &v1.BGPPeer{NeighborAddress: "", RemoteAs: 65001},
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "invalid neighbor IP",
			req: &v1.ApplyPeerRequest{
				Owner: "test-owner",
				Token: "test-token",
				Peer:  &v1.BGPPeer{NeighborAddress: "not-an-ip", RemoteAs: 65001},
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "zero remote_as",
			req: &v1.ApplyPeerRequest{
				Owner: "test-owner",
				Token: "test-token",
				Peer:  &v1.BGPPeer{NeighborAddress: "10.0.0.1", RemoteAs: 0},
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "invalid token",
			req: &v1.ApplyPeerRequest{
				Owner: "test-owner",
				Token: "wrong",
				Peer:  &v1.BGPPeer{NeighborAddress: "10.0.0.1", RemoteAs: 65001},
			},
			wantCode: codes.Unauthenticated,
		},
		{
			name: "hold_time less than 3",
			req: &v1.ApplyPeerRequest{
				Owner: "test-owner",
				Token: "test-token",
				Peer: &v1.BGPPeer{
					NeighborAddress: "10.0.0.1",
					RemoteAs:        65001,
					Keepalive:       1,
					HoldTime:        2,
				},
			},
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServer()
			_, err := srv.ApplyPeer(context.Background(), tc.req)
			assertGRPCCode(t, err, tc.wantCode)
		})
	}
}

func TestApplyPeerStoresIntent(t *testing.T) {
	srv, rec := newTestServer()
	ctx := context.Background()

	_, err := srv.ApplyPeer(ctx, &v1.ApplyPeerRequest{
		Owner: "test-owner",
		Token: "test-token",
		Peer: &v1.BGPPeer{
			NeighborAddress: "10.0.0.1",
			RemoteAs:        65001,
			BfdEnabled:      true,
		},
	})
	if err != nil {
		t.Fatalf("ApplyPeer: %v", err)
	}

	// Verify intent was stored.
	oi := srv.intentStore.GetOwnerIntents("test-owner")
	if oi == nil {
		t.Fatal("expected owner intents, got nil")
	}
	if len(oi.Peers) != 1 {
		t.Fatalf("expected 1 peer intent, got %d", len(oi.Peers))
	}

	// Verify BFD defaults were applied.
	for _, p := range oi.Peers {
		if p.BFDMinRxMs == 0 {
			t.Error("expected BFD min_rx_ms default to be set")
		}
		if p.BFDMinTxMs == 0 {
			t.Error("expected BFD min_tx_ms default to be set")
		}
		if p.BFDDetectMultiplier == 0 {
			t.Error("expected BFD detect_multiplier default to be set")
		}
	}

	// Verify reconcile was triggered.
	if rec.triggerCount != 1 {
		t.Errorf("expected 1 reconcile trigger, got %d", rec.triggerCount)
	}
}

func TestRemovePeer(t *testing.T) {
	srv, _ := newTestServer()
	ctx := context.Background()

	// Apply peer first.
	_, err := srv.ApplyPeer(ctx, &v1.ApplyPeerRequest{
		Owner: "test-owner",
		Token: "test-token",
		Peer:  &v1.BGPPeer{NeighborAddress: "10.0.0.1", RemoteAs: 65001},
	})
	if err != nil {
		t.Fatalf("ApplyPeer: %v", err)
	}

	// Remove it.
	_, err = srv.RemovePeer(ctx, &v1.RemovePeerRequest{
		Owner:           "test-owner",
		Token:           "test-token",
		NeighborAddress: "10.0.0.1",
	})
	if err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}

	// Verify it was removed.
	oi := srv.intentStore.GetOwnerIntents("test-owner")
	if oi != nil && len(oi.Peers) != 0 {
		t.Errorf("expected 0 peer intents, got %d", len(oi.Peers))
	}
}

func TestRemovePeerNotFound(t *testing.T) {
	srv, _ := newTestServer()
	_, err := srv.RemovePeer(context.Background(), &v1.RemovePeerRequest{
		Owner:           "test-owner",
		Token:           "test-token",
		NeighborAddress: "10.0.0.1",
	})
	assertGRPCCode(t, err, codes.NotFound)
}

func TestRemovePeerValidation(t *testing.T) {
	tests := []struct {
		name     string
		req      *v1.RemovePeerRequest
		wantCode codes.Code
	}{
		{
			name:     "empty owner",
			req:      &v1.RemovePeerRequest{Owner: "", Token: "test-token", NeighborAddress: "10.0.0.1"},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "empty token",
			req:      &v1.RemovePeerRequest{Owner: "test-owner", Token: "", NeighborAddress: "10.0.0.1"},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "empty neighbor",
			req:      &v1.RemovePeerRequest{Owner: "test-owner", Token: "test-token", NeighborAddress: ""},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "invalid neighbor IP",
			req:      &v1.RemovePeerRequest{Owner: "test-owner", Token: "test-token", NeighborAddress: "bad"},
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServer()
			_, err := srv.RemovePeer(context.Background(), tc.req)
			assertGRPCCode(t, err, tc.wantCode)
		})
	}
}

// --------------------------------------------------------------------------
// AdvertisePrefix / WithdrawPrefix
// --------------------------------------------------------------------------

func TestAdvertisePrefix(t *testing.T) {
	tests := []struct {
		name     string
		req      *v1.AdvertisePrefixRequest
		wantCode codes.Code
	}{
		{
			name: "success",
			req: &v1.AdvertisePrefixRequest{
				Owner:    "test-owner",
				Token:    "test-token",
				Prefix:   "10.0.0.0/24",
				Protocol: v1.Protocol_PROTOCOL_BGP,
			},
			wantCode: codes.OK,
		},
		{
			name: "empty prefix",
			req: &v1.AdvertisePrefixRequest{
				Owner:    "test-owner",
				Token:    "test-token",
				Prefix:   "",
				Protocol: v1.Protocol_PROTOCOL_BGP,
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "invalid CIDR",
			req: &v1.AdvertisePrefixRequest{
				Owner:    "test-owner",
				Token:    "test-token",
				Prefix:   "not-a-cidr",
				Protocol: v1.Protocol_PROTOCOL_BGP,
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "unspecified protocol",
			req: &v1.AdvertisePrefixRequest{
				Owner:    "test-owner",
				Token:    "test-token",
				Prefix:   "10.0.0.0/24",
				Protocol: v1.Protocol_PROTOCOL_UNSPECIFIED,
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "OSPF with BGP attributes rejected",
			req: &v1.AdvertisePrefixRequest{
				Owner:    "test-owner",
				Token:    "test-token",
				Prefix:   "10.0.0.0/24",
				Protocol: v1.Protocol_PROTOCOL_OSPF,
				Attributes: &v1.PrefixAttributes{
					LocalPreference: 100,
				},
			},
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServer()
			_, err := srv.AdvertisePrefix(context.Background(), tc.req)
			assertGRPCCode(t, err, tc.wantCode)
		})
	}
}

func TestWithdrawPrefix(t *testing.T) {
	srv, _ := newTestServer()
	ctx := context.Background()

	// Add a prefix first.
	_, err := srv.AdvertisePrefix(ctx, &v1.AdvertisePrefixRequest{
		Owner:    "test-owner",
		Token:    "test-token",
		Prefix:   "10.0.0.0/24",
		Protocol: v1.Protocol_PROTOCOL_BGP,
	})
	if err != nil {
		t.Fatalf("AdvertisePrefix: %v", err)
	}

	// Withdraw it.
	_, err = srv.WithdrawPrefix(ctx, &v1.WithdrawPrefixRequest{
		Owner:    "test-owner",
		Token:    "test-token",
		Prefix:   "10.0.0.0/24",
		Protocol: v1.Protocol_PROTOCOL_BGP,
	})
	if err != nil {
		t.Fatalf("WithdrawPrefix: %v", err)
	}
}

func TestWithdrawPrefixNotFound(t *testing.T) {
	srv, _ := newTestServer()
	_, err := srv.WithdrawPrefix(context.Background(), &v1.WithdrawPrefixRequest{
		Owner:    "test-owner",
		Token:    "test-token",
		Prefix:   "10.0.0.0/24",
		Protocol: v1.Protocol_PROTOCOL_BGP,
	})
	assertGRPCCode(t, err, codes.NotFound)
}

// --------------------------------------------------------------------------
// EnableBFD / DisableBFD
// --------------------------------------------------------------------------

func TestEnableBFD(t *testing.T) {
	tests := []struct {
		name     string
		req      *v1.EnableBFDRequest
		wantCode codes.Code
	}{
		{
			name: "success",
			req: &v1.EnableBFDRequest{
				Owner:       "test-owner",
				Token:       "test-token",
				PeerAddress: "10.0.0.1",
			},
			wantCode: codes.OK,
		},
		{
			name: "empty peer address",
			req: &v1.EnableBFDRequest{
				Owner:       "test-owner",
				Token:       "test-token",
				PeerAddress: "",
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "invalid peer IP",
			req: &v1.EnableBFDRequest{
				Owner:       "test-owner",
				Token:       "test-token",
				PeerAddress: "bad-ip",
			},
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServer()
			_, err := srv.EnableBFD(context.Background(), tc.req)
			assertGRPCCode(t, err, tc.wantCode)
		})
	}
}

func TestEnableBFDDefaults(t *testing.T) {
	srv, _ := newTestServer()
	_, err := srv.EnableBFD(context.Background(), &v1.EnableBFDRequest{
		Owner:       "test-owner",
		Token:       "test-token",
		PeerAddress: "10.0.0.1",
	})
	if err != nil {
		t.Fatalf("EnableBFD: %v", err)
	}

	oi := srv.intentStore.GetOwnerIntents("test-owner")
	if oi == nil {
		t.Fatal("expected owner intents, got nil")
	}
	for _, b := range oi.BFD {
		if b.MinRxMs != 300 {
			t.Errorf("expected default MinRxMs=300, got %d", b.MinRxMs)
		}
		if b.MinTxMs != 300 {
			t.Errorf("expected default MinTxMs=300, got %d", b.MinTxMs)
		}
		if b.DetectMultiplier != 3 {
			t.Errorf("expected default DetectMultiplier=3, got %d", b.DetectMultiplier)
		}
	}
}

func TestDisableBFD(t *testing.T) {
	srv, _ := newTestServer()
	ctx := context.Background()

	// Enable first.
	_, err := srv.EnableBFD(ctx, &v1.EnableBFDRequest{
		Owner:       "test-owner",
		Token:       "test-token",
		PeerAddress: "10.0.0.1",
	})
	if err != nil {
		t.Fatalf("EnableBFD: %v", err)
	}

	// Disable.
	_, err = srv.DisableBFD(ctx, &v1.DisableBFDRequest{
		Owner:       "test-owner",
		Token:       "test-token",
		PeerAddress: "10.0.0.1",
	})
	if err != nil {
		t.Fatalf("DisableBFD: %v", err)
	}
}

// --------------------------------------------------------------------------
// EnableOSPF / DisableOSPF
// --------------------------------------------------------------------------

func TestEnableOSPF(t *testing.T) {
	tests := []struct {
		name     string
		req      *v1.EnableOSPFRequest
		wantCode codes.Code
	}{
		{
			name: "success",
			req: &v1.EnableOSPFRequest{
				Owner:         "test-owner",
				Token:         "test-token",
				InterfaceName: "eth0",
				AreaId:        "0.0.0.0",
			},
			wantCode: codes.OK,
		},
		{
			name: "empty interface name",
			req: &v1.EnableOSPFRequest{
				Owner:         "test-owner",
				Token:         "test-token",
				InterfaceName: "",
				AreaId:        "0.0.0.0",
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "empty area_id",
			req: &v1.EnableOSPFRequest{
				Owner:         "test-owner",
				Token:         "test-token",
				InterfaceName: "eth0",
				AreaId:        "",
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "dead < hello",
			req: &v1.EnableOSPFRequest{
				Owner:         "test-owner",
				Token:         "test-token",
				InterfaceName: "eth0",
				AreaId:        "0.0.0.0",
				HelloInterval: 20,
				DeadInterval:  10,
			},
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServer()
			_, err := srv.EnableOSPF(context.Background(), tc.req)
			assertGRPCCode(t, err, tc.wantCode)
		})
	}
}

func TestDisableOSPF(t *testing.T) {
	srv, _ := newTestServer()
	ctx := context.Background()

	// Enable first.
	_, err := srv.EnableOSPF(ctx, &v1.EnableOSPFRequest{
		Owner:         "test-owner",
		Token:         "test-token",
		InterfaceName: "eth0",
		AreaId:        "0.0.0.0",
	})
	if err != nil {
		t.Fatalf("EnableOSPF: %v", err)
	}

	// Disable.
	_, err = srv.DisableOSPF(ctx, &v1.DisableOSPFRequest{
		Owner:         "test-owner",
		Token:         "test-token",
		InterfaceName: "eth0",
	})
	if err != nil {
		t.Fatalf("DisableOSPF: %v", err)
	}
}

// --------------------------------------------------------------------------
// ConfigureBGP
// --------------------------------------------------------------------------

func TestConfigureBGP(t *testing.T) {
	tests := []struct {
		name     string
		req      *v1.ConfigureBGPRequest
		wantCode codes.Code
	}{
		{
			name: "success",
			req: &v1.ConfigureBGPRequest{
				Owner:    "test-owner",
				Token:    "test-token",
				LocalAs:  65001,
				RouterId: "1.1.1.1",
			},
			wantCode: codes.OK,
		},
		{
			name: "zero AS",
			req: &v1.ConfigureBGPRequest{
				Owner:    "test-owner",
				Token:    "test-token",
				LocalAs:  0,
				RouterId: "1.1.1.1",
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "empty router_id",
			req: &v1.ConfigureBGPRequest{
				Owner:    "test-owner",
				Token:    "test-token",
				LocalAs:  65001,
				RouterId: "",
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "invalid router_id (not an IP)",
			req: &v1.ConfigureBGPRequest{
				Owner:    "test-owner",
				Token:    "test-token",
				LocalAs:  65001,
				RouterId: "bad",
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "multicast router_id",
			req: &v1.ConfigureBGPRequest{
				Owner:    "test-owner",
				Token:    "test-token",
				LocalAs:  65001,
				RouterId: "239.0.0.1",
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "IPv6 router_id",
			req: &v1.ConfigureBGPRequest{
				Owner:    "test-owner",
				Token:    "test-token",
				LocalAs:  65001,
				RouterId: "::1",
			},
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServer()
			_, err := srv.ConfigureBGP(context.Background(), tc.req)
			assertGRPCCode(t, err, tc.wantCode)
		})
	}
}

func TestConfigureBGPReturnsPrevious(t *testing.T) {
	srv, rec := newTestServer()
	ctx := context.Background()

	// Set initial BGP config.
	rec.lastAS = 65000
	rec.lastRouterID = "2.2.2.2"

	resp, err := srv.ConfigureBGP(ctx, &v1.ConfigureBGPRequest{
		Owner:    "test-owner",
		Token:    "test-token",
		LocalAs:  65001,
		RouterId: "1.1.1.1",
	})
	if err != nil {
		t.Fatalf("ConfigureBGP: %v", err)
	}
	if resp.GetPreviousAs() != 65000 {
		t.Errorf("expected previous_as=65000, got %d", resp.GetPreviousAs())
	}
	if resp.GetPreviousRouterId() != "2.2.2.2" {
		t.Errorf("expected previous_router_id=2.2.2.2, got %s", resp.GetPreviousRouterId())
	}
}

// --------------------------------------------------------------------------
// GetStatus (without FRR client)
// --------------------------------------------------------------------------

func TestGetStatusEmpty(t *testing.T) {
	srv, _ := newTestServer()
	resp, err := srv.GetStatus(context.Background(), &v1.GetStatusRequest{})
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if len(resp.GetPeers()) != 0 {
		t.Errorf("expected 0 peers, got %d", len(resp.GetPeers()))
	}
	if len(resp.GetPrefixes()) != 0 {
		t.Errorf("expected 0 prefixes, got %d", len(resp.GetPrefixes()))
	}
	if resp.GetFrrStatus().GetConnected() {
		t.Error("expected FRR not connected")
	}
}

func TestGetStatusWithIntents(t *testing.T) {
	srv, _ := newTestServer()
	ctx := context.Background()

	// Set up some intents via the RPCs.
	_, err := srv.ApplyPeer(ctx, &v1.ApplyPeerRequest{
		Owner: "test-owner",
		Token: "test-token",
		Peer:  &v1.BGPPeer{NeighborAddress: "10.0.0.1", RemoteAs: 65001},
	})
	if err != nil {
		t.Fatalf("ApplyPeer: %v", err)
	}

	_, err = srv.AdvertisePrefix(ctx, &v1.AdvertisePrefixRequest{
		Owner:    "test-owner",
		Token:    "test-token",
		Prefix:   "10.0.0.0/24",
		Protocol: v1.Protocol_PROTOCOL_BGP,
	})
	if err != nil {
		t.Fatalf("AdvertisePrefix: %v", err)
	}

	resp, err := srv.GetStatus(ctx, &v1.GetStatusRequest{})
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if len(resp.GetPeers()) != 1 {
		t.Errorf("expected 1 peer, got %d", len(resp.GetPeers()))
	}
	if len(resp.GetPrefixes()) != 1 {
		t.Errorf("expected 1 prefix, got %d", len(resp.GetPrefixes()))
	}

	// Peer state should be "pending" without FRR client.
	if resp.GetPeers()[0].GetState() != "pending" {
		t.Errorf("expected state=pending, got %s", resp.GetPeers()[0].GetState())
	}
}

func TestGetStatusOwnerFilter(t *testing.T) {
	srv, _ := newTestServer()
	ctx := context.Background()

	// Add intents for two owners.
	_, _ = srv.ApplyPeer(ctx, &v1.ApplyPeerRequest{
		Owner: "test-owner",
		Token: "test-token",
		Peer:  &v1.BGPPeer{NeighborAddress: "10.0.0.1", RemoteAs: 65001},
	})
	_, _ = srv.ApplyPeer(ctx, &v1.ApplyPeerRequest{
		Owner: "other-owner",
		Token: "other-token",
		Peer:  &v1.BGPPeer{NeighborAddress: "10.0.0.2", RemoteAs: 65002},
	})

	// Filter by test-owner.
	resp, err := srv.GetStatus(ctx, &v1.GetStatusRequest{OwnerFilter: "test-owner"})
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if len(resp.GetPeers()) != 1 {
		t.Errorf("expected 1 peer, got %d", len(resp.GetPeers()))
	}
	if resp.GetPeers()[0].GetOwner() != "test-owner" {
		t.Errorf("expected owner=test-owner, got %s", resp.GetPeers()[0].GetOwner())
	}
}

// --------------------------------------------------------------------------
// EventBus
// --------------------------------------------------------------------------

func TestEventBusSubscribePublish(t *testing.T) {
	eb := NewEventBus()

	ch := eb.Subscribe("", nil)
	event := &v1.RouteEvent{
		Type:   v1.EventType_EVENT_TYPE_PEER_UP,
		Owner:  "test-owner",
		Detail: "test event",
	}
	eb.Publish(event)

	select {
	case got := <-ch:
		if got.GetDetail() != "test event" {
			t.Errorf("expected detail=%q, got %q", "test event", got.GetDetail())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}

	eb.Unsubscribe(ch)
}

func TestEventBusOwnerFilter(t *testing.T) {
	eb := NewEventBus()

	ch := eb.Subscribe("tenant-a", nil)

	// Publish event for tenant-a (should be delivered).
	eb.Publish(&v1.RouteEvent{Owner: "tenant-a", Detail: "match"})
	// Publish event for tenant-b (should be filtered).
	eb.Publish(&v1.RouteEvent{Owner: "tenant-b", Detail: "no-match"})

	select {
	case got := <-ch:
		if got.GetDetail() != "match" {
			t.Errorf("expected detail=%q, got %q", "match", got.GetDetail())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}

	// Verify no second event.
	select {
	case evt := <-ch:
		t.Errorf("unexpected event: %v", evt)
	case <-time.After(50 * time.Millisecond):
		// Expected: no event.
	}

	eb.Unsubscribe(ch)
}

func TestEventBusTypeFilter(t *testing.T) {
	eb := NewEventBus()

	ch := eb.Subscribe("", []string{"PEER_UP"})

	eb.Publish(&v1.RouteEvent{Type: v1.EventType_EVENT_TYPE_PEER_UP, Detail: "up"})
	eb.Publish(&v1.RouteEvent{Type: v1.EventType_EVENT_TYPE_PEER_DOWN, Detail: "down"})

	select {
	case got := <-ch:
		if got.GetDetail() != "up" {
			t.Errorf("expected detail=%q, got %q", "up", got.GetDetail())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}

	// No second event should come through.
	select {
	case evt := <-ch:
		t.Errorf("unexpected event: %v", evt)
	case <-time.After(50 * time.Millisecond):
		// Expected.
	}

	eb.Unsubscribe(ch)
}

func TestEventBusUnsubscribe(t *testing.T) {
	eb := NewEventBus()
	ch := eb.Subscribe("", nil)
	eb.Unsubscribe(ch)

	// Channel should be closed.
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after unsubscribe")
	}
}

func TestPublishRouteEvent(t *testing.T) {
	eb := NewEventBus()
	ch := eb.Subscribe("", nil)

	eb.PublishRouteEvent(uint32(v1.EventType_EVENT_TYPE_PEER_UP), "test-owner", "test detail", map[string]string{"key": "val"})

	select {
	case got := <-ch:
		if got.GetType() != v1.EventType_EVENT_TYPE_PEER_UP {
			t.Errorf("expected type=PEER_UP, got %v", got.GetType())
		}
		if got.GetOwner() != "test-owner" {
			t.Errorf("expected owner=test-owner, got %s", got.GetOwner())
		}
		if got.GetDetail() != "test detail" {
			t.Errorf("expected detail=%q, got %q", "test detail", got.GetDetail())
		}
		if got.GetMetadata()["key"] != "val" {
			t.Errorf("expected metadata key=val, got %v", got.GetMetadata())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}

	eb.Unsubscribe(ch)
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// assertGRPCCode checks that err matches the expected gRPC status code.
// For codes.OK, err must be nil.
func assertGRPCCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if want == codes.OK {
		if err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
		return
	}
	if err == nil {
		t.Errorf("expected error with code %v, got nil", want)
		return
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Errorf("expected gRPC status error, got: %v", err)
		return
	}
	if st.Code() != want {
		t.Errorf("expected code %v, got %v: %s", want, st.Code(), st.Message())
	}
}
