package cmd

import (
	"fmt"
	"slices"
	"strings"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/modelplace"
	"github.com/qoryai/qory/internal/module"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/ui"
)

// effortSource is where the effort value of a compose came from, for the error that stops
// the compose with exit status 2: the flag, or the key and the file that set it.
type effortSource struct {
	// key is --effort or harness.effort.
	key string
	// file is the file that set harness.effort, as a message prints it; "" for the flag.
	file string
}

// runEffort is the effort value the main agent of a compose runs at: --effort when it is
// given, else harness.effort, with where it came from, the file shortened against root.
func runEffort(flag string, conf config.Config, root string) (string, effortSource) {
	if flag != "" {
		return flag, effortSource{key: "--effort"}
	}
	return conf.Effort, effortSource{key: "harness.effort", file: exports.HidePath(ui.Short(conf.Origin("harness.effort"), root))}
}

// checkRunEffort returns an error for an effort value that a targeted runtime's
// descriptor does not list for a run, with the fix first and the values it lists, and a
// row for each targeted runtime whose descriptor has no place for the run's effort,
// which then runs without one.
func checkRunEffort(effort string, from effortSource, targets []render.Runtime) ([][2]string, error) {
	if effort == "" {
		return nil, nil
	}
	var rows [][2]string
	for _, rt := range targets {
		name := accesskey.ShownName(rt.Name(), false)
		place, ok := modelplace.RunEffort(rt.Name())
		if !ok {
			rows = append(rows, [2]string{"skipped", "effort " + shownEffort(effort) + " for " + name + "  (qory writes no effort for " + name + ")"})
			continue
		}
		if !slices.Contains(place.Values, effort) {
			err := fmt.Errorf("set %s to one of %s for %s; %s is not one", from.key, strings.Join(place.Values, ", "), name, shownEffort(effort))
			if from.file != "" {
				err = fmt.Errorf("%s: %w", from.file, err)
			}
			return nil, err
		}
	}
	return rows, nil
}

// shownEffort is an effort value as a message or a row prints it: bare, or quoted, with
// each control character escaped, when it has one, so it ends no line it is printed in;
// a value that contains a secret prints as [exports.Value] prints it.
func shownEffort(v string) string { return exports.Value(v, module.HasControl(v)) }

// withRunEffort adds to the launch of the runtime named name the effort the harness was
// composed with, in the place the runtime's descriptor lists: a flag and its value as two
// arguments before the rest, so a flag of harness.launch that comes later wins and a
// launch that ends in a flag that takes a value keeps that end; or a variable among the
// fixed ones, in place of a default of the same name. The value is one argument or one
// variable value as given, for an exec and never a shell. A runtime with no place for an
// effort is launched as it was.
func withRunEffort(l render.Launch, name, effort string) render.Launch {
	args, env := modelplace.Launch(name, effort)
	if args != nil {
		l.Args = append(args, l.Args...)
	}
	for _, kv := range env {
		variable, _, _ := strings.Cut(kv, "=")
		same := func(s string) bool { n, _, _ := strings.Cut(s, "="); return n == variable }
		l.Fixed = append(slices.DeleteFunc(slices.Clone(l.Fixed), same), kv)
		slices.Sort(l.Fixed)
		l.Defaults = slices.DeleteFunc(slices.Clone(l.Defaults), same)
	}
	return l
}

// effortDifference is the difference between the effort a compose would record, want,
// and the one the last compose recorded, have, at target/effort: "changed", "missing" or
// "extra". A launch passes the effort and no file of the home contains it, so --check
// compares the report.
func effortDifference(want, have string) []render.Difference {
	switch {
	case want == have:
		return nil
	case have == "":
		return []render.Difference{{Path: "target/effort", What: "missing"}}
	case want == "":
		return []render.Difference{{Path: "target/effort", What: "extra"}}
	}
	return []render.Difference{{Path: "target/effort", What: "changed"}}
}
