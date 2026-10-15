// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/mrejas/lynx-cli/internal/config"
)

// historySize is how many lines the shell keeps in its history file.
const historySize = 1000

func (a *App) newShellCommand() *cobra.Command {
	var errexit bool
	cmd := &cobra.Command{
		Use:   "shell",
		Short: "Start an interactive shell",
		Long: fmt.Sprintf(`Start an interactive shell where commands are typed without the program
name. The prompt shows the profile and the current installation.

On a terminal, the shell offers:
  - Tab completion of commands, flags and names, as in bash or zsh
  - history (arrow keys), kept between sessions
  - Ctrl-C to clear the line or to stop a running command (such as watch)
  - Ctrl-D, "exit" or "quit" to leave

Global flags given to the shell itself (--profile, -i, -o, -q, --debug) apply
to every command in it, and can be overridden per command. Commands change
the configuration as usual: "use" inside the shell also changes the current
installation outside it.

When stdin is not a terminal, commands are read from it one per line, which
makes the shell a simple way to run a script. Empty lines and lines starting
with # are ignored. A line starting with a space is not saved in the
history. The exit code is that of the last command, or the one given to
"exit".`),
		Example: fmt.Sprintf(`  %[1]s shell
  %[1]s --profile dev -i "main street" shell
  %[1]s shell -e < setup.txt`, a.Name),
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.inShell {
				return usageErrorf("already in the shell")
			}
			// Pass the global flags given to the shell on to every command.
			var flags []string
			cmd.InheritedFlags().VisitAll(func(f *pflag.Flag) {
				if f.Changed {
					flags = append(flags, "--"+f.Name+"="+f.Value.String())
				}
			})
			sh := &shell{app: a, flags: flags, errexit: errexit}
			code, err := sh.run(cmd.Context())
			if err != nil {
				return err
			}
			if code != ExitOK {
				// The command has already reported its error.
				return &exitError{code: code, err: errors.New("shell failed"), silent: true}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&errexit, "errexit", "e", false, "stop at the first failing command (when reading from a script)")
	return cmd
}

// shell runs commands read from a terminal or a script.
type shell struct {
	app     *App
	flags   []string
	errexit bool
	// status is the exit code of the last command.
	status  int
	history *shellHistory
}

func (sh *shell) run(ctx context.Context) (int, error) {
	if sh.app.stdinIsTerminal() {
		return sh.interactive(ctx)
	}
	return sh.script(ctx)
}

// script runs commands from a non-terminal stdin, one per line.
func (sh *shell) script(ctx context.Context) (int, error) {
	in := sh.app.reader()
	for {
		line, err := in.ReadString('\n')
		if err != nil && (!errors.Is(err, io.EOF) || line == "") {
			if errors.Is(err, io.EOF) {
				return sh.status, nil
			}
			return sh.status, err
		}
		code, exit := sh.execute(ctx, strings.TrimRight(line, "\r\n"), false)
		if exit || (sh.errexit && code != ExitOK) {
			return code, nil
		}
	}
}

// interactive reads commands from a terminal with line editing, history and
// completion.
func (sh *shell) interactive(ctx context.Context) (int, error) {
	a := sh.app
	fd := int(a.In.(*os.File).Fd())
	sh.history = loadHistory(filepath.Join(a.cacheDir(), "shell-history"))
	in := &interruptReader{r: a.In}
	t := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{in, a.Err}, "")
	t.History = sh.history
	t.AutoCompleteCallback = func(line string, pos int, key rune) (string, int, bool) {
		if key != '\t' {
			return "", 0, false
		}
		return sh.complete(t, line, pos)
	}

	fmt.Fprintf(a.Err, "%s %s. Type \"help\" for commands, Tab to complete, \"exit\" or Ctrl-D to leave.\n", a.Name, a.Version)
	for {
		t.SetPrompt(sh.prompt())
		if w, h, err := term.GetSize(terminalFd(a.Err, fd)); err == nil && w > 0 {
			_ = t.SetSize(w, h)
		}
		state, err := term.MakeRaw(fd)
		if err != nil {
			return sh.status, err
		}
		line, err := t.ReadLine()
		_ = term.Restore(fd, state)

		if in.interrupted {
			// Ctrl-C: drop the line and start over.
			in.interrupted = false
			sh.history.discard()
			continue
		}
		sh.history.commit()
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(a.Err)
			return sh.status, nil
		}
		if err != nil && !errors.Is(err, term.ErrPasteIndicator) {
			return sh.status, err
		}
		if code, exit := sh.execute(ctx, line, true); exit {
			return code, nil
		}
	}
}

// terminalFd returns the file descriptor to read the terminal size from:
// the output if it is a terminal (needed on Windows), otherwise stdin.
func terminalFd(out io.Writer, fallback int) int {
	if f, ok := out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return int(f.Fd())
	}
	return fallback
}

// execute runs one line. It returns the exit code and whether the shell
// should exit.
func (sh *shell) execute(ctx context.Context, line string, interactive bool) (int, bool) {
	words, open := splitWords(line)
	if open {
		fmt.Fprintln(sh.app.Err, "Error: unterminated quote")
		sh.status = ExitUsage
		return sh.status, false
	}
	args := sh.stripName(wordTexts(words))
	if len(args) == 0 {
		return sh.status, false
	}
	switch args[0] {
	case "exit", "quit":
		code := sh.status
		if len(args) > 1 {
			n, err := strconv.Atoi(args[1])
			if err != nil || len(args) > 2 {
				fmt.Fprintf(sh.app.Err, "Error: usage: %s [code]\n", args[0])
				sh.status = ExitUsage
				return sh.status, false
			}
			code = n
		}
		return code, true
	}

	child := sh.app.child()
	if interactive {
		// Ctrl-C stops the command, not the shell.
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
	} else {
		// Commands that read stdin continue where the script is.
		child.stdin = sh.app.reader()
	}
	sh.status = child.run(ctx, append(append([]string{}, sh.flags...), args...))
	return sh.status, false
}

// stripName removes a leading program name, so that command lines can be
// pasted as they are.
func (sh *shell) stripName(args []string) []string {
	if len(args) > 0 {
		for _, n := range append([]string{sh.app.Name}, knownNames...) {
			if args[0] == n {
				return args[1:]
			}
		}
	}
	return args
}

// prompt shows the profile and installation that commands will use.
func (sh *shell) prompt() string {
	c := sh.app.child()
	c.profileFlag = sh.app.profileFlag
	name, err := c.profileName()
	if err != nil || name == "" {
		return sh.app.Name + "> "
	}
	inst := sh.app.installationFlag
	if inst == "" {
		inst = config.Getenv("INSTALLATION")
	}
	if inst == "" {
		if cfg, err := c.config(); err == nil {
			if p := cfg.Profile(name); p != nil && p.Installation != nil {
				inst = p.Installation.Name
				if inst == "" {
					inst = strconv.FormatInt(p.Installation.ID, 10)
				}
			}
		}
	}
	if inst == "" {
		return name + "> "
	}
	return name + ":" + inst + "> "
}

// child returns an App for running one command from the shell. Each
// command gets fresh flags and reloads the configuration, so that changes
// made by an earlier command (such as "use") are seen.
func (a *App) child() *App {
	return &App{
		Name:       a.Name,
		Version:    a.Version,
		In:         a.In,
		Out:        a.Out,
		Err:        a.Err,
		ConfigDir:  a.ConfigDir,
		Secrets:    a.Secrets,
		HTTPClient: a.HTTPClient,
		RunEditor:  a.RunEditor,
		CacheDir:   a.CacheDir,
		Subscribe:  a.Subscribe,
		inShell:    true,
	}
}

// interruptReader turns Ctrl-C into Enter while the terminal is in raw mode
// and records that it happened. term.Terminal would otherwise report Ctrl-C
// as end of input, the same as Ctrl-D.
type interruptReader struct {
	r           io.Reader
	interrupted bool
}

func (ir *interruptReader) Read(p []byte) (int, error) {
	n, err := ir.r.Read(p)
	for i := range p[:n] {
		if p[i] == 3 {
			p[i] = '\r'
			ir.interrupted = true
		}
	}
	return n, err
}

// word is a word on a command line, with the offset where it starts.
type word struct {
	text  string
	start int
}

func wordTexts(words []word) []string {
	texts := make([]string, len(words))
	for i, w := range words {
		texts[i] = w.text
	}
	return texts
}

// splitWords splits a command line into words like a POSIX shell, but
// simpler: words are separated by whitespace, '...' is taken literally, and
// in "..." a backslash escapes " and \. Outside quotes, a backslash escapes
// whitespace, quotes and backslash, and is otherwise literal so that Windows
// paths can be typed as they are. A # at the start of a word starts a
// comment. open is true if the line ends inside quotes.
func splitWords(line string) (words []word, open bool) {
	var cur strings.Builder
	inWord := false
	var quote rune
	start := 0
	end := func() {
		if inWord {
			words = append(words, word{text: cur.String(), start: start})
			cur.Reset()
			inWord = false
		}
	}
	runes := []rune(line)
	offset := 0 // byte offset of runes[i]
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		size := len(string(r))
		var next rune
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch {
			case r == '"':
				quote = 0
			case r == '\\' && (next == '"' || next == '\\'):
				cur.WriteRune(next)
				i++
				size += len(string(next))
			default:
				cur.WriteRune(r)
			}
		case unicode.IsSpace(r):
			end()
		case r == '#' && !inWord:
			end()
			return words, false
		default:
			if !inWord {
				inWord = true
				start = offset
			}
			switch {
			case r == '\'' || r == '"':
				quote = r
			case r == '\\' && (unicode.IsSpace(next) || next == '\'' || next == '"' || next == '\\'):
				cur.WriteRune(next)
				i++
				size += len(string(next))
			default:
				cur.WriteRune(r)
			}
		}
		offset += size
	}
	end()
	return words, quote != 0
}

// quoteWord quotes s if needed so that splitWords gives it back as one
// word. With open set, the closing quote is left out, so that completion
// can continue the word.
func quoteWord(s string, open bool) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"\\#") {
		return s
	}
	closing := `"`
	if open {
		closing = ""
	}
	if !strings.Contains(s, "'") {
		if open {
			return "'" + s
		}
		return "'" + s + "'"
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + closing
}

// shellHistory is the history of the interactive shell. Lines are kept in
// memory by term.Terminal and appended to a file once they are run.
type shellHistory struct {
	path    string
	lines   []string // oldest first
	pending bool     // the last line was added but not committed
}

func loadHistory(path string) *shellHistory {
	h := &shellHistory{path: path}
	if data, err := os.ReadFile(path); err == nil {
		h.lines = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		if len(h.lines) == 1 && h.lines[0] == "" {
			h.lines = nil
		}
		if len(h.lines) > historySize {
			h.lines = h.lines[len(h.lines)-historySize:]
			h.save()
		}
	}
	return h
}

// Add implements term.History. Empty lines, lines starting with a space
// and repetitions of the previous line are not saved.
func (h *shellHistory) Add(line string) {
	h.pending = false
	line = strings.TrimRightFunc(line, unicode.IsSpace)
	if strings.TrimSpace(line) == "" || strings.HasPrefix(line, " ") {
		return
	}
	if n := len(h.lines); n > 0 && h.lines[n-1] == line {
		return
	}
	h.lines = append(h.lines, line)
	h.pending = true
}

func (h *shellHistory) Len() int { return len(h.lines) }

func (h *shellHistory) At(i int) string { return h.lines[len(h.lines)-1-i] }

// discard removes a line that was added but not run.
func (h *shellHistory) discard() {
	if h.pending {
		h.lines = h.lines[:len(h.lines)-1]
		h.pending = false
	}
}

// commit appends the last added line to the history file. Errors are
// ignored: the history is a convenience.
func (h *shellHistory) commit() {
	if !h.pending || h.path == "" {
		h.pending = false
		return
	}
	h.pending = false
	if os.MkdirAll(filepath.Dir(h.path), 0o700) != nil {
		return
	}
	f, err := os.OpenFile(h.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, h.lines[len(h.lines)-1])
	if len(h.lines) > historySize {
		h.lines = h.lines[len(h.lines)-historySize:]
		h.save()
	}
}

func (h *shellHistory) save() {
	if h.path == "" {
		return
	}
	_ = os.WriteFile(h.path, []byte(strings.Join(h.lines, "\n")+"\n"), 0o600)
}

// candidate is a completion with an optional description.
type candidate struct {
	value, desc string
}

// complete implements Tab completion for term.Terminal. It asks the same
// completion machinery that the bash and zsh scripts use.
func (sh *shell) complete(t *term.Terminal, line string, pos int) (string, int, bool) {
	before, after := line[:pos], line[pos:]
	words, open := splitWords(before)
	args := wordTexts(words)
	toComplete := ""
	from := pos
	if n := len(words); n > 0 && (open || !endsWithSpace(before)) {
		toComplete = args[n-1]
		args = args[:n-1]
		from = words[n-1].start
	}
	args = sh.stripName(args)
	if len(args) > 0 && (args[0] == "exit" || args[0] == "quit") {
		return "", 0, false
	}

	all, directive := sh.candidates(args, toComplete)
	matches := filterCandidates(all, toComplete)
	insert := ""
	switch {
	case len(matches) == 1:
		m := matches[0].value
		noSpace := directive&cobra.ShellCompDirectiveNoSpace != 0 || strings.HasSuffix(m, string(filepath.Separator))
		insert = quoteWord(m, noSpace)
		if !noSpace {
			insert += " "
		}
	case len(matches) > 1:
		prefix := commonPrefix(matches)
		if len(prefix) > len(toComplete) && strings.HasPrefix(strings.ToLower(prefix), strings.ToLower(toComplete)) {
			insert = quoteWord(prefix, true)
			break
		}
		sh.list(t, matches)
		return "", 0, false
	default:
		return "", 0, false
	}
	newLine := before[:from] + insert + after
	return newLine, len(before[:from] + insert), true
}

func endsWithSpace(s string) bool {
	return s != "" && unicode.IsSpace(rune(s[len(s)-1]))
}

// candidates runs the hidden completion command for args and toComplete.
func (sh *shell) candidates(args []string, toComplete string) ([]candidate, cobra.ShellCompDirective) {
	var out strings.Builder
	c := sh.app.child()
	c.In = strings.NewReader("")
	c.Out = &out
	c.Err = io.Discard
	cmdline := append([]string{cobra.ShellCompRequestCmd}, sh.flags...)
	cmdline = append(append(cmdline, args...), toComplete)
	if c.Run(cmdline) != ExitOK {
		return nil, cobra.ShellCompDirectiveError
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	var directive int
	if _, err := fmt.Sscanf(lines[len(lines)-1], ":%d", &directive); err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	d := cobra.ShellCompDirective(directive)
	if d&cobra.ShellCompDirectiveError != 0 {
		return nil, d
	}
	var cands []candidate
	for _, l := range lines[:len(lines)-1] {
		if l == "" || strings.HasPrefix(l, "_activeHelp_") {
			continue
		}
		v, desc, _ := strings.Cut(l, "\t")
		cands = append(cands, candidate{value: v, desc: desc})
	}
	if len(cands) == 0 && d&(cobra.ShellCompDirectiveNoFileComp|cobra.ShellCompDirectiveFilterFileExt) == 0 {
		return fileCandidates(toComplete, d&cobra.ShellCompDirectiveFilterDirs != 0), d
	}
	return cands, d
}

// fileCandidates completes file names. Directories get a trailing
// separator.
func fileCandidates(prefix string, dirsOnly bool) []candidate {
	dir, base := filepath.Split(prefix)
	entries, err := os.ReadDir(filepath.Clean(dir + "."))
	if err != nil {
		return nil
	}
	var cands []candidate
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base) || (strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".")) {
			continue
		}
		isDir := e.IsDir()
		if !isDir && e.Type()&os.ModeSymlink != 0 {
			if fi, err := os.Stat(filepath.Join(dir, name)); err == nil {
				isDir = fi.IsDir()
			}
		}
		switch {
		case isDir:
			cands = append(cands, candidate{value: dir + name + string(filepath.Separator)})
		case !dirsOnly:
			cands = append(cands, candidate{value: dir + name})
		}
	}
	return cands
}

// filterCandidates keeps the candidates that start with toComplete
// (case-insensitively), or, if there are none, those that contain it, in
// line with how names are resolved.
func filterCandidates(cands []candidate, toComplete string) []candidate {
	lower := strings.ToLower(toComplete)
	var prefixed, contained []candidate
	for _, c := range cands {
		v := strings.ToLower(c.value)
		switch {
		case strings.HasPrefix(v, lower):
			prefixed = append(prefixed, c)
		case strings.Contains(v, lower):
			contained = append(contained, c)
		}
	}
	if len(prefixed) > 0 {
		return prefixed
	}
	return contained
}

// commonPrefix returns the longest prefix that all candidates share,
// ignoring case, spelled as in the first candidate.
func commonPrefix(cands []candidate) string {
	prefix := []rune(cands[0].value)
	for _, c := range cands[1:] {
		r := []rune(c.value)
		n := 0
		for n < len(prefix) && n < len(r) && unicode.ToLower(prefix[n]) == unicode.ToLower(r[n]) {
			n++
		}
		prefix = prefix[:n]
	}
	return string(prefix)
}

// maxListed is how many candidates are shown when Tab cannot choose.
const maxListed = 50

// list shows the candidates above the prompt.
func (sh *shell) list(t *term.Terminal, cands []candidate) {
	width := 0
	for _, c := range cands {
		width = max(width, len([]rune(c.value)))
	}
	// One write, so that the terminal redraws the prompt once.
	var w strings.Builder
	for i, c := range cands {
		if i == maxListed {
			fmt.Fprintf(&w, "  ... and %d more\n", len(cands)-maxListed)
			break
		}
		if c.desc == "" {
			fmt.Fprintf(&w, "  %s\n", c.value)
		} else {
			fmt.Fprintf(&w, "  %-*s  %s\n", width, c.value, c.desc)
		}
	}
	_, _ = io.WriteString(t, w.String())
}
