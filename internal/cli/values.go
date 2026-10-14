package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/mrejas/lynx-cli/internal/api"
	"github.com/mrejas/lynx-cli/internal/config"
	"github.com/mrejas/lynx-cli/internal/mqtt"
	"github.com/mrejas/lynx-cli/internal/output"
	"github.com/mrejas/lynx-cli/internal/resolve"
)

// valueView is the JSON/YAML representation of a value in get, log and watch.
type valueView struct {
	FunctionID int64    `json:"function_id,omitempty"`
	Name       string   `json:"name,omitempty"`
	Topic      string   `json:"topic"`
	Value      *float64 `json:"value"`
	Formatted  string   `json:"formatted,omitempty"`
	Timestamp  float64  `json:"timestamp,omitempty"`
	Time       string   `json:"time,omitempty"`
	Msg        string   `json:"msg,omitempty"`
}

// target is a topic to read, with the function it belongs to (if known).
type target struct {
	fn    *api.Object
	topic string // with client ID prefix
}

func (t target) name() string {
	if t.fn != nil && t.fn.Name() != "" {
		return t.fn.Name()
	}
	return t.topic
}

func (t target) meta() api.Meta {
	if t.fn == nil {
		return nil
	}
	return t.fn.Meta
}

func (t target) view(s *api.Status) valueView {
	v := valueView{Topic: t.topic}
	if t.fn != nil {
		v.FunctionID, v.Name = t.fn.ID, t.fn.Name()
	}
	if s != nil {
		value := s.Value
		v.Value, v.Formatted, v.Timestamp, v.Time, v.Msg = &value, formatValue(value, t.meta()), s.Timestamp, rfc3339(s.Timestamp), s.Msg
	}
	return v
}

// clientID returns the MQTT client ID of the scope's installation, looking
// it up if it is not known yet (e.g. with a numeric -i).
func (a *App) clientID(ctx context.Context, sc *scope) (int64, error) {
	if sc.clientID != 0 {
		return sc.clientID, nil
	}
	inst, err := a.resolveInstallation(ctx, sc.session, strconv.FormatInt(sc.installation, 10), resolve.Options{})
	if err != nil {
		return 0, err
	}
	sc.clientID = inst.ClientID
	return sc.clientID, nil
}

func fullTopic(clientID int64, topic string) string {
	prefix := strconv.FormatInt(clientID, 10) + "/"
	topic = strings.TrimPrefix(topic, "/")
	if strings.HasPrefix(topic, prefix) {
		return topic
	}
	return prefix + topic
}

var clientPrefix = regexp.MustCompile(`^[0-9]+/`)

// valueSource holds what get, log and watch need: the functions of the
// installation and the topics selected by the user.
type valueSource struct {
	sc        *scope
	clientID  int64
	functions []api.Object
	byTopic   map[string]*api.Object
	targets   []target
	// all is true when the user selected neither functions nor topics.
	all bool
}

// valueSource resolves function references and raw topics. Functions
// without a topic_read meta key are an error when named explicitly.
func (a *App) valueSource(ctx context.Context, refs, topics []string) (*valueSource, error) {
	sc, err := a.scope(ctx)
	if err != nil {
		return nil, err
	}
	cid, err := a.clientID(ctx, sc)
	if err != nil {
		return nil, err
	}
	fns, err := sc.Client.ListObjects(ctx, api.FunctionX, sc.installation, nil)
	if err != nil {
		return nil, err
	}
	sort.Slice(fns, func(i, j int) bool { return fns[i].ID < fns[j].ID })
	src := &valueSource{sc: sc, clientID: cid, functions: fns, byTopic: map[string]*api.Object{}}
	for i := range fns {
		if t := fns[i].Meta["topic_read"]; t != "" {
			src.byTopic[fullTopic(cid, t)] = &fns[i]
		}
	}

	for _, ref := range refs {
		fn, err := resolve.Find("function", ref, fns, objectKey, resolve.Options{})
		if err != nil {
			return nil, err
		}
		t := fn.Meta["topic_read"]
		if t == "" {
			return nil, fmt.Errorf("%s has no topic_read meta key, so it has no value", functionType.label(&fn))
		}
		src.targets = append(src.targets, target{fn: &fn, topic: fullTopic(cid, t)})
	}
	for _, t := range topics {
		full := fullTopic(cid, t)
		src.targets = append(src.targets, target{fn: src.byTopic[full], topic: full})
	}
	src.all = len(refs) == 0 && len(topics) == 0
	if src.all {
		for i := range fns {
			if t := fns[i].Meta["topic_read"]; t != "" {
				src.targets = append(src.targets, target{fn: &fns[i], topic: fullTopic(cid, t)})
			}
		}
	}
	return src, nil
}

func (src *valueSource) topics() []string {
	out := make([]string, len(src.targets))
	for i, t := range src.targets {
		out[i] = t.topic
	}
	return out
}

// targetFor labels a topic seen in a response.
func (src *valueSource) targetFor(topic string) target {
	return target{fn: src.byTopic[topic], topic: topic}
}

func (a *App) newGetCommand() *cobra.Command {
	var topics []string
	cmd := &cobra.Command{
		Use:   "get [function]...",
		Short: "Show the latest value of functions",
		Long: `Show the latest reported value of one or more functions, read from the
topic in their "topic_read" meta key. Without arguments, all functions of the
installation are shown.

Values are formatted with the "format" or "unit" meta key. Use -q to print
only the raw values, one per line.`,
		Example: fmt.Sprintf(`  %[1]s get                          # all functions
  %[1]s get kitchen "hall temp"
  %[1]s get kitchen -q               # just the number, for scripts
  %[1]s get --topic obj/zwave/12/temperature`, a.Name),
		ValidArgsFunction: a.completeObjects(functionType),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			pr, err := a.printer()
			if err != nil {
				return err
			}
			src, err := a.valueSource(ctx, args, topics)
			if err != nil {
				return err
			}
			var query []string
			if !src.all {
				query = src.topics()
			}
			status, err := src.sc.Client.Status(ctx, src.sc.installation, query)
			if err != nil {
				return err
			}
			latest := map[string]*api.Status{}
			for i := range status {
				latest[status[i].Topic] = &status[i]
			}
			views := []valueView{}
			rows := output.Rows{Columns: []output.Column{
				{Header: "name"}, {Header: "value"}, {Header: "time"},
				{Header: "id", Wide: true}, {Header: "topic", Wide: true}, {Header: "raw", Wide: true}, {Header: "msg", Wide: true},
			}, IDColumn: 5}
			for _, t := range src.targets {
				v := t.view(latest[t.topic])
				views = append(views, v)
				id, raw := "", ""
				if v.FunctionID != 0 {
					id = strconv.FormatInt(v.FunctionID, 10)
				}
				if v.Value != nil {
					raw = rawValue(*v.Value)
				}
				rows.Rows = append(rows.Rows, []string{t.name(), v.Formatted, formatTimestamp(v.Timestamp), id, t.topic, raw, v.Msg})
			}
			return pr.List(views, rows)
		},
	}
	cmd.Flags().StringArrayVar(&topics, "topic", nil, "read a topic directly (relative to the installation; repeatable)")
	return cmd
}

func (a *App) newSetCommand() *cobra.Command {
	var msg, topic string
	cmd := &cobra.Command{
		Use:   "set <function> <value>",
		Short: "Set the value of a function",
		Long: `Set the value of a function by publishing it to the topic in its
"topic_write" meta key. The value is a number (with "." or "," as decimal
separator), or on/off, true/false, yes/no for 1 and 0.

The value is sent through the REST API, so no MQTT connection is needed.`,
		Example: fmt.Sprintf(`  %[1]s set "hall lamp" on
  %[1]s set thermostat 21,5
  %[1]s set --topic set/obj/zwave/12/switch 1`, a.Name),
		Args: func(cmd *cobra.Command, args []string) error {
			if topic != "" {
				return usageArgs(cobra.ExactArgs(1))(cmd, args)
			}
			return usageArgs(cobra.ExactArgs(2))(cmd, args)
		},
		ValidArgsFunction: a.completeObjectArg(functionType),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			value, err := parseSetValue(args[len(args)-1])
			if err != nil {
				return err
			}
			sc, err := a.scope(ctx)
			if err != nil {
				return err
			}
			label := fmt.Sprintf("topic %q", topic)
			if topic == "" {
				fn, err := a.findObject(ctx, sc, functionType, args[0], false)
				if err != nil {
					return err
				}
				label = functionType.label(fn)
				if topic = fn.Meta["topic_write"]; topic == "" {
					return fmt.Errorf("%s has no topic_write meta key, so it cannot be set", label)
				}
			}
			// The publish endpoint takes topics relative to the installation.
			if clientPrefix.MatchString(topic) {
				cid, err := a.clientID(ctx, sc)
				if err != nil {
					return err
				}
				topic = strings.TrimPrefix(topic, strconv.FormatInt(cid, 10)+"/")
			}
			payload := map[string]any{"value": value}
			if msg != "" {
				payload["msg"] = msg
			}
			if err := sc.Client.Publish(ctx, sc.installation, api.Publish{Topic: topic, Payload: payload}); err != nil {
				return err
			}
			a.info("Set %s to %s.", label, rawValue(value))
			return nil
		},
	}
	cmd.Flags().StringVar(&msg, "msg", "", "message to send along with the value")
	cmd.Flags().StringVar(&topic, "topic", "", "publish to this topic (relative to the installation) instead of a function's")
	return cmd
}

var (
	aggrMethods  = regexp.MustCompile(`^(count|first|last|avg|mean|spread|max|min|sum|stddev|median|percentile\([0-9.]+\)|integral\([0-9]+\))$`)
	aggrInterval = regexp.MustCompile(`^[0-9]+[smhd]$`)
)

type logOptions struct {
	topics   []string
	since    string
	from, to string
	limit    int
	aggr     string
	interval string
}

func (a *App) newLogCommand() *cobra.Command {
	var opts logOptions
	cmd := &cobra.Command{
		Use:   "log [function]...",
		Short: "Show historic values of functions",
		Long: `Show logged values of one or more functions, oldest first. Without
arguments, all logged values of the installation are shown. By default the
last 24 hours are shown.

Times can be given as a duration ago (90m, 2h, 7d), a date ("2026-10-01"),
a date and time ("2026-10-01 12:00"), RFC 3339 or a Unix timestamp.

With --aggr, values are aggregated per --interval (default: chosen by the
server from the time range).`,
		Example: fmt.Sprintf(`  %[1]s log kitchen
  %[1]s log kitchen "hall temp" --since 7d --aggr avg --interval 1h
  %[1]s log kitchen --from 2026-10-01 --to 2026-10-02 -o csv > kitchen.csv`, a.Name),
		ValidArgsFunction: a.completeObjects(functionType),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.showLog(cmd.Context(), args, opts)
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&opts.topics, "topic", nil, "read a topic directly (relative to the installation; repeatable)")
	f.StringVar(&opts.since, "since", "", "show values from this long ago until now (e.g. 2h, 7d)")
	f.StringVar(&opts.from, "from", "", "start time")
	f.StringVar(&opts.to, "to", "", "end time (default: now)")
	f.IntVar(&opts.limit, "limit", 0, "maximum number of values (default: the server's limit, 10000)")
	f.StringVar(&opts.aggr, "aggr", "", "aggregate: avg, min, max, median, sum, count, first, last, spread, stddev, percentile(X), integral(X)")
	f.StringVar(&opts.interval, "interval", "", "aggregation interval, e.g. 10m, 1h, 1d")
	cmd.MarkFlagsMutuallyExclusive("since", "from")
	_ = cmd.RegisterFlagCompletionFunc("aggr", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"avg", "min", "max", "median", "sum", "count", "first", "last", "spread", "stddev"}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

func (a *App) showLog(ctx context.Context, refs []string, opts logOptions) error {
	pr, err := a.printer()
	if err != nil {
		return err
	}
	now := time.Now()
	q := api.LogQuery{Limit: opts.limit, Order: "desc"}
	if opts.since != "" {
		d, err := parseDuration(opts.since)
		if err != nil {
			return usageError(err)
		}
		q.From = now.Add(-d).Unix()
	}
	for _, t := range []struct {
		value string
		dst   *int64
	}{{opts.from, &q.From}, {opts.to, &q.To}} {
		if t.value == "" {
			continue
		}
		parsed, err := parseTime(t.value, now)
		if err != nil {
			return usageError(err)
		}
		*t.dst = parsed.Unix()
	}
	if q.From != 0 && q.To != 0 && q.From >= q.To {
		return usageErrorf("the start time must be before the end time")
	}
	if opts.aggr != "" && !aggrMethods.MatchString(opts.aggr) {
		return usageErrorf("invalid --aggr %q", opts.aggr)
	}
	if opts.interval != "" && !aggrInterval.MatchString(opts.interval) {
		return usageErrorf("invalid --interval %q (use e.g. 10m, 1h, 1d)", opts.interval)
	}
	q.AggrMethod, q.AggrInterval = opts.aggr, opts.interval
	if opts.limit < 0 {
		return usageErrorf("--limit must be positive")
	}

	src, err := a.valueSource(ctx, refs, opts.topics)
	if err != nil {
		return err
	}
	if !src.all {
		q.Topics = src.topics()
	}
	page, err := src.sc.Client.Log(ctx, src.sc.installation, q)
	if err != nil {
		return err
	}
	// Fetched newest first so that a limit keeps the latest values; shown
	// oldest first like any log.
	entries := page.Data
	slices.Reverse(entries)

	showName := len(src.targets) != 1
	rows := output.Rows{Columns: []output.Column{{Header: "time"}}}
	if showName {
		rows.Columns = append(rows.Columns, output.Column{Header: "name"})
	}
	rows.Columns = append(rows.Columns, output.Column{Header: "value"},
		output.Column{Header: "topic", Wide: true}, output.Column{Header: "raw", Wide: true}, output.Column{Header: "msg", Wide: true})
	rows.IDColumn = len(rows.Columns) - 2 // -q prints the raw values
	views := []valueView{}
	for i := range entries {
		t := src.targetFor(entries[i].Topic)
		v := t.view(&entries[i])
		views = append(views, v)
		row := []string{formatTimestamp(v.Timestamp)}
		if showName {
			row = append(row, t.name())
		}
		rows.Rows = append(rows.Rows, append(row, v.Formatted, t.topic, rawValue(entries[i].Value), v.Msg))
	}
	if err := pr.List(views, rows); err != nil {
		return err
	}
	if page.Total > page.Count {
		a.info("Showing the latest %d of %d values. Use --limit or a shorter time range to see others.", page.Count, page.Total)
	}
	return nil
}

// subscribeFunc matches mqtt.Subscribe; tests replace it.
type subscribeFunc func(ctx context.Context, opts mqtt.Options, topics []string, handle func(mqtt.Message)) error

func (a *App) newWatchCommand() *cobra.Command {
	var topics []string
	var count int
	var broker string
	cmd := &cobra.Command{
		Use:   "watch [function]...",
		Short: "Show values live as they are reported",
		Long: `Connect to the installation's MQTT broker and print values as they arrive,
until interrupted with Ctrl-C. Without arguments, all object topics of the
installation (obj/#) are watched.

Topics given with --topic are relative to the installation and may contain
the MQTT wildcards + and #. The broker is the server's host on port 8883
(TLS) unless --broker or IOTOPEN_BROKER is set.

With -o json, each value is printed as one JSON object per line.`,
		Example: fmt.Sprintf(`  %[1]s watch
  %[1]s watch kitchen "hall temp"
  %[1]s watch --topic 'obj/zwave/#'
  %[1]s watch kitchen --count 1 -q   # wait for the next value and print it`, a.Name),
		ValidArgsFunction: a.completeObjects(functionType),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.watch(cmd.Context(), args, topics, count, broker)
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&topics, "topic", nil, "watch a topic (relative to the installation, wildcards allowed; repeatable)")
	f.IntVar(&count, "count", 0, "stop after this many values")
	f.StringVar(&broker, "broker", "", "MQTT broker as host[:port], or a URL such as tcp://localhost:1883")
	return cmd
}

func (a *App) watch(ctx context.Context, refs, topics []string, count int, broker string) error {
	if a.outputFlag != "" && a.outputFlag != string(output.Table) && a.outputFlag != string(output.JSON) {
		return usageErrorf("watch supports only -o table and -o json")
	}
	src, err := a.valueSource(ctx, refs, topics)
	if err != nil {
		return err
	}
	filters := src.topics()
	if src.all {
		filters = []string{fullTopic(src.clientID, "obj/#")}
	}
	if broker == "" {
		broker = config.Getenv("BROKER")
	}
	if broker == "" {
		// The broker runs on the same host as the API, on its own port.
		broker = hostOf(src.sc.URL)
		if host, _, err := net.SplitHostPort(broker); err == nil {
			broker = host
		}
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	width := 0
	for _, t := range src.targets {
		width = max(width, len([]rune(t.name())))
	}
	width = min(width, 40)

	var mu sync.Mutex
	seen := 0
	handle := func(m mqtt.Message) {
		mu.Lock()
		defer mu.Unlock()
		if count > 0 && seen >= count {
			return
		}
		seen++
		a.printWatched(src.targetFor(m.Topic), m.Payload, width)
		if count > 0 && seen >= count {
			cancel()
		}
	}

	subscribe := a.Subscribe
	if subscribe == nil {
		subscribe = mqtt.Subscribe
	}
	a.info("Watching %d %s on %s. Press Ctrl-C to stop.", len(filters), plural(len(filters), "topic", "topics"), mqtt.BrokerURL(broker))
	return subscribe(ctx, mqtt.Options{Broker: broker, Password: src.sc.Client.APIKey}, filters, handle)
}

func (a *App) printWatched(t target, payload []byte, width int) {
	var msg struct {
		Value     *float64 `json:"value"`
		Timestamp float64  `json:"timestamp"`
		Msg       string   `json:"msg"`
	}
	if json.Unmarshal(payload, &msg) != nil || msg.Value == nil {
		// Not a value message; show it as it is.
		if a.outputFlag == string(output.JSON) {
			_ = output.WriteCompactJSON(a.Out, map[string]string{"topic": t.topic, "payload": string(payload)})
			return
		}
		fmt.Fprintf(a.Out, "%s  %-*s  %s\n", time.Now().Format("2006-01-02 15:04:05"), width, t.name(), payload)
		return
	}
	if msg.Timestamp == 0 {
		msg.Timestamp = float64(time.Now().UnixMilli()) / 1000
	}
	v := t.view(&api.Status{Value: *msg.Value, Timestamp: msg.Timestamp, Msg: msg.Msg, Topic: t.topic})
	switch {
	case a.outputFlag == string(output.JSON):
		_ = output.WriteCompactJSON(a.Out, v)
	case a.quiet:
		fmt.Fprintln(a.Out, rawValue(*msg.Value))
	default:
		line := fmt.Sprintf("%s  %-*s  %s", formatTimestamp(msg.Timestamp), width, t.name(), v.Formatted)
		if msg.Msg != "" {
			line += "  (" + msg.Msg + ")"
		}
		fmt.Fprintln(a.Out, line)
	}
}
