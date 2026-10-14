package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// editorCommand returns $VISUAL, $EDITOR or the platform default, split into
// program and arguments.
func editorCommand() []string {
	for _, name := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return strings.Fields(v)
		}
	}
	if runtime.GOOS == "windows" {
		return []string{"notepad"}
	}
	return []string{"vi"}
}

func (a *App) runEditor(path string) error {
	if a.RunEditor != nil {
		return a.RunEditor(path)
	}
	argv := append(editorCommand(), path)
	cmd := exec.Command(argv[0], argv[1:]...)
	// The editor needs the real terminal, not whatever a.In/a.Out wrap.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running editor %q: %w", argv[0], err)
	}
	return nil
}

// errEditCancelled is returned by editLoop when nothing was changed.
var errEditCancelled = errors.New("edit cancelled")

// editLoop lets the user edit body (YAML) in their editor. header is shown
// as comments above it. apply receives the edited text without comments. If
// apply fails, the editor is opened again with the error at the top. Saving
// without changes cancels.
func (a *App) editLoop(header string, body []byte, apply func([]byte) error) error {
	f, err := os.CreateTemp("", "iotopenctl-*.yaml")
	if err != nil {
		return err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)

	content := []byte(commentLines(header) + string(body))
	var lastErr error
	for {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return err
		}
		if err := a.runEditor(path); err != nil {
			return err
		}
		edited, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Equal(edited, content) {
			if lastErr != nil {
				return lastErr
			}
			return errEditCancelled
		}
		stripped := stripComments(edited)
		if len(bytes.TrimSpace(stripped)) == 0 {
			return errEditCancelled
		}
		if err := apply(stripped); err != nil {
			lastErr = err
			content = []byte(commentLines("Error: "+err.Error()+"\nFix the error and save, or save without changes to give up.") +
				"#\n" + string(stripErrorHeader(edited)))
			continue
		}
		return nil
	}
}

func commentLines(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		b.WriteString(strings.TrimRight("# "+line, " ") + "\n")
	}
	return b.String()
}

// stripComments removes lines that are entirely comments.
func stripComments(data []byte) []byte {
	var out [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if bytes.HasPrefix(bytes.TrimSpace(line), []byte("#")) {
			continue
		}
		out = append(out, line)
	}
	return bytes.Join(out, []byte("\n"))
}

// stripErrorHeader removes a previous error block so errors do not pile up.
func stripErrorHeader(data []byte) []byte {
	if !bytes.HasPrefix(data, []byte("# Error: ")) {
		return data
	}
	if i := bytes.Index(data, []byte("#\n")); i >= 0 {
		return data[i+2:]
	}
	return data
}
