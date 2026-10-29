package app

import (
	"fmt"
	"os"

	"github.com/Hudint/claudesync/internal/jsonx"
)

// MergeJSON is the git merge driver for settings layers: it merges the
// changes from base to ours and base to theirs and writes the result to ours.
// During claudesync's rebase, "theirs" is the local commit being replayed, so
// local changes win when both sides change the same value.
func MergeJSON(base, ours, theirs string) error {
	read := func(p string) (any, error) {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		v, err := jsonx.Parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		return v, nil
	}
	o, err := read(base)
	if err != nil {
		return err
	}
	a, err := read(ours)
	if err != nil {
		return err
	}
	b, err := read(theirs)
	if err != nil {
		return err
	}
	return os.WriteFile(ours, jsonx.Marshal(jsonx.Merge3(o, a, b)), 0o644)
}
