package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/mrejas/lynx-cli/internal/api"
	"github.com/mrejas/lynx-cli/internal/output"
	"github.com/mrejas/lynx-cli/internal/resolve"
)

// editableInstallation is the part of an installation that create -f and
// edit accept.
type editableInstallation struct {
	Name           string            `yaml:"name"`
	OrganizationID int64             `yaml:"organization_id"`
	Notes          string            `yaml:"notes"`
	Users          []int64           `yaml:"users"`
	Meta           map[string]string `yaml:"meta"`
}

func (a *App) newInstallationCommand() *cobra.Command {
	cmd := groupCommand("installation", "Manage installations", "inst", "i", "installations")
	cmd.Long = `Manage installations. An installation is referred to by ID, client ID, name
or a unique part of the name. Commands that take an optional installation
default to the current one (see "use"). Deleting requires the ID or the
exact name.`
	cmd.AddCommand(
		a.newInstallationListCommand(),
		a.newInstallationGetCommand(),
		a.newInstallationCreateCommand(),
		a.newInstallationEditCommand(),
		a.newInstallationDeleteCommand(),
		a.newMetaCommand(metaTarget{
			noun: "installation",
			load: func(ctx context.Context, _ string) (*metaObject, error) {
				_, inst, err := a.fullInstallation(ctx, "")
				if err != nil {
					return nil, err
				}
				return &metaObject{
					Path:  api.InstallationPath(inst.ID),
					Label: "installation " + quoted(inst.Name, inst.ID),
					Meta:  inst.Meta,
				}, nil
			},
		}),
	)
	return cmd
}

// fullInstallation fetches the installation given by ref, or the current
// installation if ref is empty.
func (a *App) fullInstallation(ctx context.Context, ref string) (*session, *api.Installation, error) {
	s, err := a.session()
	if err != nil {
		return nil, nil, err
	}
	var id int64
	if ref == "" {
		cur, err := a.installation(ctx, s)
		if err != nil {
			return nil, nil, err
		}
		id = cur.ID
	} else {
		found, err := a.resolveInstallation(ctx, s, ref, resolve.Options{})
		if err != nil {
			return nil, nil, err
		}
		id = found.ID
	}
	inst, err := s.Client.GetInstallation(ctx, id)
	return s, inst, err
}

func (a *App) newInstallationListCommand() *cobra.Command {
	var meta []string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List installations",
		Example: fmt.Sprintf(`  %[1]s installation list
  %[1]s installation list --meta customer='ACME*'`, a.Name),
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			pr, err := a.printer()
			if err != nil {
				return err
			}
			filter, err := metaFilter(meta, "")
			if err != nil {
				return err
			}
			s, err := a.session()
			if err != nil {
				return err
			}
			list, err := s.Client.ListInstallations(cmd.Context(), filter)
			if err != nil {
				return err
			}
			sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
			var current int64
			if s.Profile != nil && s.Profile.Installation != nil {
				current = s.Profile.Installation.ID
			}
			rows := output.Rows{Columns: []output.Column{
				{Header: "current"}, {Header: "id"}, {Header: "name"}, {Header: "client id"},
				{Header: "organization"}, {Header: "notes", Wide: true}, {Header: "created", Wide: true},
			}, IDColumn: 1}
			for _, inst := range list {
				mark := ""
				if inst.ID == current {
					mark = "*"
				}
				rows.Rows = append(rows.Rows, []string{
					mark, strconv.FormatInt(inst.ID, 10), inst.Name, strconv.FormatInt(inst.ClientID, 10),
					strconv.FormatInt(inst.OrganizationID, 10), firstLine(inst.Notes), formatTime(inst.Created),
				})
			}
			if list == nil {
				list = []api.Installation{}
			}
			return pr.List(list, rows)
		},
	}
	cmd.Flags().StringArrayVar(&meta, "meta", nil, "only show installations whose meta `key=value` matches (* is a wildcard; repeatable)")
	return cmd
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func (a *App) completeInstallationArg(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return a.completeInstallations(cmd, args, toComplete)
}

func (a *App) newInstallationGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "get [installation]",
		Short:             "Show an installation (default: the current one)",
		Args:              usageArgs(cobra.MaximumNArgs(1)),
		ValidArgsFunction: a.completeInstallationArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			pr, err := a.printer()
			if err != nil {
				return err
			}
			_, inst, err := a.fullInstallation(cmd.Context(), firstArg(args))
			if err != nil {
				return err
			}
			return printInstallation(pr, inst)
		},
	}
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func printInstallation(pr *output.Printer, inst *api.Installation) error {
	users := make([]string, len(inst.Users))
	for i, u := range inst.Users {
		users[i] = strconv.FormatInt(u, 10)
	}
	fields := []output.Field{
		{Label: "ID", Value: strconv.FormatInt(inst.ID, 10)},
		{Label: "Name", Value: inst.Name},
		{Label: "Client ID", Value: strconv.FormatInt(inst.ClientID, 10)},
		{Label: "Organization", Value: strconv.FormatInt(inst.OrganizationID, 10)},
		{Label: "Users", Value: strings.Join(users, ", ")},
		{Label: "Created", Value: formatTime(inst.Created)},
		{Label: "Notes", Value: firstLine(inst.Notes)},
	}
	fields = append(fields, metaFields("Meta", inst.Meta)...)
	return pr.Details(inst, strconv.FormatInt(inst.ID, 10), fields)
}

func (a *App) newInstallationCreateCommand() *cobra.Command {
	var file, name, notes string
	var org int64
	var meta []string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an installation",
		Long: `Create an installation from flags, from a YAML or JSON file, or both: flags
are applied on top of the file. If you belong to exactly one organization,
--org can be left out.`,
		Example: fmt.Sprintf(`  %[1]s installation create --name "Main Street 1"
  %[1]s installation create --name "Main Street 1" --org 7 --meta customer=ACME
  %[1]s installation create -f installation.yaml`, a.Name),
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			pr, err := a.printer()
			if err != nil {
				return err
			}
			var e editableInstallation
			if file != "" {
				data, err := a.readInput(file)
				if err != nil {
					return err
				}
				if err := decodeStrict(data, &e); err != nil {
					return usageError(err)
				}
			}
			pairs, err := parsePairs(meta, "--meta")
			if err != nil {
				return err
			}
			inst := &api.Installation{Name: e.Name, OrganizationID: e.OrganizationID, Notes: e.Notes, Users: e.Users, Meta: api.Meta(e.Meta)}
			if name != "" {
				inst.Name = name
			}
			if notes != "" {
				inst.Notes = notes
			}
			if org != 0 {
				inst.OrganizationID = org
			}
			if inst.Meta == nil {
				inst.Meta = api.Meta{}
			}
			for _, p := range pairs {
				inst.Meta[p[0]] = p[1]
			}
			if inst.Users == nil {
				inst.Users = []int64{}
			}
			if inst.Name == "" {
				return usageErrorf("a name is required. Use --name or set it in the file")
			}

			s, err := a.session()
			if err != nil {
				return err
			}
			if inst.OrganizationID == 0 {
				me, err := s.Client.Me(ctx)
				if err != nil {
					return err
				}
				if len(me.Organisations) != 1 {
					return usageErrorf("you belong to %d organizations. Choose one with --org", len(me.Organisations))
				}
				inst.OrganizationID = me.Organisations[0]
			}
			created, err := s.Client.CreateInstallation(ctx, inst)
			if err != nil {
				return err
			}
			a.info("Created installation %s. Select it with %q.", quoted(created.Name, created.ID),
				a.Name+" use "+strconv.FormatInt(created.ID, 10))
			return printInstallation(pr, created)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&file, "file", "f", "", `YAML or JSON file to create from ("-" for stdin)`)
	f.StringVar(&name, "name", "", "name (required unless given in the file)")
	f.Int64Var(&org, "org", 0, "organization ID")
	f.StringVar(&notes, "notes", "", "notes")
	f.StringArrayVar(&meta, "meta", nil, "set a meta `key=value` (repeatable)")
	return cmd
}

func (a *App) newInstallationEditCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "edit [installation]",
		Short: "Edit an installation in your editor (default: the current one)",
		Long: `Open the installation as YAML in $VISUAL or $EDITOR (default: vi, or notepad
on Windows). When you save and close the editor, the changes are sent to the
server. If the server rejects them, the editor opens again with the error.
Save without changes to cancel.`,
		Args:              usageArgs(cobra.MaximumNArgs(1)),
		ValidArgsFunction: a.completeInstallationArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, inst, err := a.fullInstallation(ctx, firstArg(args))
			if err != nil {
				return err
			}
			body, err := yaml.Marshal(editableInstallation{
				Name: inst.Name, OrganizationID: inst.OrganizationID, Notes: inst.Notes, Users: inst.Users, Meta: inst.Meta,
			})
			if err != nil {
				return err
			}
			label := "installation " + quoted(inst.Name, inst.ID)
			header := fmt.Sprintf("Editing %s.\nRead-only: client ID %d, created %s.\nSave without changes to cancel.",
				label, inst.ClientID, formatTime(inst.Created))
			err = a.editLoop(header, body, func(data []byte) error {
				var e editableInstallation
				if err := decodeStrict(data, &e); err != nil {
					return err
				}
				if e.Name == "" {
					return errors.New("name must not be empty")
				}
				updated := *inst
				updated.Name, updated.OrganizationID, updated.Notes, updated.Meta = e.Name, e.OrganizationID, e.Notes, api.Meta(e.Meta)
				updated.Users = e.Users
				if updated.Users == nil {
					updated.Users = []int64{}
				}
				_, err := s.Client.UpdateInstallation(ctx, &updated)
				return err
			})
			if errors.Is(err, errEditCancelled) {
				a.info("No changes made.")
				return nil
			}
			if err != nil {
				return err
			}
			a.info("Updated %s.", label)
			return nil
		},
	}
}

func (a *App) newInstallationDeleteCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:               "delete <installation>",
		Aliases:           []string{"rm"},
		Short:             "Delete an installation (ID, client ID or exact name required)",
		Args:              usageArgs(cobra.ExactArgs(1)),
		ValidArgsFunction: a.completeInstallationArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := a.session()
			if err != nil {
				return err
			}
			inst, err := a.resolveInstallation(ctx, s, args[0], resolve.Options{Exact: true})
			if err != nil {
				return err
			}
			label := "installation " + quoted(inst.Name, inst.ID)
			if err := a.confirm(fmt.Sprintf("Delete %s?", label), yes); err != nil {
				return err
			}
			if err := s.Client.DeleteInstallation(ctx, inst.ID); err != nil {
				return err
			}
			a.info("Deleted %s.", label)

			// Do not leave a deleted installation selected.
			if p := s.Profile; p != nil && p.Installation != nil && p.Installation.ID == inst.ID {
				p.Installation = nil
				cfg, err := a.config()
				if err != nil {
					return err
				}
				return cfg.Save()
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
