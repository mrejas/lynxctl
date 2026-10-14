package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mrejas/lynx-cli/internal/config"
)

// fakeServer is a minimal stand-in for the IoT Open API.
type fakeServer struct {
	*httptest.Server

	mu         sync.Mutex
	validKeys  map[string]bool
	loggedOut  []string
	lastPath   string
	lastQuery  string
	lastMethod string
	lastBody   string
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{validKeys: map[string]bool{"api-key-1": true}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/auth", func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		switch {
		case user == "jane@example.com" && pass == "secret":
			f.issue(w, "token-jane", "")
		case user == "twofa@example.com" && pass == "secret":
			f.issue(w, "token-challenge", "smslogin")
		default:
			unauthorized(w)
		}
	})
	mux.HandleFunc("PUT /api/v2/auth", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Challenge string }
		json.NewDecoder(r.Body).Decode(&body)
		if r.Header.Get("X-API-Key") != "token-challenge" || body.Challenge != "123456" {
			unauthorized(w)
			return
		}
		f.issue(w, "token-2fa", "")
	})
	mux.HandleFunc("DELETE /api/v2/auth", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		key := r.Header.Get("X-API-Key")
		f.loggedOut = append(f.loggedOut, key)
		delete(f.validKeys, key)
	})
	mux.HandleFunc("GET /api/v2/user/me", f.authed(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":7,"email":"jane@example.com","first_name":"Jane","last_name":"Doe","role":1,"organisations":[2]}`))
	}))
	mux.HandleFunc("GET /api/v2/installationinfo", f.authed(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
			{"id":10,"name":"Main Street 1","client_id":1010,"organization_id":2},
			{"id":11,"name":"Main Street 2","client_id":1011,"organization_id":2},
			{"id":12,"name":"Summer house","client_id":1012,"organization_id":2}]`))
	}))
	mux.HandleFunc("/api/v2/functionx/", f.authed(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.lastMethod, f.lastPath, f.lastQuery, f.lastBody = r.Method, r.URL.Path, r.URL.RawQuery, string(body)
		f.mu.Unlock()
		w.Write([]byte(`[{"id":1042,"type":"switch"}]`))
	}))
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// host is the profile name a login to this server gets by default.
func (f *fakeServer) host() string {
	return strings.TrimPrefix(f.URL, "http://")
}

func (f *fakeServer) issue(w http.ResponseWriter, token, nextStep string) {
	f.mu.Lock()
	f.validKeys[token] = true
	f.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]string{"token": token, "nextstep": nextStep})
}

func (f *fakeServer) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		ok := f.validKeys[r.Header.Get("X-API-Key")]
		f.mu.Unlock()
		if !ok {
			unauthorized(w)
			return
		}
		h(w, r)
	}
}

func unauthorized(w http.ResponseWriter) {
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(`{"message":"Unauthorized"}`))
}

// testEnv runs commands against a temporary config dir and file secret store.
type testEnv struct {
	t       *testing.T
	dir     string
	secrets *config.FileStore
}

func newTestEnv(t *testing.T) *testEnv {
	for _, name := range []string{"PROFILE", "URL", "API_KEY", "INSTALLATION", "CONFIG_DIR", "NO_KEYRING"} {
		t.Setenv("IOTOPEN_"+name, "")
		t.Setenv("LYNX_"+name, "")
	}
	dir := t.TempDir()
	return &testEnv{t: t, dir: dir, secrets: &config.FileStore{Path: filepath.Join(dir, "credentials.yaml")}}
}

type result struct {
	stdout, stderr string
	code           int
}

func (e *testEnv) run(stdin string, args ...string) result {
	e.t.Helper()
	var stdout, stderr strings.Builder
	app := &App{
		Name:      "iotopenctl",
		Version:   "test",
		In:        strings.NewReader(stdin),
		Out:       &stdout,
		Err:       &stderr,
		ConfigDir: e.dir,
		Secrets:   e.secrets,
	}
	code := app.Run(args)
	return result{stdout.String(), stderr.String(), code}
}

func (e *testEnv) mustRun(stdin string, args ...string) result {
	e.t.Helper()
	r := e.run(stdin, args...)
	if r.code != 0 {
		e.t.Fatalf("%v: exit %d\nstdout: %s\nstderr: %s", args, r.code, r.stdout, r.stderr)
	}
	return r
}

func (e *testEnv) config() *config.Config {
	e.t.Helper()
	cfg, err := config.Load(e.dir)
	if err != nil {
		e.t.Fatal(err)
	}
	return cfg
}

func (e *testEnv) login(srv *fakeServer, extra ...string) {
	e.t.Helper()
	args := append([]string{"login", srv.URL, "-u", "jane@example.com", "--password-stdin"}, extra...)
	e.mustRun("secret\n", args...)
}

func TestLoginWithPassword(t *testing.T) {
	srv := newFakeServer(t)
	env := newTestEnv(t)
	r := env.mustRun("secret\n", "login", srv.URL, "-u", "jane@example.com", "--password-stdin")
	if !strings.Contains(r.stderr, "Logged in to "+srv.URL+" as Jane Doe <jane@example.com>") {
		t.Errorf("unexpected stderr: %s", r.stderr)
	}
	cfg := env.config()
	p := cfg.Profile(srv.host())
	if cfg.CurrentProfile != srv.host() || p == nil || p.URL != srv.URL || p.Auth != config.AuthPassword || p.Username != "jane@example.com" {
		t.Errorf("unexpected config: %+v %+v", cfg, p)
	}
	if secret, _ := env.secrets.Get(srv.host()); secret != "token-jane" {
		t.Errorf("stored secret = %q", secret)
	}
}

func TestLoginWithTwoFactor(t *testing.T) {
	srv := newFakeServer(t)
	env := newTestEnv(t)
	env.mustRun("secret\n123456\n", "login", srv.URL, "-u", "twofa@example.com", "--password-stdin")
	if secret, _ := env.secrets.Get(srv.host()); secret != "token-2fa" {
		t.Errorf("stored secret = %q", secret)
	}
	r := env.run("secret\n000000\n", "login", srv.URL, "-u", "twofa@example.com", "--password-stdin")
	if r.code != ExitAuth || !strings.Contains(r.stderr, "wrong two-factor code") {
		t.Errorf("wrong code: exit %d, stderr %s", r.code, r.stderr)
	}
}

func TestLoginFailures(t *testing.T) {
	srv := newFakeServer(t)
	env := newTestEnv(t)
	r := env.run("wrong\n", "login", srv.URL, "-u", "jane@example.com", "--password-stdin")
	if r.code != ExitAuth || !strings.Contains(r.stderr, "wrong username or password") {
		t.Errorf("wrong password: exit %d, stderr %s", r.code, r.stderr)
	}
	r = env.run("", "login", srv.URL, "-u", "jane@example.com")
	if r.code != ExitUsage || !strings.Contains(r.stderr, "--password-stdin") {
		t.Errorf("no terminal: exit %d, stderr %s", r.code, r.stderr)
	}
	r = env.run("bogus\n", "login", srv.URL, "--api-key")
	if r.code != ExitAuth || !strings.Contains(r.stderr, "API key was rejected") {
		t.Errorf("bad API key: exit %d, stderr %s", r.code, r.stderr)
	}
	if env.config().Profile(srv.host()) != nil {
		t.Error("failed logins created a profile")
	}
}

func TestLoginWithAPIKeyAndLogoutKeepsKeyValid(t *testing.T) {
	srv := newFakeServer(t)
	env := newTestEnv(t)
	env.mustRun("api-key-1\n", "login", srv.URL, "--api-key")
	if p := env.config().Profile(srv.host()); p.Auth != config.AuthAPIKey {
		t.Errorf("auth = %q", p.Auth)
	}
	env.mustRun("", "logout")
	if len(srv.loggedOut) != 0 {
		t.Errorf("logout invalidated the API key on the server: %v", srv.loggedOut)
	}
	if r := env.run("", "whoami"); r.code != ExitAuth {
		t.Errorf("whoami after logout: exit %d", r.code)
	}
}

func TestReloginInvalidatesPreviousSession(t *testing.T) {
	srv := newFakeServer(t)
	env := newTestEnv(t)
	env.login(srv)
	env.mustRun("secret\n123456\n", "login", "-u", "twofa@example.com", "--password-stdin")
	if len(srv.loggedOut) != 1 || srv.loggedOut[0] != "token-jane" {
		t.Errorf("logged out tokens = %v", srv.loggedOut)
	}
}

func TestLogout(t *testing.T) {
	srv := newFakeServer(t)
	env := newTestEnv(t)
	env.login(srv)
	r := env.mustRun("", "logout")
	if !strings.Contains(r.stderr, "Logged out") || len(srv.loggedOut) != 1 || srv.loggedOut[0] != "token-jane" {
		t.Errorf("stderr %q, logged out %v", r.stderr, srv.loggedOut)
	}
	if _, err := env.secrets.Get(srv.host()); err == nil {
		t.Error("secret still stored after logout")
	}
	if env.config().Profile(srv.host()) == nil {
		t.Error("logout deleted the profile")
	}
}

func TestWhoami(t *testing.T) {
	srv := newFakeServer(t)
	env := newTestEnv(t)
	if r := env.run("", "whoami"); r.code != ExitAuth || !strings.Contains(r.stderr, `Run "iotopenctl login`) {
		t.Errorf("whoami before login: exit %d, stderr %s", r.code, r.stderr)
	}
	env.login(srv)
	r := env.mustRun("", "whoami")
	for _, want := range []string{"User:", "Jane Doe <jane@example.com>", "Server:", srv.URL, "none (select one with"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("whoami output lacks %q:\n%s", want, r.stdout)
		}
	}
	r = env.mustRun("", "whoami", "-o", "json")
	var data struct {
		Profile string
		User    struct{ Email string }
	}
	if err := json.Unmarshal([]byte(r.stdout), &data); err != nil || data.Profile != srv.host() || data.User.Email != "jane@example.com" {
		t.Errorf("json output %q: %v", r.stdout, err)
	}
	if r := env.mustRun("", "whoami", "-q"); r.stdout != "jane@example.com\n" {
		t.Errorf("quiet output = %q", r.stdout)
	}
}

func TestEnvironmentAPIKey(t *testing.T) {
	srv := newFakeServer(t)
	env := newTestEnv(t)
	t.Setenv("LYNX_URL", srv.URL)
	t.Setenv("LYNX_API_KEY", "api-key-1")
	r := env.mustRun("", "whoami")
	if !strings.Contains(r.stdout, "API key from environment") {
		t.Errorf("whoami output:\n%s", r.stdout)
	}
}

func TestUse(t *testing.T) {
	srv := newFakeServer(t)
	env := newTestEnv(t)
	env.login(srv)

	if r := env.run("", "use"); r.code != ExitError || !strings.Contains(r.stderr, "no installation selected") {
		t.Errorf("use without selection: exit %d, stderr %s", r.code, r.stderr)
	}

	r := env.run("", "use", "street")
	if r.code != ExitNotFound || !strings.Contains(r.stderr, "Main Street 1") || !strings.Contains(r.stderr, "Main Street 2") {
		t.Errorf("ambiguous: exit %d, stderr %s", r.code, r.stderr)
	}
	if r := env.run("", "use", "nowhere"); r.code != ExitNotFound {
		t.Errorf("not found: exit %d", r.code)
	}

	r = env.mustRun("", "use", "street 2")
	if !strings.Contains(r.stderr, "Main Street 2 (ID 11, client ID 1011)") {
		t.Errorf("stderr: %s", r.stderr)
	}
	if inst := env.config().Profile(srv.host()).Installation; inst == nil || inst.ID != 11 {
		t.Errorf("stored installation = %+v", inst)
	}

	env.mustRun("", "use", "1012") // client ID
	if r := env.mustRun("", "use", "-q"); r.stdout != "12\n" {
		t.Errorf("use -q = %q", r.stdout)
	}

	env.mustRun("", "use", "--clear")
	if inst := env.config().Profile(srv.host()).Installation; inst != nil {
		t.Errorf("installation after --clear = %+v", inst)
	}
}

func TestAPICommand(t *testing.T) {
	srv := newFakeServer(t)
	env := newTestEnv(t)
	env.login(srv)

	if r := env.run("", "api", "functionx/{installation}"); r.code != ExitError || !strings.Contains(r.stderr, "no installation selected") {
		t.Errorf("without installation: exit %d, stderr %s", r.code, r.stderr)
	}

	env.mustRun("", "use", "summer")
	r := env.mustRun("", "api", "functionx/{installation}?type=switch")
	if srv.lastMethod != "GET" || srv.lastPath != "/api/v2/functionx/12" || srv.lastQuery != "type=switch" {
		t.Errorf("request: %s %s?%s", srv.lastMethod, srv.lastPath, srv.lastQuery)
	}
	if r.stdout != `[{"id":1042,"type":"switch"}]` {
		t.Errorf("stdout = %q (not a terminal, so it should be unformatted)", r.stdout)
	}

	// -i overrides the current installation, and fields imply POST.
	env.mustRun("", "api", "-i", "main street 1", "/api/v2/functionx/{installation}", "-F", "meta.unit=°C", "-F", "count=3", "--raw-field", "code=007")
	if srv.lastMethod != "POST" || srv.lastPath != "/api/v2/functionx/10" {
		t.Errorf("request: %s %s", srv.lastMethod, srv.lastPath)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(srv.lastBody), &body); err != nil {
		t.Fatalf("body %q: %v", srv.lastBody, err)
	}
	if body["count"] != 3.0 || body["code"] != "007" || body["meta"].(map[string]any)["unit"] != "°C" {
		t.Errorf("body = %v", body)
	}

	env.mustRun(`{"type":"dimmer"}`, "api", "PUT", "functionx/{installation}/1042", "-f", "-")
	if srv.lastMethod != "PUT" || srv.lastBody != `{"type":"dimmer"}` {
		t.Errorf("request: %s %s", srv.lastMethod, srv.lastBody)
	}

	if r := env.run("", "api", "FETCH", "user/me"); r.code != ExitUsage {
		t.Errorf("invalid method: exit %d", r.code)
	}
	if r := env.run("", "api", "DELETE"); r.code != ExitUsage || !strings.Contains(r.stderr, "missing path") {
		t.Errorf("missing path: exit %d, stderr %s", r.code, r.stderr)
	}
	if r := env.run("", "api", "nothing/here"); r.code != ExitError || !strings.Contains(r.stderr, "404") {
		t.Errorf("404: exit %d, stderr %s", r.code, r.stderr)
	}
}

func TestSeveralServers(t *testing.T) {
	prod, dev := newFakeServer(t), newFakeServer(t)
	env := newTestEnv(t)

	env.login(prod)
	r := env.mustRun("secret\n", "login", "--profile", "dev", dev.URL, "-u", "jane@example.com", "--password-stdin")
	if !strings.Contains(r.stderr, "Switched the current profile from") {
		t.Errorf("no switch message: %s", r.stderr)
	}
	cfg := env.config()
	if cfg.CurrentProfile != "dev" || cfg.Profile(prod.host()) == nil {
		t.Fatalf("profiles after two logins: current %q, %v", cfg.CurrentProfile, cfg.ProfileNames())
	}
	want := prod.host() + "\ndev\n"
	if r := env.mustRun("", "profile", "list", "-q"); r.stdout != want {
		t.Errorf("profile list -q = %q, want %q", r.stdout, want)
	}

	// Logging in to a known server again reuses its profile.
	env.login(dev)
	if names := env.config().ProfileNames(); len(names) != 2 || env.config().CurrentProfile != "dev" {
		t.Errorf("re-login created a profile: %v", names)
	}

	// Profiles can be referred to by a unique part of the name.
	env.mustRun("", "profile", "use", "127.0")
	if cur := env.config().CurrentProfile; cur != prod.host() {
		t.Errorf("current profile = %q", cur)
	}
	if r := env.mustRun("", "--profile", "de", "profile", "show", "-q"); r.stdout != "dev\n" {
		t.Errorf("--profile de: %q", r.stdout)
	}

	// "login dev" logs in to the existing profile "dev" again.
	env.mustRun("secret\n", "login", "dev", "-u", "jane@example.com", "--password-stdin")
	if p := env.config().Profile("dev"); p.URL != dev.URL {
		t.Errorf("login dev changed the URL to %q", p.URL)
	}
	if r := env.run("", "login", "nosuchprofile"); r.code != ExitUsage {
		t.Errorf("login nosuchprofile: exit %d", r.code)
	}

	// Two profiles for the same server: the current one wins, otherwise the
	// user must choose.
	env.mustRun("secret\n", "login", "--profile", "second", prod.URL, "-u", "jane@example.com", "--password-stdin")
	env.mustRun("", "profile", "use", "dev")
	if r := env.run("secret\n", "login", prod.URL, "-u", "jane@example.com", "--password-stdin"); r.code != ExitUsage || !strings.Contains(r.stderr, "several profiles") {
		t.Errorf("ambiguous server: exit %d, stderr %s", r.code, r.stderr)
	}

	// Log out from one profile, then from all.
	env.mustRun("", "logout", "sec")
	if _, err := env.secrets.Get("second"); err == nil {
		t.Error("logout sec did not log out profile second")
	}
	if _, err := env.secrets.Get("dev"); err != nil {
		t.Error("logout sec logged out dev too")
	}
	env.mustRun("", "logout", "--all")
	for _, name := range env.config().ProfileNames() {
		if _, err := env.secrets.Get(name); err == nil {
			t.Errorf("profile %q still logged in after logout --all", name)
		}
	}
}

func TestProfileDelete(t *testing.T) {
	srv := newFakeServer(t)
	env := newTestEnv(t)
	env.login(srv, "--profile", "dev")

	if r := env.run("", "profile", "use", "nope"); r.code != ExitNotFound {
		t.Errorf("profile use nope: exit %d", r.code)
	}
	if r := env.run("", "profile", "delete", "de", "--yes"); r.code != ExitNotFound {
		t.Errorf("delete with a partial name: exit %d, want %d", r.code, ExitNotFound)
	}
	if r := env.run("", "profile", "delete", "dev"); r.code != ExitUsage || !strings.Contains(r.stderr, "--yes") {
		t.Errorf("delete without --yes: exit %d, stderr %s", r.code, r.stderr)
	}
	env.mustRun("", "profile", "delete", "dev", "--yes")
	cfg := env.config()
	if cfg.Profile("dev") != nil || cfg.CurrentProfile != "" {
		t.Errorf("after delete: %+v", cfg)
	}
	if _, err := env.secrets.Get("dev"); err == nil {
		t.Error("secret of deleted profile still stored")
	}
	if len(srv.loggedOut) != 1 {
		t.Errorf("deleting the profile did not invalidate its session: %v", srv.loggedOut)
	}
}

func TestUsageErrors(t *testing.T) {
	env := newTestEnv(t)
	for _, args := range [][]string{
		{"nosuchcommand"},
		{"whoami", "--nosuchflag"},
		{"whoami", "extra"},
		{"profile", "use"},
		{"-o", "xml", "profile", "list"},
		{"login", "--api-key", "-u", "x"},
		{"login", "dev"},
		{"logout", "--all", "dev"},
	} {
		if r := env.run("", args...); r.code != ExitUsage {
			t.Errorf("%v: exit %d, want %d (stderr %s)", args, r.code, ExitUsage, r.stderr)
		}
	}
}

func TestCommandName(t *testing.T) {
	tests := map[string]string{
		"/usr/bin/iotopenctl":           "iotopenctl",
		"/usr/local/bin/lynxctl":        "lynxctl",
		`C:\Tools\LYNXCTL.EXE`:          "lynxctl",
		"./iotopenctl.exe":              "iotopenctl",
		"/tmp/go-build123/exe/whatever": "iotopenctl",
	}
	for in, want := range tests {
		if got := commandName(in); got != want {
			t.Errorf("commandName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTypedValue(t *testing.T) {
	tests := map[string]string{
		"3":       "3",
		"-1.5":    "-1.5",
		"true":    "true",
		"null":    "null",
		"[1,2]":   "[1,2]",
		`{"a":1}`: `{"a":1}`,
		"°C":      `"°C"`,
		"007":     `"007"`,
		"1e":      `"1e"`,
		"":        `""`,
	}
	for in, want := range tests {
		data, err := json.Marshal(typedValue(in))
		if err != nil || string(data) != want {
			t.Errorf("typedValue(%q) = %s, %v; want %s", in, data, err, want)
		}
	}
}
