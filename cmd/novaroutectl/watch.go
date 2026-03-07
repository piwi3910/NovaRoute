// Package main implements the novaroutectl CLI tool.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// watchFlags holds the watch mode configuration.
type watchFlags struct {
	watch    bool
	interval time.Duration
}

// addWatchFlags adds --watch/-w and --interval flags to a command.
func addWatchFlags(cmd *cobra.Command, wf *watchFlags) {
	cmd.Flags().BoolVarP(&wf.watch, "watch", "w", false, "continuously refresh output")
	cmd.Flags().DurationVar(&wf.interval, "interval", 2*time.Second, "refresh interval for watch mode")
}

// wrapRunEWithWatch wraps a cobra RunE function with watch mode support.
// When watch mode is enabled, it clears the screen and re-runs the command
// at the configured interval until interrupted with Ctrl+C.
func wrapRunEWithWatch(wf *watchFlags, runFn func(cmd *cobra.Command, args []string) error) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if !wf.watch {
			return runFn(cmd, args)
		}

		// Set up signal handling for clean exit.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(sigCh)

		ticker := time.NewTicker(wf.interval)
		defer ticker.Stop()

		// Run immediately on first invocation.
		clearScreen()
		printTimestamp()
		if err := runFn(cmd, args); err != nil {
			fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
		}

		for {
			select {
			case <-sigCh:
				fmt.Println("\nWatch mode stopped.")
				return nil
			case <-ticker.C:
				clearScreen()
				printTimestamp()
				if err := runFn(cmd, args); err != nil {
					fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
				}
			}
		}
	}
}

// clearScreen writes ANSI escape codes to clear the terminal and move
// the cursor to the top-left corner.
func clearScreen() {
	fmt.Print("\033[2J\033[H")
}

// printTimestamp prints the current time as a header line.
func printTimestamp() {
	fmt.Printf("Last refresh: %s\n\n", time.Now().Format(time.RFC3339))
}
