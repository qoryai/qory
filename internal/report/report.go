package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/ui"
)

// Version is the report format version a [New] report carries.
const Version = 1

// Layer is one composed layer.
type Layer struct {
	// Name is the profile's name for the layer, the name excludes and entries use.
	Name string `json:"name"`
	// ManifestName is the layer's own name from harness.yaml, absent when it has none.
	ManifestName string `json:"manifest_name,omitempty"`
	// Source is the profile's source as text: the path of a path source, <git>#<ref> for a
	// git source.
	Source string `json:"source"`
	// Pin is what the source resolved to: "working-tree" for a path, the commit for a git
	// source.
	Pin string `json:"pin"`
	// Dirty is set when git saw uncommitted changes under the layer's directory.
	Dirty bool `json:"dirty,omitempty"`
	// Variant is the variant chosen for the target runtime, absent for a layer without one.
	Variant string `json:"variant,omitempty"`
}

// Entry is one composed entry.
type Entry struct {
	// Kind is one of skills, agents, commands, output-styles, hooks, mcp.
	Kind string `json:"kind"`
	// Name is the entry's name within its kind.
	Name string `json:"name"`
	// Layer is the name of the layer that provides the entry.
	Layer string `json:"layer"`
}

// Exclude is one excluded entry.
type Exclude struct {
	// Layer is the layer the entry was dropped from.
	Layer string `json:"layer"`
	// Kind is the entry kind the exclude named.
	Kind string `json:"kind"`
	// Name is the dropped entry's name.
	Name string `json:"name"`
}

// Runtimes are the runtime names of a target. It reads a JSON string as well as an array,
// so a report written by a qory that targeted one runtime, before a target could name
// several, still prints.
type Runtimes []string

// UnmarshalJSON accepts one name or an array of names.
func (r *Runtimes) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*r = Runtimes{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("target.runtime is one runtime name or a list of them: %w", err)
	}
	*r = many
	return nil
}

// Target is the runtimes and the model the harness was rendered for.
type Target struct {
	// Runtimes are the programs the harness was rendered for, such as claude or codex,
	// in the order the profile names them. The field is always an array, of one name
	// when the profile names one.
	Runtimes Runtimes `json:"runtime"`
	// Model is the model written into the runtime's settings, absent when the profile
	// names none.
	Model string `json:"model,omitempty"`
}

// Report is one report: the profile and its target, the checkout and home written into, the
// layers with their pins, the entries with their layers, and the excludes.
type Report struct {
	// Version is the format version, the Version constant for a report New built.
	Version int `json:"version"`
	// Profile is the profile's name, from the profile or from the checkout's remote.
	Profile string `json:"profile"`
	// File is the absolute path of the profile file that was composed.
	File string `json:"file"`
	// Target is the runtime and model the harness was rendered for.
	Target Target `json:"target"`
	// Checkout is the absolute path of the git working tree composed into.
	Checkout string `json:"checkout"`
	// Home is the absolute path of the composed tree inside the checkout.
	Home string `json:"home"`
	// Layers are the profile's layers in order.
	Layers []Layer `json:"layers"`
	// Entries are the composed entries, sorted by kind then name.
	Entries []Entry `json:"entries"`
	// Excludes are the entries the layers left out, in layer order.
	Excludes []Exclude `json:"excludes"`
	// Replaced are the checkout paths whose tracked file or directory the compose removed
	// under --force to put a link there, absent when it replaced none. git checkout --
	// restores each of them, and qory harness remove says so.
	Replaced []string `json:"replaced,omitempty"`
}

// New builds the report of a result rendered into home for a checkout. The name is the
// profile's name, which the caller resolves, because a profile without one is named after
// the checkout. Layers, entries and excludes come out as empty arrays rather than null.
func New(res *compose.Result, name, checkout, home string) Report {
	r := Report{
		Version:  Version,
		Profile:  name,
		File:     res.Profile.File,
		Target:   Target{Runtimes: Runtimes(res.Profile.Target.Runtimes), Model: res.Profile.Target.Model},
		Checkout: checkout,
		Home:     home,
		Layers:   []Layer{},
		Entries:  []Entry{},
		Excludes: []Exclude{},
	}
	for _, l := range res.Layers {
		r.Layers = append(r.Layers, Layer{Name: l.Name, ManifestName: l.ManifestName, Source: l.Source, Pin: l.Pin, Dirty: l.Dirty, Variant: l.Variant})
	}
	for _, e := range res.Entries {
		r.Entries = append(r.Entries, Entry{Kind: e.Kind, Name: e.Name, Layer: e.Layer})
	}
	for _, x := range res.Excludes {
		r.Excludes = append(r.Excludes, Exclude{Layer: x.Layer, Kind: x.Kind, Name: x.Name})
	}
	return r
}

// Write stores the report as indented JSON with a trailing newline, creating the parent
// directories. It overwrites a report already at path.
func Write(path string, r Report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Read loads a stored report. It does not check the version, so a caller that reads only one
// version compares [Report.Version] itself. A missing file returns the os error, which a
// caller matches with errors.Is and [os.ErrNotExist].
func Read(path string) (Report, error) {
	var r Report
	data, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

// Print writes the report for a person: the profile and target, the paths, then the layers,
// the entries and the excludes as tables, with the excludes left out when there are none.
// Paths are shortened against the checkout. Print returns nil; the error result is there for
// the callers that check one.
func (r Report) Print(w io.Writer) error {
	u := ui.New(w)
	u.Title(r.Profile, strings.TrimSpace(strings.Join(r.Target.Runtimes, ", ")+" "+r.Target.Model))
	return r.PrintBody(w)
}

// PrintBody writes everything [Report.Print] writes except the title line, for a caller that
// has printed a title of its own.
func (r Report) PrintBody(w io.Writer) error {
	u := ui.New(w)
	u.Fields([][2]string{
		{"file", ui.Short(r.File, r.Checkout)},
		{"checkout", ui.Short(r.Checkout, "")},
		{"home", ui.Short(r.Home, r.Checkout)},
	})
	u.Blank()
	u.Heading("Layers")
	var rows [][]string
	for _, l := range r.Layers {
		pin := l.Pin
		if l.Dirty {
			pin += " (dirty)"
		}
		row := []string{l.Name, l.Source, pin}
		if l.ManifestName != "" && l.ManifestName != l.Name {
			row = append(row, "named "+l.ManifestName)
		}
		if l.Variant != "" {
			row = append(row, "variant "+l.Variant)
		}
		rows = append(rows, row)
	}
	u.Table(rows)
	u.Blank()
	u.Heading("Entries")
	rows = nil
	for _, e := range r.Entries {
		rows = append(rows, []string{e.Kind + "/" + e.Name, e.Layer})
	}
	u.Table(rows)
	if len(r.Excludes) > 0 {
		u.Blank()
		u.Heading("Excludes")
		rows = nil
		for _, x := range r.Excludes {
			rows = append(rows, []string{x.Layer, x.Kind + "/" + x.Name})
		}
		u.Table(rows)
	}
	return nil
}
