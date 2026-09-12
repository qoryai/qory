package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/ui"
)

// Version is the report format version a [New] report carries.
const Version = 1

// Module is one composed module.
type Module struct {
	// Name is the module's name as its manifest declares it, the name excludes and entries
	// use.
	Name string `json:"name"`
	// Description is the module's description from its manifest, absent when it has none.
	Description string `json:"description,omitempty"`
	// Source is the stack's source as text: the path of a path source, <git>#<ref> for a
	// git source, either followed by " module <name>" for an export.
	Source string `json:"source"`
	// Pin is what the source resolved to: "working-tree" for a path, the commit for a git
	// source.
	Pin string `json:"pin"`
	// Dirty is set when git saw uncommitted changes under the module's directory.
	Dirty bool `json:"dirty,omitempty"`
	// Variant is the variant chosen for the target runtime, absent for a module without one.
	Variant string `json:"variant,omitempty"`
	// Link is the checkout-root name that links to the module's directory, absent when the
	// stack names none.
	Link string `json:"link,omitempty"`
	// Base marks a module of the base stack, when a checkout's qory.yaml extends one.
	Base bool `json:"base,omitempty"`
}

// Base is the stack a checkout's qory.yaml extends.
type Base struct {
	// Name is the base stack's name.
	Name string `json:"name"`
	// Source is the extends source as the stack writes it.
	Source string `json:"source"`
	// Pin is what the source resolved to: the commit for a git source, "working-tree" for
	// a path.
	Pin string `json:"pin"`
}

// Build is the qory that wrote the report, so two reports of one checkout, from a
// runner and a laptop say, show whether the same qory composed them.
type Build struct {
	// Version is the version without a leading v: the release, the tag of a source build
	// at that tag, or the pseudo-version of a source build between tags.
	Version string `json:"version"`
	// Commit is the commit the binary was built from, "" when the build carries none.
	Commit string `json:"commit,omitempty"`
	// Source is "release" for a release build and "source" for a go install or go build.
	Source string `json:"source"`
}

// Entry is one composed entry.
type Entry struct {
	// Kind is one of skills, agents, commands, output-styles, hooks, mcp and files.
	Kind string `json:"kind"`
	// Name is the entry's name within its kind.
	Name string `json:"name"`
	// Module is the name of the module that provides the entry.
	Module string `json:"module"`
	// For is the entry, as <kind>/<name>, that required this one, when the module's only
	// block brought it in for that entry rather than naming it; absent otherwise.
	For string `json:"for,omitempty"`
}

// Exclude is one excluded entry.
type Exclude struct {
	// Module is the module the entry was dropped from.
	Module string `json:"module"`
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
	// Runtimes are the programs the home holds after the compose, such as claude or
	// codex: the ones the compose targeted first, in the order the stack names them,
	// then the ones composed earlier and refreshed. The field is always an array, of one
	// name when the home holds one.
	Runtimes Runtimes `json:"runtime"`
	// Model is the model written into the runtime's settings, absent when the stack
	// names none.
	Model string `json:"model,omitempty"`
}

// Report is one report: the stack and its target, the checkout and home written into, the
// modules with their pins, the entries with their modules, and the excludes.
type Report struct {
	// Version is the format version, the Version constant for a report New built.
	Version int `json:"version"`
	// Stack is the stack's name, from the stack or from the checkout's remote.
	Stack string `json:"stack"`
	// Description is the stack's description, absent when it has none.
	Description string `json:"description,omitempty"`
	// File is the absolute path of the stack file that was composed.
	File string `json:"file"`
	// Target is the runtime and model the harness was rendered for.
	Target Target `json:"target"`
	// Checkout is the absolute path of the git working tree composed into.
	Checkout string `json:"checkout"`
	// Home is the absolute path of the composed tree inside the checkout.
	Home string `json:"home"`
	// Modules are the stack's modules in order.
	Modules []Module `json:"modules"`
	// Entries are the composed entries, sorted by kind then name.
	Entries []Entry `json:"entries"`
	// Excludes are the entries the modules left out, in module order.
	Excludes []Exclude `json:"excludes"`
	// Replaced are the checkout paths whose tracked file or directory the compose removed
	// under --force to put a link there, absent when it replaced none. git checkout --
	// restores each of them, and qory harness remove says so.
	Replaced []string `json:"replaced,omitempty"`
	// Env are the variables the harness exports, name to value with $QORY_HARNESS_HOME in
	// place of the home, absent when it exports none.
	Env map[string]string `json:"env,omitempty"`
	// Extensions are the stack's extensions, carried as written, absent when it has
	// none.
	Extensions map[string]map[string]any `json:"extensions,omitempty"`
	// Base is the stack the checkout's qory.yaml extends, absent for a stack.
	Base *Base `json:"base,omitempty"`
	// Qory is the build that wrote the report, absent when the build carries no version.
	// The command sets it after [New], which knows nothing about the binary.
	Qory *Build `json:"qory,omitempty"`
}

// New builds the report of a result rendered into home for a checkout. The name is the
// stack's name, which the caller resolves, because a stack without one is named after
// the checkout. Modules, entries and excludes come out as empty arrays rather than null;
// env and extensions are left out when empty.
func New(res *compose.Result, name, checkout, home string) Report {
	r := Report{
		Version:     Version,
		Stack:       name,
		Description: res.Stack.Description,
		File:        res.Stack.File,
		Target:      Target{Runtimes: Runtimes(res.Stack.Target.Runtimes), Model: res.Stack.Target.Model},
		Checkout:    checkout,
		Home:        home,
		Modules:     []Module{},
		Entries:     []Entry{},
		Excludes:    []Exclude{},
		Extensions:  res.Stack.Extensions,
	}
	if len(res.Env) > 0 {
		r.Env = res.Env
	}
	if res.Base != nil {
		r.Base = &Base{Name: res.Base.Name, Source: res.Base.Source, Pin: res.Base.Pin}
	}
	for _, l := range res.Modules {
		r.Modules = append(r.Modules, Module{Name: l.Name, Description: l.Description, Source: l.Source, Pin: l.Pin, Dirty: l.Dirty, Variant: l.Variant, Link: l.Link, Base: l.Base})
	}
	for _, e := range res.Entries {
		r.Entries = append(r.Entries, Entry{Kind: e.Kind, Name: e.Name, Module: e.Module, For: e.For})
	}
	for _, x := range res.Excludes {
		r.Excludes = append(r.Excludes, Exclude{Module: x.Module, Kind: x.Kind, Name: x.Name})
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

// Print writes the report for a person: the stack and target, the paths, then the modules,
// the entries and the excludes as tables, with the excludes left out when there are none.
// Paths are shortened against the checkout. Print returns nil; the error result is there for
// the callers that check one.
func (r Report) Print(w io.Writer) error {
	u := ui.New(w)
	u.Title(r.Stack, strings.TrimSpace(strings.Join(r.Target.Runtimes, ", ")+" "+r.Target.Model))
	return r.PrintBody(w)
}

// PrintBody writes everything [Report.Print] writes except the title line, for a caller that
// has printed a title of its own.
func (r Report) PrintBody(w io.Writer) error {
	u := ui.New(w)
	fields := [][2]string{{"file", ui.Short(r.File, r.Checkout)}}
	if r.Description != "" {
		fields = append(fields, [2]string{"about", r.Description})
	}
	if r.Base != nil {
		fields = append(fields, [2]string{"extends", r.Base.Name + "  " + r.Base.Source + "  " + r.Base.Pin})
	}
	fields = append(fields, [2]string{"checkout", ui.Short(r.Checkout, "")}, [2]string{"home", ui.Short(r.Home, r.Checkout)})
	if r.Qory != nil {
		fields = append(fields, [2]string{"qory", strings.TrimSpace(r.Qory.Version + "  " + r.Qory.Commit + "  " + r.Qory.Source)})
	}
	u.Fields(fields)
	u.Blank()
	u.Heading("Modules")
	var rows [][]string
	for _, l := range r.Modules {
		pin := l.Pin
		if l.Dirty {
			pin += " (dirty)"
		}
		row := []string{l.Name, l.Source, pin}
		if l.Variant != "" {
			row = append(row, "variant "+l.Variant)
		}
		if l.Link != "" {
			row = append(row, "linked as "+l.Link)
		}
		if l.Base {
			row = append(row, "base")
		}
		if l.Description != "" {
			row = append(row, l.Description)
		}
		rows = append(rows, row)
	}
	u.Table(rows)
	u.Blank()
	u.Heading("Entries")
	rows = nil
	for _, e := range r.Entries {
		row := []string{e.Kind + "/" + e.Name, e.Module}
		if e.For != "" {
			row = append(row, "required by "+e.For)
		}
		rows = append(rows, row)
	}
	u.Table(rows)
	if len(r.Excludes) > 0 {
		u.Blank()
		u.Heading("Excludes")
		rows = nil
		for _, x := range r.Excludes {
			rows = append(rows, []string{x.Module, x.Kind + "/" + x.Name})
		}
		u.Table(rows)
	}
	if len(r.Env) > 0 {
		u.Blank()
		u.Heading("Env")
		rows = nil
		for _, name := range sorted(r.Env) {
			rows = append(rows, []string{name, r.Env[name]})
		}
		u.Table(rows)
	}
	if len(r.Extensions) > 0 {
		u.Blank()
		u.Heading("Extensions")
		rows = nil
		for _, ns := range sorted(r.Extensions) {
			for _, key := range sorted(r.Extensions[ns]) {
				value, _ := json.Marshal(r.Extensions[ns][key])
				rows = append(rows, []string{ns + "." + key, string(value)})
			}
		}
		u.Table(rows)
	}
	return nil
}

// sorted lists a map's keys in order, so the printed rows do not follow map order.
func sorted[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
