package cmd

import (
	"github.com/spf13/cobra"

	"github.com/hugs7/jira-cli/internal/config"
	"github.com/hugs7/jira-cli/internal/tui"
)

type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// NewRootCmd builds the top-level `jr` command tree.
func NewRootCmd(info BuildInfo) *cobra.Command {
	var cfgPath string

	root := &cobra.Command{
		Use:           "jr",
		Short:         "jr is a command-line interface for Jira",
		Long:          "jr is a fast, comprehensive CLI for Jira Cloud and Jira Data Center / Server.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return config.Load(cfgPath)
		},
		// `jr` with no args opens the interactive home dashboard.
		// The home model can return a "next action" (e.g. open a
		// specific issue); we loop until the user quits cleanly so
		// they can flow between TUIs without dropping back to the
		// shell each time.
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := defaultService("")
			if err != nil {
				return err
			}
			var state *tui.HomeState
			for {
				action, next, err := tui.Home(svc, state)
				if err != nil {
					return err
				}
				state = next
				if action == nil {
					return nil
				}
				switch action.Kind {
				case "issue":
					if err := tui.Issue(svc, action.Key); err != nil {
						return err
					}
				}
			}
		},
	}

	root.PersistentFlags().StringVar(&cfgPath, "config", "", "path to config file (default: $XDG_CONFIG_HOME/jr/config.yml)")

	root.AddCommand(
		newVersionCmd(info),
		newAuthCmd(),
		newIssueCmd(),
		newBoardCmd(),
		newSearchCmd(),
		newAPICmd(),
	)

	return root
}
