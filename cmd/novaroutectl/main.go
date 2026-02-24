// Package main implements the novaroutectl CLI tool for debugging and
// inspecting the NovaRoute agent. It connects to the agent's Unix domain
// socket via gRPC and provides human-readable output of routing state.
package main

import (
	"context"
	"fmt"
	"os"
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
	defer conn.Close()

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
			fmt.Fprintln(w, "NEIGHBOR\tREMOTE AS\tSTATE\tOWNER\tPFX RECV\tPFX SENT\tBFD\tUPTIME")
			fmt.Fprintln(w, "--------\t---------\t-----\t-----\t--------\t--------\t---\t------")

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
				fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%d\t%d\t%s\t%s\n",
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
			fmt.Fprintln(w, "PREFIX\tPROTOCOL\tSTATE\tOWNER")
			fmt.Fprintln(w, "------\t--------\t-----\t-----")

			for _, p := range resp.Prefixes {
				proto := protocolName(p.Protocol)
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
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
			fmt.Fprintln(w, "PEER ADDRESS\tSTATE\tOWNER\tMIN RX (ms)\tMIN TX (ms)\tDETECT MULT\tUPTIME")
			fmt.Fprintln(w, "------------\t-----\t-----\t-----------\t-----------\t-----------\t------")

			for _, b := range resp.BfdSessions {
				uptime := b.Uptime
				if uptime == "" {
					uptime = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%s\n",
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
			fmt.Fprintln(w, "INTERFACE\tAREA\tSTATE\tOWNER\tNEIGHBORS\tCOST")
			fmt.Fprintln(w, "---------\t----\t-----\t-----\t---------\t----")

			for _, o := range resp.OspfInterfaces {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\n",
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
	default:
		return "unknown"
	}
}
