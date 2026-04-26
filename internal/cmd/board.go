package cmd

import (
	"fmt"
	"strconv"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/hugs7/jira-cli/internal/api"
	"github.com/hugs7/jira-cli/internal/tui"
)

func newBoardCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "board [ID]",
		Short:   "Open a Kanban-style board view",
		Aliases: []string{"b"},
		Long: `Open the interactive Kanban board TUI for a Jira Software board.

With no ID, jr lists the available boards (optionally filtered by
--project) so you can pick one. With an ID, jr jumps straight in.`,
		Args: cobra.MaximumNArgs(1),
	}

	var hostFlag, project, kind string
	c.Flags().StringVar(&hostFlag, "host", "", "Jira host to use")
	c.Flags().StringVar(&project, "project", "", "filter boards by project key")
	c.Flags().StringVar(&kind, "type", "", "filter boards by type (kanban / scrum)")

	c.RunE = func(cmd *cobra.Command, args []string) error {
		svc, err := defaultService(hostFlag)
		if err != nil {
			return err
		}

		boardID := 0
		if len(args) == 1 {
			id, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("board ID must be numeric, got %q", args[0])
			}
			boardID = id
		} else {
			boardID, err = pickBoard(svc, project, kind)
			if err != nil {
				return err
			}
			if boardID == 0 {
				return nil // user cancelled
			}
		}

		// Loop so opening an issue from the board returns the user
		// to the board on close.
		for {
			act, err := tui.Board(svc, boardID)
			if err != nil {
				return err
			}
			if act == nil {
				return nil
			}
			if act.Kind == "issue" {
				if err := tui.Issue(svc, act.Key); err != nil {
					return err
				}
				continue
			}
			return nil
		}
	}

	c.AddCommand(newBoardListCmd())
	return c
}

// pickBoard fetches the available boards for the host and renders a
// huh.Select so the user can pick one. Returns 0 with nil error if
// the user cancels.
func pickBoard(svc api.Service, project, kind string) (int, error) {
	boards, err := svc.ListBoards(project, kind, 200)
	if err != nil {
		return 0, err
	}
	if len(boards) == 0 {
		return 0, fmt.Errorf("no boards found (is the Jira Agile / Software addon installed?)")
	}
	opts := make([]huh.Option[int], 0, len(boards))
	for _, b := range boards {
		label := fmt.Sprintf("%s · %s · %s (id %d)", b.ProjectKey, b.Type, b.Name, b.ID)
		opts = append(opts, huh.NewOption(label, b.ID))
	}
	var picked int
	if err := huh.NewSelect[int]().
		Title("Pick a board").
		Options(opts...).
		Value(&picked).
		Run(); err != nil {
		return 0, nil // user cancelled
	}
	return picked, nil
}

// newBoardListCmd is a non-interactive list of boards, useful for
// scripting and finding an ID to pass to `jr board <id>`.
func newBoardListCmd() *cobra.Command {
	var hostFlag, project, kind string
	c := &cobra.Command{
		Use:     "list",
		Short:   "List available boards (non-interactive)",
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := defaultService(hostFlag)
			if err != nil {
				return err
			}
			boards, err := svc.ListBoards(project, kind, 200)
			if err != nil {
				return err
			}
			if len(boards) == 0 {
				fmt.Println("(no boards)")
				return nil
			}
			idStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
			typeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
			projStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("141"))
			for _, b := range boards {
				fmt.Printf("%-6s %-8s %-12s %s\n",
					idStyle.Render(strconv.Itoa(b.ID)),
					typeStyle.Render(b.Type),
					projStyle.Render(b.ProjectKey),
					b.Name)
			}
			return nil
		},
	}
	c.Flags().StringVar(&hostFlag, "host", "", "Jira host to use")
	c.Flags().StringVar(&project, "project", "", "filter boards by project key")
	c.Flags().StringVar(&kind, "type", "", "filter boards by type (kanban / scrum)")
	return c
}
