// Package resolve turns user supplied references (IDs or names) into objects.
//
// A reference is matched, case-insensitively, in this order:
//
//  1. Exact ID (or alternative ID such as an installation's client_id), if
//     the reference is numeric.
//  2. Exact name.
//  3. Unique substring of the name, unless Options.Exact is set.
package resolve

import (
	"fmt"
	"strconv"
	"strings"
)

// Key is what a reference is matched against.
type Key struct {
	ID int64
	// AltID is tried after ID for numeric references. Zero means none.
	AltID int64
	Name  string
}

// Options tune the matching.
type Options struct {
	// Exact disables substring matching. Used by destructive commands.
	Exact bool
	// NameOnly treats numeric references as names.
	NameOnly bool
}

// NotFoundError means nothing matched the reference.
type NotFoundError struct {
	Kind  string
	Ref   string
	Exact bool
}

func (e *NotFoundError) Error() string {
	if e.Exact {
		return fmt.Sprintf("no %s with ID or exact name %q", e.Kind, e.Ref)
	}
	return fmt.Sprintf("no %s matches %q", e.Kind, e.Ref)
}

// AmbiguousError means more than one object matched the reference.
type AmbiguousError struct {
	Kind    string
	Ref     string
	Matches []Key
}

func (e *AmbiguousError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%q matches %d %ss:\n", e.Ref, len(e.Matches), e.Kind)
	width := 0
	for _, m := range e.Matches {
		width = max(width, len(strconv.FormatInt(m.ID, 10)))
	}
	for _, m := range e.Matches {
		fmt.Fprintf(&b, "  %*d  %s\n", width, m.ID, m.Name)
	}
	b.WriteString("Use a more specific name or the ID.")
	return b.String()
}

// Find returns the single item that ref refers to. kind is used in error
// messages ("installation", "function", ...).
func Find[T any](kind, ref string, items []T, key func(T) Key, opts Options) (T, error) {
	var zero T
	ref = strings.TrimSpace(ref)

	if !opts.NameOnly {
		if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
			for _, it := range items {
				if key(it).ID == id {
					return it, nil
				}
			}
			for _, it := range items {
				if k := key(it); k.AltID != 0 && k.AltID == id {
					return it, nil
				}
			}
		}
	}

	if it, ok, err := single(kind, ref, items, key, func(name string) bool {
		return strings.EqualFold(name, ref)
	}); ok {
		return it, err
	}

	if !opts.Exact {
		needle := strings.ToLower(ref)
		if it, ok, err := single(kind, ref, items, key, func(name string) bool {
			return strings.Contains(strings.ToLower(name), needle)
		}); ok {
			return it, err
		}
	}

	return zero, &NotFoundError{Kind: kind, Ref: ref, Exact: opts.Exact}
}

// single applies match to the names of all items. ok is false if nothing
// matched. If more than one item matched, err is an AmbiguousError.
func single[T any](kind, ref string, items []T, key func(T) Key, match func(string) bool) (it T, ok bool, err error) {
	var matches []T
	var keys []Key
	for _, candidate := range items {
		if k := key(candidate); match(k.Name) {
			matches = append(matches, candidate)
			keys = append(keys, k)
		}
	}
	switch len(matches) {
	case 0:
		return it, false, nil
	case 1:
		return matches[0], true, nil
	}
	return it, true, &AmbiguousError{Kind: kind, Ref: ref, Matches: keys}
}
