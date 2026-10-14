// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/mrejas/lynx-cli/internal/api"
	"github.com/mrejas/lynx-cli/internal/config"
	"github.com/mrejas/lynx-cli/internal/output"
	"github.com/mrejas/lynx-cli/internal/resolve"
)

// profileView is the JSON/YAML representation of a profile.
type profileView struct {
	Name         string               `json:"name"`
	Current      bool                 `json:"current"`
	URL          string               `json:"url"`
	Username     string               `json:"username,omitempty"`
	Auth         config.AuthMethod    `json:"auth,omitempty"`
	LoggedIn     bool                 `json:"logged_in"`
	Installation *config.Installation `json:"installation,omitempty"`
}

func (a *App) profileView(cfg *config.Config, name string) profileView {
	p := cfg.Profile(name)
	_, err := a.secrets().Get(name)
	return profileView{
		Name:         name,
		Current:      cfg.CurrentProfile == name,
		URL:          p.URL,
		Username:     p.Username,
		Auth:         p.Auth,
		LoggedIn:     err == nil,
		Installation: p.Installation,
	}
}

func (a *App) newProfileCommand() *cobra.Command {
	cmd := groupCommand("profile", "Manage profiles (one per server)")
	cmd.Long = `A profile is one IoT Open server with its own credentials and current
installation. Profiles are created by "login --profile <name>".`
	cmd.AddCommand(
		a.newProfileListCommand(),
		a.newProfileUseCommand(),
		a.newProfileShowCommand(),
		a.newProfileDeleteCommand(),
	)
	return cmd
}

func (a *App) newProfileListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List profiles",
		Args:    usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			pr, err := a.printer()
			if err != nil {
				return err
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			views := []profileView{}
			rows := output.Rows{Columns: []output.Column{
				{Header: "current"}, {Header: "name"}, {Header: "url"}, {Header: "user"},
				{Header: "installation"}, {Header: "status"}, {Header: "auth", Wide: true},
			}, IDColumn: 1}
			for _, name := range cfg.ProfileNames() {
				v := a.profileView(cfg, name)
				views = append(views, v)
				current, status, inst := "", "logged out", ""
				if v.Current {
					current = "*"
				}
				if v.LoggedIn {
					status = "logged in"
				}
				if v.Installation != nil {
					inst = formatInstallation(v.Installation, a.Name)
				}
				rows.Rows = append(rows.Rows, []string{current, name, v.URL, v.Username, inst, status, string(v.Auth)})
			}
			if len(views) == 0 && !a.quiet && pr.Format == output.Table {
				a.info("No profiles yet. Run %q to create one.", a.Name+" login")
				return nil
			}
			return pr.List(views, rows)
		},
	}
}

func (a *App) newProfileUseCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "use <profile>",
		Short:             "Make a profile the current one",
		Args:              usageArgs(cobra.ExactArgs(1)),
		ValidArgsFunction: a.completeProfileArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			name, err := a.lookupProfile(args[0], false)
			if err != nil {
				return err
			}
			cfg.CurrentProfile = name
			if err := cfg.Save(); err != nil {
				return err
			}
			a.info("Now using profile %q (%s).", name, cfg.Profile(name).URL)
			return nil
		},
	}
}

func (a *App) newProfileShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "show [profile]",
		Short:             "Show a profile (default: the active one)",
		Args:              usageArgs(cobra.MaximumNArgs(1)),
		ValidArgsFunction: a.completeProfileArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			pr, err := a.printer()
			if err != nil {
				return err
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			name, err := a.profileName()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				if name, err = a.lookupProfile(args[0], false); err != nil {
					return err
				}
			}
			if name == "" {
				return &exitError{code: ExitNotFound, err: fmt.Errorf("no profile selected. Run %q", a.Name+" profile list")}
			}
			if cfg.Profile(name) == nil {
				return a.noSuchProfile(name)
			}
			v := a.profileView(cfg, name)
			status := "logged out"
			if v.LoggedIn {
				status = "logged in"
			}
			return pr.Details(v, name, []output.Field{
				{Label: "Name", Value: name},
				{Label: "Current", Value: strconv.FormatBool(v.Current)},
				{Label: "Server", Value: v.URL},
				{Label: "User", Value: v.Username},
				{Label: "Auth", Value: string(v.Auth)},
				{Label: "Status", Value: status},
				{Label: "Installation", Value: formatInstallation(v.Installation, a.Name)},
			})
		},
	}
}

func (a *App) newProfileDeleteCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:               "delete <profile>",
		Aliases:           []string{"rm"},
		Short:             "Log out and delete a profile (exact name required)",
		Args:              usageArgs(cobra.ExactArgs(1)),
		ValidArgsFunction: a.completeProfileArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.deleteProfile(cmd.Context(), args[0], yes)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func (a *App) deleteProfile(ctx context.Context, ref string, yes bool) error {
	cfg, err := a.config()
	if err != nil {
		return err
	}
	name, err := a.lookupProfile(ref, true)
	if err != nil {
		return err
	}
	p := cfg.Profile(name)
	if err := a.confirm(fmt.Sprintf("Delete profile %q (%s)?", name, p.URL), yes); err != nil {
		return err
	}
	secret, err := a.secrets().Get(name)
	if err == nil && p.Auth == config.AuthPassword {
		if baseURL, err := api.NormalizeURL(p.URL); err == nil {
			_ = a.newClient(baseURL, secret).Logout(ctx)
		}
	}
	if err := a.secrets().Delete(name); err != nil && !errors.Is(err, config.ErrNoSecret) {
		return err
	}
	cfg.DeleteProfile(name)
	if err := cfg.Save(); err != nil {
		return err
	}
	a.info("Deleted profile %q.", name)
	return nil
}

// lookupProfile resolves a profile argument, with a helpful error if there is
// no such profile.
func (a *App) lookupProfile(ref string, exact bool) (string, error) {
	name, err := a.findProfile(ref, exact)
	var nf *resolve.NotFoundError
	if errors.As(err, &nf) {
		return "", a.noSuchProfile(ref)
	}
	return name, err
}

func (a *App) noSuchProfile(name string) error {
	return &exitError{code: ExitNotFound, err: fmt.Errorf(
		"profile %q does not exist. Run %q to list profiles", name, a.Name+" profile list")}
}

func (a *App) completeProfileArg(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return a.completeProfiles(cmd, args, toComplete)
}
