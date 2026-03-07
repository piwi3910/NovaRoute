// Package main implements the novaroutectl CLI tool.
package main

import (
	"os"

	"github.com/spf13/cobra"
)

// newCompletionCmd creates the "completion" subcommand that generates shell
// completion scripts for bash, zsh, fish, and powershell.
func newCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate shell completion scripts",
		Long: `Generate shell completion scripts for novaroutectl.

To load completions:

Bash:
  $ source <(novaroutectl completion bash)

  # To load completions for each session, execute once:
  # Linux:
  $ novaroutectl completion bash > /etc/bash_completion.d/novaroutectl
  # macOS:
  $ novaroutectl completion bash > $(brew --prefix)/etc/bash_completion.d/novaroutectl

Zsh:
  # If shell completion is not already enabled in your environment,
  # you will need to enable it. You can execute the following once:
  $ echo "autoload -U compinit; compinit" >> ~/.zshrc

  # To load completions for each session, execute once:
  $ novaroutectl completion zsh > "${fpath[1]}/_novaroutectl"

  # You will need to start a new shell for this setup to take effect.

Fish:
  $ novaroutectl completion fish | source

  # To load completions for each session, execute once:
  $ novaroutectl completion fish > ~/.config/fish/completions/novaroutectl.fish

PowerShell:
  PS> novaroutectl completion powershell | Out-String | Invoke-Expression

  # To load completions for every new session, run:
  PS> novaroutectl completion powershell > novaroutectl.ps1
  # and source this file from your PowerShell profile.
`,
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return cmd.Root().GenBashCompletion(os.Stdout)
			case "zsh":
				return cmd.Root().GenZshCompletion(os.Stdout)
			case "fish":
				return cmd.Root().GenFishCompletion(os.Stdout, true)
			case "powershell":
				return cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
			default:
				return nil
			}
		},
	}

	return cmd
}
