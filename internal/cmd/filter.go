package cmd

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/hugs7/jira-cli/internal/api"
)

func newFilterCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "filter",
		Short:   "Work with Jira saved filters",
		Aliases: []string{"f"},
	}
	c.AddCommand(
		newFilterListCmd(),
		newFilterViewCmd(),
		newFilterCreateCmd(),
		newFilterEditCmd(),
		newFilterDeleteCmd(),
		newFilterFavouriteCmd(),
		newFilterUnfavouriteCmd(),
		newFilterResultsCmd(),
		newFilterShareCmd(),
	)
	return c
}

// parseFilterID is the canonical "args[0] is a numeric filter id"
// guard used by every sub-command that takes one. Centralised so
// the error message is identical across commands.
func parseFilterID(s string) (int, error) {
	id, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("filter ID must be numeric, got %q", s)
	}
	return id, nil
}

// jqlPreview returns a single-line ellipsised slice of a JQL string
// for the list view. Newlines collapse to spaces; longer-than-max
// inputs get a trailing "…".
func jqlPreview(j string, max int) string {
	j = strings.Join(strings.Fields(j), " ")
	if len([]rune(j)) <= max {
		return j
	}
	r := []rune(j)
	return string(r[:max-1]) + "…"
}

func newFilterListCmd() *cobra.Command {
	var hostFlag, owner string
	c := &cobra.Command{
		Use:     "list",
		Short:   "List saved filters (default: your favourites)",
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		Long: `List saved filters as a tab-aligned table.

With no flags, lists the filters you have marked favourite.
Use --owner me to list filters you own (favourite or not), or
--owner <username> to list filters owned by someone else.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := defaultService(hostFlag)
			if err != nil {
				return err
			}
			filters, err := svc.ListFilters(owner)
			if err != nil {
				return err
			}
			if len(filters) == 0 {
				fmt.Println("(no filters)")
				return nil
			}
			idStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
			ownerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("141"))
			favStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
			for _, f := range filters {
				fav := " "
				if f.Favourite {
					fav = favStyle.Render("★")
				}
				fmt.Printf("%-8s %s %-22s %-32s %s\n",
					idStyle.Render(strconv.Itoa(f.ID)),
					fav,
					ownerStyle.Render(truncate(f.OwnerName, 22)),
					truncate(f.Name, 32),
					jqlPreview(f.JQL, 60),
				)
			}
			return nil
		},
	}
	c.Flags().StringVar(&hostFlag, "host", "", "Jira host to use")
	c.Flags().StringVar(&owner, "owner", "", "owner to scope to (me or <username>)")
	return c
}

// truncate clips s to n runes, appending "…" if it had to cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func newFilterViewCmd() *cobra.Command {
	var hostFlag string
	c := &cobra.Command{
		Use:   "view <id>",
		Short: "Show full details for a filter",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseFilterID(args[0])
			if err != nil {
				return err
			}
			svc, err := defaultService(hostFlag)
			if err != nil {
				return err
			}
			f, err := svc.GetFilter(id)
			if err != nil {
				return err
			}
			labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
			fav := "no"
			if f.Favourite {
				fav = "yes"
			}
			fmt.Printf("%s %d  %s\n", labelStyle.Render("Filter"), f.ID, f.Name)
			fmt.Printf("%s  %s (%s)\n", labelStyle.Render("Owner:    "), f.Owner, f.OwnerName)
			fmt.Printf("%s  %s\n", labelStyle.Render("Favourite:"), fav)
			if f.ViewURL != "" {
				fmt.Printf("%s  %s\n", labelStyle.Render("URL:      "), f.ViewURL)
			}
			if f.Description != "" {
				fmt.Printf("\n%s\n%s\n", labelStyle.Render("Description:"), f.Description)
			}
			fmt.Printf("\n%s\n%s\n", labelStyle.Render("JQL:"), f.JQL)
			return nil
		},
	}
	c.Flags().StringVar(&hostFlag, "host", "", "Jira host to use")
	return c
}

func newFilterCreateCmd() *cobra.Command {
	var hostFlag, name, jql, desc string
	var fav bool
	c := &cobra.Command{
		Use:   "create",
		Short: "Create a new saved filter",
		Long: `Create a new saved filter from a JQL query.

Examples:
  jr filter create --name "My open bugs" --jql 'assignee = currentUser() AND resolution = Unresolved' --fav
  jr filter create -n "Team backlog" --jql 'project = TVD AND sprint is EMPTY' -d "All un-sprinted TVD work"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" || jql == "" {
				return fmt.Errorf("--name and --jql are required")
			}
			svc, err := defaultService(hostFlag)
			if err != nil {
				return err
			}
			f, err := svc.CreateFilter(api.FilterInput{
				Name:        name,
				Description: desc,
				JQL:         jql,
				Favourite:   fav,
			})
			if err != nil {
				return err
			}
			fmt.Printf("✓ created filter %d — %s\n", f.ID, f.Name)
			if f.ViewURL != "" {
				fmt.Printf("  %s\n", f.ViewURL)
			}
			return nil
		},
	}
	c.Flags().StringVar(&hostFlag, "host", "", "Jira host to use")
	c.Flags().StringVarP(&name, "name", "n", "", "filter name [required]")
	c.Flags().StringVar(&jql, "jql", "", "JQL query [required]")
	c.Flags().StringVarP(&desc, "description", "d", "", "filter description")
	c.Flags().BoolVar(&fav, "fav", false, "mark as favourite")
	return c
}

func newFilterEditCmd() *cobra.Command {
	var hostFlag, name, jql, desc string
	var fav, noFav bool
	c := &cobra.Command{
		Use:   "edit <id>",
		Short: "Edit fields on an existing filter",
		Long: `Edit one or more fields on an existing filter.

Jira's filter PUT is a full replacement, so jr fetches the current
filter, applies the flags you set, and PUTs the merged body back —
unchanged fields are preserved.

Examples:
  jr filter edit 266068 --name "Open tickets"
  jr filter edit 266068 --jql 'assignee = jtenedero AND resolution = Unresolved'
  jr filter edit 266068 --no-fav`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseFilterID(args[0])
			if err != nil {
				return err
			}
			if fav && noFav {
				return fmt.Errorf("--fav and --no-fav are mutually exclusive")
			}
			svc, err := defaultService(hostFlag)
			if err != nil {
				return err
			}
			cur, err := svc.GetFilter(id)
			if err != nil {
				return err
			}
			in := api.FilterInput{
				Name:        cur.Name,
				Description: cur.Description,
				JQL:         cur.JQL,
				Favourite:   cur.Favourite,
			}
			if cmd.Flags().Changed("name") {
				in.Name = name
			}
			if cmd.Flags().Changed("jql") {
				in.JQL = jql
			}
			if cmd.Flags().Changed("description") {
				in.Description = desc
			}
			if cmd.Flags().Changed("fav") {
				in.Favourite = fav
			}
			if cmd.Flags().Changed("no-fav") {
				in.Favourite = false
			}
			f, err := svc.UpdateFilter(id, in)
			if err != nil {
				return err
			}
			fmt.Printf("✓ updated filter %d — %s\n", f.ID, f.Name)
			return nil
		},
	}
	c.Flags().StringVar(&hostFlag, "host", "", "Jira host to use")
	c.Flags().StringVarP(&name, "name", "n", "", "new filter name")
	c.Flags().StringVar(&jql, "jql", "", "new JQL query")
	c.Flags().StringVarP(&desc, "description", "d", "", "new description")
	c.Flags().BoolVar(&fav, "fav", false, "mark as favourite")
	c.Flags().BoolVar(&noFav, "no-fav", false, "unmark as favourite")
	return c
}

func newFilterDeleteCmd() *cobra.Command {
	var hostFlag string
	var yes bool
	c := &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"rm", "remove"},
		Short:   "Delete a saved filter",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseFilterID(args[0])
			if err != nil {
				return err
			}
			svc, err := defaultService(hostFlag)
			if err != nil {
				return err
			}
			// Fetch first so the confirmation can show the filter
			// name — much safer than asking "delete 266068?" with no
			// context.
			cur, err := svc.GetFilter(id)
			if err != nil {
				return err
			}
			if !yes {
				var confirm bool
				if err := huh.NewConfirm().
					Title(fmt.Sprintf("Delete filter %d (%q)?", cur.ID, cur.Name)).
					Value(&confirm).Run(); err != nil {
					return err
				}
				if !confirm {
					return nil
				}
			}
			if err := svc.DeleteFilter(id); err != nil {
				return err
			}
			fmt.Printf("✓ deleted filter %d\n", id)
			return nil
		},
	}
	c.Flags().StringVar(&hostFlag, "host", "", "Jira host to use")
	c.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	return c
}

func newFilterFavouriteCmd() *cobra.Command {
	var hostFlag string
	c := &cobra.Command{
		Use:     "favourite <id>",
		Aliases: []string{"fav", "favorite"},
		Short:   "Mark a filter as one of your favourites",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseFilterID(args[0])
			if err != nil {
				return err
			}
			svc, err := defaultService(hostFlag)
			if err != nil {
				return err
			}
			f, err := svc.SetFilterFavourite(id, true)
			if err != nil && !errors.Is(err, api.ErrFavouriteNotToggled) {
				return err
			}
			if errors.Is(err, api.ErrFavouriteNotToggled) {
				fmt.Printf("! filter %d — %s\n", f.ID, f.Name)
				fmt.Printf("  warning: %s\n", err.Error())
				return nil
			}
			fmt.Printf("✓ favourited filter %d — %s\n", f.ID, f.Name)
			return nil
		},
	}
	c.Flags().StringVar(&hostFlag, "host", "", "Jira host to use")
	return c
}

func newFilterUnfavouriteCmd() *cobra.Command {
	var hostFlag string
	c := &cobra.Command{
		Use:     "unfavourite <id>",
		Aliases: []string{"unfav", "unfavorite"},
		Short:   "Remove a filter from your favourites",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseFilterID(args[0])
			if err != nil {
				return err
			}
			svc, err := defaultService(hostFlag)
			if err != nil {
				return err
			}
			f, err := svc.SetFilterFavourite(id, false)
			if err != nil && !errors.Is(err, api.ErrFavouriteNotToggled) {
				return err
			}
			if errors.Is(err, api.ErrFavouriteNotToggled) {
				fmt.Printf("! filter %d — %s\n", f.ID, f.Name)
				fmt.Printf("  warning: %s\n", err.Error())
				return nil
			}
			fmt.Printf("✓ unfavourited filter %d — %s\n", f.ID, f.Name)
			return nil
		},
	}
	c.Flags().StringVar(&hostFlag, "host", "", "Jira host to use")
	return c
}

func newFilterResultsCmd() *cobra.Command {
	var hostFlag string
	var limit int
	c := &cobra.Command{
		Use:     "results <id>",
		Aliases: []string{"run"},
		Short:   "Run a filter's JQL and print the matching issues",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseFilterID(args[0])
			if err != nil {
				return err
			}
			svc, err := defaultService(hostFlag)
			if err != nil {
				return err
			}
			f, err := svc.GetFilter(id)
			if err != nil {
				return err
			}
			issues, err := svc.SearchIssues(api.SearchInput{JQL: f.JQL, MaxResults: limit})
			if err != nil {
				return err
			}
			if len(issues) == 0 {
				fmt.Println("(no results)")
				return nil
			}
			keyStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
			statusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
			assigneeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("141"))
			for _, i := range issues {
				assignee := i.Assignee
				if assignee == "" {
					assignee = "Unassigned"
				}
				fmt.Printf("%-12s %-14s %-20s %s\n",
					keyStyle.Render(i.Key),
					statusStyle.Render(truncate(i.Status, 14)),
					assigneeStyle.Render(truncate(assignee, 20)),
					i.Summary)
			}
			return nil
		},
	}
	c.Flags().StringVar(&hostFlag, "host", "", "Jira host to use")
	c.Flags().IntVarP(&limit, "limit", "L", 50, "maximum results to return")
	return c
}

func newFilterShareCmd() *cobra.Command {
	var hostFlag, shareType, project, group, user, role string
	c := &cobra.Command{
		Use:   "share <id>",
		Short: "Grant a share permission on a filter",
		Long: `Grant a share permission on a filter so other users can see it.
Required for Jira Software boards backed by the filter — without
this the board only renders for the filter owner.

Examples:
  jr filter share 266068                       # default: authenticated (any logged-in user)
  jr filter share 266068 --type project --project 12345
  jr filter share 266068 --type group --group "TVD Devs"
  jr filter share 266068 --type user --user F083814`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseFilterID(args[0])
			if err != nil {
				return err
			}
			svc, err := defaultService(hostFlag)
			if err != nil {
				return err
			}
			in := api.FilterShareInput{Type: strings.ToLower(shareType)}
			switch in.Type {
			case "project":
				if project == "" {
					return fmt.Errorf("--project is required for --type project")
				}
				in.ProjectID = project
			case "group":
				if group == "" {
					return fmt.Errorf("--group is required for --type group")
				}
				in.GroupName = group
			case "user":
				if user == "" {
					return fmt.Errorf("--user is required for --type user")
				}
				in.User = user
			case "projectrole":
				if role == "" {
					return fmt.Errorf("--role is required for --type projectrole")
				}
				in.ProjectRole = role
			case "global", "authenticated":
				// no extra fields needed
			default:
				return fmt.Errorf("unknown --type %q (want one of: authenticated, global, project, group, projectrole, user)", shareType)
			}
			if err := svc.AddFilterPermission(id, in); err != nil {
				return err
			}
			fmt.Printf("✓ added %s share permission to filter %d\n", in.Type, id)
			return nil
		},
	}
	c.Flags().StringVar(&hostFlag, "host", "", "Jira host to use")
	c.Flags().StringVar(&shareType, "type", "authenticated", "share type: authenticated, global, project, group, projectrole, user")
	c.Flags().StringVar(&project, "project", "", "project id (for --type project)")
	c.Flags().StringVar(&group, "group", "", "group name (for --type group)")
	c.Flags().StringVar(&user, "user", "", "username (for --type user)")
	c.Flags().StringVar(&role, "role", "", "project role id (for --type projectrole)")
	return c
}
