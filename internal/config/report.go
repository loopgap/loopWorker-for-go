package config

import (
	"fmt"
	"sort"
	"strings"
)

// Entry is one resolved configuration value together with the layer it came from.
type Entry struct {
	Key    string
	Value  string
	Source string
	Secret bool
}

// Redacted is the value safe to log or print: secrets never leave the process.
func (e Entry) Redacted() string {
	if !e.Secret {
		return e.Value
	}
	if e.Value == "" {
		return "(unset)"
	}
	return fmt.Sprintf("(set, %d chars, redacted)", len(e.Value))
}

// Resolved lists every configuration key with its effective value and source.
func (c *Config) Resolved() []Entry {
	entries := make([]Entry, 0, len(specs()))
	for _, sp := range specs() {
		entries = append(entries, Entry{
			Key:    sp.path,
			Value:  sp.get(c),
			Source: c.SourceOf(sp.path),
			Secret: sp.secret,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return entries
}

// Summary renders the resolved configuration for the startup log and doctor dump.
func (c *Config) Summary() string {
	var b strings.Builder
	for _, e := range c.Resolved() {
		fmt.Fprintf(&b, "%-36s = %-24s (%s)\n", e.Key, e.Redacted(), e.Source)
	}
	return b.String()
}
