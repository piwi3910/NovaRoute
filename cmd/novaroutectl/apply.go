// Package main implements the novaroutectl CLI tool.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	v1 "github.com/azrtydxb/NovaRoute/api/v1"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Sentinel errors for batch apply validation.
var (
	errUnknownAction = errors.New("unknown action")
	errMissingPeer   = errors.New("apply-peer requires a 'peer' field")
)

// intentAction represents a single intent in a batch apply file.
// The "action" field determines which RPC to invoke, and the remaining
// fields map to the corresponding request message.
type intentAction struct {
	// Action is the RPC operation to perform. Supported values:
	// apply-peer, remove-peer, advertise, withdraw,
	// enable-bfd, disable-bfd, enable-ospf, disable-ospf,
	// configure-bgp
	Action string `json:"action" yaml:"action"`

	// Common fields
	Owner string `json:"owner" yaml:"owner"`
	Token string `json:"token" yaml:"token"`

	// BGP global config
	LocalAS  uint32 `json:"local_as,omitempty" yaml:"local_as,omitempty"`
	RouterID string `json:"router_id,omitempty" yaml:"router_id,omitempty"`

	// Peer fields
	Peer *intentPeer `json:"peer,omitempty" yaml:"peer,omitempty"`

	// For remove-peer / disable-bfd
	NeighborAddress string `json:"neighbor_address,omitempty" yaml:"neighbor_address,omitempty"`

	// Prefix fields
	Prefix     string            `json:"prefix,omitempty" yaml:"prefix,omitempty"`
	Protocol   string            `json:"protocol,omitempty" yaml:"protocol,omitempty"`
	Attributes *intentAttributes `json:"attributes,omitempty" yaml:"attributes,omitempty"`

	// BFD fields
	PeerAddress      string `json:"peer_address,omitempty" yaml:"peer_address,omitempty"`
	MinRxMs          uint32 `json:"min_rx_ms,omitempty" yaml:"min_rx_ms,omitempty"`
	MinTxMs          uint32 `json:"min_tx_ms,omitempty" yaml:"min_tx_ms,omitempty"`
	DetectMultiplier uint32 `json:"detect_multiplier,omitempty" yaml:"detect_multiplier,omitempty"`
	InterfaceName    string `json:"interface_name,omitempty" yaml:"interface_name,omitempty"`

	// OSPF fields
	AreaID        string `json:"area_id,omitempty" yaml:"area_id,omitempty"`
	Passive       bool   `json:"passive,omitempty" yaml:"passive,omitempty"`
	Cost          uint32 `json:"cost,omitempty" yaml:"cost,omitempty"`
	HelloInterval uint32 `json:"hello_interval,omitempty" yaml:"hello_interval,omitempty"`
	DeadInterval  uint32 `json:"dead_interval,omitempty" yaml:"dead_interval,omitempty"`
}

// intentPeer represents a BGP peer configuration within a batch intent.
type intentPeer struct {
	NeighborAddress     string   `json:"neighbor_address" yaml:"neighbor_address"`
	RemoteAS            uint32   `json:"remote_as" yaml:"remote_as"`
	PeerType            string   `json:"peer_type,omitempty" yaml:"peer_type,omitempty"`
	Keepalive           uint32   `json:"keepalive,omitempty" yaml:"keepalive,omitempty"`
	HoldTime            uint32   `json:"hold_time,omitempty" yaml:"hold_time,omitempty"`
	BFDEnabled          bool     `json:"bfd_enabled,omitempty" yaml:"bfd_enabled,omitempty"`
	BFDMinRxMs          uint32   `json:"bfd_min_rx_ms,omitempty" yaml:"bfd_min_rx_ms,omitempty"`
	BFDMinTxMs          uint32   `json:"bfd_min_tx_ms,omitempty" yaml:"bfd_min_tx_ms,omitempty"`
	BFDDetectMultiplier uint32   `json:"bfd_detect_multiplier,omitempty" yaml:"bfd_detect_multiplier,omitempty"`
	EBGPMultihop        uint32   `json:"ebgp_multihop,omitempty" yaml:"ebgp_multihop,omitempty"`
	BGPAuth             string   `json:"bgp_auth,omitempty" yaml:"bgp_auth,omitempty"`
	SourceAddress       string   `json:"source_address,omitempty" yaml:"source_address,omitempty"`
	MaxPrefix           uint32   `json:"max_prefix,omitempty" yaml:"max_prefix,omitempty"`
	AddressFamilies     []string `json:"address_families,omitempty" yaml:"address_families,omitempty"`
	Description         string   `json:"description,omitempty" yaml:"description,omitempty"`
}

// intentAttributes represents prefix attributes within a batch intent.
type intentAttributes struct {
	LocalPreference uint32   `json:"local_preference,omitempty" yaml:"local_preference,omitempty"`
	Communities     []string `json:"communities,omitempty" yaml:"communities,omitempty"`
	MED             uint32   `json:"med,omitempty" yaml:"med,omitempty"`
	NextHop         string   `json:"next_hop,omitempty" yaml:"next_hop,omitempty"`
}

// newApplyCmd creates the "apply" subcommand for batch operations.
func newApplyCmd() *cobra.Command {
	var filePath string

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply intents from a YAML or JSON file",
		Long: `Apply a batch of routing intents from a YAML or JSON file.

The file should contain an array of intent objects. Each intent has an "action"
field and the corresponding parameters for that action.

Supported actions:
  configure-bgp, apply-peer, remove-peer, advertise, withdraw,
  enable-bfd, disable-bfd, enable-ospf, disable-ospf

Example YAML:
  - action: apply-peer
    owner: myapp
    token: secret
    peer:
      neighbor_address: "10.0.0.1"
      remote_as: 65001
      peer_type: external
  - action: advertise
    owner: myapp
    token: secret
    prefix: "192.168.1.0/24"
    protocol: bgp

Use "-f -" to read from stdin.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApply(filePath)
		},
	}

	cmd.Flags().StringVarP(&filePath, "file", "f", "", "path to intents file (use - for stdin)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

// runApply reads intents from a file or stdin and applies them sequentially.
func runApply(filePath string) error {
	data, err := readInput(filePath)
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}

	intents, err := parseIntents(data)
	if err != nil {
		return fmt.Errorf("parsing intents: %w", err)
	}

	if len(intents) == 0 {
		fmt.Println("No intents to apply.")
		return nil
	}

	client, conn, err := connect()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	total := len(intents)
	var failures int

	for i, intent := range intents {
		fmt.Printf("Applied %d/%d intents...\r", i, total)

		if applyErr := applyIntent(client, intent); applyErr != nil {
			failures++
			fmt.Printf("\nError on intent %d (action=%q): %v\n", i+1, intent.Action, applyErr)
			continue
		}
	}

	fmt.Printf("Applied %d/%d intents", total, total)
	if failures > 0 {
		fmt.Printf(" (%d failed)", failures)
	}
	fmt.Println()

	return nil
}

// readInput reads the content from a file path or stdin if path is "-".
func readInput(filePath string) ([]byte, error) {
	if filePath == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(filePath) //nolint:gosec // file path is user-provided CLI input, not attacker-controlled
}

// parseIntents detects whether the input is JSON or YAML and unmarshals it.
func parseIntents(data []byte) ([]intentAction, error) {
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) == 0 {
		return nil, nil
	}

	var intents []intentAction

	// Try JSON first (starts with '[').
	if trimmed[0] == '[' {
		if err := json.Unmarshal(data, &intents); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		return intents, nil
	}

	// Fall back to YAML.
	if err := yaml.Unmarshal(data, &intents); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	return intents, nil
}

// applyIntent dispatches a single intent to the appropriate gRPC call.
func applyIntent(client v1.RouteControlClient, intent intentAction) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	switch strings.ToLower(intent.Action) {
	case "configure-bgp":
		return applyConfigureBGP(ctx, client, intent)
	case "apply-peer":
		return applyApplyPeer(ctx, client, intent)
	case "remove-peer":
		return applyRemovePeer(ctx, client, intent)
	case "advertise":
		return applyAdvertise(ctx, client, intent)
	case "withdraw":
		return applyWithdraw(ctx, client, intent)
	case "enable-bfd":
		return applyEnableBFD(ctx, client, intent)
	case "disable-bfd":
		return applyDisableBFD(ctx, client, intent)
	case "enable-ospf":
		return applyEnableOSPF(ctx, client, intent)
	case "disable-ospf":
		return applyDisableOSPF(ctx, client, intent)
	default:
		return fmt.Errorf("%q: %w", intent.Action, errUnknownAction)
	}
}

func applyConfigureBGP(ctx context.Context, client v1.RouteControlClient, intent intentAction) error {
	_, err := client.ConfigureBGP(ctx, &v1.ConfigureBGPRequest{
		Owner:    intent.Owner,
		Token:    intent.Token,
		LocalAs:  intent.LocalAS,
		RouterId: intent.RouterID,
	})
	if err != nil {
		return fmt.Errorf("ConfigureBGP RPC failed: %w", err)
	}
	return nil
}

func applyApplyPeer(ctx context.Context, client v1.RouteControlClient, intent intentAction) error {
	if intent.Peer == nil {
		return errMissingPeer
	}
	p := intent.Peer

	pt := parsePeerType(p.PeerType)

	var afs []v1.AddressFamily
	for _, af := range p.AddressFamilies {
		afs = append(afs, parseAddressFamily(af))
	}

	_, err := client.ApplyPeer(ctx, &v1.ApplyPeerRequest{
		Owner: intent.Owner,
		Token: intent.Token,
		Peer: &v1.BGPPeer{
			NeighborAddress:     p.NeighborAddress,
			RemoteAs:            p.RemoteAS,
			PeerType:            pt,
			Keepalive:           p.Keepalive,
			HoldTime:            p.HoldTime,
			BfdEnabled:          p.BFDEnabled,
			BfdMinRxMs:          p.BFDMinRxMs,
			BfdMinTxMs:          p.BFDMinTxMs,
			BfdDetectMultiplier: p.BFDDetectMultiplier,
			EbgpMultihop:        p.EBGPMultihop,
			Password:            p.BGPAuth,
			SourceAddress:       p.SourceAddress,
			MaxPrefix:           p.MaxPrefix,
			AddressFamilies:     afs,
			Description:         p.Description,
		},
	})
	if err != nil {
		return fmt.Errorf("ApplyPeer RPC failed: %w", err)
	}
	return nil
}

func applyRemovePeer(ctx context.Context, client v1.RouteControlClient, intent intentAction) error {
	neighbor := intent.NeighborAddress
	if neighbor == "" && intent.Peer != nil {
		neighbor = intent.Peer.NeighborAddress
	}
	_, err := client.RemovePeer(ctx, &v1.RemovePeerRequest{
		Owner:           intent.Owner,
		Token:           intent.Token,
		NeighborAddress: neighbor,
	})
	if err != nil {
		return fmt.Errorf("RemovePeer RPC failed: %w", err)
	}
	return nil
}

func applyAdvertise(ctx context.Context, client v1.RouteControlClient, intent intentAction) error {
	req := &v1.AdvertisePrefixRequest{
		Owner:    intent.Owner,
		Token:    intent.Token,
		Prefix:   intent.Prefix,
		Protocol: parseProtocol(intent.Protocol),
	}
	if intent.Attributes != nil {
		req.Attributes = &v1.PrefixAttributes{
			LocalPreference: intent.Attributes.LocalPreference,
			Communities:     intent.Attributes.Communities,
			Med:             intent.Attributes.MED,
			NextHop:         intent.Attributes.NextHop,
		}
	}
	_, err := client.AdvertisePrefix(ctx, req)
	if err != nil {
		return fmt.Errorf("AdvertisePrefix RPC failed: %w", err)
	}
	return nil
}

func applyWithdraw(ctx context.Context, client v1.RouteControlClient, intent intentAction) error {
	_, err := client.WithdrawPrefix(ctx, &v1.WithdrawPrefixRequest{
		Owner:    intent.Owner,
		Token:    intent.Token,
		Prefix:   intent.Prefix,
		Protocol: parseProtocol(intent.Protocol),
	})
	if err != nil {
		return fmt.Errorf("WithdrawPrefix RPC failed: %w", err)
	}
	return nil
}

func applyEnableBFD(ctx context.Context, client v1.RouteControlClient, intent intentAction) error {
	_, err := client.EnableBFD(ctx, &v1.EnableBFDRequest{
		Owner:            intent.Owner,
		Token:            intent.Token,
		PeerAddress:      intent.PeerAddress,
		MinRxMs:          intent.MinRxMs,
		MinTxMs:          intent.MinTxMs,
		DetectMultiplier: intent.DetectMultiplier,
		InterfaceName:    intent.InterfaceName,
	})
	if err != nil {
		return fmt.Errorf("EnableBFD RPC failed: %w", err)
	}
	return nil
}

func applyDisableBFD(ctx context.Context, client v1.RouteControlClient, intent intentAction) error {
	peerAddr := intent.PeerAddress
	if peerAddr == "" {
		peerAddr = intent.NeighborAddress
	}
	_, err := client.DisableBFD(ctx, &v1.DisableBFDRequest{
		Owner:       intent.Owner,
		Token:       intent.Token,
		PeerAddress: peerAddr,
	})
	if err != nil {
		return fmt.Errorf("DisableBFD RPC failed: %w", err)
	}
	return nil
}

func applyEnableOSPF(ctx context.Context, client v1.RouteControlClient, intent intentAction) error {
	_, err := client.EnableOSPF(ctx, &v1.EnableOSPFRequest{
		Owner:         intent.Owner,
		Token:         intent.Token,
		InterfaceName: intent.InterfaceName,
		AreaId:        intent.AreaID,
		Passive:       intent.Passive,
		Cost:          intent.Cost,
		HelloInterval: intent.HelloInterval,
		DeadInterval:  intent.DeadInterval,
	})
	if err != nil {
		return fmt.Errorf("EnableOSPF RPC failed: %w", err)
	}
	return nil
}

func applyDisableOSPF(ctx context.Context, client v1.RouteControlClient, intent intentAction) error {
	_, err := client.DisableOSPF(ctx, &v1.DisableOSPFRequest{
		Owner:         intent.Owner,
		Token:         intent.Token,
		InterfaceName: intent.InterfaceName,
	})
	if err != nil {
		return fmt.Errorf("DisableOSPF RPC failed: %w", err)
	}
	return nil
}

// parsePeerType converts a string peer type to the protobuf enum value.
func parsePeerType(s string) v1.PeerType {
	switch strings.ToLower(s) {
	case "external", "ebgp":
		return v1.PeerType_PEER_TYPE_EXTERNAL
	case "internal", "ibgp":
		return v1.PeerType_PEER_TYPE_INTERNAL
	default:
		return v1.PeerType_PEER_TYPE_EXTERNAL
	}
}

// parseAddressFamily converts a string address family to the protobuf enum.
func parseAddressFamily(s string) v1.AddressFamily {
	switch strings.ToLower(s) {
	case "ipv4-unicast", "ipv4":
		return v1.AddressFamily_ADDRESS_FAMILY_IPV4_UNICAST
	case "ipv6-unicast", "ipv6":
		return v1.AddressFamily_ADDRESS_FAMILY_IPV6_UNICAST
	default:
		return v1.AddressFamily_ADDRESS_FAMILY_UNSPECIFIED
	}
}

// parseProtocol converts a string protocol to the protobuf enum.
func parseProtocol(s string) v1.Protocol {
	switch strings.ToLower(s) {
	case "ospf":
		return v1.Protocol_PROTOCOL_OSPF
	case "bgp", "":
		return v1.Protocol_PROTOCOL_BGP
	default:
		return v1.Protocol_PROTOCOL_BGP
	}
}
