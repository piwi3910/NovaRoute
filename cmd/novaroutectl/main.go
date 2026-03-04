// Package main implements the novaroutectl CLI tool for debugging and
// inspecting the NovaRoute agent. It connects to the agent's Unix domain
// socket via gRPC and provides human-readable output of routing state.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	v1 "github.com/piwi3910/NovaRoute/api/v1"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var socketPath string

func main() {
	rootCmd := &cobra.Command{
		Use:   "novaroutectl",
		Short: "NovaRoute debugging and inspection CLI",
		Long: `novaroutectl connects to the NovaRoute agent via its Unix domain socket
and provides human-readable views of routing state, peers, prefixes,
BFD sessions, and OSPF interfaces.`,
	}

	rootCmd.PersistentFlags().StringVar(&socketPath, "socket", "/run/novaroute/novaroute.sock", "path to NovaRoute Unix socket")

	rootCmd.AddCommand(
		newStatusCmd(),
		newPeersCmd(),
		newPrefixesCmd(),
		newBFDCmd(),
		newOSPFCmd(),
		newEventsCmd(),
		newRegisterCmd(),
		newDeregisterCmd(),
		newConfigureBGPCmd(),
		newApplyPeerCmd(),
		newRemovePeerCmd(),
		newAdvertiseCmd(),
		newWithdrawCmd(),
		newEnableBFDCmd(),
		newDisableBFDCmd(),
		newEnableOSPFCmd(),
		newDisableOSPFCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// connect establishes a gRPC connection to the NovaRoute agent.
func connect() (v1.RouteControlClient, *grpc.ClientConn, error) {
	target := "unix://" + socketPath
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to %s: %w", target, err)
	}
	return v1.NewRouteControlClient(conn), conn, nil
}

// getStatus fetches the full status from the agent.
func getStatus(ownerFilter string) (*v1.GetStatusResponse, error) {
	client, conn, err := connect()
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.GetStatus(ctx, &v1.GetStatusRequest{
		OwnerFilter: ownerFilter,
	})
	if err != nil {
		return nil, fmt.Errorf("GetStatus RPC failed: %w", err)
	}
	return resp, nil
}

// newStatusCmd creates the "status" subcommand.
func newStatusCmd() *cobra.Command {
	var ownerFilter string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show overall NovaRoute agent status",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := getStatus(ownerFilter)
			if err != nil {
				return err
			}

			// FRR status.
			fmt.Println("=== FRR Status ===")
			if resp.FrrStatus != nil {
				connected := "disconnected"
				if resp.FrrStatus.Connected {
					connected = "connected"
				}
				fmt.Printf("  Connection: %s\n", connected)
				if resp.FrrStatus.Version != "" {
					fmt.Printf("  Version:    %s\n", resp.FrrStatus.Version)
				}
				if resp.FrrStatus.Uptime != "" {
					fmt.Printf("  Uptime:     %s\n", resp.FrrStatus.Uptime)
				}
			} else {
				fmt.Println("  (unavailable)")
			}

			fmt.Println()

			// Summary counts.
			fmt.Println("=== Summary ===")
			fmt.Printf("  BGP Peers:        %d\n", len(resp.Peers))
			fmt.Printf("  Prefixes:         %d\n", len(resp.Prefixes))
			fmt.Printf("  BFD Sessions:     %d\n", len(resp.BfdSessions))
			fmt.Printf("  OSPF Interfaces:  %d\n", len(resp.OspfInterfaces))

			return nil
		},
	}

	cmd.Flags().StringVar(&ownerFilter, "owner", "", "filter results by owner name")
	return cmd
}

// newPeersCmd creates the "peers" subcommand.
func newPeersCmd() *cobra.Command {
	var ownerFilter string

	cmd := &cobra.Command{
		Use:   "peers",
		Short: "Show BGP peer status table",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := getStatus(ownerFilter)
			if err != nil {
				return err
			}

			if len(resp.Peers) == 0 {
				fmt.Println("No BGP peers configured.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NEIGHBOR\tREMOTE AS\tSTATE\tOWNER\tPFX RECV\tPFX SENT\tBFD\tUPTIME")
			_, _ = fmt.Fprintln(w, "--------\t---------\t-----\t-----\t--------\t--------\t---\t------")

			for _, p := range resp.Peers {
				bfd := "no"
				if p.BfdEnabled {
					bfd = p.BfdStatus
					if bfd == "" {
						bfd = "yes"
					}
				}
				uptime := p.Uptime
				if uptime == "" {
					uptime = "-"
				}
				_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%d\t%d\t%s\t%s\n",
					p.NeighborAddress,
					p.RemoteAs,
					p.State,
					p.Owner,
					p.PrefixesReceived,
					p.PrefixesSent,
					bfd,
					uptime,
				)
			}
			return w.Flush()
		},
	}

	cmd.Flags().StringVar(&ownerFilter, "owner", "", "filter results by owner name")
	return cmd
}

// newPrefixesCmd creates the "prefixes" subcommand.
func newPrefixesCmd() *cobra.Command {
	var ownerFilter string

	cmd := &cobra.Command{
		Use:   "prefixes",
		Short: "Show prefix advertisement status table",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := getStatus(ownerFilter)
			if err != nil {
				return err
			}

			if len(resp.Prefixes) == 0 {
				fmt.Println("No prefixes advertised.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "PREFIX\tPROTOCOL\tSTATE\tOWNER")
			_, _ = fmt.Fprintln(w, "------\t--------\t-----\t-----")

			for _, p := range resp.Prefixes {
				proto := protocolName(p.Protocol)
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					p.Prefix,
					proto,
					p.State,
					p.Owner,
				)
			}
			return w.Flush()
		},
	}

	cmd.Flags().StringVar(&ownerFilter, "owner", "", "filter results by owner name")
	return cmd
}

// newBFDCmd creates the "bfd" subcommand.
func newBFDCmd() *cobra.Command {
	var ownerFilter string

	cmd := &cobra.Command{
		Use:   "bfd",
		Short: "Show BFD session status table",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := getStatus(ownerFilter)
			if err != nil {
				return err
			}

			if len(resp.BfdSessions) == 0 {
				fmt.Println("No BFD sessions configured.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "PEER ADDRESS\tSTATE\tOWNER\tMIN RX (ms)\tMIN TX (ms)\tDETECT MULT\tUPTIME")
			_, _ = fmt.Fprintln(w, "------------\t-----\t-----\t-----------\t-----------\t-----------\t------")

			for _, b := range resp.BfdSessions {
				uptime := b.Uptime
				if uptime == "" {
					uptime = "-"
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%s\n",
					b.PeerAddress,
					b.State,
					b.Owner,
					b.MinRxMs,
					b.MinTxMs,
					b.DetectMultiplier,
					uptime,
				)
			}
			return w.Flush()
		},
	}

	cmd.Flags().StringVar(&ownerFilter, "owner", "", "filter results by owner name")
	return cmd
}

// newOSPFCmd creates the "ospf" subcommand.
func newOSPFCmd() *cobra.Command {
	var ownerFilter string

	cmd := &cobra.Command{
		Use:   "ospf",
		Short: "Show OSPF interface status table",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := getStatus(ownerFilter)
			if err != nil {
				return err
			}

			if len(resp.OspfInterfaces) == 0 {
				fmt.Println("No OSPF interfaces configured.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "INTERFACE\tAREA\tSTATE\tOWNER\tNEIGHBORS\tCOST")
			_, _ = fmt.Fprintln(w, "---------\t----\t-----\t-----\t---------\t----")

			for _, o := range resp.OspfInterfaces {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\n",
					o.InterfaceName,
					o.AreaId,
					o.State,
					o.Owner,
					o.NeighborCount,
					o.Cost,
				)
			}
			return w.Flush()
		},
	}

	cmd.Flags().StringVar(&ownerFilter, "owner", "", "filter results by owner name")
	return cmd
}

// protocolName returns a human-readable protocol name.
func protocolName(p v1.Protocol) string {
	switch p {
	case v1.Protocol_PROTOCOL_BGP:
		return "BGP"
	case v1.Protocol_PROTOCOL_OSPF:
		return "OSPF"
	case v1.Protocol_PROTOCOL_UNSPECIFIED:
		return "unknown"
	default:
		return "unknown"
	}
}

// newEventsCmd creates the "events" subcommand.
func newEventsCmd() *cobra.Command {
	var ownerFilter string
	var eventTypes []string

	cmd := &cobra.Command{
		Use:   "events",
		Short: "Stream real-time route events",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := connect()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()

			stream, err := client.StreamEvents(context.Background(), &v1.StreamEventsRequest{
				OwnerFilter: ownerFilter,
				EventTypes:  eventTypes,
			})
			if err != nil {
				return fmt.Errorf("StreamEvents RPC failed: %w", err)
			}

			for {
				event, err := stream.Recv()
				if err != nil {
					return fmt.Errorf("stream error: %w", err)
				}
				ts := time.Unix(event.TimestampUnix, 0).Format(time.RFC3339)
				owner := event.Owner
				if owner == "" {
					owner = "-"
				}
				fmt.Printf("[%s] %-35s owner=%-12s %s\n", ts, event.Type.String(), owner, event.Detail)
			}
		},
	}

	cmd.Flags().StringVar(&ownerFilter, "owner", "", "filter events by owner")
	cmd.Flags().StringSliceVar(&eventTypes, "types", nil, "filter events by type (comma-separated)")
	return cmd
}

// newRegisterCmd creates the "register" subcommand.
func newRegisterCmd() *cobra.Command {
	var owner, token string
	var reassert bool

	cmd := &cobra.Command{
		Use:   "register",
		Short: "Register an owner session",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := connect()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			resp, err := client.Register(ctx, &v1.RegisterRequest{
				Owner:           owner,
				Token:           token,
				ReassertIntents: reassert,
			})
			if err != nil {
				return fmt.Errorf("register RPC failed: %w", err)
			}

			fmt.Printf("Registered: session_id=%s\n", resp.SessionId)
			fmt.Printf("  Current peers:    %v\n", resp.CurrentPeers)
			fmt.Printf("  Current prefixes: %v\n", resp.CurrentPrefixes)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "owner name (required)")
	cmd.Flags().StringVar(&token, "token", "", "authentication token (required)")
	cmd.Flags().BoolVar(&reassert, "reassert", false, "reassert existing intents")
	_ = cmd.MarkFlagRequired("owner")
	_ = cmd.MarkFlagRequired("token")
	return cmd
}

// newDeregisterCmd creates the "deregister" subcommand.
func newDeregisterCmd() *cobra.Command {
	var owner, token string
	var withdrawAll bool

	cmd := &cobra.Command{
		Use:   "deregister",
		Short: "Deregister an owner session",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := connect()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			_, err = client.Deregister(ctx, &v1.DeregisterRequest{
				Owner:       owner,
				Token:       token,
				WithdrawAll: withdrawAll,
			})
			if err != nil {
				return fmt.Errorf("deregister RPC failed: %w", err)
			}

			fmt.Printf("Deregistered: owner=%s withdraw_all=%v\n", owner, withdrawAll)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "owner name (required)")
	cmd.Flags().StringVar(&token, "token", "", "authentication token (required)")
	cmd.Flags().BoolVar(&withdrawAll, "withdraw-all", false, "withdraw all intents")
	_ = cmd.MarkFlagRequired("owner")
	_ = cmd.MarkFlagRequired("token")
	return cmd
}

// newConfigureBGPCmd creates the "configure-bgp" subcommand.
func newConfigureBGPCmd() *cobra.Command {
	var owner, token, routerID string
	var localAS uint32

	cmd := &cobra.Command{
		Use:   "configure-bgp",
		Short: "Configure BGP global settings (AS number, router ID)",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := connect()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			resp, err := client.ConfigureBGP(ctx, &v1.ConfigureBGPRequest{
				Owner:    owner,
				Token:    token,
				LocalAs:  localAS,
				RouterId: routerID,
			})
			if err != nil {
				return fmt.Errorf("ConfigureBGP RPC failed: %w", err)
			}

			fmt.Printf("BGP configured: AS=%d router_id=%s\n", localAS, routerID)
			fmt.Printf("  Previous: AS=%d router_id=%s\n", resp.PreviousAs, resp.PreviousRouterId)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "owner name (required)")
	cmd.Flags().StringVar(&token, "token", "", "authentication token (required)")
	cmd.Flags().Uint32Var(&localAS, "local-as", 0, "local AS number (required)")
	cmd.Flags().StringVar(&routerID, "router-id", "", "BGP router ID (required)")
	_ = cmd.MarkFlagRequired("owner")
	_ = cmd.MarkFlagRequired("token")
	_ = cmd.MarkFlagRequired("local-as")
	_ = cmd.MarkFlagRequired("router-id")
	return cmd
}

// newApplyPeerCmd creates the "apply-peer" subcommand.
func newApplyPeerCmd() *cobra.Command {
	var owner, token, neighbor, password, sourceAddr, description, peerType string
	var remoteAS, keepalive, holdTime, ebgpMultihop, maxPrefix uint32
	var bfdMinRx, bfdMinTx, bfdDetectMult uint32
	var bfdEnabled bool
	var addressFamilies []string

	cmd := &cobra.Command{
		Use:   "apply-peer",
		Short: "Add or update a BGP peer",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := connect()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			var pt v1.PeerType
			switch strings.ToLower(peerType) {
			case "external", "ebgp":
				pt = v1.PeerType_PEER_TYPE_EXTERNAL
			case "internal", "ibgp":
				pt = v1.PeerType_PEER_TYPE_INTERNAL
			default:
				return fmt.Errorf("invalid --peer-type %q: must be external|ebgp|internal|ibgp", peerType)
			}

			var afs []v1.AddressFamily
			for _, af := range addressFamilies {
				switch strings.ToLower(af) {
				case "ipv4-unicast", "ipv4":
					afs = append(afs, v1.AddressFamily_ADDRESS_FAMILY_IPV4_UNICAST)
				case "ipv6-unicast", "ipv6":
					afs = append(afs, v1.AddressFamily_ADDRESS_FAMILY_IPV6_UNICAST)
				default:
					return fmt.Errorf("invalid --address-families value %q: must be ipv4-unicast|ipv4|ipv6-unicast|ipv6", af)
				}
			}

			peer := &v1.BGPPeer{
				NeighborAddress:     neighbor,
				RemoteAs:            remoteAS,
				PeerType:            pt,
				Keepalive:           keepalive,
				HoldTime:            holdTime,
				BfdEnabled:          bfdEnabled,
				BfdMinRxMs:          bfdMinRx,
				BfdMinTxMs:          bfdMinTx,
				BfdDetectMultiplier: bfdDetectMult,
				EbgpMultihop:        ebgpMultihop,
				Password:            password,
				SourceAddress:       sourceAddr,
				MaxPrefix:           maxPrefix,
				AddressFamilies:     afs,
				Description:         description,
			}

			_, err = client.ApplyPeer(ctx, &v1.ApplyPeerRequest{
				Owner: owner,
				Token: token,
				Peer:  peer,
			})
			if err != nil {
				return fmt.Errorf("ApplyPeer RPC failed: %w", err)
			}

			fmt.Printf("Peer applied: neighbor=%s remote_as=%d\n", neighbor, remoteAS)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "owner name (required)")
	cmd.Flags().StringVar(&token, "token", "", "authentication token (required)")
	cmd.Flags().StringVar(&neighbor, "neighbor", "", "neighbor IP address (required)")
	cmd.Flags().Uint32Var(&remoteAS, "remote-as", 0, "remote AS number (required)")
	cmd.Flags().Uint32Var(&keepalive, "keepalive", 0, "keepalive interval (seconds)")
	cmd.Flags().Uint32Var(&holdTime, "hold-time", 0, "hold time (seconds)")
	cmd.Flags().BoolVar(&bfdEnabled, "bfd", false, "enable BFD for this peer")
	cmd.Flags().Uint32Var(&bfdMinRx, "bfd-min-rx", 0, "BFD minimum RX interval in ms (default 300 when --bfd set)")
	cmd.Flags().Uint32Var(&bfdMinTx, "bfd-min-tx", 0, "BFD minimum TX interval in ms (default 300 when --bfd set)")
	cmd.Flags().Uint32Var(&bfdDetectMult, "bfd-detect-multiplier", 0, "BFD detect multiplier (default 3 when --bfd set)")
	cmd.Flags().Uint32Var(&ebgpMultihop, "ebgp-multihop", 0, "eBGP multihop TTL")
	cmd.Flags().StringVar(&password, "password", "", "BGP session password")
	cmd.Flags().StringVar(&sourceAddr, "source-address", "", "update source address")
	cmd.Flags().Uint32Var(&maxPrefix, "max-prefix", 0, "maximum prefix limit (0 = default 1000)")
	cmd.Flags().StringVar(&peerType, "peer-type", "external", "peer type (external, internal, ibgp)")
	cmd.Flags().StringSliceVar(&addressFamilies, "address-families", []string{"ipv4-unicast"}, "address families (ipv4-unicast, ipv6-unicast)")
	cmd.Flags().StringVar(&description, "description", "", "peer description")
	_ = cmd.MarkFlagRequired("owner")
	_ = cmd.MarkFlagRequired("token")
	_ = cmd.MarkFlagRequired("neighbor")
	_ = cmd.MarkFlagRequired("remote-as")
	return cmd
}

// newRemovePeerCmd creates the "remove-peer" subcommand.
func newRemovePeerCmd() *cobra.Command {
	var owner, token, neighbor string

	cmd := &cobra.Command{
		Use:   "remove-peer",
		Short: "Remove a BGP peer",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := connect()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			_, err = client.RemovePeer(ctx, &v1.RemovePeerRequest{
				Owner:           owner,
				Token:           token,
				NeighborAddress: neighbor,
			})
			if err != nil {
				return fmt.Errorf("RemovePeer RPC failed: %w", err)
			}

			fmt.Printf("Peer removed: neighbor=%s\n", neighbor)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "owner name (required)")
	cmd.Flags().StringVar(&token, "token", "", "authentication token (required)")
	cmd.Flags().StringVar(&neighbor, "neighbor", "", "neighbor IP address (required)")
	_ = cmd.MarkFlagRequired("owner")
	_ = cmd.MarkFlagRequired("token")
	_ = cmd.MarkFlagRequired("neighbor")
	return cmd
}

// newAdvertiseCmd creates the "advertise" subcommand.
func newAdvertiseCmd() *cobra.Command {
	var owner, token, prefix, protocol, nextHop string
	var communities []string
	var localPref, med uint32

	cmd := &cobra.Command{
		Use:   "advertise",
		Short: "Advertise a prefix",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := connect()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			proto := v1.Protocol_PROTOCOL_BGP
			if protocol == "ospf" {
				proto = v1.Protocol_PROTOCOL_OSPF
			}

			req := &v1.AdvertisePrefixRequest{
				Owner:    owner,
				Token:    token,
				Prefix:   prefix,
				Protocol: proto,
			}
			if localPref > 0 || med > 0 || nextHop != "" || len(communities) > 0 {
				req.Attributes = &v1.PrefixAttributes{
					LocalPreference: localPref,
					Communities:     communities,
					Med:             med,
					NextHop:         nextHop,
				}
			}

			_, err = client.AdvertisePrefix(ctx, req)
			if err != nil {
				return fmt.Errorf("AdvertisePrefix RPC failed: %w", err)
			}

			fmt.Printf("Prefix advertised: %s via %s\n", prefix, protocol)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "owner name (required)")
	cmd.Flags().StringVar(&token, "token", "", "authentication token (required)")
	cmd.Flags().StringVar(&prefix, "prefix", "", "CIDR prefix (required)")
	cmd.Flags().StringVar(&protocol, "protocol", "bgp", "protocol (bgp or ospf)")
	cmd.Flags().Uint32Var(&localPref, "local-pref", 0, "local preference")
	cmd.Flags().Uint32Var(&med, "med", 0, "MED value")
	cmd.Flags().StringVar(&nextHop, "next-hop", "", "next-hop address")
	cmd.Flags().StringSliceVar(&communities, "communities", nil, "BGP communities")
	_ = cmd.MarkFlagRequired("owner")
	_ = cmd.MarkFlagRequired("token")
	_ = cmd.MarkFlagRequired("prefix")
	return cmd
}

// newWithdrawCmd creates the "withdraw" subcommand.
func newWithdrawCmd() *cobra.Command {
	var owner, token, prefix, protocol string

	cmd := &cobra.Command{
		Use:   "withdraw",
		Short: "Withdraw a prefix",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := connect()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			proto := v1.Protocol_PROTOCOL_BGP
			if protocol == "ospf" {
				proto = v1.Protocol_PROTOCOL_OSPF
			}

			_, err = client.WithdrawPrefix(ctx, &v1.WithdrawPrefixRequest{
				Owner:    owner,
				Token:    token,
				Prefix:   prefix,
				Protocol: proto,
			})
			if err != nil {
				return fmt.Errorf("WithdrawPrefix RPC failed: %w", err)
			}

			fmt.Printf("Prefix withdrawn: %s from %s\n", prefix, protocol)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "owner name (required)")
	cmd.Flags().StringVar(&token, "token", "", "authentication token (required)")
	cmd.Flags().StringVar(&prefix, "prefix", "", "CIDR prefix (required)")
	cmd.Flags().StringVar(&protocol, "protocol", "bgp", "protocol (bgp or ospf)")
	_ = cmd.MarkFlagRequired("owner")
	_ = cmd.MarkFlagRequired("token")
	_ = cmd.MarkFlagRequired("prefix")
	return cmd
}

// newEnableBFDCmd creates the "enable-bfd" subcommand.
func newEnableBFDCmd() *cobra.Command {
	var owner, token, peerAddr, iface string
	var minRx, minTx, detectMult uint32

	cmd := &cobra.Command{
		Use:   "enable-bfd",
		Short: "Enable BFD on a peer",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := connect()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			_, err = client.EnableBFD(ctx, &v1.EnableBFDRequest{
				Owner:            owner,
				Token:            token,
				PeerAddress:      peerAddr,
				MinRxMs:          minRx,
				MinTxMs:          minTx,
				DetectMultiplier: detectMult,
				InterfaceName:    iface,
			})
			if err != nil {
				return fmt.Errorf("EnableBFD RPC failed: %w", err)
			}

			fmt.Printf("BFD enabled: peer=%s\n", peerAddr)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "owner name (required)")
	cmd.Flags().StringVar(&token, "token", "", "authentication token (required)")
	cmd.Flags().StringVar(&peerAddr, "peer", "", "peer address (required)")
	cmd.Flags().StringVar(&iface, "interface", "", "interface name")
	cmd.Flags().Uint32Var(&minRx, "min-rx", 0, "minimum RX interval (ms)")
	cmd.Flags().Uint32Var(&minTx, "min-tx", 0, "minimum TX interval (ms)")
	cmd.Flags().Uint32Var(&detectMult, "detect-multiplier", 0, "detect multiplier")
	_ = cmd.MarkFlagRequired("owner")
	_ = cmd.MarkFlagRequired("token")
	_ = cmd.MarkFlagRequired("peer")
	return cmd
}

// newDisableBFDCmd creates the "disable-bfd" subcommand.
func newDisableBFDCmd() *cobra.Command {
	var owner, token, peerAddr string

	cmd := &cobra.Command{
		Use:   "disable-bfd",
		Short: "Disable BFD on a peer",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := connect()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			_, err = client.DisableBFD(ctx, &v1.DisableBFDRequest{
				Owner:       owner,
				Token:       token,
				PeerAddress: peerAddr,
			})
			if err != nil {
				return fmt.Errorf("DisableBFD RPC failed: %w", err)
			}

			fmt.Printf("BFD disabled: peer=%s\n", peerAddr)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "owner name (required)")
	cmd.Flags().StringVar(&token, "token", "", "authentication token (required)")
	cmd.Flags().StringVar(&peerAddr, "peer", "", "peer address (required)")
	_ = cmd.MarkFlagRequired("owner")
	_ = cmd.MarkFlagRequired("token")
	_ = cmd.MarkFlagRequired("peer")
	return cmd
}

// newEnableOSPFCmd creates the "enable-ospf" subcommand.
func newEnableOSPFCmd() *cobra.Command {
	var owner, token, iface, areaID string
	var passive bool
	var cost, hello, dead uint32

	cmd := &cobra.Command{
		Use:   "enable-ospf",
		Short: "Enable OSPF on an interface",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := connect()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			_, err = client.EnableOSPF(ctx, &v1.EnableOSPFRequest{
				Owner:         owner,
				Token:         token,
				InterfaceName: iface,
				AreaId:        areaID,
				Passive:       passive,
				Cost:          cost,
				HelloInterval: hello,
				DeadInterval:  dead,
			})
			if err != nil {
				return fmt.Errorf("EnableOSPF RPC failed: %w", err)
			}

			fmt.Printf("OSPF enabled: interface=%s area=%s\n", iface, areaID)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "owner name (required)")
	cmd.Flags().StringVar(&token, "token", "", "authentication token (required)")
	cmd.Flags().StringVar(&iface, "interface", "", "interface name (required)")
	cmd.Flags().StringVar(&areaID, "area", "", "OSPF area ID (required)")
	cmd.Flags().BoolVar(&passive, "passive", false, "set interface as passive")
	cmd.Flags().Uint32Var(&cost, "cost", 0, "OSPF cost")
	cmd.Flags().Uint32Var(&hello, "hello-interval", 0, "hello interval (seconds)")
	cmd.Flags().Uint32Var(&dead, "dead-interval", 0, "dead interval (seconds)")
	_ = cmd.MarkFlagRequired("owner")
	_ = cmd.MarkFlagRequired("token")
	_ = cmd.MarkFlagRequired("interface")
	_ = cmd.MarkFlagRequired("area")
	return cmd
}

// newDisableOSPFCmd creates the "disable-ospf" subcommand.
func newDisableOSPFCmd() *cobra.Command {
	var owner, token, iface string

	cmd := &cobra.Command{
		Use:   "disable-ospf",
		Short: "Disable OSPF on an interface",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := connect()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			_, err = client.DisableOSPF(ctx, &v1.DisableOSPFRequest{
				Owner:         owner,
				Token:         token,
				InterfaceName: iface,
			})
			if err != nil {
				return fmt.Errorf("DisableOSPF RPC failed: %w", err)
			}

			fmt.Printf("OSPF disabled: interface=%s\n", iface)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "owner name (required)")
	cmd.Flags().StringVar(&token, "token", "", "authentication token (required)")
	cmd.Flags().StringVar(&iface, "interface", "", "interface name (required)")
	_ = cmd.MarkFlagRequired("owner")
	_ = cmd.MarkFlagRequired("token")
	_ = cmd.MarkFlagRequired("interface")
	return cmd
}
