package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newVersionCmd(info BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print jr version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("jr %s (%s, %s)\n", info.Version, info.Commit, info.Date)
		},
	}
}
