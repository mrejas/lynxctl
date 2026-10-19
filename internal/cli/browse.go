// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/mrejas/lynx-cli/internal/api"
	"github.com/mrejas/lynx-cli/internal/output"
	"github.com/mrejas/lynx-cli/internal/resolve"
)

// browseTTL is how long the browser reuses what it has fetched. Commands
// run from the browser clear it at once.
const browseTTL = time.Minute

func (a *App) newBrowseCommand() *cobra.Command {
	var errexit bool
	cmd := &cobra.Command{
		Use:   "browse",
		Short: "Browse organizations, installations, devices and functions like a file system (experimental)",
		Long: fmt.Sprintf(`Browse the platform like a file system, in an interactive shell:

  /                        the top organizations you can see
  /<org>/<org>/...         sub-organizations and installations
  .../<installation>       devices, and functions without a device
  .../<device>             the device's functions
  .../<function>           the function's topic_* meta keys

Commands:
  cd [path]                change directory (default: the current installation)
  ls [-l] [path]...        list a directory (-l: with kind, ID and type)
  cat [-o format] [path]   show an object, or the log of a topic
  head [-n lines] [path]   show the oldest values (default: 10 lines)
  tail [-n lines] [-f] [path]
                           show the latest values (-f: then follow new ones)
  rm [-r] [-f] path...     remove objects or meta keys (-r: with their
                           contents, -f: without asking); give the ID or
                           the exact name
  edit [path]              edit an object in $EDITOR (vi, vim and nano
                           open that editor); on a meta key, its function
  find [path...] [-name pattern] [-type org|inst|dev|fn|meta]
       [-meta key=value] [-maxdepth N]
                           find objects below a directory (default: .)
  pwd                      print the current directory
  help                     show this help
  exit, quit, Ctrl-D       leave

On a terminal, ls shows organizations in magenta, installations in blue,
devices in yellow and functions in green (unless NO_COLOR is set).

cat on a topic, head and tail show the log of a topic, function, device or
installation. They cover the last 24 hours unless given other flags of
"log", such as --since 7d or --from 2026-10-01.

Paths are made of names, IDs or unique parts of names, separated by /. "..",
"." and "~" (the current installation) work as usual. Names that contain /
must be given by ID.

Any other command is run as in "%[1]s shell", with -i set to the installation
you are in, so "get", "watch", "function list" and so on work on it.

Like "shell", commands are read from stdin when it is not a terminal.`, a.Name),
		Example: fmt.Sprintf(`  %[1]s browse
  %[1]s --profile dev browse`, a.Name),
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.inShell {
				return usageErrorf("already in a shell")
			}
			sh := &shell{app: a, flags: inheritedFlags(cmd), errexit: errexit, historyFile: "browse-history"}
			b := &browser{sh: sh, help: cmd.Long, color: colorOn(a.Out)}
			sh.mode = b
			b.cwd = b.home(cmd.Context())
			return sh.runCommand(cmd.Context())
		},
	}
	cmd.Flags().BoolVarP(&errexit, "errexit", "e", false, "stop at the first failing command (when reading from a script)")
	return cmd
}

type nodeKind int

const (
	rootNode nodeKind = iota
	orgNode
	installationNode
	deviceNode
	functionNode
	metaNode
)

var nodeKindNames = map[nodeKind]string{
	rootNode:         "root",
	orgNode:          "organization",
	installationNode: "installation",
	deviceNode:       "device",
	functionNode:     "function",
	metaNode:         "meta",
}

// node is a directory (or, for meta keys, a file) in the browser.
type node struct {
	kind nodeKind
	id   int64
	name string
	// installation is the installation that devices and functions are in.
	installation int64
	// typ is the type of a device or function.
	typ string
	// value is the value of a meta key.
	value string
}

func (n node) isDir() bool { return n.kind != metaNode }

// label is how the node appears in paths and listings.
func (n node) label() string {
	if n.name == "" {
		return strconv.FormatInt(n.id, 10)
	}
	return n.name
}

// browser is the mode of "browse".
type browser struct {
	sh   *shell
	help string
	// color shows the kinds of objects in colors.
	color bool
	// cwd is the path to the current directory, starting with the root.
	cwd []node

	fetched time.Time
	// pinned keeps what has been fetched from expiring, during a find.
	pinned  bool
	orgs    []api.JSONObject
	insts   []api.Installation
	objects map[int64]*installationObjects
}

type installationObjects struct {
	devices, functions []api.Object
}

// app returns an App for the browser's own API calls.
func (b *browser) app() *App {
	c := b.sh.app.child()
	c.profileFlag = b.sh.app.profileFlag
	c.debug = b.sh.app.debug
	return c
}

func (b *browser) session() (*session, error) {
	return b.app().session()
}

// forget clears what has been fetched.
func (b *browser) forget() {
	b.fetched = time.Time{}
	b.orgs, b.insts, b.objects = nil, nil, nil
}

func (b *browser) expire() {
	if !b.pinned && time.Since(b.fetched) > browseTTL {
		b.forget()
		b.fetched = time.Now()
	}
}

// load fetches the organizations and installations.
func (b *browser) load(ctx context.Context) error {
	b.expire()
	if b.insts != nil {
		return nil
	}
	s, err := b.session()
	if err != nil {
		return err
	}
	// Users without access to the organization list still see their
	// installations.
	orgs, _ := s.Client.ListObjectsAt(ctx, organizationPath)
	insts, err := s.Client.ListInstallations(ctx, nil)
	if err != nil {
		return err
	}
	if infos, err := s.Client.InstallationInfos(ctx); err == nil {
		seen := map[int64]bool{}
		for _, i := range insts {
			seen[i.ID] = true
		}
		for _, i := range infos {
			if !seen[i.ID] {
				insts = append(insts, api.Installation{ID: i.ID, Name: i.Name, ClientID: i.ClientID, OrganizationID: i.OrganizationID})
			}
		}
	}
	b.orgs, b.insts = orgs, insts
	return nil
}

// loadObjects fetches the devices and functions of an installation.
func (b *browser) loadObjects(ctx context.Context, inst int64) (*installationObjects, error) {
	b.expire()
	if o := b.objects[inst]; o != nil {
		return o, nil
	}
	s, err := b.session()
	if err != nil {
		return nil, err
	}
	o, err := fetchObjects(ctx, s, inst)
	if err != nil {
		return nil, err
	}
	if b.objects == nil {
		b.objects = map[int64]*installationObjects{}
	}
	b.objects[inst] = o
	return o, nil
}

func fetchObjects(ctx context.Context, s *session, inst int64) (*installationObjects, error) {
	devices, err := s.Client.ListObjects(ctx, api.DeviceX, inst, nil)
	if err != nil {
		return nil, err
	}
	functions, err := s.Client.ListObjects(ctx, api.FunctionX, inst, nil)
	if err != nil {
		return nil, err
	}
	return &installationObjects{devices: devices, functions: functions}, nil
}

// prefetchWorkers is how many installations find fetches at a time.
const prefetchWorkers = 8

// prefetch fetches the devices and functions of several installations in
// parallel. Errors are left for loadObjects to report.
func (b *browser) prefetch(ctx context.Context, insts []int64) {
	b.expire()
	var todo []int64
	for _, id := range insts {
		if b.objects[id] == nil {
			todo = append(todo, id)
		}
	}
	if len(todo) < 2 {
		return
	}
	s, err := b.session()
	if err != nil {
		return
	}
	results := make([]*installationObjects, len(todo))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(prefetchWorkers, len(todo)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i], _ = fetchObjects(ctx, s, todo[i])
			}
		}()
	}
	for i := range todo {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if b.objects == nil {
		b.objects = map[int64]*installationObjects{}
	}
	for i, o := range results {
		if o != nil {
			b.objects[todo[i]] = o
		}
	}
}

func orgNodeOf(o api.JSONObject) node {
	return node{kind: orgNode, id: num(o, "id"), name: str(o["name"])}
}

func installationNodeOf(i api.Installation) node {
	return node{kind: installationNode, id: i.ID, name: i.Name}
}

func objectNode(kind nodeKind, o api.Object) node {
	return node{kind: kind, id: o.ID, name: o.Name(), installation: o.InstallationID, typ: o.Type}
}

// children lists a directory: organizations first, then installations,
// devices, functions and meta keys, each sorted by name.
func (b *browser) children(ctx context.Context, n node) ([]node, error) {
	var list []node
	switch n.kind {
	case rootNode, orgNode:
		if err := b.load(ctx); err != nil {
			return nil, err
		}
		visible := map[int64]bool{}
		for _, o := range b.orgs {
			visible[num(o, "id")] = true
		}
		for _, o := range b.orgs {
			id, parent := num(o, "id"), num(o, "parent")
			top := parent == 0 || parent == id || !visible[parent]
			if (n.kind == rootNode && top) || (n.kind == orgNode && parent == n.id && id != n.id) {
				list = append(list, orgNodeOf(o))
			}
		}
		for _, i := range b.insts {
			if (n.kind == rootNode && !visible[i.OrganizationID]) || (n.kind == orgNode && i.OrganizationID == n.id) {
				list = append(list, installationNodeOf(i))
			}
		}
	case installationNode, deviceNode:
		inst := n.id
		if n.kind == deviceNode {
			inst = n.installation
		}
		objs, err := b.loadObjects(ctx, inst)
		if err != nil {
			return nil, err
		}
		devices := map[string]bool{}
		for _, d := range objs.devices {
			devices[strconv.FormatInt(d.ID, 10)] = true
			if n.kind == installationNode {
				list = append(list, objectNode(deviceNode, d))
			}
		}
		for _, f := range objs.functions {
			dev := f.Meta["device_id"]
			if (n.kind == installationNode && !devices[dev]) || (n.kind == deviceNode && dev == strconv.FormatInt(n.id, 10)) {
				list = append(list, objectNode(functionNode, f))
			}
		}
	case functionNode:
		f, err := b.function(ctx, n)
		if err != nil {
			return nil, err
		}
		for _, m := range []api.Meta{f.Meta, f.ProtectedMeta} {
			for k, v := range m {
				if strings.HasPrefix(k, "topic_") {
					list = append(list, node{kind: metaNode, name: k, value: v, installation: n.installation})
				}
			}
		}
	default:
		return nil, fmt.Errorf("%s is not a directory", n.label())
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].kind != list[j].kind {
			return list[i].kind < list[j].kind
		}
		return strings.ToLower(list[i].label()) < strings.ToLower(list[j].label())
	})
	return list, nil
}

func (b *browser) function(ctx context.Context, n node) (*api.Object, error) {
	objs, err := b.loadObjects(ctx, n.installation)
	if err != nil {
		return nil, err
	}
	for i := range objs.functions {
		if objs.functions[i].ID == n.id {
			return &objs.functions[i], nil
		}
	}
	return nil, &resolve.NotFoundError{Kind: "function", Ref: n.label()}
}

// pathString returns the absolute path of a directory.
func pathString(p []node) string {
	if len(p) <= 1 {
		return "/"
	}
	labels := make([]string, len(p)-1)
	for i, n := range p[1:] {
		labels[i] = n.label()
	}
	return "/" + strings.Join(labels, "/")
}

// home returns the path to the current installation, or the root.
func (b *browser) home(ctx context.Context) []node {
	root := []node{{kind: rootNode}}
	a := b.app()
	a.installationFlag = b.sh.app.installationFlag
	s, err := a.session()
	if err != nil {
		return root
	}
	inst, err := a.installation(ctx, s)
	if err != nil || b.load(ctx) != nil {
		return root
	}
	var found *api.Installation
	for i := range b.insts {
		if b.insts[i].ID == inst.ID {
			found = &b.insts[i]
		}
	}
	if found == nil {
		return root
	}
	orgs := map[int64]api.JSONObject{}
	for _, o := range b.orgs {
		orgs[num(o, "id")] = o
	}
	path := []node{installationNodeOf(*found)}
	for id, depth := found.OrganizationID, 0; depth < len(orgs); depth++ {
		o, ok := orgs[id]
		if !ok {
			break
		}
		path = append([]node{orgNodeOf(o)}, path...)
		parent := num(o, "parent")
		if parent == id {
			break
		}
		id = parent
	}
	return append(root, path...)
}

// resolvePath resolves a path relative to the current directory. Every
// element but the last must be a directory.
func (b *browser) resolvePath(ctx context.Context, p string) ([]node, error) {
	return b.resolvePathExact(ctx, p, false)
}

// resolvePathExact is resolvePath, but with exactLast set, the last element
// must be an ID or an exact name, as for destructive commands.
func (b *browser) resolvePathExact(ctx context.Context, p string, exactLast bool) ([]node, error) {
	path := append([]node{}, b.cwd...)
	switch {
	case strings.HasPrefix(p, "/"):
		path = path[:1]
	case p == "~" || strings.HasPrefix(p, "~/"):
		path = b.home(ctx)
		p = strings.TrimPrefix(p, "~")
	}
	elems := strings.Split(p, "/")
	last := -1
	for i, elem := range elems {
		if elem != "" && elem != "." && elem != ".." {
			last = i
		}
	}
	for i, elem := range elems {
		switch elem {
		case "", ".":
			continue
		case "..":
			if len(path) > 1 {
				path = path[:len(path)-1]
			}
			continue
		}
		dir := path[len(path)-1]
		if !dir.isDir() {
			return nil, fmt.Errorf("%s is not a directory", pathString(path))
		}
		list, err := b.children(ctx, dir)
		if err != nil {
			return nil, err
		}
		n, err := resolve.Find("item", elem, list, func(n node) resolve.Key {
			return resolve.Key{ID: n.id, Name: n.label()}
		}, resolve.Options{Exact: exactLast && i == last})
		if err != nil {
			return nil, fmt.Errorf("%w (in %s)", err, pathString(path))
		}
		path = append(path, n)
	}
	return path, nil
}

// prompt shows the profile and the current directory, with each element in
// the color of its kind. Long paths are shortened to the last two elements.
func (b *browser) prompt() string {
	nodes, p := b.cwd[1:], "/"
	if len(pathString(b.cwd)) > 40 && len(nodes) > 2 {
		nodes, p = nodes[len(nodes)-2:], ".../"
	}
	for i, n := range nodes {
		if i > 0 {
			p += "/"
		}
		p += colorize(n.label(), nodeColors[n.kind], b.sh.color)
	}
	if name := b.sh.profileName(); name != "" {
		return name + ":" + p + "> "
	}
	return p + "> "
}

// installation returns the installation the current directory is in, or
// 0.
func (b *browser) installation() int64 {
	for _, n := range b.cwd {
		if n.kind == installationNode {
			return n.id
		}
	}
	return 0
}

// cliFlags are the flags put in front of commands passed on to the CLI.
func (b *browser) cliFlags() []string {
	if inst := b.installation(); inst != 0 {
		return []string{"--installation=" + strconv.FormatInt(inst, 10)}
	}
	return nil
}

func (b *browser) run(ctx context.Context, args []string, interactive bool) int {
	var err error
	switch args[0] {
	case "cd":
		err = b.cd(ctx, args[1:])
	case "ls":
		err = b.ls(ctx, args[1:])
	case "find":
		err = b.find(ctx, args[1:])
	case "cat", "head", "tail":
		return b.show(ctx, args, interactive)
	case "rm":
		code := b.rm(ctx, args[1:], interactive)
		b.forget()
		return code
	case "edit", "vi", "vim", "nano":
		code := b.edit(ctx, args, interactive)
		b.forget()
		return code
	case "pwd":
		if len(args) > 1 {
			err = usageErrorf("pwd takes no arguments")
		} else {
			fmt.Fprintln(b.sh.app.Out, pathString(b.cwd))
		}
	case "help":
		if len(args) == 1 {
			fmt.Fprintln(b.sh.app.Out, b.help)
			return ExitOK
		}
		fallthrough
	default:
		code := b.sh.runCLI(ctx, args, interactive, b.cliFlags()...)
		// The command may have changed what is shown.
		b.forget()
		return code
	}
	if err != nil {
		a := b.sh.app
		fmt.Fprintf(a.Err, "Error: %s: %s\n", args[0], a.errorMessage(err))
	}
	return exitCode(err)
}

func (b *browser) cd(ctx context.Context, args []string) error {
	switch len(args) {
	case 0:
		b.cwd = b.home(ctx)
		return nil
	case 1:
	default:
		return usageErrorf("usage: cd [path]")
	}
	path, err := b.resolvePath(ctx, args[0])
	if err != nil {
		return err
	}
	if !path[len(path)-1].isDir() {
		return fmt.Errorf("%s is not a directory", pathString(path))
	}
	b.cwd = path
	return nil
}

func (b *browser) ls(ctx context.Context, args []string) error {
	long := false
	var paths []string
	for _, arg := range args {
		switch {
		case arg == "-l":
			long = true
		case strings.HasPrefix(arg, "-") && arg != "-":
			return usageErrorf("unknown flag %s (usage: ls [-l] [path]...)", arg)
		default:
			paths = append(paths, arg)
		}
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}
	out := b.sh.app.Out
	for i, p := range paths {
		path, err := b.resolvePath(ctx, p)
		if err != nil {
			return err
		}
		n := path[len(path)-1]
		list := []node{n}
		if n.isDir() {
			if list, err = b.children(ctx, n); err != nil {
				return err
			}
		}
		if len(paths) > 1 {
			if i > 0 {
				fmt.Fprintln(out)
			}
			fmt.Fprintf(out, "%s:\n", pathString(path))
		}
		if err := b.printList(list, long); err != nil {
			return err
		}
	}
	return nil
}

// nodeColors are the ANSI colors of each kind of object, as in ls --color.
var nodeColors = map[nodeKind]string{
	orgNode:          "1;35", // bold magenta
	installationNode: "1;34", // bold blue
	deviceNode:       "33",   // yellow
	functionNode:     "32",   // green
}

// listName is how a node appears in a listing: with / for directories, and
// in the color of its kind if colors are on.
func (b *browser) listName(n node) string {
	name := n.label()
	if n.isDir() {
		name += "/"
	}
	return colorize(name, nodeColors[n.kind], b.color)
}

func (b *browser) printList(list []node, long bool) error {
	out := b.sh.app.Out
	if !long {
		for _, n := range list {
			fmt.Fprintln(out, b.listName(n))
		}
		return nil
	}
	pr := &output.Printer{Out: out, Format: output.Table}
	if len(list) > 0 && list[0].kind == metaNode {
		rows := output.Rows{Columns: []output.Column{{Header: "key"}, {Header: "value"}}}
		for _, n := range list {
			rows.Rows = append(rows.Rows, []string{n.name, n.value})
		}
		return pr.List(nil, rows)
	}
	rows := output.Rows{Columns: []output.Column{{Header: "kind"}, {Header: "id"}, {Header: "type"}, {Header: "name"}}}
	for _, n := range list {
		rows.Rows = append(rows.Rows, []string{nodeKindNames[n.kind], strconv.FormatInt(n.id, 10), n.typ, b.listName(n)})
	}
	return pr.List(nil, rows)
}

// valueFlags are the flags of get, log and watch that take a value.
var valueFlags = map[string]bool{
	"-o": true, "--output": true, "-i": true, "--installation": true, "--profile": true,
	"--since": true, "--from": true, "--to": true, "--limit": true, "--aggr": true,
	"--interval": true, "--topic": true, "--meta": true, "--type": true, "--count": true,
	// head and tail.
	"-n": true, "--lines": true,
}

// flagArg is a flag with its value, if it has one.
type flagArg []string

// splitArgs separates paths from flags.
func splitArgs(args []string) (paths []string, flags []flagArg) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case !strings.HasPrefix(arg, "-") || arg == "-":
			paths = append(paths, arg)
		case valueFlags[arg] && i+1 < len(args):
			flags = append(flags, flagArg{arg, args[i+1]})
			i++
		default:
			flags = append(flags, flagArg{arg})
		}
	}
	return paths, flags
}

// show implements cat, head and tail.
//
// cat shows an object with the get command of its kind. On a topic it shows
// the log of the topic, as do head and tail on anything that has values:
// head the oldest values, tail the latest, and tail -f then follows new
// values. Other flags are passed on to get, log or watch.
func (b *browser) show(ctx context.Context, args []string, interactive bool) int {
	cmd := args[0]
	fail := func(err error) int {
		a := b.sh.app
		fmt.Fprintf(a.Err, "Error: %s: %s\n", cmd, a.errorMessage(err))
		return exitCode(err)
	}
	paths, flags := splitArgs(args[1:])
	lines, follow := 10, false
	var rest []string
	for _, f := range flags {
		name, value, hasValue := strings.Cut(f[0], "=")
		switch {
		case cmd != "cat" && (name == "-n" || name == "--lines"):
			switch {
			case hasValue:
			case len(f) > 1:
				value = f[1]
			default:
				// -n takes a value; splitArgs does not know that.
				return fail(usageErrorf("usage: %s [-n lines] [path]", cmd))
			}
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				return fail(usageErrorf("invalid number of lines %q", value))
			}
			lines = n
		case cmd != "cat" && strings.HasPrefix(name, "-n") && len(name) > 2:
			n, err := strconv.Atoi(name[2:])
			if err != nil || n <= 0 {
				return fail(usageErrorf("invalid number of lines %q", name[2:]))
			}
			lines = n
		case cmd == "tail" && (name == "-f" || name == "--follow"):
			follow = true
		default:
			rest = append(rest, f...)
		}
	}

	path := b.cwd
	switch len(paths) {
	case 0:
	case 1:
		var err error
		if path, err = b.resolvePath(ctx, paths[0]); err != nil {
			return fail(err)
		}
	default:
		return fail(usageErrorf("%s takes one path", cmd))
	}
	n := path[len(path)-1]

	if cmd == "cat" && n.kind != metaNode {
		id := strconv.FormatInt(n.id, 10)
		inst := "--installation=" + strconv.FormatInt(n.installation, 10)
		var cmdline []string
		switch n.kind {
		case orgNode:
			cmdline = []string{"organization", "get", id}
		case installationNode:
			cmdline = []string{"installation", "get", id}
		case deviceNode:
			cmdline = []string{"device", "get", id, inst}
		case functionNode:
			cmdline = []string{"function", "get", id, inst}
		default:
			return fail(usageErrorf("%s is not an object", pathString(path)))
		}
		return b.sh.runCLI(ctx, append(cmdline, rest...), interactive)
	}

	sel, err := valueSelection(n, path)
	if err != nil {
		return fail(err)
	}
	cmdline := append([]string{"log"}, sel...)
	switch cmd {
	case "head":
		cmdline = append(cmdline, "--limit", strconv.Itoa(lines), "--oldest")
	case "tail":
		cmdline = append(cmdline, "--limit", strconv.Itoa(lines))
	}
	if code := b.sh.runCLI(ctx, append(cmdline, rest...), interactive); code != ExitOK || !follow {
		return code
	}
	// Only output flags make sense for watch.
	watch := append([]string{"watch"}, sel...)
	for _, f := range flags {
		switch f[0] {
		case "-o", "--output", "-q", "--quiet":
			watch = append(watch, f...)
		}
	}
	return b.sh.runCLI(ctx, watch, interactive)
}

// valueSelection returns the arguments of log and watch that select the
// values of a node: a topic, a function, a device's functions or all
// functions of an installation.
func valueSelection(n node, path []node) ([]string, error) {
	inst := "--installation=" + strconv.FormatInt(n.installation, 10)
	switch n.kind {
	case metaNode:
		if n.value == "" {
			return nil, fmt.Errorf("%s is empty", pathString(path))
		}
		return []string{"--topic", n.value, inst}, nil
	case functionNode:
		return []string{strconv.FormatInt(n.id, 10), inst}, nil
	case deviceNode:
		return []string{"--meta", "device_id=" + strconv.FormatInt(n.id, 10), inst}, nil
	case installationNode:
		return []string{"--installation=" + strconv.FormatInt(n.id, 10)}, nil
	}
	return nil, usageErrorf("%s has no values", pathString(path))
}

// rm removes objects or meta keys. Directories that are not empty need -r,
// and -f skips the confirmation. The last element of each path must be an
// ID or an exact name.
func (b *browser) rm(ctx context.Context, args []string, interactive bool) int {
	a := b.sh.app
	fail := func(err error) int {
		fmt.Fprintf(a.Err, "Error: rm: %s\n", a.errorMessage(err))
		return exitCode(err)
	}
	recursive, force := false, false
	var paths []string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			paths = append(paths, arg)
			continue
		}
		for _, c := range strings.TrimPrefix(arg, "-") {
			switch c {
			case 'r', 'R':
				recursive = true
			case 'f':
				force = true
			default:
				return fail(usageErrorf("unknown flag %s (usage: rm [-r] [-f] path...)", arg))
			}
		}
	}
	if len(paths) == 0 {
		return fail(usageErrorf("usage: rm [-r] [-f] path..."))
	}

	code := ExitOK
	for _, p := range paths {
		path, err := b.resolvePathExact(ctx, p, true)
		if err != nil {
			code = fail(err)
			continue
		}
		cmdline, err := b.rmCommand(ctx, path, recursive)
		if err != nil {
			code = fail(err)
			continue
		}
		if force {
			cmdline = append(cmdline, "--yes")
		}
		if c := b.sh.runCLI(ctx, cmdline, interactive); c != ExitOK {
			code = c
			continue
		}
		b.forget()
		// Leave a directory that no longer exists.
		if n := len(path); n <= len(b.cwd) && samePath(path, b.cwd[:n]) {
			b.cwd = append([]node{}, path[:n-1]...)
		}
	}
	return code
}

// rmCommand returns the CLI command that removes the last node of path.
func (b *browser) rmCommand(ctx context.Context, path []node, recursive bool) ([]string, error) {
	n := path[len(path)-1]
	id := strconv.FormatInt(n.id, 10)
	inst := "--installation=" + strconv.FormatInt(n.installation, 10)
	if n.kind == metaNode {
		fn := path[len(path)-2]
		return []string{"function", "meta", "unset", strconv.FormatInt(fn.id, 10), n.name, inst}, nil
	}
	if n.kind == rootNode {
		return nil, usageErrorf("/ cannot be removed")
	}
	children := 0
	if n.kind != functionNode {
		list, err := b.children(ctx, n)
		if err != nil {
			return nil, err
		}
		children = len(list)
	}
	if children > 0 && !recursive {
		return nil, usageErrorf("%s is not empty (%d %s). Use rm -r to remove it with its contents",
			pathString(path), children, plural(children, "item", "items"))
	}
	switch n.kind {
	case orgNode:
		cmdline := []string{"organization", "delete", id}
		if children > 0 {
			cmdline = append(cmdline, "--force")
		}
		return cmdline, nil
	case installationNode:
		return []string{"installation", "delete", id}, nil
	case deviceNode:
		cmdline := []string{"device", "delete", id, inst}
		if children > 0 {
			cmdline = append(cmdline, "--cascade")
		}
		return cmdline, nil
	}
	return []string{"function", "delete", id, inst}, nil
}

func samePath(p, q []node) bool {
	if len(p) != len(q) {
		return false
	}
	for i := range p {
		if p[i].kind != q[i].kind || p[i].id != q[i].id || p[i].name != q[i].name {
			return false
		}
	}
	return true
}

// edit opens an object in an editor with the edit command of its kind. On a
// meta key, the function it belongs to is edited. vi, vim and nano use that
// editor; edit uses $VISUAL or $EDITOR.
func (b *browser) edit(ctx context.Context, args []string, interactive bool) int {
	a := b.sh.app
	fail := func(err error) int {
		fmt.Fprintf(a.Err, "Error: %s: %s\n", args[0], a.errorMessage(err))
		return exitCode(err)
	}
	path := b.cwd
	switch len(args) {
	case 1:
	case 2:
		var err error
		if path, err = b.resolvePath(ctx, args[1]); err != nil {
			return fail(err)
		}
	default:
		return fail(usageErrorf("usage: %s [path]", args[0]))
	}
	if path[len(path)-1].kind == metaNode {
		path = path[:len(path)-1]
	}
	n := path[len(path)-1]
	id := strconv.FormatInt(n.id, 10)
	inst := "--installation=" + strconv.FormatInt(n.installation, 10)
	var cmdline []string
	switch n.kind {
	case orgNode:
		cmdline = []string{"organization", "edit", id}
	case installationNode:
		cmdline = []string{"installation", "edit", id}
	case deviceNode:
		cmdline = []string{"device", "edit", id, inst}
	case functionNode:
		cmdline = []string{"function", "edit", id, inst}
	default:
		return fail(usageErrorf("/ cannot be edited"))
	}
	if args[0] != "edit" {
		a.editor = []string{args[0]}
		defer func() { a.editor = nil }()
	}
	return b.sh.runCLI(ctx, cmdline, interactive)
}

// findKinds are the values of find -type.
var findKinds = []struct {
	names []string
	kind  nodeKind
}{
	{[]string{"org", "organization"}, orgNode},
	{[]string{"inst", "installation"}, installationNode},
	{[]string{"dev", "device"}, deviceNode},
	{[]string{"fn", "func", "function"}, functionNode},
	{[]string{"meta"}, metaNode},
}

// findUsage is shown for invalid find arguments.
const findUsage = "usage: find [path...] [-name pattern] [-type org|inst|dev|fn|meta] [-meta key=value] [-maxdepth N]"

// find lists everything below the given paths (default: the current
// directory) that matches the filters, like find(1).
func (b *browser) find(ctx context.Context, args []string) error {
	var paths, metaFlags []string
	var name string
	types := map[nodeKind]bool{}
	maxDepth := -1
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			paths = append(paths, arg)
			continue
		}
		if i+1 >= len(args) {
			return usageErrorf("%s needs a value (%s)", arg, findUsage)
		}
		i++
		value := args[i]
		switch arg {
		case "-name", "-iname":
			name = value
		case "-type":
			for _, t := range strings.Split(value, ",") {
				found := false
				for _, k := range findKinds {
					if slices.Contains(k.names, strings.ToLower(t)) {
						types[k.kind], found = true, true
					}
				}
				if !found {
					return usageErrorf("invalid -type %q (use org, inst, dev, fn or meta)", t)
				}
			}
		case "-meta":
			metaFlags = append(metaFlags, value)
		case "-maxdepth":
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 {
				return usageErrorf("invalid -maxdepth %q", value)
			}
			maxDepth = n
		default:
			return usageErrorf("unknown flag %s (%s)", arg, findUsage)
		}
	}
	pairs, err := metaPairs(metaFlags)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}

	matches := func(n node) bool {
		if len(types) > 0 && !types[n.kind] {
			return false
		}
		if name != "" && !globMatch(name, n.label()) {
			return false
		}
		if len(pairs) > 0 {
			metas := b.metaOf(n)
			return metas != nil && metaMatches(pairs, metas...)
		}
		return true
	}

	b.expire()
	b.pinned = true
	defer func() { b.pinned = false }()

	out, failed := b.sh.app.Out, false
	var walk func(n node, shown string, depth int)
	walk = func(n node, shown string, depth int) {
		if ctx.Err() != nil {
			return
		}
		if matches(n) {
			fmt.Fprintln(out, shown)
		}
		if !n.isDir() || depth == maxDepth {
			return
		}
		list, err := b.children(ctx, n)
		if err != nil {
			fmt.Fprintf(b.sh.app.Err, "Error: find: %s: %s\n", shown, b.sh.app.errorMessage(err))
			failed = true
			return
		}
		var insts []int64
		for _, c := range list {
			if c.kind == installationNode && depth+1 != maxDepth {
				insts = append(insts, c.id)
			}
		}
		b.prefetch(ctx, insts)
		prefix := shown
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		for _, c := range list {
			walk(c, prefix+colorize(c.label(), nodeColors[c.kind], b.color), depth+1)
		}
	}
	for _, p := range paths {
		path, err := b.resolvePath(ctx, p)
		if err != nil {
			return err
		}
		walk(path[len(path)-1], p, 0)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if failed {
		return &exitError{code: ExitError, err: errors.New("some directories could not be read"), silent: true}
	}
	return nil
}

// metaOf returns the meta keys of a node, or nil if it has none to filter
// on.
func (b *browser) metaOf(n node) []api.Meta {
	switch n.kind {
	case orgNode:
		for _, o := range b.orgs {
			if num(o, "id") == n.id {
				return []api.Meta{toMeta(o["meta"]), toMeta(o["protected_meta"])}
			}
		}
	case installationNode:
		for _, i := range b.insts {
			if i.ID == n.id {
				return []api.Meta{i.Meta}
			}
		}
	case deviceNode, functionNode:
		objs := b.objects[n.installation]
		if objs == nil {
			return nil
		}
		list := objs.functions
		if n.kind == deviceNode {
			list = objs.devices
		}
		for _, o := range list {
			if o.ID == n.id {
				return []api.Meta{o.Meta, o.ProtectedMeta}
			}
		}
	}
	return nil
}

// browseCommands are the browser's own commands.
var browseCommands = []candidate{
	{"cd", "Change directory"},
	{"ls", "List a directory"},
	{"cat", "Show an object, or the log of a topic"},
	{"head", "Show the oldest values"},
	{"tail", "Show the latest values (-f: and follow)"},
	{"rm", "Remove an object or a meta key (-r: with contents, -f: without asking)"},
	{"edit", "Edit an object in $EDITOR"},
	{"vi", "Edit an object in vi"},
	{"nano", "Edit an object in nano"},
	{"find", "Find objects below a directory"},
	{"pwd", "Print the current directory"},
	{"help", "Show help"},
	{"exit", "Leave"},
}

func (b *browser) candidates(args []string, toComplete string) ([]candidate, cobra.ShellCompDirective) {
	if len(args) == 0 {
		cli, _ := b.sh.cliCandidates(nil, toComplete, b.cliFlags()...)
		seen := map[string]bool{}
		var cands []candidate
		for _, c := range append(append([]candidate{}, browseCommands...), cli...) {
			if !seen[c.value] {
				seen[c.value] = true
				cands = append(cands, c)
			}
		}
		return cands, cobra.ShellCompDirectiveNoFileComp
	}
	switch args[0] {
	case "cd", "ls", "cat", "head", "tail", "rm", "edit", "vi", "vim", "nano":
		if strings.HasPrefix(toComplete, "-") {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return b.pathCandidates(toComplete, args[0] == "cd"), cobra.ShellCompDirectiveNoFileComp
	case "find":
		switch {
		case args[len(args)-1] == "-type":
			var kinds []candidate
			for _, k := range findKinds {
				kinds = append(kinds, candidate{value: k.names[0], desc: nodeKindNames[k.kind]})
			}
			return kinds, cobra.ShellCompDirectiveNoFileComp
		case slices.Contains([]string{"-name", "-iname", "-meta", "-maxdepth"}, args[len(args)-1]):
			return nil, cobra.ShellCompDirectiveNoFileComp
		case strings.HasPrefix(toComplete, "-"):
			return []candidate{{"-name", "match the name (* is a wildcard)"}, {"-type", "org, inst, dev, fn or meta"},
				{"-meta", "match a meta key=value"}, {"-maxdepth", "levels to descend"}}, cobra.ShellCompDirectiveNoFileComp
		}
		return b.pathCandidates(toComplete, true), cobra.ShellCompDirectiveNoFileComp
	case "pwd", "help":
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return b.sh.cliCandidates(args, toComplete, b.cliFlags()...)
}

// pathCandidates completes the last element of a path. Directories end
// with /. Names that contain / are completed as IDs.
func (b *browser) pathCandidates(toComplete string, dirsOnly bool) []candidate {
	dir := ""
	if i := strings.LastIndex(toComplete, "/"); i >= 0 {
		dir = toComplete[:i+1]
	}
	ctx := context.Background()
	path, err := b.resolvePath(ctx, dir)
	if err != nil || !path[len(path)-1].isDir() {
		return nil
	}
	list, err := b.children(ctx, path[len(path)-1])
	if err != nil {
		return nil
	}
	var cands []candidate
	for _, n := range list {
		if dirsOnly && !n.isDir() {
			continue
		}
		label := n.label()
		if strings.Contains(label, "/") {
			label = strconv.FormatInt(n.id, 10)
		}
		c := candidate{value: dir + label, desc: nodeKindNames[n.kind]}
		if n.isDir() {
			c.value += "/"
		}
		if n.typ != "" {
			c.desc += ", " + n.typ
		}
		cands = append(cands, c)
	}
	return cands
}
