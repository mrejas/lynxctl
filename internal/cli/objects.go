// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/mrejas/lynx-cli/internal/api"
	"github.com/mrejas/lynx-cli/internal/output"
	"github.com/mrejas/lynx-cli/internal/resolve"
)

// objectType describes devices and functions, which share model and
// endpoints in the API.
type objectType struct {
	kind    api.ObjectKind
	noun    string
	aliases []string
}

var (
	deviceType   = objectType{kind: api.DeviceX, noun: "device", aliases: []string{"dev", "d", "devices"}}
	functionType = objectType{kind: api.FunctionX, noun: "function", aliases: []string{"fn", "f", "functions"}}
)

// editableObject is the part of an object that create -f and edit accept.
type editableObject struct {
	Type          string            `yaml:"type"`
	Meta          map[string]string `yaml:"meta"`
	ProtectedMeta map[string]string `yaml:"protected_meta,omitempty"`
}

// scope is the session and installation that object commands work in.
type scope struct {
	*session
	installation int64
	clientID     int64 // zero if not known yet; see App.clientID
}

func (a *App) scope(ctx context.Context) (*scope, error) {
	s, err := a.session()
	if err != nil {
		return nil, err
	}
	inst, err := a.installation(ctx, s)
	if err != nil {
		return nil, err
	}
	return &scope{session: s, installation: inst.ID, clientID: inst.ClientID}, nil
}

// findObject resolves a reference to a device or function. A numeric
// reference is fetched directly; if there is no object with that ID, it is
// matched against names like any other reference.
func (a *App) findObject(ctx context.Context, sc *scope, t objectType, ref string, exact bool) (*api.Object, error) {
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		o, err := sc.Client.GetObject(ctx, t.kind, sc.installation, id)
		if err == nil {
			return o, nil
		}
		if !api.IsStatus(err, http.StatusNotFound) && !api.IsStatus(err, http.StatusForbidden) {
			return nil, err
		}
	}
	list, err := sc.Client.ListObjects(ctx, t.kind, sc.installation, nil)
	if err != nil {
		return nil, err
	}
	o, err := resolve.Find(t.noun, ref, list, objectKey, resolve.Options{Exact: exact, NameOnly: true})
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func objectKey(o api.Object) resolve.Key {
	return resolve.Key{ID: o.ID, Name: o.Name()}
}

func (t objectType) label(o *api.Object) string {
	return t.noun + " " + quoted(o.Name(), o.ID)
}

func (a *App) newObjectCommand(t objectType) *cobra.Command {
	cmd := groupCommand(t.noun, fmt.Sprintf("Manage %ss in the current installation", t.noun), t.aliases...)
	cmd.Long = fmt.Sprintf(`Manage %[1]ss in the current installation (or the one given with -i).

A %[1]s is referred to by ID, by its name (the "name" meta key) or by a
unique part of the name, case-insensitively. Deleting requires the ID or the
exact name.`, t.noun)
	cmd.AddCommand(
		a.newObjectListCommand(t),
		a.newObjectGetCommand(t),
		a.newObjectCreateCommand(t),
		a.newObjectEditCommand(t),
		a.newObjectDeleteCommand(t),
		a.newMetaCommand(metaTarget{
			noun:     t.noun,
			withRef:  true,
			complete: a.completeObjects(t),
			load: func(ctx context.Context, ref string) (*metaObject, error) {
				sc, err := a.scope(ctx)
				if err != nil {
					return nil, err
				}
				o, err := a.findObject(ctx, sc, t, ref, false)
				if err != nil {
					return nil, err
				}
				return &metaObject{
					Path:      api.ObjectPath(t.kind, sc.installation, o.ID),
					Label:     t.label(o),
					Meta:      o.Meta,
					Protected: o.ProtectedMeta,
				}, nil
			},
		}),
	)
	if t.kind == api.DeviceX {
		cmd.AddCommand(a.newDeviceFunctionsCommand())
	}
	return cmd
}

type listOptions struct {
	meta   []string
	typ    string
	device string
}

func (a *App) newObjectListCommand(t objectType) *cobra.Command {
	var opts listOptions
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   fmt.Sprintf("List %ss", t.noun),
		Example: fmt.Sprintf(`  %[1]s %[2]s list
  %[1]s %[2]s list --type temperature
  %[1]s %[2]s list --meta name='Kitchen*'
  %[1]s %[2]s list --meta pinned        # has the key, any value
  %[1]s %[2]s list -q            # IDs only, for scripts`, a.Name, t.noun),
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.listObjects(cmd.Context(), t, opts)
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&opts.meta, "meta", nil, metaFilterHelp)
	f.StringVar(&opts.typ, "type", "", "only show objects of this type")
	_ = cmd.RegisterFlagCompletionFunc("type", a.completeTypes(t))
	if t.kind == api.FunctionX {
		f.StringVar(&opts.device, "device", "", "only show functions of this device (ID or name)")
		_ = cmd.RegisterFlagCompletionFunc("device", a.completeObjects(deviceType))
	}
	return cmd
}

func (a *App) newDeviceFunctionsCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "functions <device>",
		Short:             "List the functions of a device",
		Args:              usageArgs(cobra.ExactArgs(1)),
		ValidArgsFunction: a.completeObjectArg(deviceType),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.listObjects(cmd.Context(), functionType, listOptions{device: args[0]})
		},
	}
}

func (a *App) listObjects(ctx context.Context, t objectType, opts listOptions) error {
	pr, err := a.printer()
	if err != nil {
		return err
	}
	filter, err := metaFilter(opts.meta, opts.typ)
	if err != nil {
		return err
	}
	sc, err := a.scope(ctx)
	if err != nil {
		return err
	}
	if opts.device != "" {
		dev, err := a.findObject(ctx, sc, deviceType, opts.device, false)
		if err != nil {
			return err
		}
		filter.Set("device_id", strconv.FormatInt(dev.ID, 10))
	}
	list, err := sc.Client.ListObjects(ctx, t.kind, sc.installation, filter)
	if err != nil {
		return err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	if list == nil {
		list = []api.Object{}
	}
	return pr.List(list, objectRows(t, list))
}

func objectRows(t objectType, list []api.Object) output.Rows {
	rows := output.Rows{Columns: []output.Column{{Header: "id"}, {Header: "name"}, {Header: "type"}}}
	if t.kind == api.FunctionX {
		rows.Columns = append(rows.Columns,
			output.Column{Header: "device"},
			output.Column{Header: "topic read"},
			output.Column{Header: "topic write", Wide: true})
	}
	rows.Columns = append(rows.Columns, output.Column{Header: "updated", Wide: true})
	for _, o := range list {
		row := []string{strconv.FormatInt(o.ID, 10), o.Name(), o.Type}
		if t.kind == api.FunctionX {
			row = append(row, o.Meta["device_id"], o.Meta["topic_read"], o.Meta["topic_write"])
		}
		rows.Rows = append(rows.Rows, append(row, formatTime(o.Updated)))
	}
	return rows
}

func (a *App) newObjectGetCommand(t objectType) *cobra.Command {
	return &cobra.Command{
		Use:               fmt.Sprintf("get <%s>", t.noun),
		Short:             fmt.Sprintf("Show a %s", t.noun),
		Args:              usageArgs(cobra.ExactArgs(1)),
		ValidArgsFunction: a.completeObjectArg(t),
		RunE: func(cmd *cobra.Command, args []string) error {
			pr, err := a.printer()
			if err != nil {
				return err
			}
			sc, err := a.scope(cmd.Context())
			if err != nil {
				return err
			}
			o, err := a.findObject(cmd.Context(), sc, t, args[0], false)
			if err != nil {
				return err
			}
			return printObject(pr, o)
		},
	}
}

func printObject(pr *output.Printer, o *api.Object) error {
	fields := []output.Field{
		{Label: "ID", Value: strconv.FormatInt(o.ID, 10)},
		{Label: "Name", Value: o.Name()},
		{Label: "Type", Value: o.Type},
		{Label: "Installation", Value: strconv.FormatInt(o.InstallationID, 10)},
		{Label: "Created", Value: formatTime(o.Created)},
		{Label: "Updated", Value: formatTime(o.Updated)},
	}
	fields = append(fields, metaFields("Meta", o.Meta)...)
	fields = append(fields, metaFields("Protected meta", o.ProtectedMeta)...)
	return pr.Details(o, strconv.FormatInt(o.ID, 10), fields)
}

type createOptions struct {
	file   string
	typ    string
	name   string
	meta   []string
	device string
}

func (a *App) newObjectCreateCommand(t objectType) *cobra.Command {
	var opts createOptions
	example := fmt.Sprintf(`  %[1]s %[2]s create --type zwave --name "Hall sensor" --meta device_id=12
  %[1]s %[2]s create -f %[2]s.yaml
  %[1]s %[2]s create -f %[2]s.yaml --name "Copy of hall sensor"`, a.Name, t.noun)
	if t.kind == api.FunctionX {
		example = fmt.Sprintf(`  %[1]s function create --type temperature --name "Kitchen" --device "hall sensor" \
      --meta topic_read=obj/zwave/12/temperature --meta unit=°C
  %[1]s function create -f function.yaml`, a.Name)
	}
	cmd := &cobra.Command{
		Use:   "create",
		Short: fmt.Sprintf("Create a %s", t.noun),
		Long: fmt.Sprintf(`Create a %[1]s from flags, from a YAML or JSON file, or both: flags are
applied on top of the file. The file has the same layout as "%[1]s edit":

  type: temperature
  meta:
    name: Kitchen
    topic_read: obj/zwave/12/temperature`, t.noun),
		Example: example,
		Args:    usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.createObject(cmd.Context(), t, opts)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&opts.file, "file", "f", "", `YAML or JSON file to create from ("-" for stdin)`)
	f.StringVar(&opts.typ, "type", "", "type (required unless given in the file)")
	_ = cmd.RegisterFlagCompletionFunc("type", a.completeTypes(t))
	f.StringVar(&opts.name, "name", "", `name (sets the "name" meta key)`)
	f.StringArrayVar(&opts.meta, "meta", nil, "set a meta `key=value` (repeatable)")
	if t.kind == api.FunctionX {
		f.StringVar(&opts.device, "device", "", `device this function belongs to (sets the "device_id" meta key)`)
		_ = cmd.RegisterFlagCompletionFunc("device", a.completeObjects(deviceType))
	}
	return cmd
}

func (a *App) createObject(ctx context.Context, t objectType, opts createOptions) error {
	pr, err := a.printer()
	if err != nil {
		return err
	}
	var e editableObject
	if opts.file != "" {
		data, err := a.readInput(opts.file)
		if err != nil {
			return err
		}
		if err := decodeStrict(data, &e); err != nil {
			return usageError(err)
		}
	}
	pairs, err := parsePairs(opts.meta, "--meta")
	if err != nil {
		return err
	}
	sc, err := a.scope(ctx)
	if err != nil {
		return err
	}

	o := &api.Object{InstallationID: sc.installation, Type: e.Type, Meta: api.Meta(e.Meta), ProtectedMeta: api.Meta(e.ProtectedMeta)}
	if o.Meta == nil {
		o.Meta = api.Meta{}
	}
	if opts.typ != "" {
		o.Type = opts.typ
	}
	for _, p := range pairs {
		o.Meta[p[0]] = p[1]
	}
	if opts.name != "" {
		o.Meta["name"] = opts.name
	}
	if opts.device != "" {
		dev, err := a.findObject(ctx, sc, deviceType, opts.device, false)
		if err != nil {
			return err
		}
		o.Meta["device_id"] = strconv.FormatInt(dev.ID, 10)
	}
	if o.Type == "" {
		return usageErrorf("a type is required. Use --type or set it in the file")
	}

	created, err := sc.Client.CreateObject(ctx, t.kind, o)
	if err != nil {
		return err
	}
	a.info("Created %s.", t.label(created))
	return printObject(pr, created)
}

func (a *App) newObjectEditCommand(t objectType) *cobra.Command {
	return &cobra.Command{
		Use:   fmt.Sprintf("edit <%s>", t.noun),
		Short: fmt.Sprintf("Edit a %s in your editor", t.noun),
		Long: fmt.Sprintf(`Open the %s as YAML in $VISUAL or $EDITOR (default: vi, or notepad on
Windows). When you save and close the editor, the changes are sent to the
server. If the server rejects them, the editor opens again with the error.
Save without changes to cancel.`, t.noun),
		Args:              usageArgs(cobra.ExactArgs(1)),
		ValidArgsFunction: a.completeObjectArg(t),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sc, err := a.scope(ctx)
			if err != nil {
				return err
			}
			o, err := a.findObject(ctx, sc, t, args[0], false)
			if err != nil {
				return err
			}
			body, err := yaml.Marshal(editableObject{Type: o.Type, Meta: o.Meta, ProtectedMeta: o.ProtectedMeta})
			if err != nil {
				return err
			}
			header := fmt.Sprintf("Editing %s in installation %d.\nRead-only: created %s, updated %s.\nSave without changes to cancel.",
				t.label(o), o.InstallationID, formatTime(o.Created), formatTime(o.Updated))
			err = a.editLoop(header, body, func(data []byte) error {
				var e editableObject
				if err := decodeStrict(data, &e); err != nil {
					return err
				}
				if e.Type == "" {
					return errors.New("type must not be empty")
				}
				updated := *o
				updated.Type, updated.Meta, updated.ProtectedMeta = e.Type, api.Meta(e.Meta), api.Meta(e.ProtectedMeta)
				if updated.Meta == nil {
					updated.Meta = api.Meta{}
				}
				_, err := sc.Client.UpdateObject(ctx, t.kind, &updated)
				return err
			})
			if errors.Is(err, errEditCancelled) {
				a.info("No changes made.")
				return nil
			}
			if err != nil {
				return err
			}
			a.info("Updated %s.", t.label(o))
			return nil
		},
	}
}

func (a *App) newObjectDeleteCommand(t objectType) *cobra.Command {
	var yes, cascade bool
	cmd := &cobra.Command{
		Use:               fmt.Sprintf("delete <%s>", t.noun),
		Aliases:           []string{"rm"},
		Short:             fmt.Sprintf("Delete a %s (ID or exact name required)", t.noun),
		Args:              usageArgs(cobra.ExactArgs(1)),
		ValidArgsFunction: a.completeObjectArg(t),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sc, err := a.scope(ctx)
			if err != nil {
				return err
			}
			o, err := a.findObject(ctx, sc, t, args[0], true)
			if err != nil {
				return err
			}
			var functions []api.Object
			if t.kind == api.DeviceX {
				functions, err = sc.Client.ListObjects(ctx, api.FunctionX, sc.installation,
					url.Values{"device_id": {strconv.FormatInt(o.ID, 10)}})
				if err != nil {
					return err
				}
			}
			question := fmt.Sprintf("Delete %s?", t.label(o))
			switch {
			case cascade && len(functions) > 0:
				question = fmt.Sprintf("Delete %s and its %d %s?", t.label(o), len(functions), plural(len(functions), "function", "functions"))
			case len(functions) > 0:
				a.info("Note: %d %s will still refer to this device. Use --cascade to delete them too.",
					len(functions), plural(len(functions), "function", "functions"))
			}
			if err := a.confirm(question, yes); err != nil {
				return err
			}
			if cascade {
				for _, fn := range functions {
					if err := sc.Client.DeleteObject(ctx, api.FunctionX, sc.installation, fn.ID); err != nil {
						return fmt.Errorf("deleting %s: %w", functionType.label(&fn), err)
					}
					a.info("Deleted %s.", functionType.label(&fn))
				}
			}
			if err := sc.Client.DeleteObject(ctx, t.kind, sc.installation, o.ID); err != nil {
				return err
			}
			a.info("Deleted %s.", t.label(o))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	if t.kind == api.DeviceX {
		cmd.Flags().BoolVar(&cascade, "cascade", false, "also delete the functions of the device")
	}
	return cmd
}
