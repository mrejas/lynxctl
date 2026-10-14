// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mrejas/lynx-cli/internal/output"
)

// knownNames are the names the binary is shipped under. Help texts use the
// name it was invoked as, so that lynxctl users never see "iotopenctl".
var knownNames = []string{"iotopenctl", "lynxctl"}

// commandName derives the program name from argv[0].
func commandName(arg0 string) string {
	// Handle both separators so that Windows paths work everywhere.
	base := arg0[strings.LastIndexAny(arg0, `/\`)+1:]
	base = strings.ToLower(base)
	base = strings.TrimSuffix(base, ".exe")
	for _, n := range knownNames {
		if base == n {
			return n
		}
	}
	return knownNames[0]
}

// Run executes the CLI and returns the process exit code.
func Run(args []string, version string, stdin io.Reader, stdout, stderr io.Writer) int {
	app := &App{
		Name:    commandName(args[0]),
		Version: version,
		In:      stdin,
		Out:     stdout,
		Err:     stderr,
	}
	return app.Run(args[1:])
}

// Run executes the CLI with the given arguments (without the program name).
func (a *App) Run(args []string) int {
	root := a.newRootCommand()
	root.SetArgs(args)
	err := root.Execute()
	if err != nil {
		fmt.Fprintf(a.Err, "Error: %s\n", a.errorMessage(err))
	}
	return exitCode(err)
}

func (a *App) newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   a.Name,
		Short: "Command line interface for IoT Open (formerly Lynx)",
		Long: fmt.Sprintf(`%s is a command line interface for the IoT Open platform (formerly Lynx).

Get started:
  %[1]s login                      Log in to https://lynx.iotopen.se
  %[1]s use <installation>         Select the installation to work with
  %[1]s api GET user/me            Call any API endpoint`, a.Name),
		Version:       a.Version,
		SilenceErrors: true,
		SilenceUsage:  true,
		// Show help when run without a subcommand.
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	root.SetIn(a.In)
	root.SetOut(a.Out)
	root.SetErr(a.Err)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageError(err)
	})
	root.SetVersionTemplate("{{.Name}} {{.Version}}\n")

	pf := root.PersistentFlags()
	pf.StringVar(&a.profileFlag, "profile", "", "profile to use (default: the current profile)")
	pf.StringVarP(&a.installationFlag, "installation", "i", "", "installation ID or name (default: the current installation)")
	pf.StringVarP(&a.outputFlag, "output", "o", "table", "output format: table, wide, json, yaml or csv")
	pf.BoolVarP(&a.quiet, "quiet", "q", false, "print only IDs or names, one per line")
	pf.BoolVar(&a.debug, "debug", false, "print HTTP requests and responses to stderr (secrets redacted)")

	_ = root.RegisterFlagCompletionFunc("profile", a.completeProfiles)
	_ = root.RegisterFlagCompletionFunc("installation", a.completeInstallations)
	_ = root.RegisterFlagCompletionFunc("output", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		names := make([]string, len(output.Formats))
		for i, f := range output.Formats {
			names[i] = string(f)
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	})

	root.AddCommand(
		a.newLoginCommand(),
		a.newLogoutCommand(),
		a.newWhoamiCommand(),
		a.newProfileCommand(),
		a.newUseCommand(),
		a.newGetCommand(),
		a.newSetCommand(),
		a.newWatchCommand(),
		a.newLogCommand(),
		a.newInstallationCommand(),
		a.newObjectCommand(deviceType),
		a.newObjectCommand(functionType),
		a.newAPICommand(),
		a.newVersionCommand(),
	)
	return root
}

// usageArgs marks argument validation errors as usage errors (exit code 2).
func usageArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validate(cmd, args); err != nil {
			return usageError(err)
		}
		return nil
	}
}

// groupCommand returns a command that only holds subcommands.
func groupCommand(use, short string, aliases ...string) *cobra.Command {
	return &cobra.Command{
		Use:     use,
		Short:   short,
		Aliases: aliases,
		Args:    usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
}

func (a *App) completeProfiles(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	cfg, err := a.config()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	return cfg.ProfileNames(), cobra.ShellCompDirectiveNoFileComp
}

func (a *App) newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  usageArgs(cobra.NoArgs),
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(a.Out, "%s %s\n", a.Name, a.Version)
		},
	}
}
