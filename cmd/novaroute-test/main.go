// novaroute-test exercises the full NovaRoute gRPC API end-to-end.
// It verifies: registration, policy enforcement, peer management,
// prefix advertisement, status queries, and cleanup.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	v1 "github.com/piwi3910/NovaRoute/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

var (
	socket = "/run/novaroute/novaroute.sock"
	passed = 0
	failed = 0
)

func main() {
	os.Exit(run())
}

func run() int {
	if s := os.Getenv("NOVAROUTE_SOCKET"); s != "" {
		socket = s
	}

	conn, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: connect: %v\n", err)
		return 1
	}
	defer conn.Close()
	client := v1.NewRouteControlClient(conn)

	fmt.Println("========================================")
	fmt.Println("  NovaRoute End-to-End Test Suite")
	fmt.Println("========================================")
	fmt.Println()

	testSessionManagement(client)
	testBGPPeerManagement(client)
	testPrefixAdvertisement(client)
	testStatusVerification(client)
	testNovanetOwnerPolicy(client)
	testCleanupOperations(client)
	testFinalCleanState(client)

	// --- Summary ---
	fmt.Println()
	fmt.Println("========================================")
	fmt.Printf("  RESULTS: %d passed, %d failed\n", passed, failed)
	fmt.Println("========================================")

	if failed > 0 {
		return 1
	}
	return 0
}

func testSessionManagement(client v1.RouteControlClient) {
	section("1. SESSION MANAGEMENT")

	test("Register novaedge owner", func() error {
		resp, err := client.Register(ctx(), &v1.RegisterRequest{
			Owner: "novaedge",
			Token: "novaedge-test-token-2024",
		})
		if err != nil {
			return err
		}
		if resp.SessionId == "" {
			return fmt.Errorf("expected non-empty session_id")
		}
		fmt.Printf("    session_id=%s\n", resp.SessionId)
		return nil
	})

	test("Register admin owner", func() error {
		resp, err := client.Register(ctx(), &v1.RegisterRequest{
			Owner: "admin",
			Token: "admin-test-token-2024",
		})
		if err != nil {
			return err
		}
		if resp.SessionId == "" {
			return fmt.Errorf("expected non-empty session_id")
		}
		return nil
	})

	test("Reject bad token", func() error {
		_, err := client.Register(ctx(), &v1.RegisterRequest{
			Owner: "novaedge",
			Token: "wrong-token",
		})
		if err == nil {
			return fmt.Errorf("expected error for bad token, got success")
		}
		st, ok := status.FromError(err)
		if !ok || st.Code() != codes.Unauthenticated {
			return fmt.Errorf("expected Unauthenticated, got %v", err)
		}
		fmt.Printf("    correctly rejected: %s\n", st.Message())
		return nil
	})

	test("Reject unknown owner", func() error {
		_, err := client.Register(ctx(), &v1.RegisterRequest{
			Owner: "unknown",
			Token: "anything",
		})
		if err == nil {
			return fmt.Errorf("expected error for unknown owner")
		}
		st, ok := status.FromError(err)
		if !ok || st.Code() != codes.Unauthenticated {
			return fmt.Errorf("expected Unauthenticated, got %v", err)
		}
		return nil
	})
}

func testBGPPeerManagement(client v1.RouteControlClient) {
	section("2. BGP PEER MANAGEMENT")

	test("ApplyPeer as admin (192.168.100.1 AS 65001)", func() error {
		_, err := client.ApplyPeer(ctx(), &v1.ApplyPeerRequest{
			Owner: "admin",
			Token: "admin-test-token-2024",
			Peer: &v1.BGPPeer{
				NeighborAddress: "192.168.100.1",
				RemoteAs:        65001,
				PeerType:        v1.PeerType_PEER_TYPE_EXTERNAL,
				Keepalive:       30,
				HoldTime:        90,
				Description:     "test-peer-admin",
				AddressFamilies: []v1.AddressFamily{v1.AddressFamily_ADDRESS_FAMILY_IPV4_UNICAST},
			},
		})
		return err
	})

	test("ApplyPeer as novaedge (10.0.0.1 AS 65002)", func() error {
		_, err := client.ApplyPeer(ctx(), &v1.ApplyPeerRequest{
			Owner: "novaedge",
			Token: "novaedge-test-token-2024",
			Peer: &v1.BGPPeer{
				NeighborAddress: "10.0.0.1",
				RemoteAs:        65002,
				PeerType:        v1.PeerType_PEER_TYPE_INTERNAL,
				Keepalive:       30,
				HoldTime:        90,
				Description:     "test-peer-novaedge",
			},
		})
		return err
	})
}

func testPrefixAdvertisement(client v1.RouteControlClient) {
	section("3. PREFIX ADVERTISEMENT")

	test("AdvertisePrefix /32 as novaedge (should succeed)", func() error {
		_, err := client.AdvertisePrefix(ctx(), &v1.AdvertisePrefixRequest{
			Owner:    "novaedge",
			Token:    "novaedge-test-token-2024",
			Prefix:   "10.0.0.100/32",
			Protocol: v1.Protocol_PROTOCOL_BGP,
			Attributes: &v1.PrefixAttributes{
				LocalPreference: 100,
				Communities:     []string{"65000:100"},
			},
		})
		return err
	})

	test("AdvertisePrefix /24 as novaedge (should be rejected)", func() error {
		_, err := client.AdvertisePrefix(ctx(), &v1.AdvertisePrefixRequest{
			Owner:    "novaedge",
			Token:    "novaedge-test-token-2024",
			Prefix:   "10.0.0.0/24",
			Protocol: v1.Protocol_PROTOCOL_BGP,
		})
		if err == nil {
			return fmt.Errorf("expected rejection for /24 prefix with host_only policy")
		}
		st, _ := status.FromError(err)
		fmt.Printf("    correctly rejected: %s (code=%s)\n", st.Message(), st.Code())
		return nil
	})

	test("AdvertisePrefix /24 as admin (should succeed)", func() error {
		_, err := client.AdvertisePrefix(ctx(), &v1.AdvertisePrefixRequest{
			Owner:    "admin",
			Token:    "admin-test-token-2024",
			Prefix:   "172.16.0.0/24",
			Protocol: v1.Protocol_PROTOCOL_BGP,
			Attributes: &v1.PrefixAttributes{
				LocalPreference: 200,
				Med:             50,
			},
		})
		return err
	})

	test("AdvertisePrefix outside allowed CIDR as novaedge (should be rejected)", func() error {
		_, err := client.AdvertisePrefix(ctx(), &v1.AdvertisePrefixRequest{
			Owner:    "novaedge",
			Token:    "novaedge-test-token-2024",
			Prefix:   "8.8.8.8/32",
			Protocol: v1.Protocol_PROTOCOL_BGP,
		})
		if err == nil {
			return fmt.Errorf("expected rejection for prefix outside allowed CIDRs")
		}
		st, _ := status.FromError(err)
		fmt.Printf("    correctly rejected: %s (code=%s)\n", st.Message(), st.Code())
		return nil
	})
}

func testStatusVerification(client v1.RouteControlClient) {
	section("4. STATUS VERIFICATION")

	test("GetStatus shows peers and prefixes", func() error {
		resp, err := client.GetStatus(ctx(), &v1.GetStatusRequest{})
		if err != nil {
			return err
		}
		fmt.Printf("    peers=%d prefixes=%d bfd=%d ospf=%d\n",
			len(resp.Peers), len(resp.Prefixes), len(resp.BfdSessions), len(resp.OspfInterfaces))
		if len(resp.Peers) != 2 {
			return fmt.Errorf("expected 2 peers, got %d", len(resp.Peers))
		}
		if len(resp.Prefixes) != 2 {
			return fmt.Errorf("expected 2 prefixes, got %d", len(resp.Prefixes))
		}
		for _, p := range resp.Peers {
			fmt.Printf("    peer: %s AS %d owner=%s\n", p.NeighborAddress, p.RemoteAs, p.Owner)
		}
		for _, p := range resp.Prefixes {
			fmt.Printf("    prefix: %s proto=%s owner=%s\n", p.Prefix, p.Protocol, p.Owner)
		}
		return nil
	})

	test("GetStatus filtered by owner=novaedge", func() error {
		resp, err := client.GetStatus(ctx(), &v1.GetStatusRequest{
			OwnerFilter: "novaedge",
		})
		if err != nil {
			return err
		}
		if len(resp.Peers) != 1 {
			return fmt.Errorf("expected 1 novaedge peer, got %d", len(resp.Peers))
		}
		if len(resp.Prefixes) != 1 {
			return fmt.Errorf("expected 1 novaedge prefix, got %d", len(resp.Prefixes))
		}
		fmt.Printf("    novaedge: 1 peer, 1 prefix (correct)\n")
		return nil
	})
}

func testNovanetOwnerPolicy(client v1.RouteControlClient) {
	section("5. NOVANET OWNER POLICY")

	test("Register novanet", func() error {
		_, err := client.Register(ctx(), &v1.RegisterRequest{
			Owner: "novanet",
			Token: "novanet-test-token-2024",
		})
		return err
	})

	test("AdvertisePrefix /16 subnet as novanet (should succeed)", func() error {
		_, err := client.AdvertisePrefix(ctx(), &v1.AdvertisePrefixRequest{
			Owner:    "novanet",
			Token:    "novanet-test-token-2024",
			Prefix:   "10.244.0.0/16",
			Protocol: v1.Protocol_PROTOCOL_BGP,
		})
		return err
	})

	test("AdvertisePrefix /32 as novanet (should be rejected)", func() error {
		_, err := client.AdvertisePrefix(ctx(), &v1.AdvertisePrefixRequest{
			Owner:    "novanet",
			Token:    "novanet-test-token-2024",
			Prefix:   "10.244.1.1/32",
			Protocol: v1.Protocol_PROTOCOL_BGP,
		})
		if err == nil {
			return fmt.Errorf("expected rejection for /32 with subnet policy")
		}
		st, _ := status.FromError(err)
		fmt.Printf("    correctly rejected: %s (code=%s)\n", st.Message(), st.Code())
		return nil
	})
}

func testCleanupOperations(client v1.RouteControlClient) {
	section("6. CLEANUP OPERATIONS")

	test("WithdrawPrefix 10.0.0.100/32 as novaedge", func() error {
		_, err := client.WithdrawPrefix(ctx(), &v1.WithdrawPrefixRequest{
			Owner:    "novaedge",
			Token:    "novaedge-test-token-2024",
			Prefix:   "10.0.0.100/32",
			Protocol: v1.Protocol_PROTOCOL_BGP,
		})
		return err
	})

	test("RemovePeer 10.0.0.1 as novaedge", func() error {
		_, err := client.RemovePeer(ctx(), &v1.RemovePeerRequest{
			Owner:           "novaedge",
			Token:           "novaedge-test-token-2024",
			NeighborAddress: "10.0.0.1",
		})
		return err
	})

	test("Verify novaedge has no peers/prefixes after cleanup", func() error {
		resp, err := client.GetStatus(ctx(), &v1.GetStatusRequest{
			OwnerFilter: "novaedge",
		})
		if err != nil {
			return err
		}
		if len(resp.Peers) != 0 {
			return fmt.Errorf("expected 0 novaedge peers, got %d", len(resp.Peers))
		}
		if len(resp.Prefixes) != 0 {
			return fmt.Errorf("expected 0 novaedge prefixes, got %d", len(resp.Prefixes))
		}
		fmt.Printf("    novaedge: 0 peers, 0 prefixes (clean)\n")
		return nil
	})

	test("Admin still has 1 peer and 1 prefix", func() error {
		resp, err := client.GetStatus(ctx(), &v1.GetStatusRequest{
			OwnerFilter: "admin",
		})
		if err != nil {
			return err
		}
		if len(resp.Peers) != 1 {
			return fmt.Errorf("expected 1 admin peer, got %d", len(resp.Peers))
		}
		if len(resp.Prefixes) != 1 {
			return fmt.Errorf("expected 1 admin prefix, got %d", len(resp.Prefixes))
		}
		fmt.Printf("    admin: 1 peer, 1 prefix (intact)\n")
		return nil
	})

	test("Deregister admin with withdraw_all=true", func() error {
		_, err := client.Deregister(ctx(), &v1.DeregisterRequest{
			Owner:       "admin",
			Token:       "admin-test-token-2024",
			WithdrawAll: true,
		})
		return err
	})

	test("Verify admin is fully cleaned after deregister", func() error {
		resp, err := client.GetStatus(ctx(), &v1.GetStatusRequest{
			OwnerFilter: "admin",
		})
		if err != nil {
			return err
		}
		if len(resp.Peers) != 0 {
			return fmt.Errorf("expected 0 admin peers, got %d", len(resp.Peers))
		}
		if len(resp.Prefixes) != 0 {
			return fmt.Errorf("expected 0 admin prefixes, got %d", len(resp.Prefixes))
		}
		fmt.Printf("    admin: 0 peers, 0 prefixes (deregistered)\n")
		return nil
	})

	test("Deregister novanet with withdraw_all=true", func() error {
		_, err := client.Deregister(ctx(), &v1.DeregisterRequest{
			Owner:       "novanet",
			Token:       "novanet-test-token-2024",
			WithdrawAll: true,
		})
		return err
	})
}

func testFinalCleanState(client v1.RouteControlClient) {
	section("7. FINAL CLEAN STATE VERIFICATION")

	test("All resources cleaned up", func() error {
		resp, err := client.GetStatus(ctx(), &v1.GetStatusRequest{})
		if err != nil {
			return err
		}
		total := len(resp.Peers) + len(resp.Prefixes) + len(resp.BfdSessions) + len(resp.OspfInterfaces)
		if total != 0 {
			return fmt.Errorf("expected 0 total resources, got %d (peers=%d prefixes=%d bfd=%d ospf=%d)",
				total, len(resp.Peers), len(resp.Prefixes), len(resp.BfdSessions), len(resp.OspfInterfaces))
		}
		fmt.Printf("    all resources: 0 (system is clean)\n")
		return nil
	})
}

func ctx() context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = cancel // leak is acceptable in short-lived test binary
	return c
}

func section(name string) {
	fmt.Printf("\n--- %s ---\n\n", name)
}

func test(name string, fn func() error) {
	fmt.Printf("  [TEST] %s ... ", name)
	if err := fn(); err != nil {
		fmt.Printf("FAIL\n    error: %v\n", err)
		failed++
	} else {
		fmt.Printf("PASS\n")
		passed++
	}
}
