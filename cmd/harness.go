package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/profile"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/report"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/ui"

	// Blank imports for their side effect: each runtime package registers itself with the
	// render package in its own init, and this list is therefore the set of runtimes this
	// build can render for. A new runtime package is reachable once it is imported here.
	_ "github.com/qoryai/qory/internal/render/amp"
	_ "github.com/qoryai/qory/internal/render/any"
	_ "github.com/qoryai/qory/internal/render/claude"
	_ "github.com/qoryai/qory/internal/render/codex"
	_ "github.com/qoryai/qory/internal/render/copilot"
	_ "github.com/qoryai/qory/internal/render/cursor"
	_ "github.com/qoryai/qory/internal/render/gemini"
	_ "github.com/qoryai/qory/internal/render/goose"
	_ "github.com/qoryai/qory/internal/render/opencode"
)

// newHarness is the harness noun. It holds no behaviour of its own; cobra prints its
// subcommands when it is run without one.
func newHarness() *cobra.Command {
	harness := &cobra.Command{
		Use:     "harness",
		Aliases: []string{"h"},
		Short:   "Write an example, then compose, inspect and remove the harness of a checkout",
	}
	harness.AddCommand(newInit("init"), newCompose("compose", "c"), newInspect("inspect", "i"), newRemove("remove", "r"))
	return harness
}

// shortcuts builds the top-level hc, hi and hr commands. They are the same constructors as
// the harness verbs, so the two names cannot drift apart, and they are hidden from help
// because the harness noun is where the tool is meant to be read.
func shortcuts() []*cobra.Command {
	var cmds []*cobra.Command
	for _, c := range []*cobra.Command{newCompose("hc"), newInspect("hi"), newRemove("hr")} {
		c.Hidden = true
		cmds = append(cmds, c)
	}
	return cmds
}

// places holds the four paths every harness verb works with, all absolute.
type places struct {
	root   string // the root of the checkout the command is standing in
	dir    string // the .qory directory in it, which the tool owns entirely
	home   string // the composed tree, .qory/harness
	report string // the report of the last compose, .qory/harness-report.json
}

// locate finds the checkout the process is standing in and derives its places. It fails
// outside a git working tree, which is deliberate: the tool writes into a checkout and
// links from it, and has nothing to write to without one.
func locate() (places, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return places{}, err
	}
	root, err := checkout.Root(cwd)
	if err != nil {
		return places{}, err
	}
	dir := checkout.QoryDir(root)
	return places{root: root, dir: dir, home: filepath.Join(dir, "harness"), report: filepath.Join(dir, "harness-report.json")}, nil
}

// newCompose builds the compose verb under the given name and aliases.
//
// The order of the run matters and is this: locate the checkout, discover or load the
// profile, apply the --runtime and --model overrides on top of it, look the runtime up
// before doing any work so an unknown name fails early, print the title, compose, and only
// then touch the disk. Nothing is written before the compose succeeds, so a collision or
// an unreadable layer leaves the checkout exactly as it was.
//
// Writing is three steps in a fixed order: [render.Build] renders the home tree,
// [render.LinkInto] links it into the checkout, and [report.Write] records what happened.
// The report is written last, so a report on disk means the harness beside it is complete.
// A --dry-run stops after the report is built and prints it instead. --force lets a link
// replace a tracked, unmodified file of the checkout, and the report records every path it
// replaced so remove can say how to get it back. --update fetches every git source again.
func newCompose(use string, aliases ...string) *cobra.Command {
	var file, runtime, model string
	var dryRun, verbose, force, update bool
	c := &cobra.Command{
		Use:     use,
		Aliases: aliases,
		Short:   "Compose the profile's layers into the checkout you stand in",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			at, err := locate()
			if err != nil {
				return err
			}
			if file == "" {
				if file, err = profile.Discover(at.root); err != nil {
					return input(err)
				}
			}
			p, err := profile.Load(file)
			if err != nil {
				return input(err)
			}
			if runtime != "" {
				p.Target.Runtimes = profile.Runtimes(strings.Split(runtime, ","))
				for i, r := range p.Target.Runtimes {
					p.Target.Runtimes[i] = strings.TrimSpace(r)
				}
			}
			if model != "" {
				p.Target.Model = model
			}
			if err := p.Target.Runtimes.Validate(); err != nil {
				return input(err)
			}
			// Every targeted runtime is looked up before anything is composed, so an
			// unknown name fails before the disk is touched.
			var targets []render.Runtime
			for _, name := range p.Target.Runtimes {
				rt, err := render.Lookup(name)
				if err != nil {
					return input(err)
				}
				targets = append(targets, rt)
			}
			name := p.Name
			if name == "" {
				name = checkout.RepoKey(at.root)
			}
			out := cmd.OutOrStdout()
			u := ui.New(out)
			u.Title(name, strings.TrimSpace(p.Target.Runtimes.String()+" "+p.Target.Model))
			res, err := compose.ComposeWith(p, compose.Options{Update: update})
			var collision *compose.CollisionError
			var fetch *source.FetchError
			switch {
			case errors.As(err, &collision):
				printCollision(u, p, collision)
				return reported(err)
			case errors.As(err, &fetch):
				return err
			case err != nil:
				return input(err)
			}
			rep := report.New(res, name, at.root, at.home)
			if dryRun {
				if err := rep.PrintBody(out); err != nil {
					return err
				}
				u.Blank()
				u.Success("dry run: nothing written")
				return nil
			}
			// A checkout can be composed for several runtimes at once, and can have been
			// composed for others before. The home is one tree that a build replaces
			// whole, so every runtime it is to hold goes into one call: the targets and
			// the runtimes already there, whose links would otherwise stop resolving.
			targeted := map[string]bool{}
			for _, rt := range targets {
				targeted[rt.Name()] = true
			}
			var also []render.Runtime
			for _, other := range render.Composed(at.home) {
				if !targeted[other.Name()] {
					also = append(also, other)
				}
			}
			all := append(append([]render.Runtime{}, targets...), also...)
			if err := render.Build(res, at.home, all...); err != nil {
				return err
			}
			// A path replaced by an earlier compose is still replaced: the report keeps
			// naming it until remove takes its link, so the restore hint is not lost to a
			// second compose.
			if previous, err := report.Read(at.report); err == nil {
				rep.Replaced = previous.Replaced
			}
			linked := map[string]render.Linked{}
			for _, rt := range all {
				l, err := render.LinkInto(rt, res, at.root, at.home, force)
				if err != nil {
					return err
				}
				linked[rt.Name()] = l
				for _, path := range l.Replaced {
					if !slices.Contains(rep.Replaced, path) {
						rep.Replaced = append(rep.Replaced, path)
					}
				}
			}
			if err := report.Write(at.report, rep); err != nil {
				return err
			}
			if verbose {
				var rows [][]string
				for _, e := range rep.Entries {
					rows = append(rows, []string{e.Kind + "/" + e.Name, e.Layer})
				}
				u.Table(rows)
			}
			u.Success("composed %s from %s %s", count(len(rep.Entries), "entry", "entries"), count(len(rep.Layers), "layer", "layers"), ui.Pot)
			rows := [][2]string{{"home", ui.Short(at.home, at.root)}}
			for _, rt := range all {
				var links []string
				for _, l := range rt.Links(res) {
					if slices.Contains(linked[rt.Name()].Skipped, l.Checkout) {
						continue
					}
					links = append(links, l.Checkout)
				}
				row := strings.Join(links, "  ")
				if slices.Contains(also, rt) {
					row += "  (composed here earlier, refreshed)"
				}
				rows = append(rows, [2]string{rt.Name(), row})
				if kept := linked[rt.Name()].Skipped; len(kept) > 0 {
					rows = append(rows, [2]string{"kept", strings.Join(kept, "  ") + "  (the checkout's own; not linked)"})
				}
				if replaced := linked[rt.Name()].Replaced; len(replaced) > 0 {
					rows = append(rows, [2]string{"replaced", strings.Join(replaced, "  ") + "  (the checkout's own; git checkout -- restores it)"})
				}
				for _, s := range render.Skipped(rt, res) {
					rows = append(rows, [2]string{"skipped", s + "  (no place in " + rt.Name() + ")"})
				}
			}
			u.Fields(rows)
			return nil
		},
	}
	c.Flags().StringVarP(&file, "file", "f", "", "the profile to read instead of discovering one")
	c.Flags().StringVar(&runtime, "runtime", "", "render for these runtimes instead of target.runtime, comma separated ("+strings.Join(render.Names(), ", ")+")")
	c.Flags().StringVar(&model, "model", "", "write this model instead of target.model")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print the report and write nothing")
	c.Flags().BoolVar(&force, "force", false, "replace a tracked, unmodified file of the checkout where a link goes; git checkout -- restores it")
	c.Flags().BoolVar(&update, "update", false, "fetch every git source again instead of reading the cached clone")
	c.Flags().BoolVarP(&verbose, "verbose", "v", false, "print one line per entry")
	return c
}

// ErrReported marks an error a command has already printed itself, with more detail than
// a single line could carry. The main package matches it with errors.Is and prints nothing
// further. A collision is the one case today: [printCollision] shows the layers involved
// and the profile lines that resolve it, and the command returns the collision wrapped so
// that [ExitCode] still sees it.
var ErrReported = errors.New("reported")

// reportedError wraps an error a command printed itself. It matches [ErrReported] and
// unwraps to the error it carries.
type reportedError struct{ err error }

// Error is the wrapped error's text.
func (e reportedError) Error() string { return e.err.Error() }

// Unwrap returns the wrapped error, so errors.As reaches a collision inside.
func (e reportedError) Unwrap() error { return e.err }

// Is matches [ErrReported] and nothing else.
func (e reportedError) Is(target error) bool { return target == ErrReported }

// reported wraps err as one the command has printed. A nil err stays nil.
func reported(err error) error {
	if err == nil {
		return nil
	}
	return reportedError{err}
}

// inputError wraps an error in what the person handed the command: the command line, the
// profile, a layer, a fragment. [ExitCode] gives it its own status.
type inputError struct{ err error }

// Error is the wrapped error's text.
func (e inputError) Error() string { return e.err.Error() }

// Unwrap returns the wrapped error.
func (e inputError) Unwrap() error { return e.err }

// input wraps err as an error in the input. A nil err stays nil, so a check that passed
// can be wrapped as it is returned.
func input(err error) error {
	if err == nil {
		return nil
	}
	return inputError{err}
}

// The exit statuses of the qory command. A caller that branches on the status tells a
// mistake in its input from a collision and from a path qory left alone, and everything
// else from all three.
const (
	// ExitInput is a mistake in the command line or an input file: a flag or an argument
	// that does not exist, a profile, a layer or a fragment that does not read.
	ExitInput = 2
	// ExitCollision is a compose refused because two layers ship the same entry.
	ExitCollision = 3
	// ExitForeign is a path qory would not replace or remove, because it did not write it.
	ExitForeign = 4
)

// ExitCode is the status a process exits with for err: 0 for nil, [ExitInput],
// [ExitCollision] or [ExitForeign] for the errors those name, and 1 for every other
// failure, such as a git source that could not be fetched or a file that could not be
// written. A command unknown to the tree is an input error too; cobra reports it as a
// plain error whose text starts with "unknown command", which is the one place this
// package reads an error's text.
func ExitCode(err error) int {
	var in inputError
	var collision *compose.CollisionError
	var foreign *render.ForeignPathError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &collision):
		return ExitCollision
	case errors.As(err, &foreign):
		return ExitForeign
	case errors.As(err, &in), strings.HasPrefix(err.Error(), "unknown command"):
		return ExitInput
	default:
		return 1
	}
}

// printCollision prints what happened, the layers involved, and the profile lines that
// resolve it by keeping the last layer that ships each entry.
func printCollision(u *ui.UI, p *profile.Profile, e *compose.CollisionError) {
	sources := map[string]string{}
	var order []string
	for _, l := range p.Layers {
		sources[l.Name] = l.Source.String()
		order = append(order, l.Name)
	}
	for _, c := range e.Collisions {
		u.Fail(fmt.Errorf("%s/%s is provided by %d layers", c.Kind, c.Name, len(c.Layers)))
		var rows [][]string
		for _, l := range c.Layers {
			rows = append(rows, []string{l, sources[l], e.Pins[l]})
		}
		u.Table(rows)
	}
	u.Blank()
	u.Heading("Fix")
	u.Text("Keep one and exclude it from the others. For example, in harness-compose.yaml:")
	layers, excludes := e.Suggest(order)
	for _, l := range layers {
		u.Blank()
		u.Code("- name: "+l, "  ...", "  exclude:")
		var kinds []string
		for k := range excludes[l] {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			u.Code("    " + k + ": [" + strings.Join(excludes[l][k], ", ") + "]")
		}
	}
}

// newInspect builds the inspect verb, which reads the report of the last compose and
// prints it. It reads nothing but the report, so it reports the harness as it was composed,
// not the layers as they are now.
func newInspect(use string, aliases ...string) *cobra.Command {
	return &cobra.Command{
		Use:     use,
		Aliases: aliases,
		Short:   "Print the report of the composed harness",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			at, err := locate()
			if err != nil {
				return err
			}
			rep, err := report.Read(at.report)
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("no composed harness for %s; run qory harness compose", at.root)
			}
			if err != nil {
				return err
			}
			return rep.Print(cmd.OutOrStdout())
		},
	}
}

// newRemove builds the remove verb. It unlinks for every registered runtime, not only the
// one the profile targets, because a checkout may have been composed for several runtimes
// in turn, and then removes the whole .qory directory. With --runtime it unlinks that
// runtime alone and drops its directory from the home, leaving the rest composed, unless
// it was the last one. Unlinking touches only links this tool wrote and pointed into
// .qory; a file or directory of the checkout's own at the same path is left alone. The
// first unlink error is returned after every runtime has been tried, so one foreign path
// does not leave the rest of the harness behind. When the last compose replaced files of
// the checkout under --force, remove names them and the git command that restores them.
func newRemove(use string, aliases ...string) *cobra.Command {
	var runtime string
	c := &cobra.Command{
		Use:     use,
		Aliases: aliases,
		Short:   "Remove the composed harness and its links from the checkout",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			at, err := locate()
			if err != nil {
				return err
			}
			u := ui.New(cmd.OutOrStdout())
			u.Title(checkout.RepoKey(at.root))
			if runtime != "" {
				rt, err := render.Lookup(runtime)
				if err != nil {
					return input(err)
				}
				return removeRuntime(u, at, rt)
			}
			rep, _ := report.Read(at.report)
			var unlinkErr error
			var removedAny []string
			for _, name := range render.Names() {
				rt, _ := render.Lookup(name)
				removed, err := render.Unlink(rt, at.root)
				removedAny = append(removedAny, removed...)
				for _, path := range removed {
					u.Success("removed %s", path)
				}
				if err != nil && unlinkErr == nil {
					unlinkErr = err
				}
			}
			_, err = os.Stat(at.dir)
			had := err == nil
			if err := os.RemoveAll(at.dir); err != nil {
				return err
			}
			switch {
			case had:
				u.Success("removed %s", ui.Short(at.dir, at.root))
			case len(removedAny) == 0:
				u.Text("nothing composed here")
			}
			restoreHint(u, at.root, rep.Replaced)
			return unlinkErr
		},
	}
	c.Flags().StringVar(&runtime, "runtime", "", "remove this runtime's links and directory only, and keep the rest composed")
	return c
}

// removeRuntime takes one runtime out of a composed checkout: its links, except the ones
// another runtime in the home shares, its directory in the home, and its name in the
// report's target. When it was the last runtime in the home, the whole harness goes, the
// way remove without --runtime takes it. A replaced file whose link went is named with
// the git command that restores it, and drops out of the report.
func removeRuntime(u *ui.UI, at places, rt render.Runtime) error {
	var others []render.Runtime
	for _, o := range render.Composed(at.home) {
		if o.Name() != rt.Name() {
			others = append(others, o)
		}
	}
	rep, _ := report.Read(at.report)
	removed, err := render.Unlink(rt, at.root, others...)
	for _, path := range removed {
		u.Success("removed %s", path)
	}
	if err != nil {
		return err
	}
	dir := filepath.Join(at.home, rt.Name())
	if _, statErr := os.Stat(dir); statErr == nil {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		u.Success("removed %s", ui.Short(dir, at.root))
	} else if len(removed) == 0 {
		u.Text("nothing composed here for " + rt.Name())
		return nil
	}
	if len(others) == 0 {
		if err := os.RemoveAll(at.dir); err != nil {
			return err
		}
		u.Success("removed %s", ui.Short(at.dir, at.root))
		restoreHint(u, at.root, rep.Replaced)
		return nil
	}
	var gone []string
	rep.Replaced = slices.DeleteFunc(rep.Replaced, func(path string) bool {
		for _, r := range removed {
			if path == r || strings.HasPrefix(path, r+"/") {
				gone = append(gone, path)
				return true
			}
		}
		return false
	})
	restoreHint(u, at.root, gone)
	rep.Target.Runtimes = slices.DeleteFunc(rep.Target.Runtimes, func(n string) bool { return n == rt.Name() })
	return report.Write(at.report, rep)
}

// restoreHint tells the person how to get back the checkout's own files a compose replaced
// under --force, now that their links are gone. The paths are relative to the checkout
// root, so the command names the root when the person stands elsewhere.
func restoreHint(u *ui.UI, root string, replaced []string) {
	if len(replaced) == 0 {
		return
	}
	command := "git checkout -- " + strings.Join(replaced, " ")
	if cwd, err := os.Getwd(); err == nil && cwd != root {
		command = "git -C " + ui.Short(root, "") + " checkout -- " + strings.Join(replaced, " ")
	}
	u.Blank()
	u.Text("The compose replaced files of the checkout. To restore them:")
	u.Code(command)
}

// count is "1 layer" and "2 layers", because a message that reads wrong makes a person
// doubt the number too.
func count(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
