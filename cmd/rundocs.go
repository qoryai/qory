package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/integrations/contracts"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/provisioner"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/report"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/runtimeinstall"
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

// codeUsedStartStage is [codeUsed] for a line of a run from a run code whose fix is
// Forager's detail at the start of the line: the one-use code cannot be redeemed again,
// so the stage starts again only from the work item in Qory Apiary.
const codeUsedStartStage = codeUsed + ", so start the stage again from the work item in Qory Apiary"

// inputsFile is the file of a run code's inputs, in the run's inputs folder.
const inputsFile = "inputs.json"

// The lines of [checkInputs]: inputs that have the member [inputsMember], which qory
// adds, and inputs that are not a JSON object, which qory cannot add it to.
const (
	textInputsHaveQory    = "remove the input " + inputsMember + ": qory adds that member to inputs.json"
	textInputsNotAnObject = "ask Qory Apiary's instance admin: the run code's inputs are not a JSON object"
)

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
		return input(errors.New("leave out --timeout: a run from a run code takes its time limit from Qory Apiary"))
	}
	return nil
}

// codeRun is what a run from a run code takes from the documents of the answer to the
// code, checked: the runtime and the compose flags its run settings map to, the
// harness document, the workflow, the inputs as the documents have them, the
// documents' time_limit_minutes, the limit of session 1, 0 for none, and
// harness_hidden. named is set when harness_hidden is true: no text of the run shows
// the address of a harness source.
// target is the address of the documents' target and target_version, which the fetch
// uses for the target's repository, and empty for a target with no address, with the
// address of each read target that has one, see [preparation.Target.Reads]. workItem is
// the id of the documents' subject when the subject is a work item, and "" for a subject
// of another kind or none.
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
	// fullSuite is full_suite of the documents' type_settings, see [fullSuiteIn]: whether
	// session 1 runs the full suite, and for a later session, full_suite of the
	// type_settings of its next-session answer.
	fullSuite bool
	// harnessDoc is the documents' harness document, which [codeRun.next] writes again
	// for the runtime of a later session; nil for none.
	harnessDoc *contracts.HarnessDocument
	// runSettings is the documents' run_settings, whose runtime a later session runs on
	// when its model choice has no runtime.
	runSettings *contracts.RunSettings
	// shared is the documents' shared_run: qory sends the next-session request after each
	// session and runs the session that the answer sets.
	shared bool
	// modelCredential is the model key the session joins with, the
	// model_provider_credential_name of a later session's next-session answer; "" for the
	// run's default model provider credential, as session 1 has.
	modelCredential string
	minutes         int
	hidden          bool
	target          preparation.Target
	// access is what the run may do on its target, from the documents' access: read,
	// write, or write-no-push (a writable checkout with no push step). It is empty when
	// the documents have no target.
	access   string
	workItem string
	// deliver is the documents' deliver, the type and the send of each artifact the run
	// delivers: the one place qory takes them from, never the composed harness's
	// process.yaml, see [codeRun.deliveredTypes] and [codeRun.deliverLines]. nil when the
	// documents have none.
	deliver []contracts.Deliver

	// runNumber is the run's number on the server, and snapshot the work item snapshot
	// that the run read at its start, nil for none: [codeRun.readWorkItem] sets both.
	runNumber int
	snapshot  *runapi.Snapshot
	// targetRef is the documents' target, nil for none: the target of a delivered output.
	targetRef *contracts.Target
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

// textDocumentsBroken is the line of documents that do not pass qory's checks, with the
// cause and [codeUsed]; a [*provisioner.BrokenRule] adds its detail on line 2.
const textDocumentsBroken = "update qory, or ask Qory Apiary's instance admin: the response to the run code does not pass qory's checks: %v%s"

// codeDocumentsOf reads documents, the documents of the answer to a run code, for a
// step qory runs: workflow, which [codeRunOf] reads further, or prebuild, which
// [runCodePrebuild] runs. The schema allows these two steps alone, so the check of the
// step is a guard. Each error is exit 1. Each ends with [codeUsed], except
// [textNoRuntime] and the error of another step, whose first lines end with it. Workflow
// documents start an agent session, so they require run settings with a runtime.
// Documents with no run settings, or with run settings whose runtime is missing, empty or
// null, get that refusal before the schema check, see [lacksRuntime].
func codeDocumentsOf(documents json.RawMessage) (contracts.Documents, error) {
	if lacksRuntime(documents) {
		return contracts.Documents{}, noRuntime()
	}
	doc, err := provisioner.ReadDocuments(documents)
	var broken *provisioner.BrokenRule
	switch {
	case errors.As(err, &broken):
		return doc, fmt.Errorf(textDocumentsBroken+"\n  %s", err, codeUsed, broken.Detail)
	case err != nil:
		return doc, fmt.Errorf(textDocumentsBroken, err, codeUsed)
	case doc.Step != provisioner.StepWorkflow && doc.Step != provisioner.StepPrebuild:
		return doc, fmt.Errorf("update qory: the run code's response has a step qory does not run%s.\n  qory runs a run code's run for the steps workflow and prebuild only, and the step is %s.", codeUsed, integration.Printable(exports.Value(doc.Step, false)))
	}
	return doc, nil
}

// codeRunOf reads doc, the documents of the answer to a run code for the step workflow.
// Each error ends with [codeUsed], [textNoRuntime] on its first line: a source of the
// harness document that qory does not fetch is an input error, see [harnessChecked], and
// every other error is exit 1.
func codeRunOf(doc contracts.Documents) (*codeRun, error) {
	if doc.Workflow == "" {
		return nil, errors.New("set a workflow for the workspace in Qory Apiary, then take the work item in again: the response to the run code has no workflow" + codeUsed)
	}
	if err := checkInputs(doc.Inputs); err != nil {
		return nil, err
	}
	hidden := doc.HarnessHidden != nil && *doc.HarnessHidden
	runtime, settings := provisioner.Settings(doc.RunSettings)
	// A workflow runs on the runtime of the workspace or of its workflow settings, which
	// Qory Apiary sends in run_settings, required in workflow documents; qory sets no
	// default runtime. codeDocumentsOf refuses such documents first; this check covers
	// documents that reach codeRunOf another way.
	if runtime == "" {
		return nil, noRuntime()
	}
	if !slices.Contains(runRuntimes(), runtime) {
		return nil, notRunRuntime(runtime)
	}
	if doc.Harness != nil {
		if err := harnessChecked(doc.Harness, hidden); err != nil {
			return nil, err
		}
	}
	harness, err := harnessFor(doc.Harness, runtime)
	if err != nil {
		return nil, err
	}
	// The fetch reads the addresses alone: a target with no address, such as a place that
	// is not on a host, is not fetched, and its target_version is not used; a read target
	// with no address is not fetched, and the run goes on.
	target := preparation.Target{FromCode: true}
	if doc.Target != nil && doc.Target.Address != nil {
		target.System, target.Path, target.Version = doc.Target.Address.System, doc.Target.Address.Path, doc.TargetVersion
	}
	for _, read := range doc.ReadTargets {
		if read.Address != nil {
			target.Reads = append(target.Reads, preparation.Read{System: read.Address.System, Path: read.Address.Path})
		}
	}
	r := &codeRun{targetRef: doc.Target, runtime: runtime, settings: settings, harness: harness, harnessDoc: doc.Harness, runSettings: doc.RunSettings, shared: doc.SharedRun, workflow: doc.Workflow, inputs: doc.Inputs, minutes: doc.TimeLimitMinutes, fullSuite: fullSuiteIn(doc.TypeSettings), hidden: hidden, target: target, access: doc.Access}
	if doc.Subject != nil && doc.Subject.Kind == provisioner.SubjectWorkItem {
		r.workItem = doc.Subject.ID
	}
	if hidden {
		r.named = source.NewNamed(r.phrase())
	}
	r.deliver = doc.Deliver
	return r, nil
}

// fullSuiteIn is full_suite of settings, the type_settings of the documents or of a
// next-session answer: false when settings is empty, has no full_suite, or has one that
// is not a boolean. qory checks type_settings against no schema: Qory Apiary checks it
// against the schema of the workspace type's process when it writes it, and qory checks
// that the schema compiles when a command reads the configuration.
func fullSuiteIn(settings json.RawMessage) bool {
	var members map[string]any
	if json.Unmarshal(settings, &members) != nil {
		return false
	}
	full, _ := members["full_suite"].(bool)
	return full
}

// harnessFor is h, the documents' harness document, as qory writes it for a session of
// runtime, nil for no document: each workflow with its access, plan and tier, and, for a
// section that defines a stack, target.runtime set to runtime.
func harnessFor(h *contracts.HarnessDocument, runtime string) ([]byte, error) {
	if h == nil {
		return nil, nil
	}
	var written any = h
	if h.Harness.Extends == nil {
		written = definedHarness{Harness: definedSection{HarnessSection: h.Harness, Target: definedTarget{Runtime: runtime}}}
	}
	harness, err := json.MarshalIndent(written, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("update qory: qory could not write the harness of the response to the run code: %v%s", err, codeUsed)
	}
	return append(harness, '\n'), nil
}

// next makes r the run of the session that a, a next-session answer of Qory Apiary's,
// starts: its runtime and compose flags, from a's model choice, else the runtime of the
// documents' run settings, see [provisioner.SettingsOf]; its model key, workflow,
// inputs, time limit and the full_suite of its type_settings; and the harness document
// written for its runtime. A runtime that a run code does not run on, and inputs that
// [checkInputs] stops, stop the session before anything of it starts.
func (r *codeRun) next(a contracts.NextSessionAnswer) error {
	runtime, settings := provisioner.SettingsOf(a.ModelChoice, r.runSettings)
	if !slices.Contains(runRuntimes(), runtime) {
		return notRunRuntime(runtime)
	}
	if err := checkInputs(a.Inputs); err != nil {
		return err
	}
	harness, err := harnessFor(r.harnessDoc, runtime)
	if err != nil {
		return err
	}
	r.runtime, r.settings, r.harness, r.modelCredential = runtime, settings, harness, a.ModelProviderCredentialName
	r.workflow, r.inputs, r.minutes, r.fullSuite = a.Workflow, a.Inputs, a.TimeLimitMinutes, fullSuiteIn(a.TypeSettings)
	return nil
}

// writes reports whether the documents of the run r give it the access to write and push
// to its target: its access is [stack.AccessWrite]. The workflow's access can lower it,
// see [accessOf]. A run not from a run code has no access, and neither does a run
// whose documents have no target.
func (r *codeRun) writes() bool {
	return r != nil && r.access == stack.AccessWrite
}

// definedHarness is a harness document whose section defines a stack, as qory writes it
// to [harnessFile]: the section with target.runtime set to the run's runtime. The
// documents' section has no target, and the compose of a stack that a section defines
// requires one; the run's runtime is the one the compose takes, as --runtime sets it.
type definedHarness struct {
	Harness definedSection `json:"harness"`
}

// definedSection is the section of a [definedHarness].
type definedSection struct {
	contracts.HarnessSection
	Target definedTarget `json:"target"`
}

// definedTarget is the target of a [definedSection].
type definedTarget struct {
	Runtime string `json:"runtime"`
}

// harnessChecked stops h, the documents' harness document, before anything is fetched,
// when a source of it is one that qory does not fetch: a folder, {path}; a URL with a
// scheme that has a user part, a user name or a password, or an @ between :// and the
// first / after it; one that url.Parse cannot read; one whose host has a character
// outside ASCII; one that is not https; and a path that is absolute, the repository's
// root, or leads out of the repository. A module with no source and an exclude, beside
// extends, is the form that excludes from a base module in a checkout's qory.yaml, and is
// stopped with an error that does not call it a folder. The schema of the documents
// allows no folder; this check does not depend on it. Each error is an input error and starts with the
// fix in the workspace's settings in Qory Apiary. No error shows a path. Only the error
// of a URL that is not https shows the URL, and only when the harness is not hidden:
// then an scp form such as git@host:path shows with its user.
func harnessChecked(h *contracts.HarnessDocument, hidden bool) error {
	if b := h.Harness.Extends; b != nil {
		if err := sourceChecked("extends", b.Git, b.Path, hidden); err != nil {
			return err
		}
	}
	for i, m := range h.Harness.Modules {
		what := fmt.Sprintf("modules[%d]", i)
		if m.Name != "" {
			what = "the module " + accesskey.ShownName(m.Name, false)
		}
		if h.Harness.Extends != nil && m.Source == (contracts.HarnessSource{}) && m.Exclude != nil {
			// The form of a checkout's qory.yaml that excludes from a module of the base;
			// a module of a harness document has a source.
			return input(fmt.Errorf("give %s %s an https source: a module of a harness document has a source, so the document cannot exclude entries from a module of the stack it extends%s", what, inSettings, codeUsed))
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

// applyTo sets the compose flags of r on o for a later session of the run, as [codeRun.apply]
// sets them on the command for session 1: each flag that r's settings do not set is
// cleared, so no model, effort or tier of an earlier session carries over.
func (r *codeRun) applyTo(o *composeOptions) {
	o.model, o.effort, o.tiers, o.tierEfforts = "", "", nil, nil
	for _, s := range r.settings {
		switch s.Flag {
		case "model":
			o.model = s.Value
		case "effort":
			o.effort = s.Value
		case "tier":
			o.tiers = append(o.tiers, s.Value)
		case "tier-effort":
			o.tierEfforts = append(o.tierEfforts, s.Value)
		}
	}
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
// gets each mount of given and the home, read-only; [codeRun.readExposed] checks the
// checkout and the working folder of a read run. The run stops when a mount of given is,
// contains or lies inside qory's folder in the cache directory, <cache>/qory, where qory
// writes the home and harness report of each run with a run key or a run code and no
// --home or harness.home; when one of them is or contains report, this run's harness
// report, beside its home; and when the home is or contains <cache>/qory. A home that lies inside it, as the default one under
// <cache>/qory/homes does, passes: report is beside it, and the other runs' reports are
// not inside it. The paths are compared as Forager compares a mount, by
// [session.Overlap]: by file through links and in any letter case on a disk that
// ignores case. The cache folder passes when the lookup of the cache directory fails,
// as qory then writes no home under it. It runs before the harness is fetched and
// before the run writes its inputs, home or report; each error ends with [codeUsed].
func (r *codeRun) reportExposed(home, report string, given []givenMount) error {
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
	if how := overlap(home, cache); how == "is" || how == "contains" {
		where := "other than"
		if how == "contains" {
			where = "that does not contain"
		}
		return input(fmt.Errorf("set --home or harness.home to a folder %s %s: the home %s is mounted read-only into the agent's container, and qory writes the harness report of each run with a run key or a run code that has no --home or harness.home under %s%s", where, shown(cache), shown(home), shown(cache), codeUsed))
	}
	return nil
}

// The texts of a walled session that a configured mount is at or inside the read-only
// inputs folder of: the cause alone for a run from a run code, the fix first for a
// person's run with a run key and no run code.
const (
	textInputsMounted    = "A wall mount is at or inside the read-only inputs folder, so the session did not start.\n  Mount %s.\n  Inputs folder %s."
	textInputsMountedFix = "Remove the mount at or inside the read-only inputs folder from wall.mounts or --mount.\n  Mount %s.\n  Inputs folder %s."
)

// inputsMounted stops a walled session of r when a mount of given is in, the run's inputs
// folder, or lies inside it, as [session.Overlap] compares them: the agent's container
// would see that mount in place of part of the read-only folder, such as the session's
// start artifacts. A mount that contains in passes, as the read-only mount of in lies
// over it. For a run from a run code the error closes the run machine_setup_invalid; a
// run with a run key and no run code, r nil, gets the fix and no end code.
func (r *codeRun) inputsMounted(in string, given []givenMount) error {
	for _, m := range given {
		if how := session.Overlap(m.path, in); how != "is" && how != "lies inside" {
			continue
		}
		mount, folder := exports.Value(m.path, false), exports.Value(in, false)
		if r == nil {
			return &runLineError{fmt.Errorf(textInputsMountedFix, mount, folder)}
		}
		return &runLineError{&reasonError{code: string(contracts.EndCodeMachineSetupInvalid), text: fmt.Sprintf(textInputsMounted, mount, folder)}}
	}
	return nil
}

// The stops of [codeRun.readExposed], in the form of [reviewStop], each with the folder's
// kind, "checkout" or "working folder", and its path. The cache stop has how the folder
// overlaps the cache folder, which [session.Overlap] returns: "is", "contains" or "lies
// inside", and line 2 shows the cache folder. The report stop has the harness report that
// the folder contains, and its line 2 is the same for every folder.
const (
	textReadExposedCache        = "The %s %s %s %s, so the run did not start"
	textReadExposedCacheDetail  = "qory writes harness reports in %s"
	textReadExposedReport       = "The %s %s contains %s, so the run did not start"
	textReadExposedReportDetail = "That file is the harness report of this run, which qory writes"
)

// readExposed stops a run from the run code r that only reads the checkout, when the
// checkout at root or the working folder cwd, which the agent's container binds read-only,
// overlaps qory's cache folder or the harness report of the run. A run that may write works
// in a copy, and binds neither.
func (r *codeRun) readExposed(root, cwd, report string) error {
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
	if how := overlap(root, cache); how != "" {
		return input(reviewStop(fmt.Sprintf(textReadExposedCache, "checkout", shown(root), how, shown(cache)), fmt.Sprintf(textReadExposedCacheDetail, shown(cache))))
	}
	if how := overlap(root, report); how == "is" || how == "contains" {
		return input(reviewStop(fmt.Sprintf(textReadExposedReport, "checkout", shown(root), shown(report)), textReadExposedReportDetail))
	}
	if how := overlap(cwd, cache); how != "" {
		return input(reviewStop(fmt.Sprintf(textReadExposedCache, "working folder", shown(cwd), how, shown(cache)), fmt.Sprintf(textReadExposedCacheDetail, shown(cache))))
	}
	if how := overlap(cwd, report); how == "is" || how == "contains" {
		return input(reviewStop(fmt.Sprintf(textReadExposedReport, "working folder", shown(cwd), shown(report)), textReadExposedReportDetail))
	}
	return nil
}

// clonesDestinations reports whether the run from the run code r skips the fetch before
// its session, and its agent clones what it needs with qory destination clone: the
// documents' access is read, the documents contain a harness document, and the inputs list
// destinations. A run whose documents contain no harness document fetches its target, whose
// harness it composes; a run that may write fetches its target, works on its branch and
// pushes. False for a run with no run code. It reads the documents' access, not the level
// that the composed harness's workflow lowers it to, since the fetch comes before the
// compose.
func (r *codeRun) clonesDestinations() bool {
	return r != nil && r.access == stack.AccessRead && r.harness != nil && hasDestinations(r.inputs)
}

// fetchesTargetAlone reports whether the run from the run code r fetches its target and no
// read target: a read run whose inputs list destinations and whose documents contain no
// harness document, which fetches its target only for the harness of the target's
// repository, see [codeRun.clonesDestinations]. The agent clones any other destination with
// qory destination clone.
func (r *codeRun) fetchesTargetAlone() bool {
	return r != nil && r.access == stack.AccessRead && r.harness == nil && hasDestinations(r.inputs)
}

// fetchTarget is the target the fetch of the run's repositories takes, as
// [preparation.RunAt] uses it: r's target and target_version, and its read targets but with
// [codeRun.fetchesTargetAlone]; none for a nil r, a run not from a run code.
func (r *codeRun) fetchTarget() preparation.Target {
	if r == nil {
		return preparation.Target{}
	}
	t := r.target
	if r.fetchesTargetAlone() {
		t.Reads = nil
	}
	return t
}

// runList is the list of repositories that the fetch of run takes on its system, see
// [preparation.RunAt]: for a run from the run code r, the documents' target and read
// targets, as [preparation.Target.Repositories] lists them; for a run from no run code, the
// run configuration's [preparation.EnvRepositories], as [preparation.Check] reads it.
type runList struct {
	system string
	paths  []string
	// target is whether paths[0] is the run's repository, which goes into the working
	// folder: false for a list of read targets alone.
	target bool
	// err is the stop of the list's check, which the fetch stops on too.
	err error
	// code is whether the list is of a run code's documents, whose entries after the
	// target are read targets.
	code bool
}

// fetchList is the [runList] of run, from the run code r or from none.
func (r *codeRun) fetchList(run openedRun) runList {
	l := runList{system: run.System()}
	switch {
	case r != nil:
		l.code = true
		l.paths, l.target, l.err = r.fetchTarget().Repositories(l.system)
	case run.Repositories() != "":
		l.paths, l.err = preparation.Check(l.system, run.Repositories())
		l.target = l.err == nil
	}
	return l
}

// fetches reports whether the list has a repository to fetch, or a stop that the fetch
// reports.
func (l runList) fetches() bool { return len(l.paths) > 0 || l.err != nil }

// working reports whether the fetch of the list puts a repository into the working folder.
func (l runList) working() bool { return l.err == nil && l.target }

// setupValue is the value of [preparation.EnvRepositories] in the setup container: the
// list, target first, which qory run prepare reads, and "" for a list with no target's
// repository, since qory run prepare takes the first entry for the working folder's.
func (l runList) setupValue() string {
	if !l.working() {
		return ""
	}
	return strings.Join(l.paths, ",")
}

// sessionDeadline is the time at which a session whose clock starts at start reaches its
// time limit, limit, and the zero time for a limit of 0 or below, no limit. Each session's
// clock starts when its agent starts: qory run passes the time of session 1's join, so
// the steps before it, which [setupLimit] bounds, do not count. A caller for a later
// session passes that session's start.
func sessionDeadline(start time.Time, limit time.Duration) time.Time {
	if limit <= 0 {
		return time.Time{}
	}
	return start.Add(limit)
}

// sessionMinute is a minute of the documents' time_limit_minutes; a test sets a shorter one.
var sessionMinute = time.Minute

// sessionLimit is the time limit of the current session of the run r: time_limit_minutes
// of the documents for session 1, or of the next-session answer for a later session, see
// [codeRun.next], and 0, no limit, when they set none. qory sets no
// default: Qory Apiary writes a limit for each workflow, 180 minutes when the workspace
// stores none. The limit counts from the agent's start, see [sessionDeadline].
func (r *codeRun) sessionLimit() time.Duration {
	return time.Duration(r.minutes) * sessionMinute
}

// startLine is the line qory run prints once the answer to the run code is read.
func (r *codeRun) startLine(runID string) string {
	return fmt.Sprintf("qory run: the run %s starts the workflow %s of %s on %s", runID, r.workflow, r.phrase(), r.runtime)
}

// inputsDir makes a new folder in/, mode 0700, in a new temporary folder, for r's
// inputs.json, which [codeRun.writeInputs] writes once the harness is composed, and
// returns in/, links resolved, and what removes the temporary folder. in/ is outside the
// state directory, which holds the run records that no mount may reach. A nil r, a run
// with no run code, gets the same folder. The temporary
// folder begins with qory-inputs-: Forager reserves the qory-run-, qory-tool-, qory-wall-
// and qory-link- folders of the system's temporary directory, and the wall stops a run
// that mounts one.
func (r *codeRun) inputsDir() (string, func(), error) {
	top, err := os.MkdirTemp("", "qory-inputs-")
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
	return in, remove, nil
}

// inputsMember is the member of inputs.json that qory adds to a run's inputs, with
// [qoryInputs] as its value. Inputs that have it stop the run, see [codeRunOf].
const inputsMember = "qory"

// qoryInputs is the value of [inputsMember]: what qory adds to the inputs of a run from
// a run code, and all that inputs.json has for a run with a run key and no run code, or
// for a person's run whose harness has a submit program. Each field is one member, always
// written, except BranchBase, Workflow, ReviewSession and Outbox.
type qoryInputs struct {
	// HarnessExtensions is the harness report's extensions, {} for none.
	HarnessExtensions map[string]any `json:"harness_extensions"`
	// Outputs is the outputs of the run's stage, in the stage's order, [] for none, as
	// [codeRun.outputs] has them.
	Outputs []inputOutput `json:"outputs"`
	// Outbox is the absolute path of the session's outbox, see [outboxPath], and no member
	// for a run with no outbox.
	Outbox string `json:"outbox,omitempty"`
	// StartCommits is the commit each repository the run may push is at when the session
	// starts, [] for a run with no branch, and StartArtifacts the folder of the session's launch
	// snapshot, see [startArtifacts]. The push step's scope check reads the same values.
	StartCommits   []inputCommit `json:"start_commits"`
	StartArtifacts string        `json:"start_artifacts"`
	// BranchBase is the commit where the run's work branch was cut from the target's history,
	// with its tree id, the same in every session; nil, and no member, for a run with no
	// branch and for one with no or several merge bases, see [preparation.WorkBranch.Base].
	BranchBase *inputBase `json:"branch_base,omitempty"`
	// Workflow is the values of the session's workflow that the harness reads, see
	// [workflowInputs]; nil, and no member, for a run with no run code, which has no
	// workflow.
	Workflow *workflowInputs `json:"workflow,omitempty"`
	// ReviewSession is the files and folders of a review-changes session, see
	// [reviewSession]; nil, and no member, for any other session.
	ReviewSession *reviewSession `json:"review_session,omitempty"`
}

// inputCommit is one entry of [qoryInputs.StartCommits]: the repository's folder, as an
// absolute path, and its [preparation.WorkRepository.Commit].
type inputCommit struct {
	Path   string `json:"path"`
	Commit string `json:"commit"`
}

// inputBase is [qoryInputs.BranchBase]: a commit and the id of its tree.
type inputBase struct {
	Commit string `json:"commit"`
	Tree   string `json:"tree"`
}

// branchBase is the [qoryInputs.BranchBase] of branch; nil for no branch and for a branch with
// no base.
func branchBase(branch *preparation.WorkBranch) *inputBase {
	if branch == nil || branch.Base == nil {
		return nil
	}
	return &inputBase{Commit: branch.Base.Commit, Tree: branch.Base.Tree}
}

// startCommits is the [qoryInputs.StartCommits] of branch, whose repositories are in the
// working folder cwd, an absolute path; nil for no branch. A read target,
// [preparation.WorkRepository.ReadOnly], has no entry: the push and its checks skip it.
func startCommits(branch *preparation.WorkBranch, cwd string) []inputCommit {
	if branch == nil {
		return nil
	}
	out := []inputCommit{}
	for _, r := range branch.Repositories {
		if !r.ReadOnly {
			out = append(out, inputCommit{Path: filepath.Join(cwd, filepath.FromSlash(r.Dir)), Commit: r.Commit})
		}
	}
	return out
}

// inputOutput is one output of the run's stage in [qoryInputs]: its type, whether the
// stage requires it, and the type's JSON Schema from the composed harness's process.yaml
// with the primitives written out, null for a type with none there.
type inputOutput struct {
	Type     string          `json:"type"`
	Required bool            `json:"required"`
	Schema   json.RawMessage `json:"schema"`
}

// outputs is the outputs of the stage of r's work item, from the work item snapshot of
// [codeRun.readWorkItem], each with the schema of its type in types, the artifact types of
// the composed harness: nil for a run with no snapshot.
// shortcut: a type that the composed process.yaml does not define, such as a standard
// type, has the schema null, as qory has no copy of a standard type's schema; replace it
// when the run API gives each output's schema.
func (r *codeRun) outputs(types []exports.ProcessType) []inputOutput {
	if r == nil || r.snapshot == nil {
		return nil
	}
	var out []inputOutput
	for _, o := range r.snapshot.Outputs {
		entry := inputOutput{Type: o.Type, Required: o.Required, Schema: json.RawMessage("null")}
		for _, t := range types {
			if t.Name == o.Type && t.Schema != nil {
				entry.Schema = t.Schema
			}
		}
		out = append(out, entry)
	}
	return out
}

// writtenTypes is the types of the outputs of the stage of r's work item: the types that
// its sessions write, nil for a run with no snapshot, which writes none.
func (r *codeRun) writtenTypes() []string {
	if r == nil || r.snapshot == nil {
		return nil
	}
	var types []string
	for _, o := range r.snapshot.Outputs {
		types = append(types, o.Type)
	}
	return types
}

// checkOutputTypes stops the run of r on a type of types, the artifact types of the
// composed harness, whose json_schema does not convert, when the stage's outputs or the
// documents' deliver list the type: the outputs of such a type would go unchecked. nil for r
// nil, a run with a run key and no run code.
func (r *codeRun) checkOutputTypes(types []exports.ProcessType) error {
	if r == nil {
		return nil
	}
	listed := map[string]bool{}
	if r.snapshot != nil {
		for _, o := range r.snapshot.Outputs {
			listed[o.Type] = true
		}
	}
	for _, d := range r.deliver {
		listed[d.Type] = true
	}
	for _, t := range types {
		if t.Unread != "" && listed[t.Name] {
			return fmt.Errorf(textSchemaUnread, shownText(provisioner.ShownType(t.Name)), shownText(t.Unread))
		}
	}
	return nil
}

// deliveredTypes is types, the artifact types of the composed harness, with the send of each
// from r's deliver, the documents' deliver: a type that deliver does not list has no send. A
// type that deliver lists and types do not define is [textDeliverNoType]: qory has no schema
// to check its output against and no marks to fill. The harness's process.yaml sends nothing
// on a run from a run code.
func (r *codeRun) deliveredTypes(types []exports.ProcessType) ([]exports.ProcessType, error) {
	out := make([]exports.ProcessType, 0, len(types))
	for _, t := range types {
		t.Send = ""
		out = append(out, t)
	}
	if r == nil {
		return out, nil
	}
	for _, d := range r.deliver {
		i := slices.IndexFunc(out, func(t exports.ProcessType) bool { return t.Name == d.Type })
		if i < 0 {
			shown := shownText(provisioner.ShownType(d.Type))
			return nil, fmt.Errorf(textDeliverNoType, shown, shown)
		}
		out[i].Send = d.Send
	}
	return out, nil
}

// harnessTypes is the artifact types of the composed harness rep: those of the process.yaml
// of the first module in stack order that has one, and found false when no module has one.
func harnessTypes(rep report.Report) (types []exports.ProcessType, found bool) {
	for _, m := range rep.Modules {
		if types, found = exports.ProcessTypes(filepath.Join(rep.Home, "modules", m.Name)); found {
			return types, true
		}
	}
	return nil, false
}

// writeInputs writes inputs.json, mode 0600, into in, the folder of [codeRun.inputsDir]:
// r's inputs as they came, with the member [inputsMember] set to q added after the
// others. It returns the file's path and the bytes it wrote, which a copy of the file
// takes from memory: a read of the file could return what something else wrote there. A nil
// r, a run with no run code, has no inputs, so the file has the member qory alone.
func (r *codeRun) writeInputs(in string, q qoryInputs) (string, []byte, error) {
	inputs := []byte("{}")
	if r != nil {
		inputs = r.inputs
	}
	data, err := withQoryMember(inputs, q)
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(in, inputsFile)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", nil, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", nil, err
	}
	return path, data, f.Close()
}

// copyInputs writes data, the bytes of the run's inputs.json, to inputs.json in home,
// the composed home, mode 0644 whatever the umask, so the run's user can read it as
// $QORY_HARNESS_HOME/inputs.json. A file of that name in the home stops the run and stays
// as it was. When a step after the file's creation fails, copyInputs removes the file it
// created, so the next run in a given home finds none.
func copyInputs(home string, data []byte) error {
	return copyInputsVia(home, data, fillCopy)
}

// copyInputsVia is [copyInputs] with fill, the steps that set the mode of the new file f
// and write data to it, as an argument, so a test can make them fail.
func copyInputsVia(home string, data []byte, fill func(f *os.File, data []byte) error) error {
	path := filepath.Join(home, inputsFile)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	err = fill(f, data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

// fillCopy sets the mode of f to 0644 whatever the umask and writes data to it.
func fillCopy(f *os.File, data []byte) error {
	if err := f.Chmod(0o644); err != nil {
		return err
	}
	_, err := f.Write(data)
	return err
}

// withQoryMember is inputs, a JSON object, with the member [inputsMember] set to q added
// after its last member. The bytes of inputs from its opening brace to its last member
// stay as they came, a nil map of q is written as {}, and nil outputs as [].
func withQoryMember(inputs []byte, q qoryInputs) ([]byte, error) {
	if q.HarnessExtensions == nil {
		q.HarnessExtensions = map[string]any{}
	}
	if q.Outputs == nil {
		q.Outputs = []inputOutput{}
	}
	if q.StartCommits == nil {
		q.StartCommits = []inputCommit{}
	}
	value, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(inputs)
	if !json.Valid(trimmed) || len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil, errors.New("the inputs are not a JSON object")
	}
	body := trimmed[:len(trimmed)-1]
	out := append([]byte{}, body...)
	if len(bytes.TrimSpace(body[1:])) > 0 {
		out = append(out, ',')
	}
	out = append(out, `"`+inputsMember+`":`...)
	out = append(out, value...)
	return append(out, '}', '\n'), nil
}

// checkInputs stops a run whose inputs are not a JSON object, or have a member
// [inputsMember] at the top level, before the run opens. Each error ends with
// [codeUsed].
func checkInputs(inputs []byte) error {
	var members map[string]json.RawMessage
	if json.Unmarshal(inputs, &members) != nil || members == nil {
		return errors.New(textInputsNotAnObject + codeUsed)
	}
	if _, ok := members[inputsMember]; ok {
		return errors.New(textInputsHaveQory + codeUsed)
	}
	return nil
}

// reasonWorkflowNotInHarness is the reason a run from a run code closes with, failed,
// when the composed harness has no skill or command of the workflow, under either name
// of [stack.WorkflowEntries].
const reasonWorkflowNotInHarness = "workflow_not_in_harness"

// workflowArgs are the arguments after -- that start r's workflow on the runtime name,
// as the harness rep was composed for it, with the inputs in the folder in: the
// runtime's headless start, see [render.Headless], with the prompt that invokes the
// workflow's skill or command, by the name the runtime registers it under, with the path
// of inputs.json. It tries skill workflow-<name>, command workflow-<name>, skill <name>,
// then command <name>, as [stack.WorkflowEntries] lists the names, and takes the first
// that the runtime can start. A runtime with no headless start, a workflow that is only
// of a kind the runtime renders or invokes by no name, and a harness with no such skill
// or command, stop the run. The last is a [*reasonError] of
// [reasonWorkflowNotInHarness], whose line shows the harness as [codeRun.harnessAt]
// does for system, the system the run's repositories are on.
func (r *codeRun) workflowArgs(name string, rep report.Report, in, system string) ([]string, error) {
	rt, err := render.Lookup(name)
	if err != nil {
		return nil, input(err)
	}
	h, ok := rt.(render.Headless)
	if !ok {
		return nil, notRunRuntime(name)
	}
	// Every kind, the skipped ones too, so a workflow of a kind the runtime does not
	// render gets the line with the kind it does invoke.
	addresses := render.Addresses(entryKeys(rep), rep.Bind, render.AddressOf(rt), nil)
	var found []string
	entries := stack.WorkflowEntries(r.workflow)
	for _, entry := range entries {
		for _, kind := range []string{"skills", "commands"} {
			registered, ok := addresses[kind][entry]
			if !ok {
				continue
			}
			if !slices.Contains(rt.Skips(), kind) {
				if args, ok := h.HeadlessArgs(kind, registered, filepath.Join(in, inputsFile), in); ok {
					return args, nil
				}
			}
			found = append(found, kind)
		}
	}
	prefixed, plain := entries[0], entries[1]
	if len(found) > 0 {
		add, runsNo := "command", "skill"
		if found[0] == "commands" {
			add, runsNo = "skill", "command"
		}
		return nil, fmt.Errorf("add a %s %s to %s, or set another runtime in Qory Apiary: %s runs no %s by its name from a headless start, and the composed harness has no %s %s or %s%s", add, prefixed, r.phrase(), accesskey.ShownName(name, false), runsNo, add, prefixed, plain, codeUsed)
	}
	add := "skill"
	if _, ok := h.HeadlessArgs("skills", prefixed, "", ""); !ok {
		add = "command"
	}
	text := fmt.Sprintf("add a %s %s to %s, or set another workflow in Qory Apiary: the composed harness has no skill or command %s or %s (%s)%s", add, prefixed, r.harnessAt(system), prefixed, plain, reasonWorkflowNotInHarness, codeUsed)
	return nil, &reasonError{code: reasonWorkflowNotInHarness, text: text}
}

// harnessAt is r's harness as the line of a missing workflow shows it: [codeRun.phrase],
// and for the checkout's harness the path of the documents' target after it. The target
// is "the repository" when it is on system, the system the run's repositories are on,
// which the run's fetch puts in the working folder, and "the target" on any other.
func (r *codeRun) harnessAt(system string) string {
	if r.harness != nil || r.target.Path == "" {
		return r.phrase()
	}
	path := integration.Printable(exports.Value(r.target.Path, false))
	if system != "" && r.target.System == system {
		return checkoutHarness + " in the repository " + path
	}
	return checkoutHarness + " for the target " + path
}

// textNoRuntime is the refusal of documents whose run settings name no runtime, with the
// runtimes that a run from a run code runs on, see [runRuntimes]. Its first line ends
// with [codeUsed] and a full stop.
const textNoRuntime = "The run settings set no runtime, so the run did not start" + codeUsed + ".\n  qory runs one of %s."

// noRuntime is the error of workflow documents with no run settings, or with run
// settings that set no runtime. It is exit 1.
func noRuntime() error {
	return fmt.Errorf(textNoRuntime, listOf(runRuntimes()))
}

// lacksRuntime reports whether documents are for the step workflow, which starts an agent
// session and so requires run settings with a runtime, and have no run_settings, a null
// one, or run settings with no runtime, an empty one or a null one. Any other form, such as run
// settings that are no object, is left to the schema check. A prebuild starts no agent
// session, and the schema refuses run settings in its documents.
func lacksRuntime(documents json.RawMessage) bool {
	var d struct {
		Step        string          `json:"step,case:strict"`
		RunSettings json.RawMessage `json:"run_settings,case:strict"`
	}
	if json.Unmarshal(documents, &d) != nil || d.Step != provisioner.StepWorkflow {
		return false
	}
	if len(d.RunSettings) == 0 || string(d.RunSettings) == "null" {
		return true
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal(d.RunSettings, &settings) != nil {
		return false
	}
	runtime, ok := settings["runtime"]
	return !ok || string(runtime) == `""` || string(runtime) == "null"
}

// runRuntimes are the runtimes that a run from a run code runs on, in alphabetical
// order: those that qory installs, see [runtimeinstall.Runtimes], that have a
// descriptor in Forager's runtimes.json, see [foragerList], and that qory renders with
// a headless start. The runtimes.json is embedded in the Forager module, and
// TestForagerAndQoryListTheSameRuntimes reads it, so an error reading it gives none. A
// test replaces the set with SetRunRuntimes.
var runRuntimes = installedHeadlessRuntimes

// installedHeadlessRuntimes is the set of [runRuntimes].
func installedHeadlessRuntimes() []string {
	described, _ := foragerList()
	var names []string
	for _, n := range runtimeinstall.Runtimes() {
		if !slices.ContainsFunc(described, func(rt foragerRuntime) bool { return rt.Name == n }) {
			continue
		}
		if rt, err := render.Lookup(n); err == nil {
			if _, ok := rt.(render.Headless); ok {
				names = append(names, n)
			}
		}
	}
	return names
}

// listOf is names as a list for a message: "claude and codex", or "claude, codex, and
// gemini".
func listOf(names []string) string {
	switch len(names) {
	case 0, 1:
		return strings.Join(names, "")
	case 2:
		return names[0] + " and " + names[1]
	}
	return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
}

// notRunRuntime is the error of a run from a run code on the runtime name, which is not
// one of [runRuntimes]. Only a dispatched run gets it, so it is the cause alone, with no
// fix line, since a fix line is a step for a person at a terminal, and Qory Apiary shows a
// dispatched run's output. It is on two lines that each stay within 100 characters with
// "qory run: ". It is exit 1 and ends with [codeUsed].
func notRunRuntime(name string) error {
	return fmt.Errorf("qory runs a run code's workflow on one of %s,\n  and not on %s%s", listOf(runRuntimes()), accesskey.ShownName(name, false), codeUsed)
}

// entryKeys are rep's entries as <kind>/<name>, the form [render.Addresses] takes.
func entryKeys(rep report.Report) []string {
	keys := make([]string, 0, len(rep.Entries))
	for _, e := range rep.Entries {
		keys = append(keys, e.Kind+"/"+e.Name)
	}
	return keys
}

// The lines of [codeRun.deliverLines], one for each type whose send differs between the
// documents' deliver and the harness's process.yaml. The run uses the documents' send.
const (
	textDeliverDiffers   = "deliver: %s at %s from the documents; harness has %s"
	textDeliverNoHarness = "deliver: %s at %s from the documents; harness has none"
	textDeliverNoDocs    = "deliver: no %s from the documents; harness has %s"
)

// deliverLines are the lines that show where the harness's deliver, of the first module in
// stack order that has a process.yaml, differs from r's deliver, which the run uses: one for
// each type, in the documents' order and then the harness's, with the type cut as
// [provisioner.ShownType] cuts it. They are nil for a run whose level, its [accessOf] level,
// is not [stack.AccessWrite], for a harness with no process.yaml, and for r nil.
func (r *codeRun) deliverLines(rep report.Report, level string) []string {
	if r == nil || level != stack.AccessWrite {
		return nil
	}
	types, found := harnessTypes(rep)
	if !found {
		return nil
	}
	harness := map[string]string{}
	var order []string
	for _, t := range types {
		if t.Send != "" {
			harness[t.Name] = t.Send
			order = append(order, t.Name)
		}
	}
	var lines []string
	listed := map[string]bool{}
	for _, d := range r.deliver {
		listed[d.Type] = true
		send, has := harness[d.Type]
		switch {
		case !has:
			lines = append(lines, fmt.Sprintf(textDeliverNoHarness, provisioner.ShownType(d.Type), d.Send))
		case send != d.Send:
			lines = append(lines, fmt.Sprintf(textDeliverDiffers, provisioner.ShownType(d.Type), d.Send, send))
		}
	}
	for _, name := range order {
		if !listed[name] {
			lines = append(lines, fmt.Sprintf(textDeliverNoDocs, provisioner.ShownType(name), harness[name]))
		}
	}
	return lines
}
