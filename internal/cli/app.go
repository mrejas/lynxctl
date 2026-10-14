// Package cli implements the iotopenctl commands.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/mrejas/lynx-cli/internal/api"
	"github.com/mrejas/lynx-cli/internal/config"
	"github.com/mrejas/lynx-cli/internal/output"
	"github.com/mrejas/lynx-cli/internal/resolve"
)

// App holds everything the commands share: streams, configuration and the
// values of the global flags.
type App struct {
	// Name is the name the program was invoked as (iotopenctl or lynxctl).
	Name    string
	Version string

	In  io.Reader
	Out io.Writer
	Err io.Writer

	// ConfigDir and Secrets may be set by tests. Otherwise they are
	// derived from the environment on first use.
	ConfigDir  string
	Secrets    config.SecretStore
	HTTPClient *http.Client

	// Global flags.
	profileFlag      string
	installationFlag string
	outputFlag       string
	quiet            bool
	debug            bool

	cfg   *config.Config
	stdin *bufio.Reader
}

// session is what a command needs to talk to the API.
type session struct {
	ProfileName string
	// Profile is nil when running purely from environment variables.
	Profile *config.Profile
	URL     string
	Client  *api.Client
	// FromEnv is true when the API key came from IOTOPEN_API_KEY.
	FromEnv bool
}

func (a *App) config() (*config.Config, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	if a.ConfigDir == "" {
		dir, err := config.Dir()
		if err != nil {
			return nil, err
		}
		a.ConfigDir = dir
	}
	cfg, err := config.Load(a.ConfigDir)
	if err != nil {
		return nil, err
	}
	a.cfg = cfg
	return cfg, nil
}

func (a *App) secrets() config.SecretStore {
	if a.Secrets == nil {
		a.Secrets = config.DefaultStore(a.ConfigDir)
	}
	return a.Secrets
}

// profileName returns the active profile: --profile, then IOTOPEN_PROFILE,
// then the current profile in the config. A reference from the flag or the
// environment may be an exact name or a unique part of one; if it matches no
// profile it is returned as is (login creates it). An empty name means no
// profile is selected.
func (a *App) profileName() (string, error) {
	ref := a.profileFlag
	if ref == "" {
		ref = config.Getenv("PROFILE")
	}
	cfg, err := a.config()
	if err != nil {
		return "", err
	}
	if ref == "" {
		return cfg.CurrentProfile, nil
	}
	name, err := a.findProfile(ref, false)
	var nf *resolve.NotFoundError
	if errors.As(err, &nf) {
		return ref, nil
	}
	return name, err
}

// findProfile resolves ref to the name of an existing profile: exact name,
// then (unless exact is set) a unique part of a name.
func (a *App) findProfile(ref string, exact bool) (string, error) {
	cfg, err := a.config()
	if err != nil {
		return "", err
	}
	return resolve.Find("profile", ref, cfg.ProfileNames(), func(n string) resolve.Key {
		return resolve.Key{Name: n}
	}, resolve.Options{Exact: exact, NameOnly: true})
}

func (a *App) newClient(baseURL, apiKey string) *api.Client {
	c := &api.Client{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		UserAgent:  config.AppName + "/" + a.Version,
		HTTPClient: a.HTTPClient,
	}
	if a.debug {
		c.Debug = a.Err
	}
	return c
}

// serverURL returns IOTOPEN_URL, then the profile's URL, then the default.
func serverURL(p *config.Profile) (string, error) {
	raw := config.Getenv("URL")
	if raw == "" && p != nil {
		raw = p.URL
	}
	if raw == "" {
		raw = config.DefaultURL
	}
	return api.NormalizeURL(raw)
}

// session returns an authenticated API client for the active profile.
func (a *App) session() (*session, error) {
	name, err := a.profileName()
	if err != nil {
		return nil, err
	}
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	s := &session{ProfileName: name, Profile: cfg.Profile(name)}
	if s.URL, err = serverURL(s.Profile); err != nil {
		return nil, err
	}

	key := config.Getenv("API_KEY")
	s.FromEnv = key != ""
	if key == "" {
		if s.Profile == nil {
			switch {
			case name == "" && len(cfg.Profiles) > 0:
				return nil, authErrorf("no profile selected. Run %q or pass --profile", a.Name+" profile use <profile>")
			case name == "":
				return nil, authErrorf("not logged in. Run %q", a.Name+" login [server]")
			}
			return nil, authErrorf("profile %q does not exist. Run %q to create it", name, a.Name+" login --profile "+name+" <server>")
		}
		key, err = a.secrets().Get(name)
		if errors.Is(err, config.ErrNoSecret) {
			return nil, authErrorf("not logged in (profile %q). Run %q", name, a.loginCommand(name))
		}
		if err != nil {
			return nil, fmt.Errorf("reading credentials for profile %q: %w", name, err)
		}
	}
	s.Client = a.newClient(s.URL, key)
	return s, nil
}

// loginCommand returns the command a user should run to log in to profile.
func (a *App) loginCommand(name string) string {
	if cfg, err := a.config(); err == nil {
		if p := cfg.Profile(name); p != nil && hostOf(p.URL) == name {
			return a.Name + " login " + name
		}
	}
	return a.Name + " login --profile " + name
}

// hostOf returns the host (with port, if any) of a server URL.
func hostOf(serverURL string) string {
	u, err := url.Parse(serverURL)
	if err != nil || u.Host == "" {
		return serverURL
	}
	return u.Host
}

// installation returns the installation to operate on: -i, then
// IOTOPEN_INSTALLATION, then the profile's current installation. A numeric
// reference from a flag or the environment is taken as an ID without an API
// call; names are resolved against the installations the user can access.
func (a *App) installation(ctx context.Context, s *session) (*config.Installation, error) {
	ref := a.installationFlag
	if ref == "" {
		ref = config.Getenv("INSTALLATION")
	}
	if ref == "" {
		if s.Profile != nil && s.Profile.Installation != nil {
			return s.Profile.Installation, nil
		}
		return nil, &exitError{code: ExitError, err: fmt.Errorf(
			"no installation selected. Run %q or pass -i", a.Name+" use <installation>")}
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		return &config.Installation{ID: id}, nil
	}
	return a.resolveInstallation(ctx, s, ref)
}

func (a *App) resolveInstallation(ctx context.Context, s *session, ref string) (*config.Installation, error) {
	list, err := s.Client.InstallationInfos(ctx)
	if err != nil {
		return nil, err
	}
	inst, err := resolve.Find("installation", ref, list, func(i api.InstallationInfo) resolve.Key {
		return resolve.Key{ID: i.ID, AltID: i.ClientID, Name: i.Name}
	}, resolve.Options{})
	if err != nil {
		return nil, err
	}
	return &config.Installation{ID: inst.ID, Name: inst.Name, ClientID: inst.ClientID}, nil
}

func (a *App) printer() (*output.Printer, error) {
	f, err := output.ParseFormat(a.outputFlag)
	if err != nil {
		return nil, usageError(err)
	}
	return &output.Printer{Out: a.Out, Format: f, Quiet: a.quiet}, nil
}

// info prints an informational message to stderr, keeping stdout clean for
// data.
func (a *App) info(format string, args ...any) {
	fmt.Fprintf(a.Err, format+"\n", args...)
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func (a *App) stdinIsTerminal() bool  { return isTerminal(a.In) }
func (a *App) stdoutIsTerminal() bool { return isTerminal(a.Out) }

func (a *App) reader() *bufio.Reader {
	if a.stdin == nil {
		a.stdin = bufio.NewReader(a.In)
	}
	return a.stdin
}

// readLine reads one line from stdin without the line ending.
func (a *App) readLine() (string, error) {
	line, err := a.reader().ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		if errors.Is(err, io.EOF) {
			return "", errors.New("unexpected end of input")
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// prompt asks for a line of input. The label goes to stderr, and only when
// stdin is a terminal, so that piped input does not produce stray prompts.
func (a *App) prompt(label string) (string, error) {
	if a.stdinIsTerminal() {
		fmt.Fprint(a.Err, label)
	}
	return a.readLine()
}

// promptSecret asks for input without echoing it when stdin is a terminal.
func (a *App) promptSecret(label string) (string, error) {
	f, ok := a.In.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return a.prompt(label)
	}
	fmt.Fprint(a.Err, label)
	secret, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(a.Err)
	return string(secret), err
}

// confirm asks a yes/no question unless yes is already set. Without a
// terminal to ask on, --yes is required.
func (a *App) confirm(question string, yes bool) error {
	if yes {
		return nil
	}
	if !a.stdinIsTerminal() {
		return usageErrorf("%s Pass --yes to confirm when not running interactively", question)
	}
	answer, err := a.prompt(question + " [y/N] ")
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	}
	return &exitError{code: ExitError, err: errors.New("aborted")}
}
