package frr

import (
	"context"
	"sort"
	"testing"
)

// testPeerAddr is a commonly used test IP address.
const testPeerAddr = "10.0.0.1"

// ---------------------------------------------------------------------------
// BGP neighbor JSON parsing tests
// ---------------------------------------------------------------------------

func TestParseBGPNeighborsJSON_Empty(t *testing.T) {
	for _, input := range []string{"", "  ", "{}"} {
		result, err := parseBGPNeighborsJSON(input)
		if err != nil {
			t.Errorf("parseBGPNeighborsJSON(%q) error = %v", input, err)
		}
		if result != nil {
			t.Errorf("parseBGPNeighborsJSON(%q) = %v, want nil", input, result)
		}
	}
}

func TestParseBGPNeighborsJSON_SinglePeer(t *testing.T) {
	input := `{
		"192.168.100.1": {
			"remoteAs": 65000,
			"bgpState": "Established",
			"bgpTimerUpString": "01:23:45",
			"msgRcvd": 100,
			"msgSent": 50
		}
	}`

	result, err := parseBGPNeighborsJSON(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("got %d neighbors, want 1", len(result))
	}

	nbr := result[0]
	if nbr.Address != "192.168.100.1" {
		t.Errorf("Address = %q, want %q", nbr.Address, "192.168.100.1")
	}
	if nbr.RemoteAS != 65000 {
		t.Errorf("RemoteAS = %d, want 65000", nbr.RemoteAS)
	}
	if nbr.State != "Established" {
		t.Errorf("State = %q, want %q", nbr.State, "Established")
	}
	if nbr.UpTime != "01:23:45" {
		t.Errorf("UpTime = %q, want %q", nbr.UpTime, "01:23:45")
	}
	if nbr.MsgRcvd != 100 {
		t.Errorf("MsgRcvd = %d, want 100", nbr.MsgRcvd)
	}
	if nbr.MsgSent != 50 {
		t.Errorf("MsgSent = %d, want 50", nbr.MsgSent)
	}
}

func TestParseBGPNeighborsJSON_MultiplePeers(t *testing.T) {
	input := `{
		"192.168.100.1": {
			"remoteAs": 65000,
			"bgpState": "Established",
			"bgpTimerUpString": "02:00:00",
			"msgRcvd": 200,
			"msgSent": 150
		},
		"10.0.0.2": {
			"remoteAs": 65001,
			"bgpState": "Connect",
			"bgpTimerUpString": "",
			"msgRcvd": 0,
			"msgSent": 5
		}
	}`

	result, err := parseBGPNeighborsJSON(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("got %d neighbors, want 2", len(result))
	}

	// Sort by address for deterministic comparison.
	sort.Slice(result, func(i, j int) bool {
		return result[i].Address < result[j].Address
	})

	if result[0].Address != "10.0.0.2" {
		t.Errorf("result[0].Address = %q, want %q", result[0].Address, "10.0.0.2")
	}
	if result[0].State != "Connect" {
		t.Errorf("result[0].State = %q, want %q", result[0].State, "Connect")
	}
	if result[0].RemoteAS != 65001 {
		t.Errorf("result[0].RemoteAS = %d, want 65001", result[0].RemoteAS)
	}

	if result[1].Address != "192.168.100.1" {
		t.Errorf("result[1].Address = %q, want %q", result[1].Address, "192.168.100.1")
	}
	if result[1].State != "Established" {
		t.Errorf("result[1].State = %q, want %q", result[1].State, "Established")
	}
}

func TestParseBGPNeighborsJSON_DifferentStates(t *testing.T) {
	tests := []struct {
		name  string
		state string
	}{
		{"Idle", "Idle"},
		{"Connect", "Connect"},
		{"Active", "Active"},
		{"OpenSent", "OpenSent"},
		{"OpenConfirm", "OpenConfirm"},
		{"Established", "Established"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := `{ "10.0.0.1": { "remoteAs": 65000, "bgpState": "` + tt.state + `", "bgpTimerUpString": "", "msgRcvd": 0, "msgSent": 0 } }`
			result, err := parseBGPNeighborsJSON(input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(result) != 1 {
				t.Fatalf("got %d neighbors, want 1", len(result))
			}
			if result[0].State != tt.state {
				t.Errorf("State = %q, want %q", result[0].State, tt.state)
			}
		})
	}
}

func TestParseBGPNeighborsJSON_Malformed(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"not JSON", "this is not json"},
		{"truncated", `{ "10.0.0.1": { "remoteAs": `},
		{"bad value type", `{ "10.0.0.1": "not an object" }`},
		{"array instead of object", `[{"remoteAs": 65000}]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseBGPNeighborsJSON(tt.input)
			if err == nil {
				t.Error("expected error for malformed JSON input")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BFD peer JSON parsing tests
// ---------------------------------------------------------------------------

func TestParseBFDPeersJSON_Empty(t *testing.T) {
	for _, input := range []string{"", "  ", "[]"} {
		result, err := parseBFDPeersJSON(input)
		if err != nil {
			t.Errorf("parseBFDPeersJSON(%q) error = %v", input, err)
		}
		if result != nil {
			t.Errorf("parseBFDPeersJSON(%q) = %v, want nil", input, result)
		}
	}
}

func TestParseBFDPeersJSON_SinglePeer(t *testing.T) {
	input := `[{ "peer": "10.0.0.1", "interface": "eth0", "status": "up", "uptime": 3661 }]`

	result, err := parseBFDPeersJSON(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("got %d peers, want 1", len(result))
	}

	p := result[0]
	if p.PeerAddress != testPeerAddr {
		t.Errorf("PeerAddress = %q, want %q", p.PeerAddress, testPeerAddr)
	}
	if p.Interface != "eth0" {
		t.Errorf("Interface = %q, want %q", p.Interface, "eth0")
	}
	if p.Status != "up" {
		t.Errorf("Status = %q, want %q", p.Status, "up")
	}
	if p.Uptime != "1h1m1s" {
		t.Errorf("Uptime = %q, want %q", p.Uptime, "1h1m1s")
	}
}

func TestParseBFDPeersJSON_MultiplePeers(t *testing.T) {
	input := `[
		{ "peer": "10.0.0.1", "interface": "eth0", "status": "up", "uptime": 120 },
		{ "peer": "10.0.0.2", "interface": "eth1", "status": "down", "uptime": 0 },
		{ "peer": "10.0.0.3", "interface": "", "status": "init", "uptime": 0 }
	]`

	result, err := parseBFDPeersJSON(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("got %d peers, want 3", len(result))
	}

	if result[0].Status != "up" {
		t.Errorf("result[0].Status = %q, want %q", result[0].Status, "up")
	}
	if result[1].Status != "down" {
		t.Errorf("result[1].Status = %q, want %q", result[1].Status, "down")
	}
	if result[2].Status != "init" {
		t.Errorf("result[2].Status = %q, want %q", result[2].Status, "init")
	}
	if result[2].Interface != "" {
		t.Errorf("result[2].Interface = %q, want empty", result[2].Interface)
	}
}

func TestParseBFDPeersJSON_Malformed(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"not JSON", "garbage"},
		{"object instead of array", `{ "peer": "10.0.0.1" }`},
		{"truncated", `[{ "peer": "10.0.0.1", `},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseBFDPeersJSON(tt.input)
			if err == nil {
				t.Error("expected error for malformed JSON input")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// OSPF neighbor JSON parsing tests
// ---------------------------------------------------------------------------

func TestParseOSPFNeighborsJSON_Empty(t *testing.T) {
	for _, input := range []string{"", "  ", "{}", `{ "neighbors": {} }`} {
		result, err := parseOSPFNeighborsJSON(input)
		if err != nil {
			t.Errorf("parseOSPFNeighborsJSON(%q) error = %v", input, err)
		}
		if result != nil {
			t.Errorf("parseOSPFNeighborsJSON(%q) = %v, want nil", input, result)
		}
	}
}

func TestParseOSPFNeighborsJSON_SingleNeighbor(t *testing.T) {
	input := `{
		"neighbors": {
			"10.0.0.1": [{
				"nbrState": "Full/DR",
				"ifaceName": "eth0",
				"srcAddress": "192.168.1.1"
			}]
		}
	}`

	result, err := parseOSPFNeighborsJSON(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("got %d neighbors, want 1", len(result))
	}

	nbr := result[0]
	if nbr.NeighborID != testPeerAddr {
		t.Errorf("NeighborID = %q, want %q", nbr.NeighborID, testPeerAddr)
	}
	if nbr.Address != "192.168.1.1" {
		t.Errorf("Address = %q, want %q", nbr.Address, "192.168.1.1")
	}
	if nbr.Interface != "eth0" {
		t.Errorf("Interface = %q, want %q", nbr.Interface, "eth0")
	}
	if nbr.State != "Full" {
		t.Errorf("State = %q, want %q", nbr.State, "Full")
	}
}

func TestParseOSPFNeighborsJSON_StateExtraction(t *testing.T) {
	// The OSPF state string includes the role suffix after "/".
	// We should extract just the state part.
	tests := []struct {
		name     string
		nbrState string
		want     string
	}{
		{"Full/DR", "Full/DR", "Full"},
		{"Full/BDR", "Full/BDR", "Full"},
		{"2-Way/DROther", "2-Way/DROther", "2-Way"},
		{"Init", "Init", "Init"},
		{"ExStart/DR", "ExStart/DR", "ExStart"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := `{ "neighbors": { "1.1.1.1": [{ "nbrState": "` + tt.nbrState + `", "ifaceName": "eth0" }] } }`
			result, err := parseOSPFNeighborsJSON(input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(result) != 1 {
				t.Fatalf("got %d neighbors, want 1", len(result))
			}
			if result[0].State != tt.want {
				t.Errorf("State = %q, want %q", result[0].State, tt.want)
			}
		})
	}
}

func TestParseOSPFNeighborsJSON_MultipleNeighbors(t *testing.T) {
	input := `{
		"neighbors": {
			"10.0.0.1": [{
				"nbrState": "Full/DR",
				"ifaceName": "eth0",
				"srcAddress": "192.168.1.1"
			}],
			"10.0.0.2": [{
				"nbrState": "2-Way/DROther",
				"ifaceName": "eth1",
				"srcAddress": "192.168.1.2"
			}]
		}
	}`

	result, err := parseOSPFNeighborsJSON(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("got %d neighbors, want 2", len(result))
	}

	// Sort for deterministic comparison.
	sort.Slice(result, func(i, j int) bool {
		return result[i].NeighborID < result[j].NeighborID
	})

	if result[0].NeighborID != testPeerAddr {
		t.Errorf("result[0].NeighborID = %q, want %q", result[0].NeighborID, testPeerAddr)
	}
	if result[0].State != "Full" {
		t.Errorf("result[0].State = %q, want %q", result[0].State, "Full")
	}
	if result[1].NeighborID != "10.0.0.2" {
		t.Errorf("result[1].NeighborID = %q, want %q", result[1].NeighborID, "10.0.0.2")
	}
	if result[1].State != "2-Way" {
		t.Errorf("result[1].State = %q, want %q", result[1].State, "2-Way")
	}
}

func TestParseOSPFNeighborsJSON_FallbackAddress(t *testing.T) {
	// When srcAddress is absent, should fall back to neighbor ID.
	input := `{
		"neighbors": {
			"10.0.0.1": [{
				"nbrState": "Full/DR",
				"ifaceName": "eth0"
			}]
		}
	}`

	result, err := parseOSPFNeighborsJSON(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("got %d neighbors, want 1", len(result))
	}
	if result[0].Address != testPeerAddr {
		t.Errorf("Address = %q, want %q (fallback to neighbor ID)", result[0].Address, testPeerAddr)
	}
}

func TestParseOSPFNeighborsJSON_MultipleEntriesPerNeighbor(t *testing.T) {
	// A neighbor ID can map to multiple entries (e.g., multi-area adjacencies).
	input := `{
		"neighbors": {
			"10.0.0.1": [
				{ "nbrState": "Full/DR", "ifaceName": "eth0", "srcAddress": "192.168.1.1" },
				{ "nbrState": "Full/BDR", "ifaceName": "eth1", "srcAddress": "192.168.2.1" }
			]
		}
	}`

	result, err := parseOSPFNeighborsJSON(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("got %d neighbors, want 2", len(result))
	}

	// Both should have the same neighbor ID.
	for i, nbr := range result {
		if nbr.NeighborID != testPeerAddr {
			t.Errorf("result[%d].NeighborID = %q, want %q", i, nbr.NeighborID, testPeerAddr)
		}
	}
}

func TestParseOSPFNeighborsJSON_Malformed(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"not JSON", "not json"},
		{"truncated", `{ "neighbors": { "10.0.0.1": [`},
		{"wrong neighbors type", `{ "neighbors": "not a map" }`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseOSPFNeighborsJSON(tt.input)
			if err == nil {
				t.Error("expected error for malformed JSON input")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Integration-style tests using the fake vtysh (GetXxx methods)
// ---------------------------------------------------------------------------

func TestParseBGPNeighborsJSON_ViaGetBGPNeighbors(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	setFakeResponse(t, dir, `{
		"10.0.0.1": {
			"remoteAs": 65001,
			"bgpState": "Established",
			"bgpTimerUpString": "00:30:00",
			"msgRcvd": 42,
			"msgSent": 38
		}
	}`)

	result, err := client.GetBGPNeighbors(context.Background())
	if err != nil {
		t.Fatalf("GetBGPNeighbors error: %v", err)
	}

	cmd := readRecordedShowCmd(t, dir)
	if cmd != "show bgp neighbors json" {
		t.Errorf("show command = %q, want %q", cmd, "show bgp neighbors json")
	}

	if len(result) != 1 {
		t.Fatalf("got %d neighbors, want 1", len(result))
	}
	if result[0].Address != testPeerAddr {
		t.Errorf("Address = %q, want %q", result[0].Address, testPeerAddr)
	}
}

func TestParseBFDPeersJSON_ViaGetBFDPeers(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	setFakeResponse(t, dir, `[{ "peer": "10.0.0.1", "interface": "eth0", "status": "up", "uptime": 60 }]`)

	result, err := client.GetBFDPeers(context.Background())
	if err != nil {
		t.Fatalf("GetBFDPeers error: %v", err)
	}

	cmd := readRecordedShowCmd(t, dir)
	if cmd != "show bfd peers json" {
		t.Errorf("show command = %q, want %q", cmd, "show bfd peers json")
	}

	if len(result) != 1 {
		t.Fatalf("got %d peers, want 1", len(result))
	}
	if result[0].PeerAddress != testPeerAddr {
		t.Errorf("PeerAddress = %q, want %q", result[0].PeerAddress, testPeerAddr)
	}
}

func TestParseOSPFNeighborsJSON_ViaGetOSPFNeighbors(t *testing.T) {
	client, dir := setupFakeVtysh(t)
	setFakeResponse(t, dir, `{
		"neighbors": {
			"10.0.0.1": [{ "nbrState": "Full/DR", "ifaceName": "eth0", "srcAddress": "192.168.1.1" }]
		}
	}`)

	result, err := client.GetOSPFNeighbors(context.Background())
	if err != nil {
		t.Fatalf("GetOSPFNeighbors error: %v", err)
	}

	cmd := readRecordedShowCmd(t, dir)
	if cmd != "show ip ospf neighbor json" {
		t.Errorf("show command = %q, want %q", cmd, "show ip ospf neighbor json")
	}

	if len(result) != 1 {
		t.Fatalf("got %d neighbors, want 1", len(result))
	}
	if result[0].NeighborID != testPeerAddr {
		t.Errorf("NeighborID = %q, want %q", result[0].NeighborID, testPeerAddr)
	}
}
