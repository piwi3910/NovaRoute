package frr

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"

	frr "github.com/piwi3910/NovaRoute/api/frr"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

// mockNorthboundServer is a test implementation of the FRR northbound gRPC server.
// It tracks all calls made to it and allows configuring failure behavior.
type mockNorthboundServer struct {
	frr.UnimplementedNorthboundServer

	mu             sync.Mutex
	nextCandidate  uint32
	edits          []*frr.EditCandidateRequest
	commits        []*frr.CommitRequest
	deletedCandIDs []uint32

	// Configuration for failure injection.
	failCommit         bool
	failEditCandidate  bool
	failCreateCandidate bool
}

func newMockServer() *mockNorthboundServer {
	return &mockNorthboundServer{
		nextCandidate: 1,
	}
}

func (m *mockNorthboundServer) GetCapabilities(_ context.Context, _ *frr.GetCapabilitiesRequest) (*frr.GetCapabilitiesResponse, error) {
	return &frr.GetCapabilitiesResponse{
		FrrVersion: "10.0-NovaRoute-test",
	}, nil
}

func (m *mockNorthboundServer) CreateCandidate(_ context.Context, _ *frr.CreateCandidateRequest) (*frr.CreateCandidateResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.failCreateCandidate {
		return nil, fmt.Errorf("mock: CreateCandidate failed")
	}

	id := m.nextCandidate
	m.nextCandidate++
	return &frr.CreateCandidateResponse{
		CandidateId: id,
	}, nil
}

func (m *mockNorthboundServer) DeleteCandidate(_ context.Context, req *frr.DeleteCandidateRequest) (*frr.DeleteCandidateResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.deletedCandIDs = append(m.deletedCandIDs, req.GetCandidateId())
	return &frr.DeleteCandidateResponse{}, nil
}

func (m *mockNorthboundServer) EditCandidate(_ context.Context, req *frr.EditCandidateRequest) (*frr.EditCandidateResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.failEditCandidate {
		return nil, fmt.Errorf("mock: EditCandidate failed")
	}

	m.edits = append(m.edits, req)
	return &frr.EditCandidateResponse{}, nil
}

func (m *mockNorthboundServer) Commit(_ context.Context, req *frr.CommitRequest) (*frr.CommitResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.failCommit {
		return nil, fmt.Errorf("mock: Commit failed")
	}

	m.commits = append(m.commits, req)
	return &frr.CommitResponse{
		TransactionId: 100,
	}, nil
}

func (m *mockNorthboundServer) getEdits() []*frr.EditCandidateRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*frr.EditCandidateRequest, len(m.edits))
	copy(result, m.edits)
	return result
}

func (m *mockNorthboundServer) getCommits() []*frr.CommitRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*frr.CommitRequest, len(m.commits))
	copy(result, m.commits)
	return result
}

func (m *mockNorthboundServer) getDeletedCandIDs() []uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]uint32, len(m.deletedCandIDs))
	copy(result, m.deletedCandIDs)
	return result
}

// setupTest creates a mock FRR server, an in-memory gRPC connection via bufconn,
// and returns a Client connected to it along with the mock server for assertions.
func setupTest(t *testing.T) (*Client, *mockNorthboundServer) {
	t.Helper()

	mock := newMockServer()
	lis := bufconn.Listen(bufSize)

	srv := grpc.NewServer()
	frr.RegisterNorthboundServer(srv, mock)

	go func() {
		if err := srv.Serve(lis); err != nil {
			// Server stopped, expected during test cleanup.
		}
	}()

	t.Cleanup(func() {
		srv.GracefulStop()
		lis.Close()
	})

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
	)
	if err != nil {
		t.Fatalf("failed to create bufconn client: %v", err)
	}

	t.Cleanup(func() {
		conn.Close()
	})

	logger := zap.NewNop()
	client := &Client{
		nb:   frr.NewNorthboundClient(conn),
		conn: conn,
		log:  logger,
	}

	return client, mock
}

// findUpdatePath searches the recorded edits for a PathValue with the given path.
func findUpdatePath(edits []*frr.EditCandidateRequest, path string) *frr.PathValue {
	for _, edit := range edits {
		for _, u := range edit.GetUpdate() {
			if u.GetPath() == path {
				return u
			}
		}
	}
	return nil
}

// findDeletePath searches the recorded edits for a delete PathValue with the given path.
func findDeletePath(edits []*frr.EditCandidateRequest, path string) *frr.PathValue {
	for _, edit := range edits {
		for _, d := range edit.GetDelete() {
			if d.GetPath() == path {
				return d
			}
		}
	}
	return nil
}

// --- applyChanges tests ---

func TestApplyChangesSuccess(t *testing.T) {
	client, mock := setupTest(t)
	ctx := context.Background()

	updates := []*frr.PathValue{
		{Path: "/test/path", Value: "value1"},
	}

	err := client.applyChanges(ctx, updates, nil)
	if err != nil {
		t.Fatalf("applyChanges failed: %v", err)
	}

	// Verify the full transaction flow: create -> edit -> commit -> delete.
	edits := mock.getEdits()
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].GetCandidateId() != 1 {
		t.Errorf("expected candidate_id=1, got %d", edits[0].GetCandidateId())
	}
	if len(edits[0].GetUpdate()) != 1 {
		t.Errorf("expected 1 update, got %d", len(edits[0].GetUpdate()))
	}
	if edits[0].GetUpdate()[0].GetPath() != "/test/path" {
		t.Errorf("expected path /test/path, got %s", edits[0].GetUpdate()[0].GetPath())
	}

	commits := mock.getCommits()
	if len(commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(commits))
	}
	if commits[0].GetPhase() != frr.CommitRequest_ALL {
		t.Errorf("expected commit phase ALL, got %v", commits[0].GetPhase())
	}

	deletedIDs := mock.getDeletedCandIDs()
	if len(deletedIDs) != 1 {
		t.Fatalf("expected 1 candidate deleted, got %d", len(deletedIDs))
	}
	if deletedIDs[0] != 1 {
		t.Errorf("expected deleted candidate_id=1, got %d", deletedIDs[0])
	}
}

func TestApplyChangesEmptyNoOp(t *testing.T) {
	client, mock := setupTest(t)
	ctx := context.Background()

	err := client.applyChanges(ctx, nil, nil)
	if err != nil {
		t.Fatalf("applyChanges with empty changes should not error: %v", err)
	}

	edits := mock.getEdits()
	if len(edits) != 0 {
		t.Errorf("expected 0 edits for empty changes, got %d", len(edits))
	}
}

func TestApplyChangesRollbackOnCommitFailure(t *testing.T) {
	client, mock := setupTest(t)
	ctx := context.Background()

	mock.mu.Lock()
	mock.failCommit = true
	mock.mu.Unlock()

	updates := []*frr.PathValue{
		{Path: "/test/fail", Value: "value"},
	}

	err := client.applyChanges(ctx, updates, nil)
	if err == nil {
		t.Fatal("expected error on commit failure, got nil")
	}

	// The candidate should still be cleaned up (deleted) even on failure.
	deletedIDs := mock.getDeletedCandIDs()
	if len(deletedIDs) != 1 {
		t.Fatalf("expected candidate to be deleted on rollback, got %d deletes", len(deletedIDs))
	}
	if deletedIDs[0] != 1 {
		t.Errorf("expected deleted candidate_id=1, got %d", deletedIDs[0])
	}
}

func TestApplyChangesRollbackOnEditFailure(t *testing.T) {
	client, mock := setupTest(t)
	ctx := context.Background()

	mock.mu.Lock()
	mock.failEditCandidate = true
	mock.mu.Unlock()

	updates := []*frr.PathValue{
		{Path: "/test/edit-fail", Value: "value"},
	}

	err := client.applyChanges(ctx, updates, nil)
	if err == nil {
		t.Fatal("expected error on edit failure, got nil")
	}

	// Candidate should still be cleaned up.
	deletedIDs := mock.getDeletedCandIDs()
	if len(deletedIDs) != 1 {
		t.Fatalf("expected candidate to be deleted on edit failure, got %d deletes", len(deletedIDs))
	}

	// No commits should have been attempted.
	commits := mock.getCommits()
	if len(commits) != 0 {
		t.Errorf("expected 0 commits on edit failure, got %d", len(commits))
	}
}

func TestGetVersion(t *testing.T) {
	client, _ := setupTest(t)
	ctx := context.Background()

	version, err := client.GetVersion(ctx)
	if err != nil {
		t.Fatalf("GetVersion failed: %v", err)
	}
	if version != "10.0-NovaRoute-test" {
		t.Errorf("expected version '10.0-NovaRoute-test', got '%s'", version)
	}
}

// --- BGP tests ---

func TestBGPConfigureGlobal(t *testing.T) {
	client, mock := setupTest(t)
	ctx := context.Background()

	err := client.ConfigureBGPGlobal(ctx, 65000, "10.0.0.1")
	if err != nil {
		t.Fatalf("ConfigureBGPGlobal failed: %v", err)
	}

	edits := mock.getEdits()
	asUpdate := findUpdatePath(edits, bgpGlobalAS)
	if asUpdate == nil {
		t.Fatal("expected update for bgpGlobalAS path")
	}
	if asUpdate.GetValue() != "65000" {
		t.Errorf("expected AS value '65000', got '%s'", asUpdate.GetValue())
	}

	ridUpdate := findUpdatePath(edits, bgpGlobalRouterID)
	if ridUpdate == nil {
		t.Fatal("expected update for bgpGlobalRouterID path")
	}
	if ridUpdate.GetValue() != "10.0.0.1" {
		t.Errorf("expected router-id '10.0.0.1', got '%s'", ridUpdate.GetValue())
	}
}

func TestBGPAddNeighbor(t *testing.T) {
	client, mock := setupTest(t)
	ctx := context.Background()

	err := client.AddNeighbor(ctx, "192.168.1.1", 65001, "external", 30, 90)
	if err != nil {
		t.Fatalf("AddNeighbor failed: %v", err)
	}

	edits := mock.getEdits()
	neighborBase := BGPNeighborPath("192.168.1.1")

	asUpdate := findUpdatePath(edits, neighborBase+bgpNeighborRemoteAS)
	if asUpdate == nil {
		t.Fatal("expected update for neighbor remote-as")
	}
	if asUpdate.GetValue() != "65001" {
		t.Errorf("expected remote-as '65001', got '%s'", asUpdate.GetValue())
	}

	peerTypeUpdate := findUpdatePath(edits, neighborBase+bgpNeighborPeerType)
	if peerTypeUpdate == nil {
		t.Fatal("expected update for neighbor peer-type")
	}
	if peerTypeUpdate.GetValue() != "external" {
		t.Errorf("expected peer-type 'external', got '%s'", peerTypeUpdate.GetValue())
	}

	enabledUpdate := findUpdatePath(edits, neighborBase+bgpNeighborEnabled)
	if enabledUpdate == nil {
		t.Fatal("expected update for neighbor enabled")
	}
	if enabledUpdate.GetValue() != "true" {
		t.Errorf("expected enabled 'true', got '%s'", enabledUpdate.GetValue())
	}

	keepaliveUpdate := findUpdatePath(edits, neighborBase+bgpNeighborTimersKeepalive)
	if keepaliveUpdate == nil {
		t.Fatal("expected update for neighbor keepalive")
	}
	if keepaliveUpdate.GetValue() != "30" {
		t.Errorf("expected keepalive '30', got '%s'", keepaliveUpdate.GetValue())
	}

	holdUpdate := findUpdatePath(edits, neighborBase+bgpNeighborTimersHoldTime)
	if holdUpdate == nil {
		t.Fatal("expected update for neighbor hold-time")
	}
	if holdUpdate.GetValue() != "90" {
		t.Errorf("expected hold-time '90', got '%s'", holdUpdate.GetValue())
	}
}

func TestBGPAddNeighborDefaultTimers(t *testing.T) {
	client, mock := setupTest(t)
	ctx := context.Background()

	err := client.AddNeighbor(ctx, "10.0.0.2", 65002, "internal", 0, 0)
	if err != nil {
		t.Fatalf("AddNeighbor failed: %v", err)
	}

	edits := mock.getEdits()
	neighborBase := BGPNeighborPath("10.0.0.2")

	// Timer paths should not be present when set to 0.
	keepaliveUpdate := findUpdatePath(edits, neighborBase+bgpNeighborTimersKeepalive)
	if keepaliveUpdate != nil {
		t.Error("expected no keepalive update when value is 0")
	}

	holdUpdate := findUpdatePath(edits, neighborBase+bgpNeighborTimersHoldTime)
	if holdUpdate != nil {
		t.Error("expected no hold-time update when value is 0")
	}
}

func TestBGPRemoveNeighbor(t *testing.T) {
	client, mock := setupTest(t)
	ctx := context.Background()

	err := client.RemoveNeighbor(ctx, "192.168.1.1")
	if err != nil {
		t.Fatalf("RemoveNeighbor failed: %v", err)
	}

	edits := mock.getEdits()
	neighborBase := BGPNeighborPath("192.168.1.1")

	del := findDeletePath(edits, neighborBase)
	if del == nil {
		t.Fatal("expected delete for neighbor path")
	}
}

func TestBGPActivateNeighborAFI(t *testing.T) {
	client, mock := setupTest(t)
	ctx := context.Background()

	err := client.ActivateNeighborAFI(ctx, "192.168.1.1", "ipv4")
	if err != nil {
		t.Fatalf("ActivateNeighborAFI failed: %v", err)
	}

	edits := mock.getEdits()
	afiPath := BGPNeighborAFIPath("192.168.1.1", bgpAFIIPv4Unicast)

	enabledUpdate := findUpdatePath(edits, afiPath+bgpNeighborAFIEnabled)
	if enabledUpdate == nil {
		t.Fatal("expected update for neighbor AFI enabled path")
	}
	if enabledUpdate.GetValue() != "true" {
		t.Errorf("expected enabled 'true', got '%s'", enabledUpdate.GetValue())
	}
}

func TestBGPActivateNeighborAFIIPv6(t *testing.T) {
	client, mock := setupTest(t)
	ctx := context.Background()

	err := client.ActivateNeighborAFI(ctx, "fd00::1", "ipv6-unicast")
	if err != nil {
		t.Fatalf("ActivateNeighborAFI failed: %v", err)
	}

	edits := mock.getEdits()
	afiPath := BGPNeighborAFIPath("fd00::1", bgpAFIIPv6Unicast)

	enabledUpdate := findUpdatePath(edits, afiPath+bgpNeighborAFIEnabled)
	if enabledUpdate == nil {
		t.Fatal("expected update for neighbor AFI enabled path (IPv6)")
	}
}

func TestBGPAdvertiseNetwork(t *testing.T) {
	client, mock := setupTest(t)
	ctx := context.Background()

	err := client.AdvertiseNetwork(ctx, "10.0.0.0/24", "ipv4")
	if err != nil {
		t.Fatalf("AdvertiseNetwork failed: %v", err)
	}

	edits := mock.getEdits()
	networkPath := BGPNetworkPath("10.0.0.0/24", bgpAFIIPv4Unicast)

	update := findUpdatePath(edits, networkPath)
	if update == nil {
		t.Fatal("expected update for network path")
	}
}

func TestBGPWithdrawNetwork(t *testing.T) {
	client, mock := setupTest(t)
	ctx := context.Background()

	err := client.WithdrawNetwork(ctx, "10.0.0.0/24", "ipv4")
	if err != nil {
		t.Fatalf("WithdrawNetwork failed: %v", err)
	}

	edits := mock.getEdits()
	networkPath := BGPNetworkPath("10.0.0.0/24", bgpAFIIPv4Unicast)

	del := findDeletePath(edits, networkPath)
	if del == nil {
		t.Fatal("expected delete for network path")
	}
}

// --- BFD tests ---

func TestBFDAddPeer(t *testing.T) {
	client, mock := setupTest(t)
	ctx := context.Background()

	err := client.AddBFDPeer(ctx, "192.168.1.1", 300, 300, 3, "eth0")
	if err != nil {
		t.Fatalf("AddBFDPeer failed: %v", err)
	}

	edits := mock.getEdits()
	peerBase := BFDPeerPath("192.168.1.1")

	rxUpdate := findUpdatePath(edits, peerBase+bfdMinRxInterval)
	if rxUpdate == nil {
		t.Fatal("expected update for BFD min-rx")
	}
	if rxUpdate.GetValue() != "300" {
		t.Errorf("expected min-rx '300', got '%s'", rxUpdate.GetValue())
	}

	txUpdate := findUpdatePath(edits, peerBase+bfdMinTxInterval)
	if txUpdate == nil {
		t.Fatal("expected update for BFD min-tx")
	}
	if txUpdate.GetValue() != "300" {
		t.Errorf("expected min-tx '300', got '%s'", txUpdate.GetValue())
	}

	detectUpdate := findUpdatePath(edits, peerBase+bfdDetectMultiplier)
	if detectUpdate == nil {
		t.Fatal("expected update for BFD detect-multiplier")
	}
	if detectUpdate.GetValue() != "3" {
		t.Errorf("expected detect-mult '3', got '%s'", detectUpdate.GetValue())
	}

	ifaceUpdate := findUpdatePath(edits, peerBase+bfdInterface)
	if ifaceUpdate == nil {
		t.Fatal("expected update for BFD interface")
	}
	if ifaceUpdate.GetValue() != "eth0" {
		t.Errorf("expected interface 'eth0', got '%s'", ifaceUpdate.GetValue())
	}
}

func TestBFDAddPeerNoInterface(t *testing.T) {
	client, mock := setupTest(t)
	ctx := context.Background()

	err := client.AddBFDPeer(ctx, "10.0.0.1", 200, 200, 5, "")
	if err != nil {
		t.Fatalf("AddBFDPeer failed: %v", err)
	}

	edits := mock.getEdits()
	peerBase := BFDPeerPath("10.0.0.1")

	// Interface should not be set when empty.
	ifaceUpdate := findUpdatePath(edits, peerBase+bfdInterface)
	if ifaceUpdate != nil {
		t.Error("expected no interface update when interface is empty")
	}
}

func TestBFDRemovePeer(t *testing.T) {
	client, mock := setupTest(t)
	ctx := context.Background()

	err := client.RemoveBFDPeer(ctx, "192.168.1.1")
	if err != nil {
		t.Fatalf("RemoveBFDPeer failed: %v", err)
	}

	edits := mock.getEdits()
	peerBase := BFDPeerPath("192.168.1.1")

	del := findDeletePath(edits, peerBase)
	if del == nil {
		t.Fatal("expected delete for BFD peer path")
	}
}

// --- OSPF tests ---

func TestOSPFEnableInterface(t *testing.T) {
	client, mock := setupTest(t)
	ctx := context.Background()

	err := client.EnableOSPFInterface(ctx, "eth0", "0.0.0.0", true, 10, 5, 20)
	if err != nil {
		t.Fatalf("EnableOSPFInterface failed: %v", err)
	}

	edits := mock.getEdits()
	ifacePath := OSPFInterfacePath("eth0", "0.0.0.0")

	passiveUpdate := findUpdatePath(edits, ifacePath+ospfInterfacePassive)
	if passiveUpdate == nil {
		t.Fatal("expected update for OSPF passive")
	}
	if passiveUpdate.GetValue() != "true" {
		t.Errorf("expected passive 'true', got '%s'", passiveUpdate.GetValue())
	}

	costUpdate := findUpdatePath(edits, ifacePath+ospfInterfaceCost)
	if costUpdate == nil {
		t.Fatal("expected update for OSPF cost")
	}
	if costUpdate.GetValue() != "10" {
		t.Errorf("expected cost '10', got '%s'", costUpdate.GetValue())
	}

	helloUpdate := findUpdatePath(edits, ifacePath+ospfInterfaceHelloInterval)
	if helloUpdate == nil {
		t.Fatal("expected update for OSPF hello-interval")
	}
	if helloUpdate.GetValue() != "5" {
		t.Errorf("expected hello '5', got '%s'", helloUpdate.GetValue())
	}

	deadUpdate := findUpdatePath(edits, ifacePath+ospfInterfaceDeadInterval)
	if deadUpdate == nil {
		t.Fatal("expected update for OSPF dead-interval")
	}
	if deadUpdate.GetValue() != "20" {
		t.Errorf("expected dead '20', got '%s'", deadUpdate.GetValue())
	}
}

func TestOSPFEnableInterfaceDefaultTimers(t *testing.T) {
	client, mock := setupTest(t)
	ctx := context.Background()

	err := client.EnableOSPFInterface(ctx, "eth1", "0.0.0.1", false, 0, 0, 0)
	if err != nil {
		t.Fatalf("EnableOSPFInterface failed: %v", err)
	}

	edits := mock.getEdits()
	ifacePath := OSPFInterfacePath("eth1", "0.0.0.1")

	passiveUpdate := findUpdatePath(edits, ifacePath+ospfInterfacePassive)
	if passiveUpdate == nil {
		t.Fatal("expected update for OSPF passive")
	}
	if passiveUpdate.GetValue() != "false" {
		t.Errorf("expected passive 'false', got '%s'", passiveUpdate.GetValue())
	}

	// Optional fields should not be present when 0.
	costUpdate := findUpdatePath(edits, ifacePath+ospfInterfaceCost)
	if costUpdate != nil {
		t.Error("expected no cost update when value is 0")
	}

	helloUpdate := findUpdatePath(edits, ifacePath+ospfInterfaceHelloInterval)
	if helloUpdate != nil {
		t.Error("expected no hello-interval update when value is 0")
	}

	deadUpdate := findUpdatePath(edits, ifacePath+ospfInterfaceDeadInterval)
	if deadUpdate != nil {
		t.Error("expected no dead-interval update when value is 0")
	}
}

func TestOSPFDisableInterface(t *testing.T) {
	client, mock := setupTest(t)
	ctx := context.Background()

	err := client.DisableOSPFInterface(ctx, "eth0", "0.0.0.0")
	if err != nil {
		t.Fatalf("DisableOSPFInterface failed: %v", err)
	}

	edits := mock.getEdits()
	ifacePath := OSPFInterfacePath("eth0", "0.0.0.0")

	del := findDeletePath(edits, ifacePath)
	if del == nil {
		t.Fatal("expected delete for OSPF interface path")
	}
}

// --- Path helper tests ---

func TestBGPNeighborPath(t *testing.T) {
	path := BGPNeighborPath("192.168.1.1")
	expected := bgpBase + "/neighbors/neighbor[remote-address='192.168.1.1']"
	if path != expected {
		t.Errorf("BGPNeighborPath mismatch:\n  got:  %s\n  want: %s", path, expected)
	}
}

func TestBGPNetworkPath(t *testing.T) {
	path := BGPNetworkPath("10.0.0.0/24", bgpAFIIPv4Unicast)
	expected := bgpBase + "/global/afi-safis/afi-safi[afi-safi-name='frr-routing:ipv4-unicast']/network-config[prefix='10.0.0.0/24']"
	if path != expected {
		t.Errorf("BGPNetworkPath mismatch:\n  got:  %s\n  want: %s", path, expected)
	}
}

func TestBFDPeerPath(t *testing.T) {
	path := BFDPeerPath("192.168.1.1")
	expected := "/frr-bfdd:bfdd/bfd/sessions/single-hop[dest-addr='192.168.1.1']"
	if path != expected {
		t.Errorf("BFDPeerPath mismatch:\n  got:  %s\n  want: %s", path, expected)
	}
}

func TestOSPFInterfacePath(t *testing.T) {
	path := OSPFInterfacePath("eth0", "0.0.0.0")
	expected := ospfBase + "/areas/area[area-id='0.0.0.0']/interfaces/interface[name='eth0']"
	if path != expected {
		t.Errorf("OSPFInterfacePath mismatch:\n  got:  %s\n  want: %s", path, expected)
	}
}

func TestResolveAFI(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"ipv4", bgpAFIIPv4Unicast},
		{"ipv4-unicast", bgpAFIIPv4Unicast},
		{"ipv6", bgpAFIIPv6Unicast},
		{"ipv6-unicast", bgpAFIIPv6Unicast},
		{"frr-routing:ipv4-unicast", "frr-routing:ipv4-unicast"},
		{"custom-afi", "custom-afi"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := resolveAFI(tt.input)
			if result != tt.expected {
				t.Errorf("resolveAFI(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
