package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/integrations/contracts"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/provisioner"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/report"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// A run from a run code takes everything but the run key from the documents of Qory
// Apiary's signed answer to the code: the harness it composes, the workflow it starts,
// the runtime and the compose flags, the inputs and the time limit. The harness is the
// workspace's harness document when the documents contain one, and otherwise the
// checkout's qory.yaml or harness.yaml. The documents are run/v1 documents.schema.json,
// checked as [provisioner.ReadDocuments] describes. So the person who starts the run
// gives none of them.

// codeUsed ends each line of a run from a run code that stops after the redemption: the
// one-use code cannot be redeemed again.
const codeUsed = "; the code is used"

// inputsFile is the file of a run code's inputs, in the run's inputs folder.
const inputsFile = "inputs.json"

// runAnswerDocuments is the documents of the verified run answer ans, the bytes as the
// server signed them.
func runAnswerDocuments(ans *accesskey.RunAnswer) json.RawMessage {
	return ans.Documents
}

// fromAnswerOnly refuses, as input, what a run from a run code takes from the answer to
// the code: -f, a runtime, the compose flags, arguments after -- and --timeout. It is
// checked before the redemption, so a run it stops leaves the code unused.
func fromAnswerOnly(cmd *cobra.Command, runtime string, extra []string) error {
	changed := func(names ...string) bool {
		for _, n := range names {
			if cmd.Flags().Changed(n) {
				return true
			}
		}
		return false
	}
	switch {
	case changed("file"):
		return input(errors.New("leave out -f: a run from a run code composes the harness in Qory Apiary's response to the code, or the checkout's qory.yaml or harness.yaml"))
	case runtime != "":
		return input(fmt.Errorf("leave out the runtime %s: a run from a run code takes its runtime from Qory Apiary's response to the code", accesskey.ShownName(runtime, false)))
	case changed("model", "effort", "tier", "tier-effort"):
		return input(errors.New("leave out --model, --effort, --tier and --tier-effort: a run from a run code takes them from Qory Apiary's response to the code"))
	case len(extra) > 0:
		return input(errors.New("leave out the arguments after --: a run from a run code starts its workflow with the arguments qory sets"))
	case changed("timeout"):
		return input(errors.New("leave out --timeout: a run from a run code takes its time limit from Qory Apiary's response to the code, else from its workflow"))
	}
	return nil
}

// codeRun is what a run from a run code takes from the documents of the answer to the
// code, checked: the runtime and the compose flags its run settings map to, the
// harness document, the workflow, the inputs as the documents have them, the run
// settings' time_limit_minutes, 0 for none, and harness_hidden. named is set when
// harness_hidden is true: no text of the run shows the address of a harness source.
// target is the documents' target and target_version, which the fetch uses for the
// target's repository.
type codeRun struct {
	runtime  string
	settings []provisioner.Setting
	// harness is the documents' harness document as JSON, which qory composes in place of
	// the checkout's file; nil when the documents contain none, and the run composes the
	// checkout's qory.yaml or harness.yaml.
	harness  []byte
	named    *source.Named
	workflow string
	inputs   []byte
	minutes  int
	hidden   bool
	target   preparation.Target
}

// The harness of a run from a run code as a text shows it: the documents' harness
// document, from the workspace's settings in Qory Apiary, or the checkout's qory.yaml or
// harness.yaml.
const (
	workspaceHarness = "the workspace's harness"
	checkoutHarness  = "the checkout's harness"
)

// inSettings is where the harness document of the documents is set.
const inSettings = "of the harness in the workspace's settings in Qory Apiary"

// harnessFile is the file in the run directory that qory writes the documents' harness
// document to, mode 0600, and composes as -f does.
const harnessFile = "harness.yaml"

// codeDocumentsOf reads documents, the documents of the answer to a run code, for a
// step qory runs: explore, which [codeRunOf] reads further, or prebuild, which
// [runCodePrebuild] runs. Each error is exit 1 and ends with [codeUsed].
func codeDocumentsOf(documents json.RawMessage) (contracts.Documents, error) {
	doc, err := provisioner.ReadDocuments(documents)
	switch {
	case err != nil:
		return doc, fmt.Errorf("update qory, or ask Qory Apiary's instance admin: the response to the run code does not pass qory's checks: %v%s", err, codeUsed)
	case doc.Step != provisioner.StepExplore && doc.Step != provisioner.StepPrebuild:
		return doc, fmt.Errorf("update qory: the response to the run code is for the step %s, and qory runs a run code's run for the steps explore and prebuild only%s", doc.Step, codeUsed)
	}
	return doc, nil
}

// codeRunOf reads doc, the documents of the answer to a run code for the step explore.
// Each error ends with [codeUsed]: a source of the harness document that qory does not
// fetch is an input error, see [harnessChecked], and every other error is exit 1.
func codeRunOf(doc contracts.Documents) (*codeRun, error) {
	if doc.Workflow == "" {
		return nil, errors.New("set a workflow for the workspace in Qory Apiary, then take the work item in again: the response to the run code has no workflow" + codeUsed)
	}
	hidden := doc.HarnessHidden != nil && *doc.HarnessHidden
	var harness []byte
	if doc.Harness != nil {
		if err := harnessChecked(doc.Harness, hidden); err != nil {
			return nil, err
		}
		var err error
		if harness, err = json.MarshalIndent(doc.Harness, "", "  "); err != nil {
			return nil, fmt.Errorf("update qory: qory could not write the harness of the response to the run code: %v%s", err, codeUsed)
		}
		harness = append(harness, '\n')
	}
	var minutes int
	if doc.RunSettings != nil {
		minutes = doc.RunSettings.TimeLimitMinutes
	}
	runtime, settings := provisioner.Settings(doc.RunSettings)
	if !isClaude(runtime) {
		return nil, fmt.Errorf("set the runtime claude for the workspace in Qory Apiary, then take the work item in again: qory starts a run code's workflow on claude alone, and the response to the run code sets the runtime %s%s", accesskey.ShownName(runtime, false), codeUsed)
	}
	var target preparation.Target
	if doc.Target != nil {
		target = preparation.Target{System: doc.Target.System, Path: doc.Target.Path, Version: doc.TargetVersion}
	}
	r := &codeRun{runtime: runtime, settings: settings, harness: harness, workflow: doc.Workflow, inputs: doc.Inputs, minutes: minutes, hidden: hidden, target: target}
	if hidden {
		r.named = source.NewNamed(r.phrase())
	}
	return r, nil
}

// harnessChecked stops h, the documents' harness document, before anything is fetched,
// when a source of it is one that qory does not fetch: a folder, {path}; a URL with a
// scheme that has a user part, a user name or a password, or an @ between :// and the
// first / after it; one that url.Parse cannot read; one whose host has a character
// outside ASCII; one that is not https; and a path that is absolute, the repository's
// root, or leads out of the repository. The schema of the documents allows no folder;
// this check does not depend on it. Each error is an input error and starts with the
// fix in the workspace's settings in Qory Apiary. No error shows a path. Only the error
// of a URL that is not https shows the URL, and only when the harness is not hidden:
// then an scp form such as git@host:path shows with its user.
func harnessChecked(h *contracts.HarnessDocument, hidden bool) error {
	if err := sourceChecked("extends", h.Harness.Extends.Git, h.Harness.Extends.Path, hidden); err != nil {
		return err
	}
	for i, m := range h.Harness.Modules {
		what := fmt.Sprintf("modules[%d]", i)
		if m.Name != "" {
			what = "the module " + accesskey.ShownName(m.Name, false)
		}
		if err := sourceChecked(what, m.Source.Git, m.Source.Path, hidden); err != nil {
			return err
		}
	}
	return nil
}

// sourceChecked is [harnessChecked] for one source, what, such as extends or the module
// tools, whose git URL is git and whose path in its repository is folder.
func sourceChecked(what, git, folder string, hidden bool) error {
	what += " " + inSettings
	if git == "" {
		return input(fmt.Errorf("set %s to an https source: it is a folder, and a run from a run code reads no folder of this machine%s", what, codeUsed))
	}
	if strings.Contains(git, "://") {
		u, err := url.Parse(git)
		switch {
		case stack.AtBeforePath(git) || err == nil && u.User != nil:
			return input(fmt.Errorf("remove the user name and password from the URL of %s: the run's stack proxy sets the harness credentials of the run configuration, so a source's URL needs none%s", what, codeUsed))
		case err != nil:
			return input(fmt.Errorf("correct the URL of %s: qory could not read it as a URL%s", what, codeUsed))
		case stack.NonASCIIHost(git):
			return input(fmt.Errorf("write the host in the URL of %s in ASCII, with the xn-- form for an international name: the host has a character outside ASCII, which git may map to another, such as a fullwidth @ to @%s", what, codeUsed))
		}
	}
	if source.HTTPSFix(git, "") != "" {
		cause := "its URL is not an https URL with a host"
		if !hidden {
			cause = exports.URL(git) + " is not an https URL with a host"
		}
		return input(fmt.Errorf("set %s to an https source: %s, and qory fetches a harness only over https%s", what, cause, codeUsed))
	}
	if clean := filepath.Clean(folder); folder != "" && (filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))) {
		return input(fmt.Errorf("set the path of %s to a folder inside its repository: the path is absolute, the repository's root, or leads out of the repository%s", what, codeUsed))
	}
	return nil
}

// phrase is r's harness as a text shows it: [workspaceHarness] for a harness document,
// [checkoutHarness] for the checkout's file.
func (r *codeRun) phrase() string {
	if r.harness != nil {
		return workspaceHarness
	}
	return checkoutHarness
}

// writeHarness writes r's harness document to [harnessFile], mode 0600, in dir, the run
// directory, and returns the file's path.
func (r *codeRun) writeHarness(dir string) (string, error) {
	file := filepath.Join(dir, harnessFile)
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, err = f.Write(r.harness)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		return "", fmt.Errorf("check the run's folder %s: qory run could not write the workspace's harness there: %w%s", exports.Value(dir, false), exports.HideError(err), codeUsed)
	}
	return file, nil
}

// noHarness stops r, a run whose documents contain no harness document, in a checkout at
// root that has no qory.yaml or harness.yaml, before anything is fetched. Qory Apiary
// sends a run that composes a harness either a harness document or the version of a
// target whose harness file it read, so this stop is a guard.
func (r *codeRun) noHarness(root string) error {
	if r.harness != nil {
		return nil
	}
	if file, err := config.FileIn(root); err != nil || file != "" {
		// A checkout with both names gets the compose's error.
		return nil
	}
	return &runLineError{errors.New("take the work item in again in Qory Apiary: the run's documents contain no harness and the checkout has no qory.yaml or harness.yaml" + codeUsed)}
}

// apply sets the compose flags of r on cmd, as a person sets them on qory run.
func (r *codeRun) apply(cmd *cobra.Command) error {
	for _, s := range r.settings {
		if err := cmd.Flags().Set(s.Flag, s.Value); err != nil {
			return fmt.Errorf("update qory, or ask Qory Apiary's instance admin: the response to the run code sets --%s to a value that qory run does not take: %w%s", s.Flag, exports.HideValueError(err, s.Value), codeUsed)
		}
	}
	return nil
}

// givenMount is a mount of a walled run that wall.mounts in forager.yaml or --mount
// adds; from is which of the two adds it, as an error prints it.
type givenMount struct {
	path string
	from string
}

// reportExposed stops, as input, a walled run from a run code whose agent's container
// would get a harness report, which contains the address of each harness source. It
// stops a run whose harness is shown as well: through <cache>/qory, such a run would
// read the reports of the runs on this machine whose harness is hidden. The container
// gets each mount of given, the checkout at root and the working folder cwd, both
// writable, and the home, read-only. The run stops when a mount of given, the checkout
// or the working folder is, contains or lies inside qory's folder in the cache
// directory, <cache>/qory, where qory writes the home and harness report of each run
// with a run key or a run code and no --home or harness.home; when one of them is or
// contains report, this run's harness report, beside its home; and when the home is or
// contains <cache>/qory. A home that lies inside it, as the default one under
// <cache>/qory/homes does, passes: report is beside it, and the other runs' reports are
// not inside it. The paths are compared as Forager compares a mount, by
// [session.Overlap]: by file through links and in any letter case on a disk that
// ignores case. The cache folder passes when the lookup of the cache directory fails,
// as qory then writes no home under it. It runs before the harness is fetched and
// before the run writes its inputs, home or report; each error ends with [codeUsed].
func (r *codeRun) reportExposed(root, cwd, home, report string, given []givenMount) error {
	cache := ""
	if base, err := os.UserCacheDir(); err == nil {
		cache = filepath.Join(base, "qory")
	}
	shown := func(p string) string { return exports.Value(p, false) }
	overlap := func(mount, path string) string {
		if path == "" {
			return ""
		}
		return session.Overlap(mount, path)
	}
	for _, m := range given {
		if how := overlap(m.path, cache); how != "" {
			return input(fmt.Errorf("remove %s from %s: it %s %s, where qory writes the harness report of each run with a run key or a run code that has no --home or harness.home, and the agent's container has every mount%s", shown(m.path), m.from, how, shown(cache), codeUsed))
		}
		if how := overlap(m.path, report); how == "is" || how == "contains" {
			return input(fmt.Errorf("remove %s from %s: it %s %s, the harness report of this run, and the agent's container has every mount%s", shown(m.path), m.from, how, shown(report), codeUsed))
		}
	}
	if how := overlap(root, cache); how != "" {
		where := "outside"
		if how == "contains" {
			where = "that does not contain"
		}
		return input(fmt.Errorf("start the run in a checkout %s %s: the checkout %s is mounted into the agent's container, and qory writes the harness report of each run with a run key or a run code that has no --home or harness.home under %s%s", where, shown(cache), shown(root), shown(cache), codeUsed))
	}
	if how := overlap(root, report); how == "is" || how == "contains" {
		return input(fmt.Errorf("start the run in a checkout that does not contain %s: the checkout %s is mounted into the agent's container, and %s is the harness report of this run%s", shown(report), shown(root), shown(report), codeUsed))
	}
	if how := overlap(cwd, cache); how != "" {
		where := "outside"
		if how == "contains" {
			where = "that does not contain"
		}
		return input(fmt.Errorf("run qory from a folder %s %s: the working folder %s is mounted into the agent's container, and qory writes the harness report of each run with a run key or a run code that has no --home or harness.home under %s%s", where, shown(cache), shown(cwd), shown(cache), codeUsed))
	}
	if how := overlap(cwd, report); how == "is" || how == "contains" {
		return input(fmt.Errorf("run qory from a folder that does not contain %s: the working folder %s is mounted into the agent's container, and %s is the harness report of this run%s", shown(report), shown(cwd), shown(report), codeUsed))
	}
	if how := overlap(home, cache); how == "is" || how == "contains" {
		where := "other than"
		if how == "contains" {
			where = "that does not contain"
		}
		return input(fmt.Errorf("set --home or harness.home to a folder %s %s: the home %s is mounted read-only into the agent's container, and qory writes the harness report of each run with a run key or a run code that has no --home or harness.home under %s%s", where, shown(cache), shown(home), shown(cache), codeUsed))
	}
	return nil
}

// fetchTarget is the target the fetch of the run's repositories takes, as
// [preparation.RunAt] uses it: r's target and target_version, and none for a nil r, a
// run not from a run code.
func (r *codeRun) fetchTarget() preparation.Target {
	if r == nil {
		return preparation.Target{}
	}
	return r.target
}

// timedOutLine is the line of a run from a run code whose time limit, limit, came before
// the runtime started: [timedOutBeforeJoin] for a run that cannot take --timeout. A
// harness document always extends a stack; the checkout's file may extend none, and
// then only Qory Apiary sets the limit, since the harness section of a qory.yaml has no
// workflows.
func (r *codeRun) timedOutLine(limit time.Duration, runtime string) string {
	base := "the base of " + r.phrase()
	if r.harness == nil {
		base += ", when it has one"
	}
	return fmt.Sprintf("set a longer time_limit_minutes in Qory Apiary, or a longer workflows.%s.time_limit in %s: the run was cancelled at its time limit of %s, before %s started", r.workflow, base, limit, agentName(runtime))
}

// startLine is the line qory run prints once the answer to the run code is read.
func (r *codeRun) startLine(runID string) string {
	return fmt.Sprintf("qory run: the run %s starts the workflow %s of %s on %s", runID, r.workflow, r.phrase(), r.runtime)
}

// writeInputs writes r's inputs, unchanged, to inputs.json, mode 0600, in a new folder
// in/, mode 0700, of a new temporary folder, and returns in/, links resolved, and what
// removes the temporary folder. in/ is outside the state directory, which holds the run
// records that no mount may reach.
func (r *codeRun) writeInputs() (string, func(), error) {
	top, err := os.MkdirTemp("", "qory-run-")
	if err != nil {
		return "", func() {}, err
	}
	remove := func() { os.RemoveAll(top) }
	if top, err = filepath.EvalSymlinks(top); err != nil {
		return "", remove, err
	}
	in := filepath.Join(top, "in")
	if err := os.Mkdir(in, 0o700); err != nil {
		return "", remove, err
	}
	f, err := os.OpenFile(filepath.Join(in, inputsFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", remove, err
	}
	if _, err := f.Write(r.inputs); err != nil {
		f.Close()
		return "", remove, err
	}
	return in, remove, f.Close()
}

// workflowArgs are the arguments after -- that start r's workflow on the runtime name,
// as the harness rep was composed for it, with the inputs in the folder in: Claude
// Code's JSON lines, in/ added to its folders, and the prompt that invokes the skill or
// command named like the workflow, by the name the runtime registers it under, with
// the path of inputs.json. A harness with no such skill or command stops the run.
func (r *codeRun) workflowArgs(name string, rep report.Report, in string) ([]string, error) {
	rt, err := render.Lookup(name)
	if err != nil {
		return nil, input(err)
	}
	addresses := render.Addresses(entryKeys(rep), rep.Bind, render.AddressOf(rt), rt.Skips())
	registered, ok := addresses["skills"][r.workflow]
	if !ok {
		registered, ok = addresses["commands"][r.workflow]
	}
	if !ok {
		return nil, fmt.Errorf("add a skill or command %s to %s, or set another workflow in Qory Apiary: the composed harness has no skill or command %s%s", r.workflow, r.phrase(), r.workflow, codeUsed)
	}
	prompt := "/" + registered + " " + filepath.Join(in, inputsFile)
	return []string{"--output-format", "stream-json", "--verbose", "--add-dir", in, "-p", prompt}, nil
}

// entryKeys are rep's entries as <kind>/<name>, the form [render.Addresses] takes.
func entryKeys(rep report.Report) []string {
	keys := make([]string, 0, len(rep.Entries))
	for _, e := range rep.Entries {
		keys = append(keys, e.Kind+"/"+e.Name)
	}
	return keys
}
