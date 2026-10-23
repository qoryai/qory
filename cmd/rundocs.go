package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/integrations/contracts"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/provisioner"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/report"
)

// A run from a run code takes everything but the run key from the documents of Qory
// Apiary's signed answer to the code: the stack it composes, the workflow it starts, the
// runtime and the compose flags, the inputs and the time limit. The documents are
// run/v1 documents.schema.json, checked as [provisioner.ReadDocuments] describes. So the
// person who starts the run gives none of them.

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
		return input(errors.New("leave out -f: a run from a run code takes its stack from Qory Apiary's response to the code"))
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
// code, checked: the runtime and the compose flags its run settings map to, the stack as
// -f takes it, the workflow, the inputs as the documents have them, the run settings'
// time_limit_minutes, 0 for none, and harness_hidden, which the documents set exactly
// when they set the stack.
type codeRun struct {
	runtime  string
	settings []provisioner.Setting
	stack    string
	workflow string
	inputs   []byte
	minutes  int
	hidden   bool
}

// stackReferencePrefix starts a stack reference, url#ref:path, whose url is https. A
// stack name, the other form of the documents' stack, is a lower-case letter, then
// lower-case letters, digits and hyphens, so it never starts so.
const stackReferencePrefix = "https://"

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
// Each error is exit 1 and ends with [codeUsed].
func codeRunOf(doc contracts.Documents) (*codeRun, error) {
	switch {
	case doc.Stack == "":
		return nil, errors.New("set a stack for the workspace in Qory Apiary, then take the work item in again: the response to the run code has no stack and no workflow" + codeUsed)
	case !strings.HasPrefix(doc.Stack, stackReferencePrefix):
		return nil, fmt.Errorf("update qory: the response to the run code sets the stack %s by its name, and this qory takes a stack reference (url#ref:path) alone%s", doc.Stack, codeUsed)
	}
	var minutes int
	if doc.RunSettings != nil {
		minutes = doc.RunSettings.TimeLimitMinutes
	}
	runtime, settings := provisioner.Settings(doc.RunSettings)
	if !isClaude(runtime) {
		return nil, fmt.Errorf("set the runtime claude for the workspace in Qory Apiary, then take the work item in again: qory starts a run code's workflow on claude alone, and the response to the run code sets the runtime %s%s", accesskey.ShownName(runtime, false), codeUsed)
	}
	return &codeRun{
		runtime: runtime, settings: settings, workflow: doc.Workflow, inputs: doc.Inputs, minutes: minutes, stack: doc.Stack,
		hidden: doc.HarnessHidden != nil && *doc.HarnessHidden,
	}, nil
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

// namedStack reports whether r's stack is a stack name, not a stack reference.
func (r *codeRun) namedStack() bool { return !strings.HasPrefix(r.stack, stackReferencePrefix) }

// givenMount is a mount of a walled run that wall.mounts in forager.yaml or --mount
// adds; from is which of the two adds it, as an error prints it.
type givenMount struct {
	path string
	from string
}

// cacheMounted stops, as input, a walled run of a named stack with a mount of given that
// is, contains or lies inside qory's folder in the cache directory, <cache>/qory: qory
// writes the home and harness report of each run with a run key or a run code and no
// --home or harness.home under it, and the agent's container has every mount. The
// mount and the folder are compared as Forager compares a mount, by [session.Overlap]:
// by file through links and in any letter case on a disk that ignores case. A stack
// reference passes, and so does a failed lookup of the cache directory, under which
// qory then writes no home. It runs before anything is fetched or composed; its error
// ends with [codeUsed].
func (r *codeRun) cacheMounted(given []givenMount) error {
	if !r.namedStack() {
		return nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return nil
	}
	cache := filepath.Join(base, "qory")
	for _, m := range given {
		if how := session.Overlap(m.path, cache); how != "" {
			return input(fmt.Errorf("remove %s from %s: it %s %s, where qory writes the harness report of each run with a run key or a run code that has no --home or harness.home, and a walled run of a named stack mounts no part of %s%s", exports.Value(m.path, false), m.from, how, exports.Value(cache, false), exports.Value(cache, false), codeUsed))
		}
	}
	return nil
}

// shownStack is r's stack as a line prints it: the URL as [exports.URL] prints it.
func (r *codeRun) shownStack() string { return exports.URL(r.stack) }

// timedOutLine is the line of a run from a run code whose time limit, limit, came before
// the runtime started: [timedOutBeforeJoin] for a run that cannot take --timeout.
func (r *codeRun) timedOutLine(limit time.Duration, runtime string) string {
	return fmt.Sprintf("set a longer time_limit_minutes in Qory Apiary, or a longer workflows.%s.time_limit in the stack %s: the run was cancelled at its time limit of %s, before %s started", r.workflow, r.shownStack(), limit, agentName(runtime))
}

// startLine is the line qory run prints once the answer to the run code is read.
func (r *codeRun) startLine(runID string) string {
	return fmt.Sprintf("qory run: the run %s starts the workflow %s of %s on %s", runID, r.workflow, r.shownStack(), r.runtime)
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
		return nil, fmt.Errorf("add a skill or command %s to the stack %s, or set another workflow in Qory Apiary: the composed harness has no skill or command %s%s", r.workflow, r.shownStack(), r.workflow, codeUsed)
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
