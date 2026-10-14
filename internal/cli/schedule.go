// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mrejas/lynx-cli/internal/api"
)

// cronFields are the schedule fields in crontab order.
var cronFields = []string{"minute", "hour", "day_of_month", "month", "day_of_week"}

// cronString shows a schedule's time fields as a crontab expression.
func cronString(o api.JSONObject) string {
	parts := make([]string, len(cronFields))
	for i, f := range cronFields {
		parts[i] = str(o[f])
		if parts[i] == "" {
			parts[i] = "*"
		}
	}
	return strings.Join(parts, " ")
}

func (a *App) scheduleResource() *resource {
	return &resource{
		noun:       "schedule",
		collection: a.installationPath("/api/v2/schedule/%d"),
		columns: []column{
			field("id", "id"),
			{header: "active", value: func(o api.JSONObject) string { return yesNo(o["active"] == true) }},
			{header: "cron", value: cronString},
			field("topic", "topic"),
			field("value", "value"),
			field("executor", "executor"),
			{header: "updated", wide: true, value: func(o api.JSONObject) string { return formatTimestamp(float64(num(o, "updated_at"))) }},
		},
		readOnly: []string{"id", "installation_id", "created_at", "updated_at"},
		required: []string{"topic", "value"},
		createExample: fmt.Sprintf(`  # Turn on the hall lamp at 07:00 on weekdays
  %[1]s schedule create --function "hall lamp" --value on --cron "0 7 * * 1,2,3,4,5"

  # Publish to a topic every day at 22:30
  %[1]s schedule create --topic set/obj/zwave/13/switch --value 0 --cron "30 22 * * *"`, a.Name),
		createFlags: func(cmd *cobra.Command) func(ctx context.Context, o api.JSONObject) error {
			var function, topic, value, cron, executor string
			var inactive bool
			f := cmd.Flags()
			f.StringVar(&function, "function", "", `function to set (uses its "topic_write" meta key)`)
			f.StringVar(&topic, "topic", "", "topic to publish to")
			f.StringVar(&value, "value", "", "value to publish (a number, or on/off)")
			f.StringVar(&cron, "cron", "", `when to run, as "minute hour day-of-month month day-of-week" (* for any)`)
			f.StringVar(&executor, "executor", "", `where the schedule runs (default: "gateway")`)
			f.BoolVar(&inactive, "inactive", false, "create the schedule disabled")
			cmd.MarkFlagsMutuallyExclusive("function", "topic")
			_ = cmd.RegisterFlagCompletionFunc("function", a.completeObjects(functionType))
			return func(ctx context.Context, o api.JSONObject) error {
				sc, err := a.scope(ctx)
				if err != nil {
					return err
				}
				o["installation_id"] = sc.installation
				if function != "" {
					fn, err := a.findObject(ctx, sc, functionType, function, false)
					if err != nil {
						return err
					}
					if topic = fn.Meta["topic_write"]; topic == "" {
						return fmt.Errorf("%s has no topic_write meta key, so it cannot be scheduled", functionType.label(fn))
					}
				}
				if topic != "" {
					o["topic"] = topic
				}
				if value != "" {
					v, err := parseSetValue(value)
					if err != nil {
						return err
					}
					o["value"] = v
				}
				if cron != "" {
					parts := strings.Fields(cron)
					if len(parts) != len(cronFields) {
						return usageErrorf(`--cron needs 5 fields: "minute hour day-of-month month day-of-week", got %q`, cron)
					}
					for i, f := range cronFields {
						o[f] = parts[i]
					}
				}
				if executor != "" {
					o["executor"] = executor
				}
				if _, ok := o["active"]; !ok || inactive {
					o["active"] = !inactive
				}
				return nil
			}
		},
		listFlags: func(cmd *cobra.Command) func() (url.Values, error) {
			var executor string
			cmd.Flags().StringVar(&executor, "executor", "", "only show schedules run by this executor")
			return func() (url.Values, error) {
				if executor == "" {
					return nil, nil
				}
				return url.Values{"executor": {executor}}, nil
			}
		},
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func (a *App) newScheduleCommand() *cobra.Command {
	r := a.scheduleResource()
	cmd := groupCommand("schedule", "Manage schedules in the current installation", "schedules", "sched")
	cmd.Long = `Schedules publish a value to a topic at given times, like cron. Times are
given as "minute hour day-of-month month day-of-week", where each field is *
or a comma-separated list. Schedules are referred to by ID.`
	cmd.AddCommand(a.resourceCommands(r)...)
	cmd.AddCommand(a.newScheduleToggleCommand(r, true), a.newScheduleToggleCommand(r, false))
	return cmd
}

func (a *App) newScheduleToggleCommand(r *resource, active bool) *cobra.Command {
	verb := map[bool]string{true: "enable", false: "disable"}[active]
	return &cobra.Command{
		Use:               verb + " <schedule-id>...",
		Short:             strings.ToUpper(verb[:1]) + verb[1:] + " schedules",
		Args:              usageArgs(cobra.MinimumNArgs(1)),
		ValidArgsFunction: a.completeResource(r),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			for _, ref := range args {
				o, s, path, err := a.findResource(ctx, r, ref, true)
				if err != nil {
					return err
				}
				o["active"] = active
				if _, err := s.Client.CallJSON(ctx, http.MethodPut, fmt.Sprintf("%s/%d", path, num(o, "id")), o); err != nil {
					return err
				}
				a.info("%sd %s.", strings.ToUpper(verb[:1])+verb[1:], r.label(o))
			}
			return nil
		},
	}
}
