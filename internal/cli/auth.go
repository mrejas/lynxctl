package cli

import (
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
)

type loginOptions struct {
	url           string
	username      string
	passwordStdin bool
	apiKey        bool
}

func (a *App) newLoginCommand() *cobra.Command {
	var opts loginOptions
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in and store the credentials in a profile",
		Long: `Log in with username and password (and a two-factor code if enabled), or
with an API key. The credentials are stored in the system keyring, or in a
file readable only by you if no keyring is available.

Each profile is one server with its own credentials. Without --profile the
current profile is used, or "default" if there is none.`,
		Example: fmt.Sprintf(`  # Log in to https://lynx.iotopen.se
  %[1]s login

  # Log in to another server, in a separate profile
  %[1]s login --profile dev --url lynx-dev.iotopen.se

  # Log in with an API key, for example in a script
  echo "$KEY" | %[1]s login --api-key

  # Non-interactive password login
  %[1]s login -u jane@example.com --password-stdin < password.txt`, a.Name),
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.login(cmd.Context(), opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.url, "url", "", "server URL (default: the profile's URL or "+config.DefaultURL+")")
	f.StringVarP(&opts.username, "username", "u", "", "username (email address)")
	f.BoolVar(&opts.passwordStdin, "password-stdin", false, "read the password from stdin")
	f.BoolVar(&opts.apiKey, "api-key", false, "log in with an API key instead of a password (read from the prompt or stdin)")
	cmd.MarkFlagsMutuallyExclusive("api-key", "password-stdin")
	cmd.MarkFlagsMutuallyExclusive("api-key", "username")
	return cmd
}

func (a *App) login(ctx context.Context, opts loginOptions) error {
	name, err := a.profileName()
	if err != nil {
		return err
	}
	cfg, err := a.config()
	if err != nil {
		return err
	}
	old := cfg.Profile(name)

	rawURL := opts.url
	if rawURL == "" {
		rawURL = config.Getenv("URL")
	}
	if rawURL == "" && old != nil {
		rawURL = old.URL
	}
	if rawURL == "" {
		rawURL = config.DefaultURL
	}
	baseURL, err := api.NormalizeURL(rawURL)
	if err != nil {
		return usageError(err)
	}
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
	if cfg.CurrentProfile == "" {
		cfg.CurrentProfile = name
	}
	if err := cfg.Save(); err != nil {
		return err
	}

	a.info("Logged in to %s as %s (profile %q).", baseURL, userLabel(me), name)
	a.info("Credentials stored in %s.", where)
	if cfg.CurrentProfile != name {
		a.info("%q is not the current profile. Use --profile %s, or run %q.", name, name, a.Name+" profile use "+name)
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

// deviceID labels the session on the server, e.g. "iotopenctl@laptop".
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
	return &cobra.Command{
		Use:   "logout",
		Short: "Log out and remove the stored credentials",
		Long: `Log out from the active profile. A session token from a password login is
invalidated on the server. An API key is only removed locally; it stays valid
until it is deleted on the server.

The profile itself (server URL, username, current installation) is kept.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.logout(cmd.Context())
		},
	}
}

func (a *App) logout(ctx context.Context) error {
	name, err := a.profileName()
	if err != nil {
		return err
	}
	cfg, err := a.config()
	if err != nil {
		return err
	}
	p := cfg.Profile(name)
	if p == nil {
		return usageErrorf("profile %q does not exist", name)
	}
	secret, err := a.secrets().Get(name)
	if errors.Is(err, config.ErrNoSecret) {
		a.info("Not logged in (profile %q).", name)
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
		{Label: "Profile", Value: s.ProfileName},
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
