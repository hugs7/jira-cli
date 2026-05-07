package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/hugs7/jira-cli/internal/api"
	"github.com/hugs7/jira-cli/internal/gitctx"
	"github.com/hugs7/jira-cli/internal/tui"
)

func newIssueCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "issue",
		Short:   "Work with Jira issues",
		Aliases: []string{"i"},
	}
	c.AddCommand(
		newIssueViewCmd(),
		newIssueCommentCmd(),
		newIssueTransitionCmd(),
		newIssueStartCmd(),
		newIssueCurrentCmd(),
		newIssueCreateCmd(),
	)
	return c
}

// newIssueCreateCmd creates a new issue non-interactively. Project +
// summary are required; description, epic link, labels and
// components are optional. Description can be passed inline or read
// from a file via --description-file (use "-" for stdin).
func newIssueCreateCmd() *cobra.Command {
	var (
		hostFlag        string
		project         string
		summary         string
		issueType       string
		description     string
		descriptionFile string
		epicKey         string
		labels          []string
		components      []string
	)
	c := &cobra.Command{
		Use:   "create",
		Short: "Create a new Jira issue",
		Long: `Create a new Jira issue and print its key.

Project + summary are required. Description can be inline or read
from a file (use --description-file=- to read from stdin).
--epic links the new issue to a parent epic.

Examples:
  jr issue create -p TVD -s "Build login UI" -t Story --epic TVD-83792
  jr issue create -p TVD -s "Refactor api" --description-file ./body.md -l backend -l refactor`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == "" || summary == "" {
				return fmt.Errorf("--project and --summary are required")
			}
			body := description
			if descriptionFile != "" {
				b, err := readDescription(descriptionFile)
				if err != nil {
					return err
				}
				body = b
			}
			svc, err := defaultService(hostFlag)
			if err != nil {
				return err
			}
			issue, err := svc.CreateIssue(api.CreateIssueInput{
				Project:     strings.ToUpper(project),
				Summary:     summary,
				IssueType:   issueType,
				Description: body,
				EpicKey:     strings.ToUpper(epicKey),
				Labels:      labels,
				Components:  components,
			})
			if err != nil {
				return err
			}
			fmt.Printf("✓ created %s — %s\n  %s\n", issue.Key, issue.Summary, issue.WebURL)
			return nil
		},
	}
	c.Flags().StringVar(&hostFlag, "host", "", "Jira host to use")
	c.Flags().StringVarP(&project, "project", "p", "", "project key (e.g. TVD) [required]")
	c.Flags().StringVarP(&summary, "summary", "s", "", "issue summary / title [required]")
	c.Flags().StringVarP(&issueType, "type", "t", "Story", "issue type (Story, Task, Bug, …)")
	c.Flags().StringVarP(&description, "description", "d", "", "issue description (wiki markup)")
	c.Flags().StringVar(&descriptionFile, "description-file", "", "read description from file ('-' for stdin)")
	c.Flags().StringVarP(&epicKey, "epic", "e", "", "parent epic key to link via Epic Link")
	c.Flags().StringSliceVarP(&labels, "label", "l", nil, "label (repeatable)")
	c.Flags().StringSliceVar(&components, "component", nil, "component name (repeatable)")
	return c
}

// readDescription reads description body from a file, or stdin when
// path is "-". Returns the contents as-is (no trim) so callers can
// preserve trailing newlines if desired.
func readDescription(path string) (string, error) {
	if path == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// resolveKey returns the issue key from args[0] when provided, or
// falls back to detecting one in the current git branch name. This
// powers the "no-arg in a feature branch" UX shared by view /
// comment / transition.
func resolveKey(args []string) (string, error) {
	if len(args) > 0 && args[0] != "" {
		return strings.ToUpper(args[0]), nil
	}
	key, err := gitctx.CurrentIssueKey()
	if err != nil {
		return "", fmt.Errorf("%w (pass <KEY> explicitly)", err)
	}
	return key, nil
}

func newIssueViewCmd() *cobra.Command {
	var hostFlag string
	c := &cobra.Command{
		Use:   "view [KEY]",
		Short: "Open the interactive issue viewer (KEY defaults to current branch)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := defaultService(hostFlag)
			if err != nil {
				return err
			}
			key, err := resolveKey(args)
			if err != nil {
				return err
			}
			return tui.Issue(svc, key)
		},
	}
	c.Flags().StringVar(&hostFlag, "host", "", "Jira host to use")
	return c
}

func newIssueCommentCmd() *cobra.Command {
	var hostFlag string
	c := &cobra.Command{
		Use:   "comment [KEY]",
		Short: "Add a comment to an issue (opens $EDITOR)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := defaultService(hostFlag)
			if err != nil {
				return err
			}
			key, err := resolveKey(args)
			if err != nil {
				return err
			}
			body, err := captureEditorBody("jr-comment", "")
			if err != nil {
				return err
			}
			if body == "" {
				fmt.Println("aborted: empty comment")
				return nil
			}
			c, err := svc.AddComment(key, body)
			if err != nil {
				return err
			}
			fmt.Printf("✓ added comment %s on %s\n", c.ID, key)
			return nil
		},
	}
	c.Flags().StringVar(&hostFlag, "host", "", "Jira host to use")
	return c
}

func newIssueTransitionCmd() *cobra.Command {
	var hostFlag string
	c := &cobra.Command{
		Use:     "transition [KEY]",
		Short:   "Move an issue through its workflow",
		Aliases: []string{"t"},
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := defaultService(hostFlag)
			if err != nil {
				return err
			}
			key, err := resolveKey(args)
			if err != nil {
				return err
			}
			ts, err := svc.ListTransitions(key)
			if err != nil {
				return err
			}
			if len(ts) == 0 {
				return fmt.Errorf("no transitions available for %s", key)
			}

			opts := make([]huh.Option[string], 0, len(ts))
			for _, t := range ts {
				label := fmt.Sprintf("%s → %s", t.Name, t.To)
				opts = append(opts, huh.NewOption(label, t.ID))
			}
			var picked string
			if err := huh.NewSelect[string]().
				Title(fmt.Sprintf("Transition %s", key)).
				Options(opts...).
				Value(&picked).
				Run(); err != nil {
				return err
			}
			if err := svc.DoTransition(key, picked); err != nil {
				return err
			}
			fmt.Println(lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("✓ transitioned"))
			return nil
		},
	}
	c.Flags().StringVar(&hostFlag, "host", "", "Jira host to use")
	return c
}

// newIssueStartCmd creates (or switches to) a feature branch named
// after the given Jira issue, mirroring the workflow of `gh issue
// develop`. The branch slug is derived from the issue summary so the
// branch is self-documenting.
func newIssueStartCmd() *cobra.Command {
	var hostFlag, prefix string
	c := &cobra.Command{
		Use:   "start <KEY>",
		Short: "Create a git branch named after a Jira issue and check it out",
		Long: `Create a git branch from the current HEAD named after a Jira issue,
e.g. "PROJ-123-add-foo" (or "feature/PROJ-123-add-foo" with --prefix).
The branch slug is derived from the issue summary. If a matching local
branch already exists it's checked out instead of recreated.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := defaultService(hostFlag)
			if err != nil {
				return err
			}
			key := strings.ToUpper(args[0])
			issue, err := svc.GetIssue(key)
			if err != nil {
				return err
			}
			branch := gitctx.BranchName(prefix, issue.Key, issue.Summary)

			if gitctx.BranchExists(branch) {
				if err := gitctx.CheckoutBranch(branch); err != nil {
					return err
				}
				fmt.Printf("✓ checked out existing branch %s\n", branch)
				return nil
			}
			if err := gitctx.CheckoutNewBranch(branch); err != nil {
				return err
			}
			fmt.Printf("✓ created and checked out %s\n", branch)
			fmt.Printf("  %s — %s\n", issue.Key, issue.Summary)
			return nil
		},
	}
	c.Flags().StringVar(&hostFlag, "host", "", "Jira host to use")
	c.Flags().StringVar(&prefix, "prefix", "", "branch prefix (e.g. \"feature\", \"bugfix\")")
	return c
}

// newIssueCurrentCmd prints the issue key implied by the current git
// branch — useful for shell pipelines and scripts.
func newIssueCurrentCmd() *cobra.Command {
	var verbose bool
	c := &cobra.Command{
		Use:     "current",
		Short:   "Print the Jira issue key for the current git branch",
		Aliases: []string{"key"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			key, err := gitctx.CurrentIssueKey()
			if err != nil {
				return err
			}
			if !verbose {
				fmt.Println(key)
				return nil
			}
			svc, err := defaultService("")
			if err != nil {
				return err
			}
			issue, err := svc.GetIssue(key)
			if err != nil {
				return err
			}
			fmt.Printf("%s  %s  [%s]\n", issue.Key, issue.Summary, issue.Status)
			return nil
		},
	}
	c.Flags().BoolVarP(&verbose, "verbose", "v", false, "also fetch and print summary + status")
	return c
}

func newSearchCmd() *cobra.Command {
	var hostFlag string
	var max int
	c := &cobra.Command{
		Use:   "search <JQL>",
		Short: "Search issues by JQL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := defaultService(hostFlag)
			if err != nil {
				return err
			}
			issues, err := svc.SearchIssues(api.SearchInput{JQL: args[0], MaxResults: max})
			if err != nil {
				return err
			}
			if len(issues) == 0 {
				fmt.Println("(no results)")
				return nil
			}
			keyStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
			statusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
			for _, i := range issues {
				fmt.Printf("%-12s %-12s %s\n",
					keyStyle.Render(i.Key),
					statusStyle.Render(i.Status),
					i.Summary)
			}
			return nil
		},
	}
	c.Flags().StringVar(&hostFlag, "host", "", "Jira host to use")
	c.Flags().IntVarP(&max, "max", "n", 50, "maximum results to return")
	return c
}
