// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mrejas/lynx-cli/internal/api"
	"github.com/mrejas/lynx-cli/internal/config"
)

// completionTTL is how long lists fetched for shell completion are reused.
// Pressing Tab several times in a row then costs one API call, while new
// objects show up within a minute.
const completionTTL = time.Minute

type completeFunc = func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective)

// cacheDir returns where completion lists are cached: LYNX_CACHE_DIR, or
// the platform's user cache directory.
func (a *App) cacheDir() string {
	if a.CacheDir != "" {
		return a.CacheDir
	}
	if dir := config.Getenv("CACHE_DIR"); dir != "" {
		return dir
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, config.AppName)
}

type cacheEntry[T any] struct {
	Expires time.Time `json:"expires"`
	Data    T         `json:"data"`
}

// cached returns the value stored under key if it has not expired, and
// otherwise calls fetch and stores the result. Cache errors are ignored:
// the cache only makes completion faster.
func cached[T any](a *App, key string, fetch func() (T, error)) (T, error) {
	dir := a.cacheDir()
	if dir == "" {
		return fetch()
	}
	sum := sha256.Sum256([]byte(key))
	path := filepath.Join(dir, "completion-"+hex.EncodeToString(sum[:8])+".json")

	var entry cacheEntry[T]
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &entry) == nil && time.Now().Before(entry.Expires) {
		return entry.Data, nil
	}
	value, err := fetch()
	if err != nil {
		return value, err
	}
	if data, err := json.Marshal(cacheEntry[T]{Expires: time.Now().Add(completionTTL), Data: value}); err == nil {
		if os.MkdirAll(dir, 0o700) == nil {
			_ = os.WriteFile(path, data, 0o600)
		}
	}
	return value, nil
}

// completionItem is what completion needs to know about an object.
type completionItem struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Info string `json:"info"`
	Type string `json:"type,omitempty"`
}

func (it completionItem) candidate() string {
	if it.Name == "" {
		return fmt.Sprintf("%d\t%s", it.ID, it.Info)
	}
	return it.Name + "\t" + it.Info
}

// candidates turns items into completions, leaving out those already given
// as arguments.
func candidates(items []completionItem, args []string) []string {
	var out []string
	for _, it := range items {
		given := false
		for _, arg := range args {
			given = given || strings.EqualFold(arg, it.Name) || arg == fmt.Sprint(it.ID)
		}
		if !given {
			out = append(out, it.candidate())
		}
	}
	return out
}

func (a *App) installationItems(ctx context.Context) ([]completionItem, error) {
	s, err := a.session()
	if err != nil {
		return nil, err
	}
	return cached(a, "installations\x00"+s.ProfileName+"\x00"+s.URL, func() ([]completionItem, error) {
		list, err := s.Client.InstallationInfos(ctx)
		if err != nil {
			return nil, err
		}
		if all, err := s.Client.ListInstallations(ctx, nil); err == nil {
			list = mergeInstallations(list, all)
		}
		items := make([]completionItem, len(list))
		for i, inst := range list {
			items[i] = completionItem{ID: inst.ID, Name: inst.Name, Info: fmt.Sprintf("ID %d, client ID %d", inst.ID, inst.ClientID)}
		}
		return items, nil
	})
}

func (a *App) objectItems(ctx context.Context, t objectType) ([]completionItem, error) {
	sc, err := a.scope(ctx)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%d", t.kind, sc.ProfileName, sc.URL, sc.installation)
	return cached(a, key, func() ([]completionItem, error) {
		list, err := sc.Client.ListObjects(ctx, t.kind, sc.installation, nil)
		if err != nil {
			return nil, err
		}
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		items := make([]completionItem, len(list))
		for i, o := range list {
			items[i] = completionItem{ID: o.ID, Name: o.Name(), Info: fmt.Sprintf("ID %d, %s", o.ID, o.Type), Type: o.Type}
		}
		return items, nil
	})
}

func (a *App) completeInstallations(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	items, err := a.installationItems(cmd.Context())
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	return candidates(items, nil), cobra.ShellCompDirectiveNoFileComp
}

func (a *App) completeInstallationArg(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return a.completeInstallations(cmd, args, toComplete)
}

// completeObjects completes device or function names in the current
// installation, for every argument.
func (a *App) completeObjects(t objectType) completeFunc {
	return func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		items, err := a.objectItems(cmd.Context(), t)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		return candidates(items, args), cobra.ShellCompDirectiveNoFileComp
	}
}

// completeObjectArg completes the first positional argument only.
func (a *App) completeObjectArg(t objectType) completeFunc {
	complete := a.completeObjects(t)
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return complete(cmd, args, toComplete)
	}
}

// completeTypes completes the types already used by devices or functions.
func (a *App) completeTypes(t objectType) completeFunc {
	return func(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		items, err := a.objectItems(cmd.Context(), t)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		seen := map[string]bool{}
		var types []string
		for _, it := range items {
			if it.Type != "" && !seen[it.Type] {
				seen[it.Type] = true
				types = append(types, it.Type)
			}
		}
		sort.Strings(types)
		return types, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeMeta completes the object (if the target takes one) and then the
// meta keys of that object. For "set", keys are completed as "key=".
func (a *App) completeMeta(t metaTarget, mode string) completeFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if t.withRef && len(args) == 0 {
			if t.complete == nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return t.complete(cmd, args, toComplete)
		}
		ref, given := t.split(args)
		if mode == "get" && len(given) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		if mode == "set" && strings.Contains(toComplete, "=") {
			// Completing a value; nothing sensible to offer.
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		obj, err := t.load(cmd.Context(), ref)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		done := map[string]bool{}
		for _, g := range given {
			k, _, _ := strings.Cut(g, "=")
			done[k] = true
		}
		var keys []string
		for _, m := range []api.Meta{obj.Meta, obj.Protected} {
			for k, v := range m {
				if done[k] {
					continue
				}
				if mode == "set" {
					keys = append(keys, k+"=\tcurrently "+v)
				} else {
					keys = append(keys, k+"\t"+v)
				}
			}
		}
		sort.Strings(keys)
		if mode == "set" {
			return keys, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
		}
		return keys, cobra.ShellCompDirectiveNoFileComp
	}
}
