// Package modelplace reads where a runtime takes an effort value, the run's and an
// agent's, from the model section of the runtime's Forager descriptor, with the values the
// runtime accepts there.
package modelplace

import (
	"slices"
	"sync"

	"github.com/qoryai/forager/session/runtimes"
	"github.com/qoryai/forager/session/runtimes/catalog"
)

// section is one runtime's model section as [Of] returns it.
type section struct {
	places runtimes.ModelPlaces
	ok     bool
}

// read keeps the model section of each runtime [Of] has read. The descriptors Forager
// ships are compiled into qory and do not change while it runs, and reading one takes
// about 4 ms, so a compose that asks several times reads each once.
var read = struct {
	sync.Mutex
	by map[string]section
}{by: map[string]section{}}

// Of is the model section of the runtime named name, from the descriptor Forager ships
// for it, and true; the zero ModelPlaces and false for a runtime without one, and for a
// name Forager does not take. The caller may change the lists it returns: each call
// returns a copy.
func Of(name string) (runtimes.ModelPlaces, bool) {
	read.Lock()
	defer read.Unlock()
	s, done := read.by[name]
	if !done {
		s = lookup(name)
		read.by[name] = s
	}
	return clone(s.places), s.ok
}

// lookup reads the model section of the runtime named name from Forager's catalog.
func lookup(name string) section {
	rt, err := catalog.Lookup(name, "")
	if err != nil {
		return section{}
	}
	m, ok := rt.(runtimes.Model)
	if !ok {
		return section{}
	}
	p, ok := m.Model()
	return section{places: p, ok: ok}
}

// clone is p with each list it contains copied.
func clone(p runtimes.ModelPlaces) runtimes.ModelPlaces {
	p.Formats, p.Keys, p.Overrides = slices.Clone(p.Formats), slices.Clone(p.Keys), slices.Clone(p.Overrides)
	if p.Effort != nil {
		e := *p.Effort
		e.Run.Values = slices.Clone(e.Run.Values)
		if e.Agent != nil {
			a := *e.Agent
			a.Values = slices.Clone(a.Values)
			e.Agent = &a
		}
		p.Effort = &e
	}
	return p
}

// RunEffort is where the runtime named name takes the run's effort value, a flag or a
// variable, with the values it accepts there, and true; false when its descriptor has no
// such place. A place that is a settings key counts as none: qory writes no settings key
// for the effort.
func RunEffort(name string) (runtimes.ValuedPlace, bool) {
	p, ok := Of(name)
	if !ok || p.Effort == nil {
		return runtimes.ValuedPlace{}, false
	}
	run := p.Effort.Run
	if run.Flag == "" && run.Variable == "" {
		return runtimes.ValuedPlace{}, false
	}
	return run, true
}

// AgentEffort is the frontmatter key an agent's file of the runtime named name takes its
// effort value in, with the values it accepts there, and true; false when its descriptor
// has no place for an agent's effort.
func AgentEffort(name string) (runtimes.AgentPlace, bool) {
	p, ok := Of(name)
	if !ok || p.Effort == nil || p.Effort.Agent == nil || p.Effort.Agent.Frontmatter == "" {
		return runtimes.AgentPlace{}, false
	}
	return *p.Effort.Agent, true
}

// Launch is what starts the runtime named name at the effort value: the flag and the
// value as two arguments, or NAME=value for a variable, the value as given, for an exec
// and never for a shell. Both are nil for an empty effort and for a runtime with no place
// for one.
func Launch(name, effort string) (args, env []string) {
	place, ok := RunEffort(name)
	if !ok || effort == "" {
		return nil, nil
	}
	if place.Flag != "" {
		return []string{place.Flag, effort}, nil
	}
	return nil, []string{place.Variable + "=" + effort}
}
