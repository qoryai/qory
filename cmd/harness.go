package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/report"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
	"github.com/qoryai/qory/internal/ui"
	"github.com/qoryai/qory/internal/worktree"

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
		Short:   "Compose, inspect, remove and launch the harness of a checkout",
		// A verb this noun does not have is an input error, not a help page and exit 0.
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return input(fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath()))
			}
			return cmd.Help()
		},
	}
	harness.AddCommand(newCompose("compose", "c"), newInspect("inspect", "i"), newRemove("remove", "r"), newLaunch("launch", "l"))
	return harness
}

// shortcuts builds the top-level hc, hi, hr and hl commands. They are the same
// constructors as the harness verbs, so the two names cannot drift apart, and they are
// hidden from help because the harness noun is where the tool is meant to be read.
func shortcuts() []*cobra.Command {
	var cmds []*cobra.Command
	for _, c := range []*cobra.Command{newCompose("hc"), newInspect("hi"), newRemove("hr"), newLaunch("hl")} {
		c.Hidden = true
		cmds = append(cmds, c)
	}
	return cmds
}

// places holds the paths every harness verb works with, all absolute, and whether the
// checkout links into the home.
type places struct {
	root   string // the root of the checkout the harness is composed for
	dir    string // the .qory directory in it, which the tool owns entirely; "" when the home is outside the checkout
	home   string // the composed tree: .qory/harness, or its directory under the root harness.home names
	report string // the report of the last compose, beside the home as <home>-report.json
	links  bool   // whether the checkout gets links into the home and exclude lines for them
}

// homeOptions is what a verb was told about the home on its command line: --home, "" for
// the configuration's, and --no-links.
type homeOptions struct {
	home    string
	noLinks bool
}

// reportFor is the report's path for a home, beside it.
func reportFor(home string) string { return home + "-report.json" }

// locate finds the places a verb works with, and the configuration of their checkout:
// from the --home flag, when it names a composed home whose report says which checkout
// it is for, so a verb runs from either side of the pair; else from the checkout the
// process stands in and its configuration.
func locate(o homeOptions) (places, config.Config, error) {
	at, ok, err := placesFromReport(o)
	if err != nil {
		return at, config.Config{}, err
	}
	root := at.root
	if !ok {
		if root, err = locateRoot(""); err != nil {
			return places{}, config.Config{}, err
		}
	}
	conf, err := configFor(root)
	if err != nil {
		return places{}, conf, err
	}
	if !ok {
		at, err = placesFor(root, conf, o)
	}
	return at, conf, err
}

// placesFromReport reads the --home flag as a composed home: when a report stands beside
// the directory it names, the report says which checkout the home is for and whether the
// checkout links into it, and the verb needs no checkout to stand in. It reports false
// when the flag is empty or names no such home.
func placesFromReport(o homeOptions) (places, bool, error) {
	if o.home == "" {
		return places{}, false, nil
	}
	home, err := filepath.Abs(o.home)
	if err != nil {
		return places{}, false, err
	}
	rep, err := report.Read(reportFor(home))
	if errors.Is(err, os.ErrNotExist) || err == nil && rep.Checkout == "" {
		return places{}, false, nil
	}
	if err != nil {
		return places{}, false, err
	}
	at := places{root: rep.Checkout, home: home, report: reportFor(home), links: rep.Links != report.NoLinks && !o.noLinks}
	if within(at.root, home) {
		at.dir = checkout.QoryDir(at.root)
	} else {
		at.links = false
	}
	return at, true, nil
}

// locateRoot finds the checkout holding dir, "" for the working directory. It fails
// outside a git working tree, which is deliberate: qory composes for a checkout, and
// links into it and excludes what it wrote through git when the home is inside it.
func locateRoot(dir string) (string, error) {
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = cwd
	}
	root, err := checkout.Root(dir)
	if err != nil {
		return "", err
	}
	if checkout.ExcludeFile(root) == "" {
		return "", input(fmt.Errorf("%s is not inside a git working tree; qory composes for a checkout", root))
	}
	return root, nil
}

// configFor reads the configuration a verb other than compose works with for the
// checkout at root: the checkout's own harness keys left out under extends, the way a
// compose leaves them out, and read when the checkout holds no stack at all.
func configFor(root string) (config.Config, error) {
	own := true
	if file, err := config.DiscoverStack(root); err == nil {
		if p, err := config.LoadStack(file); err == nil {
			own = p.Extends.Path == "" && p.Extends.Git == ""
		}
	}
	conf, err := config.Load(root, own)
	if err != nil {
		return conf, input(err)
	}
	return conf, nil
}

// placesFor derives the places for the checkout at root from the configuration and the
// flags. The home is --home, else harness.home, else [config.DefaultHome], a relative
// value under the checkout root. A home inside the checkout is .qory/harness and nothing
// else, and .qory has to be a real directory or absent, a symlink a repository committed
// say, because everything qory writes and removes there goes through that path. A home
// outside the checkout is a root holding one home per checkout, named by
// [checkout.Key], so one directory serves every checkout and every worktree, and the
// checkout gets no links: harness.links: checkout is refused there. Inside, the links are
// written unless harness.links: none or --no-links.
func placesFor(root string, conf config.Config, o homeOptions) (places, error) {
	var home string
	switch {
	case o.home != "":
		abs, err := filepath.Abs(o.home)
		if err != nil {
			return places{}, err
		}
		home = abs
	case conf.Home != "":
		home = conf.Home
	default:
		home = config.DefaultHome
	}
	if !filepath.IsAbs(home) {
		home = filepath.Join(root, home)
	}
	home = filepath.Clean(home)
	if !within(root, home) {
		// The root's own symlinks are resolved so the tree stands where the directory
		// really is; the home under it is qory's own and is checked by the build.
		if real, err := filepath.EvalSymlinks(home); err == nil {
			home = real
		}
		if conf.Links == config.LinksCheckout && !o.noLinks {
			return places{}, input(fmt.Errorf("harness.links: checkout needs the home inside the checkout, %s; the home is %s", config.DefaultHome, home))
		}
		home = filepath.Join(home, checkout.Key(root))
		return places{root: root, home: home, report: reportFor(home)}, nil
	}
	if home != filepath.Join(root, config.DefaultHome) {
		return places{}, input(fmt.Errorf("a home inside the checkout is %s; %s is not it, and a home elsewhere goes outside the checkout", config.DefaultHome, ui.Short(home, root)))
	}
	qdir := checkout.QoryDir(root)
	if info, err := os.Lstat(qdir); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		target, _ := os.Readlink(qdir)
		return places{}, &render.ForeignPathError{Path: qdir, Target: target}
	}
	return places{root: root, dir: qdir, home: home, report: reportFor(home), links: !o.noLinks && conf.Links != config.LinksNone}, nil
}

// placesAt is the places of the checkout at root under its configuration and no flags,
// for a worktree verb that needs to know where a worktree's home is.
func placesAt(root string) (places, error) {
	conf, err := configFor(root)
	if err != nil {
		return places{}, err
	}
	return placesFor(root, conf, homeOptions{})
}

// within reports whether path is root or below it, both absolute and clean.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// homeFlags adds --home to a harness verb.
func homeFlags(c *cobra.Command, o *homeOptions) {
	c.Flags().StringVar(&o.home, "home", "", "where the harness is composed: a directory outside the checkout, one home per checkout under it, or "+config.DefaultHome+" (qory.yaml: harness.home)")
}

// newCompose builds the compose verb under the given name and aliases.
//
// The order of the run matters and is this: locate the checkout, read the configuration,
// discover or load the stack, apply the configuration's runtime and model and then the
// --runtime and --model flags on top of it, look the runtime up before doing any work so
// an unknown name fails early, print the title, compose, and only then touch the disk.
// Nothing is written before the compose succeeds, so a collision or an unreadable module
// leaves the checkout exactly as it was.
//
// -f names the document to compose instead of discovering one. A stack it names, in a
// checkout whose own document extends a stack, is that document's base in place of what
// extends names, and the document may leave extends out altogether: a runner that holds
// the stack tree supplies the base, and the checkout's file carries only what is the
// repository's own.
//
// Writing is four steps in a fixed order: [render.Build] renders the home tree,
// [render.LinkInto] links it into the checkout, [render.LinkModules] writes the module
// links the stack names, and [report.Write] records what happened. The report is
// written last, so a report on disk means a compose went through; the one exception is a
// link step that failed after replacing a file, which writes the report so that remove
// still knows what to restore. A --dry-run stops after the report is built and prints it
// instead. A --check renders the tree again into a scratch directory, compares it with
// the home and exits with [ExitStale] when they differ; the merged outputs are generated
// copies, so an edit to a module's instructions or settings leaves the home behind until
// the next compose, and a check is how a CI gate sees that. --force lets a link replace a
// tracked, unmodified file of the checkout, and the report records every path it replaced
// so remove can say how to get it back. --update fetches every git source again. Both
// flags have their standing value in qory.yaml, and a flag given on the command line,
// --force=false say, wins over the file.
func newCompose(use string, aliases ...string) *cobra.Command {
	var file, runtime, model string
	var dryRun, check, force, update bool
	var h homeOptions
	c := &cobra.Command{
		Use:     use,
		Aliases: aliases,
		Short:   "Compose the stack's modules into the checkout you stand in",
		Long: `Compose the stack's modules into the checkout you stand in.

The home, the composed tree, is .qory/harness in the checkout, linked from the paths
each runtime reads and excluded from git. With --home or harness.home in qory.yaml naming
a directory outside the checkout, the tree goes under that directory instead, one home
per checkout named after it, and the checkout gets nothing: no link, no .qory, no
exclude line. A runtime reads such a home through the arguments qory harness launch
prints. --no-links keeps the checkout untouched with the home inside it too.

--verbose prints one line per entry, the entry and the module it came from.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			defer buzzing(cmd)()
			o := composeOptions{file: file, runtime: runtime, model: model, dryRun: dryRun, check: check, verbose: verbose(cmd), homeOptions: h}
			if cmd.Flags().Changed("force") {
				if check {
					return input(errors.New("--check writes nothing, so --force has nothing to replace"))
				}
				o.force = &force
			}
			if cmd.Flags().Changed("update") {
				o.update = &update
			}
			return runCompose(cmd.OutOrStdout(), cmd.ErrOrStderr(), o)
		},
	}
	c.Flags().StringVarP(&file, "file", "f", "", "the qory-stack.yaml, or the qory.yaml or harness.yaml whose harness section to compose, instead of discovering one. A stack named here, in a checkout whose own document extends one, is that document's base in place of extends")
	c.Flags().StringVar(&runtime, "runtime", "", "render for these runtimes instead of target.runtime, comma separated ("+strings.Join(render.Names(), ", ")+"; qory.yaml: runtime)")
	c.Flags().StringVar(&model, "model", "", "write this model instead of target.model (qory.yaml: model)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print the report and write nothing")
	c.Flags().BoolVar(&check, "check", false, "compare the home with what the stack and modules say now and write nothing; exit 6 when a file or link differs. The links from the checkout into the home and the report are not compared")
	c.Flags().BoolVar(&force, "force", false, "replace a tracked, unmodified file of the checkout where a link goes; git checkout -- restores it (qory.yaml: force)")
	c.Flags().BoolVar(&update, "update", false, "fetch every git source again instead of reading the cached clone (qory.yaml: update)")
	homeFlags(c, &h)
	c.Flags().BoolVar(&h.noLinks, "no-links", false, "write nothing into the checkout, no link and no exclude line; qory harness launch says how a runtime reads the home (qory.yaml: harness.links: none)")
	return c
}

// composeOptions is what one compose is told: the checkout to compose, "" for the working
// directory's, and the flags of the compose verb, with force and update nil when the flag
// was not given so the configuration decides.
type composeOptions struct {
	dir            string
	file           string
	runtime, model string
	dryRun         bool
	check          bool
	verbose        bool
	force, update  *bool
	homeOptions
}

// staleError is what a --check returns when the home does not match what the stack and
// modules say now, or when nothing is composed. [ExitCode] gives it [ExitStale].
type staleError struct {
	// Differences are the paths that differ, empty when nothing is composed.
	Differences []render.Difference
}

// Error says what to do; the paths were printed as rows before the error went up.
func (e *staleError) Error() string {
	if len(e.Differences) == 0 {
		return "nothing composed here; run qory harness compose"
	}
	return "stale: run qory harness compose"
}

// runCheck is --check: it renders the prepared compose into a scratch directory,
// addressed as the home, and compares the two trees. The runtimes are the ones a compose
// would build, the targets and the ones composed earlier, so the comparison is against
// the tree a compose would write. A home that matches prints up to date and the rows a
// compose prints; one that differs prints one row per path and returns a [*staleError].
// The links from the checkout into the home and the report are not compared: a missing
// link is a foreign path or a remove, and the report changes when the home does.
func runCheck(out io.Writer, pr *prepared) error {
	u := pr.u
	if _, err := os.Stat(pr.at.home); err != nil {
		return &staleError{}
	}
	stage, err := os.MkdirTemp("", "qory-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := render.BuildAt(pr.res, stage, pr.at.home, allRuntimes(pr.at.home, pr.targets)...); err != nil {
		return err
	}
	differences, err := render.Diff(stage, pr.at.home)
	if err != nil {
		return err
	}
	rows := [][2]string{{"home", ui.Short(pr.at.home, pr.at.root)}}
	if len(differences) == 0 {
		u.Success("up to date")
		u.Fields(append(rows, pr.rows...))
		return nil
	}
	for _, d := range differences {
		rows = append(rows, [2]string{d.Path, d.What})
	}
	u.Fields(append(rows, pr.rows...))
	return &staleError{Differences: differences}
}

// prepared is a compose that has been read and composed but not written: what
// [prepare] hands to the compose, the dry run and the check.
type prepared struct {
	at       places
	res      *compose.Result
	rep      report.Report
	previous report.Report
	targets  []render.Runtime
	force    bool
	rows     [][2]string // the rows every outcome prints after its own: skipped keys, unchecked ranges
	u        *ui.UI
}

// runCompose is the compose verb's body, for the verb and for a worktree add, which
// composes the worktree it made. out gets the rows, errOut the collision text. With
// check set it compares instead of writing, see [runCheck].
func runCompose(out, errOut io.Writer, o composeOptions) error {
	pr, err := prepare(out, errOut, o)
	if err != nil {
		return err
	}
	if o.check {
		return runCheck(out, pr)
	}
	at, res, rep, previous, targets, force, u := pr.at, pr.res, pr.rep, pr.previous, pr.targets, pr.force, pr.u
	skippedConfig := pr.rows
	if o.dryRun {
		if err := rep.PrintBody(out); err != nil {
			return err
		}
		u.Blank()
		u.Success("dry run: nothing written")
		u.Fields(skippedConfig)
		return nil
	}
	// A checkout can be composed for several runtimes at once, and can have been
	// composed for others before. The home is one tree that a build replaces
	// whole, so every runtime it is to hold goes into one call: the targets and
	// the runtimes already there, whose links would otherwise stop resolving.
	all := allRuntimes(at.home, targets)
	// The report's target is every runtime the home holds after this compose,
	// the targets first, since the harness was rendered for all of them.
	rep.Target.Runtimes = nil
	for _, rt := range all {
		rep.Target.Runtimes = append(rep.Target.Runtimes, rt.Name())
	}
	if err := render.Build(res, at.home, all...); err != nil {
		return err
	}
	return write(out, o, at, res, rep, previous, targets, all, force, skippedConfig, u)
}

// allRuntimes is every runtime the home holds after a compose for targets: the targets
// first, then the runtimes composed into home earlier, which the build refreshes so their
// links keep resolving.
func allRuntimes(home string, targets []render.Runtime) []render.Runtime {
	targeted := map[string]bool{}
	for _, rt := range targets {
		targeted[rt.Name()] = true
	}
	all := append([]render.Runtime{}, targets...)
	for _, other := range render.Composed(home) {
		if !targeted[other.Name()] {
			all = append(all, other)
		}
	}
	return all
}

// prepare does everything a compose does before it touches the disk: it locates the
// checkout, reads the configuration, discovers or loads the stack, checks every document's
// qory key, resolves the base, applies the configuration's runtime and model and then the
// --runtime and --model flags, looks every runtime up, prints the title, composes, and
// builds the report. A collision is printed on errOut and comes back marked reported.
func prepare(out, errOut io.Writer, o composeOptions) (*prepared, error) {
	// A --home naming a composed home says which checkout it is for; otherwise the
	// checkout is the one the process, or the worktree add, stands in.
	at, located, err := placesFromReport(o.homeOptions)
	if err != nil {
		return nil, err
	}
	root := at.root
	if !located {
		if root, err = locateRoot(o.dir); err != nil {
			return nil, err
		}
	}
	file := o.file
	if file == "" {
		if file, err = config.DiscoverStack(root); err != nil {
			return nil, input(err)
		}
	}
	p, err := config.LoadStack(file)
	if err != nil {
		return nil, input(err)
	}
	retired := newRetiredRows(root)
	retired.add(p.File, p.RetiredAPIVersion)
	// A stack named with -f, in a checkout whose own document extends one, is that
	// document's base, in place of what extends names: the document composes on it,
	// its modules appended and its extensions over the base's. That is how a runner
	// holding the stack tree supplies the base, and the checkout's file need name no
	// version, ref or URL of it. A checkout without a document composes the stack
	// alone, as it always has.
	var base *stack.Stack
	var named stack.Source
	if o.file != "" && !config.IsDocument(file) {
		doc, err := config.Document(root)
		if err != nil {
			return nil, input(err)
		}
		if doc != "" {
			base = p
			if p, named, err = config.OnBase(doc, base.File); err != nil {
				return nil, input(err)
			}
			retired.add(p.File, p.RetiredAPIVersion)
		}
	}
	// A checkout that extends a closed base says what it composes the base for in
	// its document's target, and the harness, git and env keys of its own
	// qory.yaml are not read: the runner's configuration is the only one that
	// stands over the document.
	extends := p.Extends.Path != "" || p.Extends.Git != ""
	conf, err := config.Load(root, !extends)
	if err != nil {
		return nil, input(err)
	}
	if !located {
		if at, err = placesFor(root, conf, o.homeOptions); err != nil {
			return nil, err
		}
	}
	// Every document with a qory key is checked against the running qory as it is
	// read, before anything is fetched or written: the configuration files, the stack,
	// and the base once extends has resolved it.
	checks := newQoryChecks(root)
	for _, r := range conf.Qory {
		if err := checks.check(r.File, "the file", r.Qory); err != nil {
			return nil, err
		}
	}
	for _, r := range conf.Retired {
		retired.add(r.File, r.APIVersion)
	}
	if err := checks.check(p.File, "the stack", p.Qory); err != nil {
		return nil, err
	}
	force, update := conf.Force, conf.Update
	if o.force != nil {
		force = *o.force
	}
	if o.update != nil {
		update = *o.update
	}
	// The last report pins each git module, and the base, to the commit it was
	// composed from, so a compose without --update stays there as long as the
	// stack still names the same source: an edited ref resolves anew.
	previous, _ := report.Read(at.report)
	pins := map[string]string{}
	for _, l := range previous.Modules {
		if l.Pin != source.WorkingTree {
			pins[l.Source] = l.Pin
		}
	}
	opts := compose.Options{Update: update, Pins: pins, Cache: conf.Git.Cache, Timeout: conf.Git.Timeout, Env: conf.Env}
	var basePin string
	if previous.Base != nil && previous.Base.Source == p.Extends.String() {
		basePin = previous.Base.Pin
	}
	if base != nil {
		p, opts.Base, err = compose.ExtendOn(p, base, source.WorkingTree)
	} else {
		p, opts.Base, err = compose.LoadBase(p, basePin, opts)
	}
	if err != nil {
		return nil, composeError(err)
	}
	if opts.Base != nil {
		if err := checks.check(p.File, "the base stack "+opts.Base.String(), opts.Base.Qory); err != nil {
			return nil, err
		}
		if base == nil {
			retired.add("the base stack "+opts.Base.String(), opts.Base.RetiredAPIVersion)
		}
	}
	if err := applyTarget(p, opts.Base, conf, o.runtime, o.model); err != nil {
		return nil, input(err)
	}
	// Every targeted runtime is looked up before anything is composed, so an
	// unknown name fails before the disk is touched.
	var targets []render.Runtime
	for _, name := range p.Target.Runtimes {
		rt, err := render.Lookup(name)
		if err != nil {
			return nil, input(err)
		}
		targets = append(targets, rt)
	}
	name := p.Name
	if name == "" {
		name = checkout.RepoKey(at.root)
	}
	u := ui.New(out)
	u.Title(name, strings.TrimSpace(p.Target.Runtimes.String()+" "+p.Target.Model))
	res, err := compose.ComposeWith(p, opts)
	var collision *compose.CollisionError
	switch {
	case errors.As(err, &collision):
		printCollision(ui.New(errOut), collision)
		return nil, reported(err)
	case err != nil:
		return nil, composeError(err)
	}
	if err := render.CheckFiles(res); err != nil {
		return nil, input(err)
	}
	for _, m := range res.Modules {
		retired.add("module "+m.Name, m.RetiredAPIVersion)
	}
	rep := report.New(res, name, at.root, at.home)
	if !at.links {
		rep.Links = report.NoLinks
	}
	// A runtime's own hosts join the declaration when there is one, under the
	// runtime's name, so a stack that declares needs to know no endpoint.
	if rep.Egress != nil {
		for _, rt := range targets {
			if d, ok := rt.(render.Declarer); ok {
				rep.AddEgress(rt.Name(), d.Egress())
			}
		}
	}
	// The report says which qory wrote it, so a runner's report and a laptop's can be
	// compared; a build with no version, a source build without version control, is
	// left out rather than recorded as nothing.
	if b := build(); b.Version != "" {
		rep.Qory = &report.Build{Version: b.Version, Commit: b.Commit, Source: b.Source}
	}
	var baseErr error
	rep.Worktree, baseErr = reportBase(at.root, conf)
	// The machine keys of a qory.yaml at the checkout root are not read under
	// extends, and a row says so, on a dry run as well, so the person who wrote
	// them learns that the base stack decides.
	var skippedConfig [][2]string
	if base != nil {
		what := "(named by -f)"
		if named.Path != "" || named.Git != "" {
			what = "(named by -f, in place of extends " + named.String() + ")"
		}
		skippedConfig = append(skippedConfig, [2]string{"base", ui.Short(base.File, at.root) + "  " + what})
	}
	if own, _ := config.FileIn(at.root); extends && own != "" {
		skippedConfig = append(skippedConfig, [2]string{"skipped", filepath.Base(own) + "  (its harness, git and env keys; the base stack decides under extends)"})
	}
	if baseErr != nil {
		skippedConfig = append(skippedConfig, [2]string{"skipped", "worktree.base  (" + baseErr.Error() + "; the report names no base)"})
	}
	skippedConfig = append(skippedConfig, checks.rows...)
	skippedConfig = append(skippedConfig, retired.rows...)
	return &prepared{at: at, res: res, rep: rep, previous: previous, targets: targets, force: force, rows: skippedConfig, u: u}, nil
}

// reportBase resolves the configuration's worktree.base for the report of the checkout at
// root, so a program beside qory reads the repository's base branch instead of deriving
// it. It reads local refs alone. A repository with no base to default to, one with no
// commit or a main checkout on no branch, gets no base and no error; a worktree.base that
// names nothing gets the error, which the compose prints as a row and goes on.
func reportBase(root string, conf config.Config) (*report.Worktree, error) {
	main, err := worktree.Main(root)
	if err != nil {
		return nil, nil
	}
	b, err := worktree.ResolveBase(main, conf.Worktree.Base)
	if err != nil {
		if conf.Worktree.Base == "" {
			err = nil
		}
		return nil, err
	}
	source := b.From
	if b.From == worktree.FromGiven {
		source = conf.Origin("worktree.base")
	}
	return &report.Worktree{Base: &report.WorktreeBase{Branch: b.Branch, Ref: b.Ref, Source: source}}, nil
}

// write is the second half of a compose, after [render.Build] has replaced the home:
// the links into the checkout, the module links, the report, and the rows. all is every
// runtime the home now holds, the targets first.
func write(out io.Writer, o composeOptions, at places, res *compose.Result, rep, previous report.Report, targets, all []render.Runtime, force bool, skippedConfig [][2]string, u *ui.UI) error {
	// A path replaced by an earlier compose is still replaced: the report keeps
	// naming it until remove takes its link, so the restore hint is not lost to a
	// second compose. A link step that fails has replaced what it replaced, so the
	// report is written on that path too when it holds a replaced path, before
	// the error goes up; a compose that replaced nothing leaves the report as it
	// was, so a report on disk still means a compose that went through.
	rep.Replaced = previous.Replaced
	linked := map[string]render.Linked{}
	var moduleLinks render.Linked
	var takenBack []string
	record := func(l render.Linked, err error) error {
		for _, path := range l.Replaced {
			if !slices.Contains(rep.Replaced, path) {
				rep.Replaced = append(rep.Replaced, path)
			}
		}
		if err != nil && len(rep.Replaced) > 0 {
			_ = report.Write(at.report, rep)
		}
		return err
	}
	switch {
	case at.links:
		for _, rt := range all {
			l, err := render.LinkInto(rt, res, at.root, at.home, force)
			linked[rt.Name()] = l
			if err := record(l, err); err != nil {
				return err
			}
		}
		var previousLinks []string
		for _, l := range previous.Modules {
			if l.Link != "" {
				previousLinks = append(previousLinks, l.Link)
			}
		}
		var err error
		moduleLinks, err = render.LinkModules(res, at.root, at.home, previousLinks, force)
		if err := record(moduleLinks, err); err != nil {
			return composeError(err)
		}
	case at.dir != "":
		// The home is in the checkout and nothing links to it: the qory directory is
		// still qory's, and still kept out of git, and the links an earlier compose
		// wrote go the way a compose takes back what it no longer asks for.
		if err := render.Exclude(at.root, "/"+checkout.Dir); err != nil {
			return err
		}
		var err error
		if takenBack, err = unlinkAll(at.root); err != nil {
			return err
		}
	}
	if err := report.Write(at.report, rep); err != nil {
		return err
	}
	if o.verbose {
		var rows [][]string
		for _, e := range rep.Entries {
			rows = append(rows, []string{e.Kind + "/" + e.Name, e.Module})
		}
		u.Table(rows)
	}
	u.Success("composed %s from %s %s", count(len(rep.Entries), "entry", "entries"), count(len(rep.Modules), "module", "modules"), ui.Pot)
	rows := [][2]string{{"home", ui.Short(at.home, at.root)}}
	for _, path := range takenBack {
		rows = append(rows, [2]string{"removed", path + "  (linked by an earlier compose; the checkout gets no links)"})
	}
	for _, rt := range all {
		var links []string
		for _, l := range rt.Links(res) {
			if slices.Contains(linked[rt.Name()].Skipped, l.Checkout) {
				continue
			}
			links = append(links, l.Checkout)
		}
		row := strings.Join(links, "  ")
		if !at.links {
			row = "no links  (" + launchHint(rt) + ")"
		}
		if !slices.Contains(targets, rt) {
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
	rows = append(rows, skippedConfig...)
	for _, l := range res.Modules {
		if l.Link == "" {
			continue
		}
		switch {
		case !at.links:
			rows = append(rows, [2]string{"link", l.Link + "  (module " + l.Name + "; not written, the checkout gets no links)"})
		case slices.Contains(moduleLinks.Replaced, l.Link):
			rows = append(rows, [2]string{"replaced", l.Link + "  (the checkout's own; git checkout -- restores it)"})
		default:
			rows = append(rows, [2]string{"link", l.Link + "  (module " + l.Name + ")"})
		}
	}
	u.Fields(rows)
	return nil
}

// launchHint says how a runtime reads a home the checkout does not link to: the launch
// verb for a runtime that has a launch spec, and for one that has none, that it reads
// the harness through links alone.
func launchHint(rt render.Runtime) string {
	if _, ok := rt.(render.Launcher); ok {
		return "qory harness launch --runtime " + rt.Name() + " prints how to start it"
	}
	return rt.Name() + " reads its harness through links alone; compose with harness.links: checkout"
}

// applyTarget puts the configuration's runtime and model, then the --runtime and --model
// flags, over the document's target, and checks the result: a runtime from one of the
// three, and, under a base stack that says what it is written for in extending.target,
// a target inside that, whichever of the three set it.
func applyTarget(p *stack.Stack, base *compose.Base, conf config.Config, runtime, model string) error {
	want := p.Target
	if conf.Runtime != nil {
		want.Runtimes = conf.Runtime
	}
	if conf.Model != "" {
		want.Model = conf.Model
	}
	if runtime != "" {
		want.Runtimes = stack.Runtimes(strings.Split(runtime, ","))
		for i, r := range want.Runtimes {
			want.Runtimes[i] = strings.TrimSpace(r)
		}
	}
	if model != "" {
		want.Model = model
	}
	if err := want.Runtimes.Validate(); err != nil {
		if len(want.Runtimes) == 0 && base != nil {
			return fmt.Errorf("target.runtime is required; the base stack %s carries no target, so the document sets one beside extends, or the configuration or --runtime does", base)
		}
		return err
	}
	if base != nil && base.Extending != nil {
		if err := base.Extending.Target.Check(want, base.String()); err != nil {
			return err
		}
	}
	p.Target = want
	return nil
}

// composeError classifies what a compose returned: a git source that could not be fetched
// and a file the operating system would not read are failures of the machine, left as
// they are; everything else is a mistake in the stack or a module, an input error.
func composeError(err error) error {
	var fetch *source.FetchError
	var pathErr *fs.PathError
	if errors.As(err, &fetch) || errors.As(err, &pathErr) && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return input(err)
}

// ErrReported marks an error a command has already printed itself, with more detail than
// a single line could carry. The main package matches it with errors.Is and prints nothing
// further. A collision is the one case today: [printCollision] shows the modules involved
// and the stack lines that resolve it, and the command returns the collision wrapped so
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
// stack, a module, a fragment. [ExitCode] gives it its own status.
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
	// that does not exist, a stack, a module or a fragment that does not read.
	ExitInput = 2
	// ExitCollision is a compose refused because two modules ship the same entry.
	ExitCollision = 3
	// ExitForeign is a path qory would not replace or remove, because it did not write it.
	ExitForeign = 4
	// ExitVersion is a document whose qory key excludes the running qory.
	ExitVersion = 5
	// ExitStale is a --check that found the home behind the stack and modules, or
	// nothing composed.
	ExitStale = 6
)

// ExitCode is the status a process exits with for err: 0 for nil, the runtime's own
// status for a qory run whose runtime exited with one, [ExitInput], [ExitCollision],
// [ExitForeign], [ExitVersion] or [ExitStale] for the errors those name, and 1 for every other
// failure, such as a git source that could not be fetched or a file that could not be
// written. A command unknown to the tree is an input error too; cobra reports it as a
// plain error whose text starts with "unknown command", which is the one place this
// package reads an error's text.
func ExitCode(err error) int {
	var in inputError
	var collision *compose.CollisionError
	var foreign *render.ForeignPathError
	var version *versionError
	var stale *staleError
	switch {
	case err == nil:
		return 0
	case exitStatusOf(err):
		code, _ := exitStatus(err)
		return code
	case errors.As(err, &collision):
		return ExitCollision
	case errors.As(err, &foreign):
		return ExitForeign
	case errors.As(err, &version):
		return ExitVersion
	case errors.As(err, &stale):
		return ExitStale
	case errors.As(err, &in), strings.HasPrefix(err.Error(), "unknown command"):
		return ExitInput
	default:
		return 1
	}
}

// printCollision prints what happened, the modules involved, and the stack lines that
// resolve it by keeping the last module that ships each entry.
func printCollision(u *ui.UI, e *compose.CollisionError) {
	for _, c := range e.Collisions {
		u.Fail(fmt.Errorf("%s/%s is provided by %d modules", c.Kind, c.Name, len(c.Modules)))
		var rows [][]string
		for _, l := range c.Modules {
			rows = append(rows, []string{l, e.Sources[l], e.Pins[l]})
		}
		u.Table(rows)
		if c.Base != "" {
			u.Text("It belongs to the base stack " + c.Base + "; rename yours.")
		}
	}
	modules, excludes := e.Suggest(e.Order)
	if len(modules) == 0 {
		return
	}
	u.Blank()
	u.Heading("Fix")
	u.Text("Keep one and exclude it from the others. For example, in qory-stack.yaml:")
	for _, l := range modules {
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
// not the modules as they are now, and it refuses a report of a version it does not read.
func newInspect(use string, aliases ...string) *cobra.Command {
	var h homeOptions
	c := &cobra.Command{
		Use:     use,
		Aliases: aliases,
		Short:   "Print the report of the composed harness",
		Long: `Print the report of the composed harness.

--verbose adds nothing here.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			at, _, err := locate(h)
			if err != nil {
				return err
			}
			rep, err := readReport(at)
			if err != nil {
				return err
			}
			return rep.Print(cmd.OutOrStdout())
		},
	}
	homeFlags(c, &h)
	return c
}

// readReport reads the report of the last compose for the places, and refuses a report
// of a version this qory does not read. Nothing composed is a plain error naming the
// compose verb.
func readReport(at places) (report.Report, error) {
	rep, err := report.Read(at.report)
	if errors.Is(err, os.ErrNotExist) {
		return rep, fmt.Errorf("no composed harness for %s; run qory harness compose", at.root)
	}
	if err != nil {
		return rep, err
	}
	if rep.Version != report.Version {
		return rep, input(fmt.Errorf("%s: report version %d is not one this qory reads; versions: %d", at.report, rep.Version, report.Version))
	}
	return rep, nil
}

// newLaunch builds the launch verb, which prints what starts a runtime's program on the
// composed home: the program, its arguments and its environment, from the runtime's own
// launch template with the configuration's harness.launch over it, resolved against the
// home. A launcher evals the line and knows nothing of the home's layout. The runtime is
// --runtime, or the one runtime the harness is composed for; a runtime whose program
// reads its harness from the checkout alone has no launch template, and the verb says so.
func newLaunch(use string, aliases ...string) *cobra.Command {
	var runtime, address string
	var asJSON bool
	var h homeOptions
	c := &cobra.Command{
		Use:     use,
		Aliases: aliases,
		Short:   "Print the command that starts a runtime on the composed harness",
		Long: `Print the command that starts a runtime on the composed harness, on one line quoted for a
POSIX shell, so a launcher runs it as it is, with arguments of its own after it:

  cd <checkout> && eval "$(qory harness launch --runtime claude)"

The line is the runtime's own launch template, with harness.launch.<runtime> in
qory.yaml over it: the program, the arguments that hand it the home's files, and the
variables it takes them from, ${dir} being the runtime's directory in the home. For
claude it is --plugin-dir, --settings, --mcp-config, --append-system-prompt-file and
--setting-sources user; for codex it is CODEX_HOME. A group of arguments naming a file
the compose did not write, mcp.json without a server say, is left out. The home is found
the way compose finds it, from --home, harness.home or the checkout you stand in; the
paths printed are absolute, so the line works wherever the home is. --json prints the
command, the arguments and the variables as one JSON object, for a launcher that spawns
the program without a shell, with the names the session registers the composed agents,
skills and commands under on that launch under addresses, per kind, a bound role beside
them as the entry it is bound to: harness:<name> for claude, whose plugin prefixes
every kind, the name as the module wrote it elsewhere. --address <kind>/<name> prints
that one registered name alone, for a launcher that builds its first prompt from an
entry point, /harness:implement say. A runtime that reads its harness from the
checkout alone has no launch template, and the verb says so.

--verbose adds nothing here.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			at, conf, err := locate(h)
			if err != nil {
				return err
			}
			rep, err := readReport(at)
			if err != nil {
				return err
			}
			name, launch, err := resolveLaunch(rep, conf, runtime)
			if err != nil {
				return err
			}
			rt, err := render.Lookup(name)
			if err != nil {
				return input(err)
			}
			keys := make([]string, 0, len(rep.Entries))
			for _, e := range rep.Entries {
				keys = append(keys, e.Kind+"/"+e.Name)
			}
			addresses := render.Addresses(keys, rep.Bind, render.AddressOf(rt), rt.Skips())
			if address != "" {
				kind, entry, _ := strings.Cut(address, "/")
				registered, ok := addresses[kind][entry]
				if !ok {
					return input(fmt.Errorf("%s is not an agent, skill, command or bound role the harness is composed with for %s; --json lists them under addresses", address, name))
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), registered)
				return err
			}
			if asJSON {
				data, err := json.MarshalIndent(launchJSON{Command: launch.Command, Args: append([]string{}, launch.Args...), Env: launch.Env, Addresses: addresses}, "", "  ")
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), shellLine(launch))
			return err
		},
	}
	c.Flags().StringVar(&runtime, "runtime", "", "the runtime to start, one the harness is composed for; the only one when left out")
	c.Flags().BoolVar(&asJSON, "json", false, "print the command, the arguments, the variables and the registered names as one JSON object")
	c.Flags().StringVar(&address, "address", "", "print the name the session registers this <kind>/<name>, or bound role, under on this launch, and nothing else")
	homeFlags(c, &h)
	return c
}

// launchJSON is what --json prints: the arguments always an array, the variables left
// out when there are none, and the registered names per kind, left out when the harness
// holds no agent, skill or command the runtime places.
type launchJSON struct {
	Command   string                       `json:"command"`
	Args      []string                     `json:"args"`
	Env       map[string]string            `json:"env,omitempty"`
	Addresses map[string]map[string]string `json:"addresses,omitempty"`
}

// shellLine is a launch as one line a POSIX shell reads back as the same command: the
// variables through env when there are any, then the program and its arguments, a word
// of plain characters as it is and anything else in single quotes with its own single
// quotes escaped.
func shellLine(l render.Launch) string {
	var words []string
	if len(l.Env) > 0 {
		words = append(words, "env")
		for _, k := range sortedKeys(l.Env) {
			words = append(words, shellQuote(k+"="+l.Env[k]))
		}
	}
	words = append(words, shellQuote(l.Command))
	for _, a := range l.Args {
		words = append(words, shellQuote(a))
	}
	return strings.Join(words, " ")
}

// sortedKeys lists a map's keys in order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// shellQuote is one word for [shellLine].
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:@%+,", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// newRemove builds the remove verb. It unlinks for every registered runtime, not only the
// one the stack targets, because a checkout may have been composed for several runtimes
// in turn, and then removes the whole .qory directory. With --runtime it unlinks that
// runtime alone and drops its directory from the home, leaving the rest composed, unless
// it was the last one. Unlinking touches only links this tool wrote and pointed into
// .qory; a file or directory of the checkout's own at the same path is left alone. The
// first unlink error is returned after every runtime has been tried, so one foreign path
// does not leave the rest of the harness behind. When the last compose replaced files of
// the checkout under --force, remove names them and the git command that restores them.
func newRemove(use string, aliases ...string) *cobra.Command {
	var runtime string
	var h homeOptions
	c := &cobra.Command{
		Use:     use,
		Aliases: aliases,
		Short:   "Remove the composed harness and its links from the checkout",
		Long: `Remove the composed harness and its links from the checkout.

A home outside the checkout, under the directory --home or harness.home names, is
removed with its report, and the checkout is not touched: nothing was written there.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			at, _, err := locate(h)
			if err != nil {
				return err
			}
			u := ui.New(cmd.OutOrStdout())
			rep, _ := report.Read(at.report)
			name := rep.Stack
			if name == "" {
				name = checkout.RepoKey(at.root)
			}
			u.Title(name)
			if runtime != "" {
				rt, err := render.Lookup(runtime)
				if err != nil {
					return input(err)
				}
				return removeRuntime(u, at, rt)
			}
			if at.dir == "" {
				return removeOutside(u, at)
			}
			removedAny, unlinkErr := unlinkAll(at.root)
			for _, path := range removedAny {
				u.Success("removed %s", path)
			}
			present := had(at)
			if err := removeDir(u, at); err != nil {
				return err
			}
			if !present && len(removedAny) == 0 {
				u.Text("nothing composed here")
			}
			restoreHint(u, at.root, rep.Replaced)
			return unlinkErr
		},
	}
	c.Flags().StringVar(&runtime, "runtime", "", "remove this runtime's links and directory only, and keep the rest composed")
	homeFlags(c, &h)
	return c
}

// unlinkAll takes every link qory wrote into the checkout at root, for every runtime
// and every module, and returns their checkout paths. The first error comes back after
// every runtime has been tried, so one foreign path does not leave the rest behind.
func unlinkAll(root string) ([]string, error) {
	var removed []string
	var first error
	for _, name := range render.Names() {
		rt, _ := render.Lookup(name)
		gone, err := render.Unlink(rt, root)
		removed = append(removed, gone...)
		if err != nil && first == nil {
			first = err
		}
	}
	gone, err := render.UnlinkModuleLinks(root)
	removed = append(removed, gone...)
	if err != nil && first == nil {
		first = err
	}
	return removed, first
}

// removeOutside removes a home outside the checkout: the tree, its report and a staging
// directory a failed build left, and says so for each that was there. The checkout is
// left alone, since a compose into such a home writes nothing into it.
func removeOutside(u *ui.UI, at places) error {
	removed := false
	for _, path := range []string{at.home, at.report, at.home + ".tmp"} {
		if _, err := os.Lstat(path); err != nil {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		u.Success("removed %s", ui.Short(path, ""))
		removed = true
	}
	if !removed {
		u.Text("nothing composed here")
	}
	return nil
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
	var removed []string
	if at.dir != "" {
		// A home in the checkout may have been linked by an earlier compose, whatever
		// this one wrote; a home outside it never was.
		var err error
		removed, err = render.Unlink(rt, at.root, others...)
		for _, path := range removed {
			u.Success("removed %s", path)
		}
		if err != nil {
			return err
		}
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
		if at.dir == "" {
			return removeOutside(u, at)
		}
		gone, err := render.UnlinkModuleLinks(at.root)
		for _, path := range gone {
			u.Success("removed %s", path)
		}
		if err != nil {
			return err
		}
		if err := removeDir(u, at); err != nil {
			return err
		}
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
	if rep.Version == 0 {
		// No report to keep current; the home says what is composed.
		return nil
	}
	rep.Target.Runtimes = nil
	for _, o := range others {
		rep.Target.Runtimes = append(rep.Target.Runtimes, o.Name())
	}
	return report.Write(at.report, rep)
}

// had reports whether the qory directory is there before a remove takes it, so the
// remove can say whether it removed anything. It is read before [removeDir] runs.
func had(at places) bool {
	_, err := os.Stat(at.dir)
	return err == nil
}

// removeDir removes the qory directory with its exclude line and says so when it was
// there. The line goes because the directory does: a stale line would hide a .qory the
// repository later adds. It stays while another worktree of the repository has a .qory of
// its own, since the exclude file is one for all of them and that worktree still needs
// the line; its own remove takes it.
func removeDir(u *ui.UI, at places) error {
	present := had(at)
	if err := os.RemoveAll(at.dir); err != nil {
		return err
	}
	if err := render.RemoveExclude(at.root, "/"+checkout.Dir); err != nil {
		return err
	}
	if present {
		u.Success("removed %s", ui.Short(at.dir, at.root))
	}
	return nil
}

// restoreHint tells the person how to get back the checkout's own files a compose replaced
// under --force, now that their links are gone. The paths are relative to the checkout
// root, so the command names the root when the person stands elsewhere.
func restoreHint(u *ui.UI, root string, replaced []string) {
	if len(replaced) == 0 {
		return
	}
	command := "git checkout -- " + strings.Join(replaced, " ")
	cwd, _ := os.Getwd()
	if here, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = here
	}
	if there, err := filepath.EvalSymlinks(root); err == nil && cwd != there {
		command = "git -C " + ui.Short(root, "") + " checkout -- " + strings.Join(replaced, " ")
	}
	u.Blank()
	u.Text("The compose replaced files of the checkout. To restore them:")
	u.Code(command)
}

// count is "1 module" and "2 modules", because a message that reads wrong makes a person
// doubt the number too.
func count(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
