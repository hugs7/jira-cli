package main

import (
	"fmt"
	"os"

	"github.com/hugs7/jira-cli/internal/cmd"
)

// Set via -ldflags at release time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	root := cmd.NewRootCmd(cmd.BuildInfo{
		Version: version,
		Commit:  commit,
		Date:    date,
	})
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
