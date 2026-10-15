// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mrejas/lynx-cli/internal/api"
	"github.com/mrejas/lynx-cli/internal/config"
	"github.com/mrejas/lynx-cli/internal/output"
	"github.com/mrejas/lynx-cli/internal/resolve"
)

type loginOptions struct {
	server        string
	username      string
	passwordStdin bool
	apiKey        bool
}

func (a *App) newLoginCommand() *cobra.Command {
	var opts loginOptions
	cmd := &cobra.Command{
		Use:   "login [server | profile]",
		Short: "Log in to a server and make it the current profile",
		Long: `Log in with username and password (and a two-factor code if enabled), or
with an API key. The credentials are stored in the system keyring, or in a
file readable only by you if no keyring is available.

You can be logged in to several servers at once. Each login is kept in a
profile, which is named after the server unless --profile is given. Logging
in to a server again reuses its profile. The profile you log in to becomes
the current one.

An argument without a dot, colon or slash is taken as the name (or part of
the name) of an existing profile. Without an argument, the current profile is
logged in again, or ` + config.DefaultURL + ` if there is none.`,
		Example: fmt.Sprintf(`  # Log in to https://lynx.iotopen.se
  %[1]s login

  # Log in to another server as well (profile "lynx-dev.iotopen.se")
  %[1]s login lynx-dev.iotopen.se

  # Log in again to that profile when the session has expired
  %[1]s login dev

  # Pick a shorter profile name, or use a second account on the same server
  %[1]s login --profile customer customer.example.com

  # Log in with an API key, for example in a script
  echo "$KEY" | %[1]s login --api-key

  # Non-interactive password login
  %[1]s login -u jane@example.com --password-stdin < password.txt`, a.Name),
		Args: usageArgs(cobra.MaximumNArgs(1)),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return a.knownServers(), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				opts.server = args[0]
			}
			return a.login(cmd.Context(), opts)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&opts.username, "username", "u", "", "username (email address)")
	f.BoolVar(&opts.passwordStdin, "password-stdin", false, "read the password from stdin")
	f.BoolVar(&opts.apiKey, "api-key", false, "log in with an API key instead of a password (read from the prompt or stdin)")
	cmd.MarkFlagsMutuallyExclusive("api-key", "password-stdin")
	cmd.MarkFlagsMutuallyExclusive("api-key", "username")
	return cmd
}

// loginTarget decides which profile and server a login is for.
func (a *App) loginTarget(server string) (name, baseURL string, err error) {
	cfg, err := a.config()
	if err != nil {
		return "", "", err
	}
	// An argument that cannot be a server address ("dev") refers to an
	// existing profile, which makes re-login after an expired session short.
	var profileRef string
	if server != "" && !strings.ContainsAny(server, ".:/") {
		profileRef, server = server, ""
	}
	rawURL := server
	if rawURL == "" {
		rawURL = config.Getenv("URL")
	}

	explicit := a.profileFlag != "" || config.Getenv("PROFILE") != ""
	switch {
	case profileRef != "":
		if explicit {
			return "", "", usageErrorf("%q is not a server address and --profile is also given", profileRef)
		}
		if name, err = a.findProfile(profileRef, false); err != nil {
			var nf *resolve.NotFoundError
			if errors.As(err, &nf) {
				return "", "", usageErrorf("%q is neither a profile nor a server address", profileRef)
			}
			return "", "", err
		}
		rawURL = ""
	case explicit:
		if name, err = a.profileName(); err != nil {
			return "", "", err
		}
	case rawURL != "":
		if baseURL, err = api.NormalizeURL(rawURL); err != nil {
			return "", "", usageError(err)
		}
		if name, err = a.profileForURL(cfg, baseURL); err != nil {
			return "", "", err
		}
	case cfg.CurrentProfile != "" && cfg.Profile(cfg.CurrentProfile) != nil:
		name = cfg.CurrentProfile
	default:
		if name, err = a.profileForURL(cfg, config.DefaultURL); err != nil {
			return "", "", err
		}
	}

	if rawURL == "" {
		if p := cfg.Profile(name); p != nil {
			rawURL = p.URL
		}
	}
	if rawURL == "" {
		rawURL = config.DefaultURL
	}
	if baseURL, err = api.NormalizeURL(rawURL); err != nil {
		return "", "", usageError(err)
	}
	return name, baseURL, nil
}

// profileForURL returns the profile already used for baseURL, or a new
// profile name derived from its host. If several profiles use the server,
// the current one wins; otherwise the user has to choose.
func (a *App) profileForURL(cfg *config.Config, baseURL string) (string, error) {
	var matches []string
	for _, name := range cfg.ProfileNames() {
		if u, err := api.NormalizeURL(cfg.Profile(name).URL); err == nil && u == baseURL {
			matches = append(matches, name)
		}
	}
	switch len(matches) {
	case 0:
		return hostOf(baseURL), nil
	case 1:
		return matches[0], nil
	}
	for _, m := range matches {
		if m == cfg.CurrentProfile {
			return m, nil
		}
	}
	return "", usageErrorf("several profiles use %s: %s. Choose one with --profile",
		baseURL, strings.Join(matches, ", "))
}

// knownServers lists the hosts of all profiles, for shell completion.
func (a *App) knownServers() []string {
	cfg, err := a.config()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var hosts []string
	for _, name := range cfg.ProfileNames() {
		if h := hostOf(cfg.Profile(name).URL); !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	return hosts
}

func (a *App) login(ctx context.Context, opts loginOptions) error {
	name, baseURL, err := a.loginTarget(opts.server)
	if err != nil {
		return err
	}
	cfg, err := a.config()
	if err != nil {
		return err
	}
	old := cfg.Profile(name)
	client := a.newClient(baseURL, "")

	var secret string
	var method config.AuthMethod
	if opts.apiKey {
		secret, err = a.promptSecret("API key: ")
		if err != nil {
			return err
		}
		secret = strings.TrimSpace(secret)
		if secret == "" {
			return usageErrorf("no API key given")
		}
		method = config.AuthAPIKey
	} else {
		username := opts.username
		if username == "" && old != nil && old.URL == baseURL {
			username = old.Username
		}
		secret, err = a.passwordLogin(ctx, client, username, opts.passwordStdin)
		if err != nil {
			return err
		}
		method = config.AuthPassword
	}

	client.APIKey = secret
	me, err := client.Me(ctx)
	if err != nil {
		if method == config.AuthAPIKey && api.IsStatus(err, http.StatusUnauthorized) {
			return authErrorf("the API key was rejected by %s", baseURL)
		}
		return err
	}

	// Invalidate the previous session of this profile, if any. Failures do
	// not matter: the token is replaced locally either way.
	if old != nil && old.Auth == config.AuthPassword {
		if prev, err := a.secrets().Get(name); err == nil && prev != secret {
			if oldURL, err := api.NormalizeURL(old.URL); err == nil {
				_ = a.newClient(oldURL, prev).Logout(ctx)
			}
		}
	}

	where, err := a.secrets().Set(name, secret)
	if err != nil {
		return fmt.Errorf("storing credentials: %w", err)
	}
	p := cfg.EnsureProfile(name)
	if p.URL != baseURL {
		// An installation on one server means nothing on another.
		p.Installation = nil
	}
	p.URL = baseURL
	p.Username = me.Email
	p.Auth = method
	previous := cfg.CurrentProfile
	cfg.CurrentProfile = name
	if err := cfg.Save(); err != nil {
		return err
	}

	a.info("Logged in to %s as %s (profile %q).", baseURL, userLabel(me), name)
	a.info("Credentials stored in %s.", where)
	if previous != "" && previous != name {
		a.info("Switched the current profile from %q to %q. Switch back with %q.",
			previous, name, a.Name+" profile use "+previous)
	}
	if p.Installation == nil {
		a.info("Next, select an installation: %s use <installation>", a.Name)
	}
	return nil
}

// passwordLogin logs in with username and password, handling two-factor
// authentication. It returns the session token.
func (a *App) passwordLogin(ctx context.Context, client *api.Client, username string, passwordStdin bool) (string, error) {
	interactive := a.stdinIsTerminal()
	if username == "" {
		if !interactive {
			return "", usageErrorf("no username given. Use --username")
		}
		var err error
		if username, err = a.prompt("Username: "); err != nil {
			return "", err
		}
		username = strings.TrimSpace(username)
	}

	var password string
	var err error
	switch {
	case passwordStdin:
		password, err = a.readLine()
	case interactive:
		password, err = a.promptSecret(fmt.Sprintf("Password for %s: ", username))
	default:
		return "", usageErrorf("cannot prompt for a password without a terminal. Use --password-stdin")
	}
	if err != nil {
		return "", err
	}

	resp, err := client.Login(ctx, username, password, api.LoginRequest{
		DeviceID:   deviceID(a.Name),
		AppVersion: a.Version,
	})
	if api.IsStatus(err, http.StatusUnauthorized) {
		return "", authErrorf("login failed: wrong username or password")
	}
	if err != nil {
		return "", err
	}

	token := resp.Token
	if resp.NeedsChallenge() {
		code, err := a.prompt("Two-factor code: ")
		if err != nil {
			return "", err
		}
		client.APIKey = token
		token, err = client.Challenge(ctx, strings.TrimSpace(code))
		if api.IsStatus(err, http.StatusUnauthorized) {
			return "", authErrorf("login failed: wrong two-factor code")
		}
		if err != nil {
			return "", err
		}
	}
	if token == "" {
		return "", errors.New("login failed: the server returned no token")
	}
	return token, nil
}

// deviceID labels the session on the server, e.g. "lynxctl@laptop".
func deviceID(name string) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return name
	}
	return name + "@" + host
}

func userLabel(u *api.User) string {
	if n := u.FullName(); n != "" {
		return fmt.Sprintf("%s <%s>", n, u.Email)
	}
	return u.Email
}

func (a *App) newLogoutCommand() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "logout [profile]",
		Short: "Log out and remove the stored credentials",
		Long: `Log out from a profile (default: the active one), or from all profiles with
--all. The profile can be given by name or a unique part of the name.

A session token from a password login is invalidated on the server. An API
key is only removed locally; it stays valid until it is deleted on the
server. The profile itself (server URL, username, current installation) is
kept.`,
		Example: fmt.Sprintf(`  %[1]s logout
  %[1]s logout dev
  %[1]s logout --all`, a.Name),
		Args:              usageArgs(cobra.MaximumNArgs(1)),
		ValidArgsFunction: a.completeProfileArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			names, err := a.logoutTargets(args, all)
			if err != nil {
				return err
			}
			for _, name := range names {
				if err := a.logout(cmd.Context(), name); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "log out from all profiles")
	return cmd
}

func (a *App) logoutTargets(args []string, all bool) ([]string, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	switch {
	case all && len(args) > 0:
		return nil, usageErrorf("--all cannot be combined with a profile")
	case all:
		return cfg.ProfileNames(), nil
	case len(args) == 1:
		name, err := a.lookupProfile(args[0], false)
		if err != nil {
			return nil, err
		}
		return []string{name}, nil
	}
	name, err := a.profileName()
	if err != nil {
		return nil, err
	}
	if name == "" {
		a.info("Not logged in.")
		return nil, nil
	}
	if cfg.Profile(name) == nil {
		return nil, a.noSuchProfile(name)
	}
	return []string{name}, nil
}

func (a *App) logout(ctx context.Context, name string) error {
	cfg, err := a.config()
	if err != nil {
		return err
	}
	p := cfg.Profile(name)
	secret, err := a.secrets().Get(name)
	if errors.Is(err, config.ErrNoSecret) {
		a.info("Not logged in to %s (profile %q).", p.URL, name)
		return nil
	}
	if err != nil {
		return err
	}
	if p.Auth == config.AuthPassword {
		if baseURL, err := api.NormalizeURL(p.URL); err == nil {
			err := a.newClient(baseURL, secret).Logout(ctx)
			if err != nil && !api.IsStatus(err, http.StatusUnauthorized) {
				a.info("Warning: could not invalidate the session on the server: %v", err)
			}
		}
	}
	if err := a.secrets().Delete(name); err != nil {
		return err
	}
	a.info("Logged out from %s (profile %q).", p.URL, name)
	return nil
}

func (a *App) newWhoamiCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the logged in user, server and current installation",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.whoami(cmd.Context())
		},
	}
}

func (a *App) whoami(ctx context.Context) error {
	pr, err := a.printer()
	if err != nil {
		return err
	}
	s, err := a.session()
	if err != nil {
		return err
	}
	me, err := s.Client.Me(ctx)
	if err != nil {
		return err
	}
	// The installation is informational here; not having one is fine.
	inst, _ := a.installation(ctx, s)

	auth := "API key from environment"
	if !s.FromEnv && s.Profile != nil {
		switch s.Profile.Auth {
		case config.AuthAPIKey:
			auth = "API key"
		default:
			auth = "password (session token)"
		}
	}

	data := struct {
		Profile      string               `json:"profile"`
		Server       string               `json:"server"`
		Auth         string               `json:"auth"`
		User         *api.User            `json:"user"`
		Installation *config.Installation `json:"installation,omitempty"`
	}{s.ProfileName, s.URL, auth, me, inst}

	return pr.Details(data, me.Email, []output.Field{
		{Label: "User", Value: userLabel(me)},
		{Label: "Server", Value: s.URL},
		{Label: "Profile", Value: cmp.Or(s.ProfileName, "(none)")},
		{Label: "Auth", Value: auth},
		{Label: "Installation", Value: formatInstallation(inst, a.Name)},
	})
}

// formatInstallation gives e.g. `Home (ID 42, client ID 1010)`.
func formatInstallation(inst *config.Installation, name string) string {
	if inst == nil {
		return fmt.Sprintf("none (select one with %q)", name+" use <installation>")
	}
	ids := fmt.Sprintf("ID %d", inst.ID)
	if inst.ClientID != 0 {
		ids += fmt.Sprintf(", client ID %d", inst.ClientID)
	}
	if inst.Name == "" {
		return ids
	}
	return fmt.Sprintf("%s (%s)", inst.Name, ids)
}
