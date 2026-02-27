package frr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// BGPNeighborState holds the parsed state of a single BGP neighbor from FRR.
type BGPNeighborState struct {
	Address          string
	RemoteAS         uint32
	State            string // "Established", "Connect", "Idle", etc.
	UpTime           string
	MsgRcvd          uint64
	MsgSent          uint64
	PrefixesReceived uint32
	PrefixesSent     uint32
}

// BFDPeerState holds the parsed state of a single BFD peer from FRR.
type BFDPeerState struct {
	PeerAddress string
	Interface   string
	Status      string // "up", "down", "init"
	Uptime      string
}

// OSPFNeighborState holds the parsed state of a single OSPF neighbor from FRR.
type OSPFNeighborState struct {
	NeighborID string
	Address    string
	Interface  string
	State      string // "Full", "2-Way", etc.
}

// GetBGPNeighbors queries FRR for BGP neighbor state via "show bgp neighbors json".
func (c *Client) GetBGPNeighbors(ctx context.Context) ([]BGPNeighborState, error) {
	output, err := c.runShow(ctx, "show bgp neighbors json")
	if err != nil {
		return nil, fmt.Errorf("frr: get BGP neighbors: %w", err)
	}
	return parseBGPNeighborsJSON(output)
}

// GetBFDPeers queries FRR for BFD peer state via "show bfd peers json".
func (c *Client) GetBFDPeers(ctx context.Context) ([]BFDPeerState, error) {
	output, err := c.runShow(ctx, "show bfd peers json")
	if err != nil {
		return nil, fmt.Errorf("frr: get BFD peers: %w", err)
	}
	return parseBFDPeersJSON(output)
}

// GetOSPFNeighbors queries FRR for OSPF neighbor state via "show ip ospf neighbor json".
func (c *Client) GetOSPFNeighbors(ctx context.Context) ([]OSPFNeighborState, error) {
	output, err := c.runShow(ctx, "show ip ospf neighbor json")
	if err != nil {
		return nil, fmt.Errorf("frr: get OSPF neighbors: %w", err)
	}
	return parseOSPFNeighborsJSON(output)
}

// parseBGPNeighborsJSON parses FRR's "show bgp neighbors json" output.
// FRR format: { "<address>": { "remoteAs": N, "bgpState": "...", "bgpTimerUpString": "...", "msgRcvd": N, "msgSent": N }, ... }
func parseBGPNeighborsJSON(data string) ([]BGPNeighborState, error) {
	data = strings.TrimSpace(data)
	if data == "" || data == "{}" {
		return nil, nil
	}

	// Each neighbor is a top-level key mapping to its state object.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil, fmt.Errorf("parse BGP neighbors JSON: %w", err)
	}

	if len(raw) == 0 {
		return nil, nil
	}

	type addressFamilyEntry struct {
		AcceptedPrefixCounter uint32 `json:"acceptedPrefixCounter"`
		SentPrefixCounter     uint32 `json:"sentPrefixCounter"`
	}

	type bgpNeighborJSON struct {
		RemoteAS          uint32                        `json:"remoteAs"`
		BGPState          string                        `json:"bgpState"`
		BGPTimerUpString  string                        `json:"bgpTimerUpString"`
		MsgRcvd           uint64                        `json:"msgRcvd"`
		MsgSent           uint64                        `json:"msgSent"`
		AddressFamilyInfo map[string]addressFamilyEntry `json:"addressFamilyInfo"`
	}

	var result []BGPNeighborState
	for addr, rawVal := range raw {
		var nbr bgpNeighborJSON
		if err := json.Unmarshal(rawVal, &nbr); err != nil {
			return nil, fmt.Errorf("parse BGP neighbor %s: %w", addr, err)
		}
		var pfxRecv, pfxSent uint32
		for _, afi := range nbr.AddressFamilyInfo {
			pfxRecv += afi.AcceptedPrefixCounter
			pfxSent += afi.SentPrefixCounter
		}
		result = append(result, BGPNeighborState{
			Address:          addr,
			RemoteAS:         nbr.RemoteAS,
			State:            nbr.BGPState,
			UpTime:           nbr.BGPTimerUpString,
			MsgRcvd:          nbr.MsgRcvd,
			MsgSent:          nbr.MsgSent,
			PrefixesReceived: pfxRecv,
			PrefixesSent:     pfxSent,
		})
	}

	return result, nil
}

// parseBFDPeersJSON parses FRR's "show bfd peers json" output.
// FRR format: [{ "peer": "...", "interface": "...", "status": "..." }, ...]
func parseBFDPeersJSON(data string) ([]BFDPeerState, error) {
	data = strings.TrimSpace(data)
	if data == "" || data == "[]" {
		return nil, nil
	}

	type bfdPeerJSON struct {
		Peer      string `json:"peer"`
		Interface string `json:"interface"`
		Status    string `json:"status"`
		Uptime    uint64 `json:"uptime"`
	}

	var peers []bfdPeerJSON
	if err := json.Unmarshal([]byte(data), &peers); err != nil {
		return nil, fmt.Errorf("parse BFD peers JSON: %w", err)
	}

	if len(peers) == 0 {
		return nil, nil
	}

	result := make([]BFDPeerState, len(peers))
	for i, p := range peers {
		uptime := ""
		if p.Uptime > 0 {
			h := p.Uptime / 3600
			m := (p.Uptime % 3600) / 60
			s := p.Uptime % 60
			uptime = fmt.Sprintf("%dh%dm%ds", h, m, s)
		}
		result[i] = BFDPeerState{
			PeerAddress: p.Peer,
			Interface:   p.Interface,
			Status:      p.Status,
			Uptime:      uptime,
		}
	}

	return result, nil
}

// parseOSPFNeighborsJSON parses FRR's "show ip ospf neighbor json" output.
// FRR format: { "neighbors": { "<neighbor-id>": [{ "nbrState": "Full/DR", "ifaceName": "eth0" }] } }
func parseOSPFNeighborsJSON(data string) ([]OSPFNeighborState, error) {
	data = strings.TrimSpace(data)
	if data == "" || data == "{}" {
		return nil, nil
	}

	type ospfNeighborEntry struct {
		NbrState  string `json:"nbrState"`
		IfaceName string `json:"ifaceName"`
		// FRR also includes the neighbor's IP address in the entry.
		SrcAddress string `json:"srcAddress"`
	}

	type ospfJSON struct {
		Neighbors map[string][]ospfNeighborEntry `json:"neighbors"`
	}

	var parsed ospfJSON
	if err := json.Unmarshal([]byte(data), &parsed); err != nil {
		return nil, fmt.Errorf("parse OSPF neighbors JSON: %w", err)
	}

	if len(parsed.Neighbors) == 0 {
		return nil, nil
	}

	var result []OSPFNeighborState
	for nbrID, entries := range parsed.Neighbors {
		for _, entry := range entries {
			// Extract the base state (e.g., "Full" from "Full/DR").
			state := entry.NbrState
			if idx := strings.Index(state, "/"); idx >= 0 {
				state = state[:idx]
			}

			addr := entry.SrcAddress
			if addr == "" {
				// Fall back to the neighbor ID as the address.
				addr = nbrID
			}

			result = append(result, OSPFNeighborState{
				NeighborID: nbrID,
				Address:    addr,
				Interface:  entry.IfaceName,
				State:      state,
			})
		}
	}

	return result, nil
}
