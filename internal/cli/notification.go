// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/mrejas/lynx-cli/internal/api"
)

func (a *App) messageResource() *resource {
	return &resource{
		noun:       "message",
		collection: a.installationPath("/api/v2/notification/%d/message"),
		nameKey:    "name",
		columns: []column{
			field("id", "id"),
			field("name", "name"),
			{header: "text", value: func(o api.JSONObject) string { return firstLine(str(o["text"])) }},
		},
		readOnly: []string{"id"},
		required: []string{"name", "text"},
		createExample: fmt.Sprintf(`  %[1]s notification message create --name Alarm \
      --text '{{.installation.Name}}: {{.payload.message}}'
  %[1]s notification message create --name Report --text-file report.tmpl`, a.Name),
		createFlags: func(cmd *cobra.Command) func(context.Context, api.JSONObject) error {
			var name, text, textFile string
			f := cmd.Flags()
			f.StringVar(&name, "name", "", "name of the message")
			f.StringVar(&text, "text", "", "message text (a Go template)")
			f.StringVar(&textFile, "text-file", "", `read the message text from a file ("-" for stdin)`)
			cmd.MarkFlagsMutuallyExclusive("text", "text-file")
			return func(_ context.Context, o api.JSONObject) error {
				if name != "" {
					o["name"] = name
				}
				if text != "" {
					o["text"] = text
				}
				if textFile != "" {
					data, err := a.readInput(textFile)
					if err != nil {
						return err
					}
					o["text"] = string(data)
				}
				return nil
			}
		},
	}
}

func (a *App) executorResource() *resource {
	return &resource{
		noun:       "executor",
		collection: a.installationPath("/api/v2/notification/%d/executor"),
		nameKey:    "name",
		columns: []column{
			field("id", "id"),
			field("name", "name"),
			field("type", "type"),
			wideField("organization", "organization_id"),
		},
	}
}

func (a *App) outputResource() *resource {
	messages, executors := a.messageResource(), a.executorResource()
	return &resource{
		noun:       "output",
		collection: a.installationPath("/api/v2/notification/%d/output"),
		nameKey:    "name",
		columns: []column{
			field("id", "id"),
			field("name", "name"),
			field("executor", "notification_output_executor_id"),
			field("message", "notification_message_id"),
			{header: "config", wide: true, value: func(o api.JSONObject) string {
				data, _ := json.Marshal(o["config"])
				return string(data)
			}},
		},
		readOnly: []string{"id", "installation_id"},
		required: []string{"name", "notification_output_executor_id", "notification_message_id"},
		createExample: fmt.Sprintf(`  %[1]s notification output create --name "Mail on-call" --executor mail --message alarm \
      --config recipients=oncall@example.com --config subject='Alarm in {{.installation.Name}}'`, a.Name),
		createFlags: func(cmd *cobra.Command) func(context.Context, api.JSONObject) error {
			var name, executor, message string
			var config []string
			f := cmd.Flags()
			f.StringVar(&name, "name", "", "name of the output")
			f.StringVar(&executor, "executor", "", "executor that sends it (ID or name)")
			f.StringVar(&message, "message", "", "message to send (ID or name)")
			f.StringArrayVar(&config, "config", nil, "executor setting `key=value`, may use templates (repeatable)")
			_ = cmd.RegisterFlagCompletionFunc("executor", a.completeResource(executors))
			_ = cmd.RegisterFlagCompletionFunc("message", a.completeResource(messages))
			return func(ctx context.Context, o api.JSONObject) error {
				pairs, err := parsePairs(config, "--config")
				if err != nil {
					return err
				}
				sc, err := a.scope(ctx)
				if err != nil {
					return err
				}
				o["installation_id"] = sc.installation
				if name != "" {
					o["name"] = name
				}
				if executor != "" {
					e, _, _, err := a.findResource(ctx, executors, executor, false)
					if err != nil {
						return err
					}
					o["notification_output_executor_id"] = num(e, "id")
				}
				if message != "" {
					m, _, _, err := a.findResource(ctx, messages, message, false)
					if err != nil {
						return err
					}
					o["notification_message_id"] = num(m, "id")
				}
				cfg, _ := o["config"].(map[string]any)
				if cfg == nil {
					cfg = map[string]any{}
				}
				for _, p := range pairs {
					cfg[p[0]] = p[1]
				}
				o["config"] = cfg
				return nil
			}
		},
	}
}

func (a *App) newNotificationCommand() *cobra.Command {
	cmd := groupCommand("notification", "Manage notifications in the current installation", "notifications", "notif")
	cmd.Long = `Notifications are sent by outputs. An output combines a message (a text
template) with an executor (such as mail or SMS) and its settings. Send an
output with "notification send".`

	messages := groupCommand("message", "Manage notification messages", "messages", "msg")
	messages.AddCommand(a.resourceCommands(a.messageResource())...)

	outputs := groupCommand("output", "Manage notification outputs", "outputs", "out")
	outputs.AddCommand(a.resourceCommands(a.outputResource())...)

	executors := groupCommand("executor", "Show the executors available to the installation", "executors")
	er := a.executorResource()
	executors.AddCommand(a.newResourceListCommand(er), a.newResourceGetCommand(er))

	cmd.AddCommand(messages, outputs, executors, a.newNotificationSendCommand())
	return cmd
}

func (a *App) newNotificationSendCommand() *cobra.Command {
	var file string
	var fields, rawFields []string
	outputs := a.outputResource()
	cmd := &cobra.Command{
		Use:   "send <output>",
		Short: "Send a notification through an output",
		Long: `Send a notification through an output. Fields given with -F, --raw-field
or a JSON file (-f) are available to the message and config templates as
{{.payload.<key>}}.`,
		Example: fmt.Sprintf(`  %[1]s notification send "mail on-call" -F message="Water leak in the basement"
  %[1]s notification send alarm -f payload.json`, a.Name),
		Args:              usageArgs(cobra.ExactArgs(1)),
		ValidArgsFunction: a.completeResource(outputs),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			var payload any
			switch {
			case file != "" && (len(fields) > 0 || len(rawFields) > 0):
				return usageErrorf("-f cannot be combined with -F or --raw-field")
			case file != "":
				data, err := a.readInput(file)
				if err != nil {
					return err
				}
				if !json.Valid(data) {
					return usageErrorf("%s is not valid JSON", file)
				}
				payload = json.RawMessage(data)
			default:
				obj, err := buildFields(fields, rawFields)
				if err != nil {
					return err
				}
				payload = obj
			}
			o, s, path, err := a.findResource(ctx, outputs, args[0], false)
			if err != nil {
				return err
			}
			if _, err := s.Client.CallJSON(ctx, http.MethodPost, fmt.Sprintf("%s/%d/send", path, num(o, "id")), payload); err != nil {
				return err
			}
			a.info("Sent %s.", outputs.label(o))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&file, "file", "f", "", `JSON file with the payload ("-" for stdin)`)
	f.StringArrayVarP(&fields, "field", "F", nil, "add a typed payload field `key=value` (repeatable)")
	f.StringArrayVar(&rawFields, "raw-field", nil, "add a string payload field `key=value` (repeatable)")
	return cmd
}
