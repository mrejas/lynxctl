// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mrejas/lynx-cli/internal/config"
	"github.com/mrejas/lynx-cli/internal/output"
	"github.com/mrejas/lynx-cli/internal/resolve"
)

func (a *App) newUseCommand() *cobra.Command {
	var clearFlag bool
	cmd := &cobra.Command{
		Use:   "use [installation]",
		Short: "Select the installation to work with",
		Long: `Select the current installation of the active profile. Most commands
operate on it unless -i is given.

The installation can be given as ID, client ID, exact name or a unique part
of the name (case-insensitive). Without an argument, the current installation
is shown.`,
		Example: fmt.Sprintf(`  %[1]s use "Main Street 1"
  %[1]s use main          # unique part of the name
  %[1]s use 42            # ID or client ID
  %[1]s use               # show the current installation
  %[1]s use --clear`, a.Name),
		Args: usageArgs(cobra.MaximumNArgs(1)),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return a.completeInstallations(cmd, args, toComplete)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case clearFlag && len(args) > 0:
				return usageErrorf("--clear cannot be combined with an installation")
			case clearFlag:
				return a.clearInstallation()
			case len(args) == 0:
				return a.showInstallation()
			}
			return a.useInstallation(cmd, args[0])
		},
	}
	cmd.Flags().BoolVar(&clearFlag, "clear", false, "unset the current installation")
	return cmd
}

func (a *App) useInstallation(cmd *cobra.Command, ref string) error {
	s, err := a.session()
	if err != nil {
		return err
	}
	inst, err := a.resolveInstallation(cmd.Context(), s, ref, resolve.Options{})
	if err != nil {
		return err
	}
	cfg, err := a.config()
	if err != nil {
		return err
	}
	p := cfg.EnsureProfile(s.ProfileName)
	if p.URL == "" {
		p.URL = s.URL
	}
	p.Installation = inst
	if err := cfg.Save(); err != nil {
		return err
	}
	a.info("Now using installation %s (profile %q).", formatInstallation(inst, a.Name), s.ProfileName)
	return nil
}

func (a *App) showInstallation() error {
	pr, err := a.printer()
	if err != nil {
		return err
	}
	name, err := a.profileName()
	if err != nil {
		return err
	}
	cfg, err := a.config()
	if err != nil {
		return err
	}
	var inst *config.Installation
	if p := cfg.Profile(name); p != nil {
		inst = p.Installation
	}
	if inst == nil {
		return &exitError{code: ExitError, err: fmt.Errorf(
			"no installation selected (profile %q). Run %q", name, a.Name+" use <installation>")}
	}
	return pr.Details(inst, fmt.Sprint(inst.ID), []output.Field{
		{Label: "Installation", Value: inst.Name},
		{Label: "ID", Value: fmt.Sprint(inst.ID)},
		{Label: "Client ID", Value: fmt.Sprint(inst.ClientID)},
		{Label: "Profile", Value: name},
	})
}

func (a *App) clearInstallation() error {
	name, err := a.profileName()
	if err != nil {
		return err
	}
	cfg, err := a.config()
	if err != nil {
		return err
	}
	if p := cfg.Profile(name); p != nil && p.Installation != nil {
		p.Installation = nil
		if err := cfg.Save(); err != nil {
			return err
		}
	}
	a.info("No installation selected (profile %q).", name)
	return nil
}
