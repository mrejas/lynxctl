package cli

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/mrejas/lynx-cli/internal/api"
	"github.com/mrejas/lynx-cli/internal/output"
)

// metaObject is anything with meta keys: an installation, device or function.
type metaObject struct {
	Path      string // API path; meta endpoints live below it
	Label     string // for messages, e.g. `function "Kitchen" (ID 1042)`
	Meta      api.Meta
	Protected api.Meta
}

// metaTarget tells the shared meta commands how to find their object.
type metaTarget struct {
	// noun is used in help texts ("function").
	noun string
	// withRef is true when the object is given as the first argument.
	// Installations use -i or the current installation instead.
	withRef bool
	load    func(ctx context.Context, ref string) (*metaObject, error)
	// complete completes the object argument (may be nil).
	complete func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective)
}

type metaEntry struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Protected bool   `json:"protected"`
}

func (a *App) newMetaCommand(t metaTarget) *cobra.Command {
	cmd := groupCommand("meta", "Show and change meta keys of a "+t.noun)
	cmd.AddCommand(a.newMetaGetCommand(t), a.newMetaSetCommand(t), a.newMetaUnsetCommand(t))
	return cmd
}

// split separates the object reference from the remaining arguments.
func (t metaTarget) split(args []string) (ref string, rest []string) {
	if t.withRef {
		return args[0], args[1:]
	}
	return "", args
}

func (t metaTarget) use(rest string) string {
	if t.withRef {
		return "<" + t.noun + "> " + rest
	}
	return rest
}

// exampleRef is the object argument used in examples.
func (t metaTarget) exampleRef() string {
	if t.withRef {
		return "kitchen "
	}
	return ""
}

func (t metaTarget) minArgs(n int) cobra.PositionalArgs {
	if t.withRef {
		n++
	}
	return usageArgs(cobra.MinimumNArgs(n))
}

func (t metaTarget) completeRef(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if t.withRef && len(args) == 0 && t.complete != nil {
		return t.complete(cmd, args, toComplete)
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func (a *App) newMetaGetCommand(t metaTarget) *cobra.Command {
	maxArgs := 1
	if t.withRef {
		maxArgs = 2
	}
	return &cobra.Command{
		Use:   "get " + t.use("[key]"),
		Short: "Show all meta keys, or the value of one",
		Long: `Without a key, all meta keys are listed. With a key, only its value is
printed, which is handy in scripts.`,
		Args:              usageArgs(cobra.RangeArgs(minIf(t.withRef), maxArgs)),
		ValidArgsFunction: t.completeRef,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, rest := t.split(args)
			obj, err := t.load(cmd.Context(), ref)
			if err != nil {
				return err
			}
			pr, err := a.printer()
			if err != nil {
				return err
			}
			if len(rest) == 1 {
				return a.printMetaValue(pr, obj, rest[0])
			}
			return a.printMeta(pr, obj)
		},
	}
}

func minIf(withRef bool) int {
	if withRef {
		return 1
	}
	return 0
}

func (a *App) printMetaValue(pr *output.Printer, obj *metaObject, key string) error {
	v, protected := obj.Meta[key], false
	if pv, ok := obj.Protected[key]; ok {
		v, protected = pv, true
	} else if _, ok := obj.Meta[key]; !ok {
		return &exitError{code: ExitNotFound, err: fmt.Errorf("%s has no meta key %q", obj.Label, key)}
	}
	switch pr.Format {
	case output.JSON, output.YAML:
		return pr.Details(metaEntry{key, v, protected}, "", nil)
	}
	fmt.Fprintln(a.Out, v)
	return nil
}

func (a *App) printMeta(pr *output.Printer, obj *metaObject) error {
	var entries []metaEntry
	for k, v := range obj.Meta {
		entries = append(entries, metaEntry{k, v, false})
	}
	for k, v := range obj.Protected {
		entries = append(entries, metaEntry{k, v, true})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	rows := output.Rows{Columns: []output.Column{{Header: "key"}, {Header: "value"}, {Header: "protected", Wide: true}}}
	for _, e := range entries {
		rows.Rows = append(rows.Rows, []string{e.Key, e.Value, strconv.FormatBool(e.Protected)})
	}
	if entries == nil {
		entries = []metaEntry{}
	}
	return pr.List(entries, rows)
}

func (a *App) newMetaSetCommand(t metaTarget) *cobra.Command {
	var protected bool
	cmd := &cobra.Command{
		Use:   "set " + t.use("key=value..."),
		Short: "Create or update meta keys",
		Long: `Create or update one or more meta keys. A key that is protected stays
protected unless --protected=false is given.`,
		Example:           fmt.Sprintf(`  %s %s meta set %sunit=°C format="%%.1f °C"`, a.Name, t.noun, t.exampleRef()),
		Args:              t.minArgs(1),
		ValidArgsFunction: t.completeRef,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, rest := t.split(args)
			pairs, err := parsePairs(rest, "meta key")
			if err != nil {
				return err
			}
			obj, err := t.load(cmd.Context(), ref)
			if err != nil {
				return err
			}
			s, err := a.session()
			if err != nil {
				return err
			}
			for _, p := range pairs {
				_, isProtected := obj.Protected[p[0]]
				if cmd.Flags().Changed("protected") {
					isProtected = protected
				}
				if err := s.Client.SetMeta(cmd.Context(), obj.Path, p[0], api.MetaValue{Value: p[1], Protected: isProtected}); err != nil {
					return fmt.Errorf("setting %q: %w", p[0], err)
				}
			}
			a.info("Updated %d meta %s of %s.", len(pairs), plural(len(pairs), "key", "keys"), obj.Label)
			return nil
		},
	}
	cmd.Flags().BoolVar(&protected, "protected", false, "make the keys protected (readable only with the right permissions)")
	return cmd
}

func (a *App) newMetaUnsetCommand(t metaTarget) *cobra.Command {
	return &cobra.Command{
		Use:               "unset " + t.use("key..."),
		Aliases:           []string{"rm", "delete"},
		Short:             "Remove meta keys",
		Args:              t.minArgs(1),
		ValidArgsFunction: t.completeRef,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, keys := t.split(args)
			obj, err := t.load(cmd.Context(), ref)
			if err != nil {
				return err
			}
			s, err := a.session()
			if err != nil {
				return err
			}
			removed := 0
			for _, k := range keys {
				err := s.Client.DeleteMeta(cmd.Context(), obj.Path, k)
				if api.IsStatus(err, http.StatusNotFound) {
					a.info("%s has no meta key %q.", obj.Label, k)
					continue
				}
				if err != nil {
					return fmt.Errorf("removing %q: %w", k, err)
				}
				removed++
			}
			if removed > 0 {
				a.info("Removed %d meta %s from %s.", removed, plural(removed, "key", "keys"), obj.Label)
			}
			return nil
		},
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
