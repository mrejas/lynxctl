// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mrejas/lynx-cli/internal/api"
)

func (a *App) newFileCommand() *cobra.Command {
	var org int64
	r := &resource{
		noun: "file",
		collection: func(ctx context.Context) (string, error) {
			if org != 0 {
				return fmt.Sprintf("/api/v2/file/organization/%d", org), nil
			}
			return a.installationPath("/api/v2/file/installation/%d")(ctx)
		},
		nameKey: "name",
		columns: []column{
			field("id", "id"),
			field("name", "name"),
			field("mime", "mime"),
			{header: "updated", value: func(o api.JSONObject) string { return formatTimestamp(float64(num(o, "updated"))) }},
			wideField("hash", "hash"),
		},
		listFlags: func(cmd *cobra.Command) func() (url.Values, error) {
			var mimeFilter string
			cmd.Flags().StringVar(&mimeFilter, "mime", "", "only show files of this MIME type (* is a wildcard, e.g. 'image/*')")
			return func() (url.Values, error) {
				if mimeFilter == "" {
					return nil, nil
				}
				return url.Values{"mime": {mimeFilter}}, nil
			}
		},
	}

	cmd := groupCommand("file", "Manage files of the current installation or an organization", "files")
	cmd.Long = `Manage files stored on the server. Files belong to the current installation
(or the one given with -i), or to an organization with --org. Files are
referred to by ID, name or a unique part of the name.`
	cmd.PersistentFlags().Int64Var(&org, "org", 0, "use the files of this organization instead of an installation")
	cmd.AddCommand(
		a.newResourceListCommand(r),
		a.newResourceGetCommand(r),
		a.newFileUploadCommand(r),
		a.newFileReplaceCommand(r),
		a.newFileDownloadCommand(r),
		a.newResourceDeleteCommand(r),
	)
	return cmd
}

// fileType guesses a file's MIME type from its extension, falling back to
// its content.
func fileType(path string, data []byte) string {
	if t := mime.TypeByExtension(filepath.Ext(path)); t != "" {
		return t
	}
	return http.DetectContentType(data)
}

func (a *App) newFileUploadCommand(r *resource) *cobra.Command {
	var contentType string
	cmd := &cobra.Command{
		Use:   "upload <path>...",
		Short: "Upload files",
		Long: `Upload one or more files. The MIME type is guessed from the file name and
content; set it with --type if the guess is wrong.`,
		Example: fmt.Sprintf(`  %[1]s file upload floorplan.png manual.pdf
  %[1]s file upload --org 7 logo.svg`, a.Name),
		Args: usageArgs(cobra.MinimumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			pr, err := a.printer()
			if err != nil {
				return err
			}
			var parts []api.FilePart
			for _, path := range args {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				t := contentType
				if t == "" {
					t = fileType(path, data)
				}
				parts = append(parts, api.FilePart{Field: "filename", Filename: filepath.Base(path), ContentType: t, Data: data})
			}
			s, err := a.session()
			if err != nil {
				return err
			}
			path, err := r.collection(ctx)
			if err != nil {
				return err
			}
			body, _, err := s.Client.Upload(ctx, http.MethodPost, path, parts)
			if err != nil {
				return err
			}
			v, err := api.DecodeJSON(body)
			if err != nil {
				return err
			}
			var created []api.JSONObject
			switch v := v.(type) {
			case []any:
				for _, x := range v {
					if o, ok := x.(map[string]any); ok {
						created = append(created, o)
					}
				}
			case map[string]any:
				created = append(created, v)
			}
			a.info("Uploaded %d %s.", len(args), plural(len(args), "file", "files"))
			if len(created) == 0 {
				return nil
			}
			return pr.List(created, r.rows(created))
		},
	}
	cmd.Flags().StringVar(&contentType, "type", "", "MIME type of the files (default: guessed)")
	return cmd
}

func (a *App) newFileReplaceCommand(r *resource) *cobra.Command {
	return &cobra.Command{
		Use:               "replace <file> <path>",
		Short:             "Replace the content of a file",
		Long:              `Replace the content of a stored file with a local file of the same MIME type.`,
		Args:              usageArgs(cobra.ExactArgs(2)),
		ValidArgsFunction: a.completeResource(r),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			o, s, path, err := a.findResource(ctx, r, args[0], false)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(args[1])
			if err != nil {
				return err
			}
			t := str(o["mime"])
			if guessed := fileType(args[1], data); t != "" && !sameType(t, guessed) {
				return usageErrorf("%s looks like %s, but %s is %s; a file can only be replaced by one of the same type",
					args[1], guessed, r.label(o), t)
			}
			part := api.FilePart{Field: "filename", Filename: str(o["name"]), ContentType: t, Data: data}
			if _, _, err := s.Client.Upload(ctx, http.MethodPut, fmt.Sprintf("%s/%d", path, num(o, "id")), []api.FilePart{part}); err != nil {
				return err
			}
			a.info("Replaced %s.", r.label(o))
			return nil
		},
	}
}

// sameType compares MIME types without parameters such as charset.
func sameType(a, b string) bool {
	ma, _, _ := mime.ParseMediaType(a)
	mb, _, _ := mime.ParseMediaType(b)
	return strings.EqualFold(ma, mb)
}

func (a *App) newFileDownloadCommand(r *resource) *cobra.Command {
	var out string
	var force bool
	cmd := &cobra.Command{
		Use:   "download <file>",
		Short: "Download a file",
		Long: `Download a file. By default it is saved under its own name in the current
directory; use -O to choose another path, or -O - to write to stdout.`,
		Example: fmt.Sprintf(`  %[1]s file download floorplan
  %[1]s file download manual.pdf -O /tmp/manual.pdf
  %[1]s file download config.json -O - | jq .`, a.Name),
		Args:              usageArgs(cobra.ExactArgs(1)),
		ValidArgsFunction: a.completeResource(r),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			o, s, _, err := a.findResource(ctx, r, args[0], false)
			if err != nil {
				return err
			}
			hash := str(o["hash"])
			if hash == "" {
				return fmt.Errorf("%s has no hash, so it cannot be downloaded", r.label(o))
			}
			if out == "-" {
				return s.Client.Download(ctx, "/api/v2/file/download/"+url.PathEscape(hash), a.Out)
			}
			if out == "" {
				out = filepath.Base(str(o["name"]))
				if out == "" || out == "." || out == string(filepath.Separator) {
					out = hash
				}
			}
			return a.downloadTo(ctx, s, "/api/v2/file/download/"+url.PathEscape(hash), out, force, r.label(o))
		},
	}
	cmd.Flags().StringVarP(&out, "output-file", "O", "", `where to save the file ("-" for stdout)`)
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	return cmd
}

// downloadTo saves a download to path, refusing to overwrite unless force
// is set. A partial file is removed if the download fails.
func (a *App) downloadTo(ctx context.Context, s *session, apiPath, path string, force bool, label string) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if errors.Is(err, os.ErrExist) {
		return usageErrorf("%s already exists. Use --force to overwrite it, or -O to choose another path", path)
	}
	if err != nil {
		return err
	}
	err = s.Client.Download(ctx, apiPath, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return err
	}
	info, _ := os.Stat(path)
	size := ""
	if info != nil {
		size = " (" + humanSize(info.Size()) + ")"
	}
	a.info("Saved %s to %s%s.", label, path, size)
	return nil
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
