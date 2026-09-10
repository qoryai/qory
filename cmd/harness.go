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
// A --dry-run stops after the report is built and prints it instead.
func newCompose(use string, aliases ...string) *cobra.Command {
	var file, runtime, model string
	var dryRun, verbose bool
	c := &cobra.Command{
		Use:     use,
		Aliases: aliases,
		Short:   "Compose the profile's layers into the checkout you stand in",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			at, err := locate()
			if err != nil {
				return err
			}
			if file == "" {
				if file, err = profile.Discover(at.root); err != nil {
					return err
				}
			}
			p, err := profile.Load(file)
			if err != nil {
				return err
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
				return err
			}
			// Every targeted runtime is looked up before anything is composed, so an
			// unknown name fails before the disk is touched.
			var targets []render.Runtime
			for _, name := range p.Target.Runtimes {
				prov, err := render.Lookup(name)
				if err != nil {
					return err
				}
				targets = append(targets, prov)
			}
			name := p.Name
			if name == "" {
				name = checkout.RepoKey(at.root)
			}
			out := cmd.OutOrStdout()
			u := ui.New(out)
			u.Title(name, strings.TrimSpace(p.Target.Runtimes.String()+" "+p.Target.Model))
			res, err := compose.Compose(p)
			var collision *compose.CollisionError
			if errors.As(err, &collision) {
				printCollision(u, p, collision)
				return ErrReported
			}
			if err != nil {
				return err
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
			for _, prov := range targets {
				targeted[prov.Name()] = true
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
			skippedLinks := map[string][]string{}
			for _, prov := range all {
				skipped, err := render.LinkInto(prov, res, at.root, at.home)
				if err != nil {
					return err
				}
				skippedLinks[prov.Name()] = skipped
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
			for _, prov := range all {
				var links, kept []string
				for _, l := range prov.Links(res) {
					if slices.Contains(skippedLinks[prov.Name()], l.Checkout) {
						kept = append(kept, l.Checkout)
						continue
					}
					links = append(links, l.Checkout)
				}
				row := strings.Join(links, "  ")
				if slices.Contains(also, prov) {
					row += "  (composed here earlier, refreshed)"
				}
				rows = append(rows, [2]string{prov.Name(), row})
				if len(kept) > 0 {
					rows = append(rows, [2]string{"kept", strings.Join(kept, "  ") + "  (the checkout's own; not linked)"})
				}
				for _, s := range render.Skipped(prov, res) {
					rows = append(rows, [2]string{"skipped", s + "  (no place in " + prov.Name() + ")"})
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
	c.Flags().BoolVarP(&verbose, "verbose", "v", false, "print one line per entry")
	return c
}

// ErrReported is the error a command returns when it has already printed the failure
// itself, with more detail than a single line could carry. The main package recognises it
// with errors.Is, prints nothing further, and exits 1. A collision is the one case today:
// [printCollision] shows the layers involved and the profile lines that resolve it.
var ErrReported = errors.New("reported")

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
		Args:    cobra.NoArgs,
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
// in turn, and then removes the whole .qory directory. Unlinking touches only links this
// tool wrote and pointed into .qory; a file or directory of the checkout's own at the same
// path is left alone. The first unlink error is returned after every runtime has been
// tried, so one foreign path does not leave the rest of the harness behind.
func newRemove(use string, aliases ...string) *cobra.Command {
	return &cobra.Command{
		Use:     use,
		Aliases: aliases,
		Short:   "Remove the composed harness and its link from the checkout",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			at, err := locate()
			if err != nil {
				return err
			}
			u := ui.New(cmd.OutOrStdout())
			u.Title(checkout.RepoKey(at.root))
			var unlinkErr error
			var removedAny []string
			for _, name := range render.Names() {
				prov, _ := render.Lookup(name)
				removed, err := render.Unlink(prov, at.root)
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
			return unlinkErr
		},
	}
}

// count is "1 layer" and "2 layers", because a message that reads wrong makes a person
// doubt the number too.
func count(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
