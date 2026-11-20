package stack

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/qoryai/forager/accesskey"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/runtimeinstall"
)

// APIVersion is the format version this qory reads, [exports.APIVersion], and the one a
// retired spelling of it, [exports.RetiredAPIVersions], is read as. A stack
// that declares another one is refused, and the message contains this one.
const APIVersion = exports.APIVersion

// FileName is the stack's file name on disk, in a directory of the harness repository
// that delivers it, or in an ancestor directory covering several checkouts. The file name
// defines which document a file contains; the document declares no kind. A checkout's
// own stack, and the stack it extends, are in the harness section of its qory.yaml, which
// [github.com/qoryai/qory/internal/config] reads and turns into a Stack with
// [NewCompose].
const FileName = exports.StackFileName

// Kinds are the atomic entry kinds an exclude may list. An exclude that lists anything
// else is refused.
var Kinds = []string{"skills", "agents", "commands", "output-styles", "hooks", "mcp", "files"}

// Source is where a module comes from: a directory, a git repository at a ref, or an
// export of a repository, one it lists in the exports section of its qory.yaml.
//
//	source: {path: ../harness/core}
//	source: {git: https://github.com/acme/harness, ref: v2.4.0}
//	source: {git: https://github.com/acme/harness, ref: v2.4.0, path: modules/nextjs}
//	source: {git: https://github.com/acme/harness, ref: v2.4.0, module: nextjs}
//	source: {path: ../harness, module: nextjs}
//
// A path source is pinned by nothing and reads the directory as it stands. A git source
// is pinned by the commit the ref resolves to, and its path, when set, is the module's
// directory inside the repository. With a module or a stack selected, the source is the
// repository, the clone or the path, and the export's directory is what the repository's
// qory.yaml sets, so the publisher may move it. The same shape selects the base a
// checkout extends, with stack in place of module.
type Source struct {
	// Path is the module directory, relative to the stack file unless it is absolute;
	// with Git set, the module's directory inside the repository, relative to its root.
	// With Module or Stack set and no Git, the repository whose export is read.
	Path string `yaml:"path,omitempty"`
	// Git is the repository URL, in any form git clones from.
	Git string `yaml:"git,omitempty"`
	// Ref is the tag, branch or commit to read from a git source, required with Git.
	Ref string `yaml:"ref,omitempty"`
	// Module is the name of an exported module of the repository, in a module's source.
	Module string `yaml:"module,omitempty"`
	// Stack is the name of an exported stack of the repository, in extends.
	Stack string `yaml:"stack,omitempty"`
}

// String returns the source as the report and the error messages show it: the path as the
// stack writes it for a path source, and <git>#<ref>, with :<path> appended when there
// is one, for a git source; an export follows as " module <name>" or " stack <name>".
func (s Source) String() string {
	out := s.Path
	if s.Git != "" {
		out = s.Git + "#" + s.Ref
		if s.Path != "" {
			out += ":" + s.Path
		}
	}
	switch {
	case s.Stack != "":
		out += " stack " + s.Stack
	case s.Module != "":
		out += " module " + s.Module
	}
	return out
}

// Shown is the source as a message prints it: [Source.String]'s form, with the git URL
// as [exports.URL] prints it, the ref and the path as [exports.Value] prints them, and
// the export's name as [accesskey.ShownName] prints it, so neither a secret nor the
// password of a URL's user part is printed.
func (s Source) Shown() string {
	out := exports.Value(s.Path, false)
	if s.Git != "" {
		out = exports.URL(s.Git) + "#" + exports.Value(s.Ref, false)
		if s.Path != "" {
			out += ":" + exports.Value(s.Path, false)
		}
	}
	switch {
	case s.Stack != "":
		out += " stack " + accesskey.ShownName(s.Stack, false)
	case s.Module != "":
		out += " module " + accesskey.ShownName(s.Module, false)
	}
	return out
}

// exported reports whether the source names an export of a repository.
func (s Source) exported() bool { return s.Module != "" || s.Stack != "" }

// Module is one entry of the stack's ordered list of modules. An entry has a name, a
// source, or both: a name alone reads <name> under the modules directory of the
// repository the stack is in, modules/ at its root unless the root's qory.yaml sets
// another under exports.dir; a source alone takes the module's name from its manifest;
// both means the manifest must declare that name.
type Module struct {
	// Name is the module's name, the one its manifest declares. Alone, it is the address
	// too: <name> under [Stack.ModulesDir] under [Stack.Root].
	Name string `yaml:"name,omitempty"`
	// Source is where the module is read from, when it is not in the repository's
	// modules directory.
	Source Source `yaml:"source,omitempty"`
	// Exclude lists what of the module the compose leaves out: entries by kind, the
	// instruction section, settings fragments, exported variables. Everything else is
	// composed.
	Exclude Selection `yaml:"exclude,omitempty"`
	// Only lists the only things of the module the compose takes, with the same keys as
	// Exclude, and brings in what the listed entries require from the module; everything
	// else is left out. Exclude beside it lists entries brought in that way to leave out.
	Only Selection `yaml:"only,omitempty"`
	// Base marks a module that came from the base stack a checkout's qory.yaml extends.
	// It is set by [Extend], not by the YAML, which also rewrites the module's source so
	// it resolves from the checkout's qory.yaml.
	Base bool `yaml:"-"`
	// CheckoutExclude, on a base module, lists the entries the checkout's qory.yaml
	// leaves out of it: the exclude of the entry there that lists the base module by its
	// name. It is set by [Extend], not by the YAML, and the compose applies it after the
	// base's exclude or only block.
	CheckoutExclude Selection `yaml:"-"`
	// BaseName, on a base module, is the name a checkout's qory.yaml lists it by to
	// exclude from it: the name the base stack sets for it, else the export its source
	// selects; "" for a module the base lists by a path or a git source alone, which the
	// checkout cannot list. It is set by [Extend], not by the YAML.
	BaseName string `yaml:"-"`
	// Variant forces one of the module's variants instead of the one named like the
	// runtime.
	Variant string `yaml:"variant,omitempty"`
	// Link is a name at the checkout root that links to the module's directory, so a
	// permission rule or a script reaches the module's files by a checkout-relative path.
	Link string `yaml:"link,omitempty"`
}

// Target is the set of runtimes the harness is rendered for.
type Target struct {
	// Runtimes are the programs that run the harness, such as claude or codex. In the
	// file the key is runtime, and it contains either one name or a list of them.
	Runtimes Runtimes `yaml:"runtime"`
	// Model is written into the settings of every targeted runtime that has a place for
	// it. A target that lists several runtimes usually leaves it out, because one model
	// name rarely means anything to two of them.
	Model string `yaml:"model,omitempty"`
	// Effort is the effort value the main agent runs at, from --effort or harness.effort;
	// "" for none. It is no key of a document.
	Effort string `yaml:"-"`
}

// Tier is one entry of a tier map: the model id a tier maps to and, when the entry sets
// one, the effort, with where the entry came from.
type Tier struct {
	// Model is the model id, written as the entry has it.
	Model string
	// Effort is the effort value, "" when the entry sets none.
	Effort string
	// From is where the entry was set: the path of the file, or the flag.
	From string
}

// Runtimes is the list of runtime names a target lists. Both spellings decode into it, so
// a stack for one runtime stays as short as it reads:
//
//	target:
//	  runtime: claude
//
//	target:
//	  runtime: [claude, codex]
//
// The order is the order of the file, and it is the order the runtimes are rendered and
// reported in. The first name is the one a message contains when it can contain only one.
type Runtimes []string

// UnmarshalYAML accepts a single name or a sequence of names.
func (r *Runtimes) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var one string
		if err := n.Decode(&one); err != nil {
			return err
		}
		*r = Runtimes{one}
		return nil
	}
	var many []string
	if err := n.Decode(&many); err != nil {
		return &decodeRefusal{exports.Refuse("Set target.runtime to one runtime name or a list of names.", append(decoderLines(err), "target.runtime is in "+exports.FilePlace+".")...), err}
	}
	*r = many
	return nil
}

// String joins the runtimes for a message or a title: "claude" for one, "claude, codex"
// for several.
func (r Runtimes) String() string { return strings.Join(r, ", ") }

// Validate refuses an empty target, an empty name in it, and the same runtime twice. It
// is called on a loaded stack, and again by the command module after the --runtime flag
// has replaced what the stack set.
func (r Runtimes) Validate() error { return r.ValidateAs("target.runtime") }

// ValidateAs is [Runtimes.Validate] for the runtimes that key sets, such as
// harness.runtime in a machine's configuration, which the refusal prints as key.
func (r Runtimes) ValidateAs(key string) error {
	return r.ValidateAt(key, key+" is in "+exports.FilePlace+".")
}

// ValidateAt is [Runtimes.ValidateAs] with where as the last line of the refusal, for a
// list that a flag or the configuration set, which no file of the document contains.
func (r Runtimes) ValidateAt(key, where string) error {
	if len(r) == 0 {
		return exports.Refuse("Set "+key+" to one runtime name or a list of names.", "No runtime is set.", where)
	}
	seen := map[string]bool{}
	for _, name := range r {
		if name == "" {
			return exports.Refuse("Remove the empty name from "+key+".", "A runtime name is not empty.", where)
		}
		if seen[name] {
			return exports.Refuse(fmt.Sprintf("List %s once in %s.", accesskey.ShownName(name, false), key), key+" lists it twice.", where)
		}
		seen[name] = true
	}
	return nil
}

// First is the runtime a single-runtime caller means, and "" for an empty target.
func (r Runtimes) First() string {
	if len(r) == 0 {
		return ""
	}
	return r[0]
}

// Extending is what a stack lets a checkout's own modules ship. A stack
// without the block is closed: no stack may extend it.
type Extending struct {
	// Kinds are the entry kinds an appended module may ship, from [Kinds] without files,
	// which Files governs by path. Hooks and MCP servers are never allowed, since the
	// runtime executes them without the agent.
	Kinds []string `yaml:"kinds,omitempty"`
	// Instructions allows an appended module's AGENTS.md, appended after the base's.
	Instructions bool `yaml:"instructions,omitempty"`
	// Settings are the dotted key paths an appended module's settings fragment may set,
	// such as permissions.allow; a fragment setting anything else fails the compose.
	Settings []string `yaml:"settings,omitempty"`
	// Files are the <runtime>/<path> prefixes an appended module's files may sit under,
	// such as claude/rules, matched by path segment; a file elsewhere fails the compose.
	Files []string `yaml:"files,omitempty"`
	// Target is the targets the stack is written for, and so the ones a checkout
	// extending it may compose for; nil accepts every target.
	Target *TargetPolicy `yaml:"target,omitempty"`
	// Hooks lets a checkout extending the stack add workflow hooks at the hook points
	// its workflows declare, within these limits; nil allows no workflow hook. A
	// workflow hook is not a runtime hook, which Kinds never allows.
	Hooks *HookLimits `yaml:"hooks,omitempty"`
}

// TargetPolicy is what a stack sets under extending.target: the runtimes and the
// models it is written for. A resolved target outside either list fails the compose,
// and a stack that lists models makes the model required, since a target without one
// runs whatever the runtime defaults to, which is a model the stack did not list.
//
//	extending:
//	  target:
//	    runtime: [claude]
//	    model: [opus, sonnet]
//
// Either key may be left out, and the other alone is the policy; a block that lists
// neither is refused.
type TargetPolicy struct {
	// Runtime lists the runtimes a checkout may compose for, one name or a list.
	Runtime Names `yaml:"runtime,omitempty"`
	// Model lists the models a checkout may compose with, one name or a list. When set,
	// a target selects one of them.
	Model Names `yaml:"model,omitempty"`
}

// Names is a list of names that reads from one name or a sequence of them, the way
// [Runtimes] does.
type Names []string

// UnmarshalYAML accepts a single name or a sequence of names.
func (n *Names) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var one string
		if err := node.Decode(&one); err != nil {
			return err
		}
		*n = Names{one}
		return nil
	}
	var many []string
	if err := node.Decode(&many); err != nil {
		return &decodeRefusal{exports.Refuse("Set extending.target.runtime and extending.target.model each to one name or a list of names.", append(decoderLines(err), "extending.target is in "+exports.FilePlace+".")...), err}
	}
	*n = many
	return nil
}

// validate refuses a policy naming nothing, an empty name and a name given twice.
func (t *TargetPolicy) validate() error {
	if len(t.Runtime) == 0 && len(t.Model) == 0 {
		return exports.Refuse("List a runtime, a model, or both in extending.target.", "The block lists neither. It lists the runtimes and the models a checkout may compose for.", "extending.target is in "+exports.FilePlace+".")
	}
	for _, l := range []struct {
		key   string
		names Names
		shown func(string) string
	}{{"runtime", t.Runtime, shownName}, {"model", t.Model, shownValue}} {
		seen := map[string]bool{}
		for _, name := range l.names {
			if name == "" {
				return exports.Refuse(fmt.Sprintf("Remove the empty name from extending.target.%s.", l.key), "A name is not empty.", "extending.target is in "+exports.FilePlace+".")
			}
			if seen[name] {
				return exports.Refuse(fmt.Sprintf("List %s once in extending.target.%s.", l.shown(name), l.key), "extending.target."+l.key+" lists it twice.", "extending.target is in "+exports.FilePlace+".")
			}
			seen[name] = true
		}
	}
	return nil
}

// shownName and shownValue are a name and a value as a message prints them, for a list
// of runtime names beside a list of model ids.
func shownName(n string) string  { return accesskey.ShownName(n, false) }
func shownValue(v string) string { return exports.Value(v, false) }

// Check refuses a resolved target outside the policy: a runtime not listed, a model not
// listed, and, when models are listed, no model at all. base is the stack in the
// message, as the report shows it.
func (t *TargetPolicy) Check(target Target, base string) error {
	if t == nil {
		return nil
	}
	if len(t.Runtime) > 0 {
		for _, r := range target.Runtimes {
			if !slices.Contains(t.Runtime, r) {
				return exports.Refuse("Set the target runtime to one that the base stack lists.", fmt.Sprintf("Runtime %s is not one the base stack %s is written for.", accesskey.ShownName(r, false), accesskey.ShownName(base, false)), "Runtimes: "+strings.Join(accesskey.ShownNames(t.Runtime), ", ")+".")
			}
		}
	}
	if len(t.Model) > 0 {
		if target.Model == "" {
			return exports.Refuse("Set the target model to one that the base stack lists.", fmt.Sprintf("The target sets no model, and the base stack %s is written for one of these.", accesskey.ShownName(base, false)), "Models: "+strings.Join(exports.Values(t.Model), ", ")+".")
		}
		if !slices.Contains(t.Model, target.Model) {
			return exports.Refuse("Set the target model to one that the base stack lists.", fmt.Sprintf("Model %s is not one the base stack %s is written for.", exports.Value(target.Model, false), accesskey.ShownName(base, false)), "Models: "+strings.Join(exports.Values(t.Model), ", ")+".")
		}
	}
	return nil
}

// Stack is one qory-stack.yaml, validated, or the document a checkout's qory.yaml
// contains under harness: the checkout's own stack, with a Target and Modules, or a Stack
// whose Extends selects the base, with the Target the checkout composes for; [Extend]
// merges that one onto its base and returns the stack that composes. A qory-stack.yaml
// has no Target: it is delivered to be extended, and the checkout extending it
// sets what it composes for.
type Stack struct {
	APIVersion string `yaml:"apiVersion"`
	// Qory is the range of qory versions the stack is written for, from the qory key,
	// joined by [Load] with the qory key of the qory.yaml at the root of the
	// repository the stack is in; empty when neither sets one. The compose refuses a
	// qory outside it.
	Qory Constraint `yaml:"qory,omitempty"`
	// Name is the stack's name in the report. A stack that leaves it out is named after
	// the checkout by the caller.
	Name string `yaml:"name,omitempty"`
	// Description is what the stack is for, copied into the report.
	Description string `yaml:"description,omitempty"`
	// Extends, in a checkout's qory.yaml, selects the base stack it appends to: a
	// directory that contains a qory-stack.yaml, as a path or inside a git repository at
	// a ref. The base's modules come first and cannot be changed; the checkout may only
	// leave entries out of them, see [Extend].
	Extends Source `yaml:"extends,omitempty"`
	// Target is the runtime and model the harness is rendered for, in a checkout's
	// qory.yaml: required for its own stack, and beside Extends the target the checkout
	// composes the base for, which the machine's configuration and the flags may
	// replace. A qory-stack.yaml has none, and one that has one is refused.
	Target Target `yaml:"target,omitempty"`
	// Tiers is read from a qory-stack.yaml only to refuse it: a stack file maps no tier,
	// since a repository's qory.yaml and the machine's configuration map them.
	Tiers map[string]any `yaml:"tiers,omitempty"`
	// Modules are the modules to compose, in the order they merge.
	Modules []Module `yaml:"modules"`
	// Bind selects the entry that fills each role a document references, by kind and
	// then by role, to the entry's name in that kind: a module writes
	// ${qory:agents/coder}, and bind: {agents: {coder: rails-coder}} selects which agent
	// that is. A group role takes a list of agents. After [Extend], it is the bind of the
	// checkout's qory.yaml, and BaseBind is the base's.
	Bind Fills `yaml:"bind,omitempty"`
	// BaseBind is the bind of the stack a checkout's qory.yaml extends, set by
	// [Extend]; nil for a stack that extends none. The compose sets each role from
	// BaseBind first, then from the modules' provides, then from Bind.
	BaseBind Fills `yaml:"-"`
	// Extending, on a stack, is what a checkout's own modules may ship. A stack that
	// leaves it out cannot be extended.
	Extending *Extending `yaml:"extending,omitempty"`
	// Extensions are values qory copies into the report and does not read: a map of
	// keys to values of any shape, for the scripts of a team that keep their own
	// settings beside the stack.
	Extensions map[string]any `yaml:"extensions,omitempty"`
	// Workflows are the settings of the workflows the stack starts, by name: a
	// dispatched run starts the skill or command of a workflow's name, as
	// [WorkflowEntries] lists the names it looks up. A stack file sets them, and so does
	// the harness section of a checkout's qory.yaml that defines a stack; a section that
	// extends a stack sets none, and [Extend] takes the base's.
	Workflows map[string]Workflow `yaml:"workflows,omitempty"`
	// RuntimePins are the versions of the runtimes that qory run installs, by runtime
	// name, from runtimes; a runtime they leave out installs its newest release. A stack
	// file sets them, and so does the harness section of a checkout's qory.yaml. After
	// [Extend], the checkout's pin of a runtime replaces the base's.
	RuntimePins map[string]RuntimePin `yaml:"runtimes,omitempty"`
	// Hooks are the workflow hooks of a checkout's qory.yaml, from harness.hooks, read
	// only in a document that extends a stack; a stack file has none. After [Extend],
	// they are the checkout's.
	Hooks []Hook `yaml:"-"`

	// RetiredAPIVersion is the apiVersion the file declared when it is a retired
	// spelling of [APIVersion], one of [exports.RetiredAPIVersions], "" when the file
	// declares the current one. It is set as the stack is validated, and APIVersion
	// contains what the file is read as; a compose reports which files want the line
	// rewritten.
	RetiredAPIVersion string `yaml:"-"`

	// File is the absolute path the stack was read from, set by [Load], not by the YAML.
	File string `yaml:"-"`
	// Root is the root of the repository the stack is in, set by [Load]: git's toplevel
	// for the stack's directory, else that directory. A module listed without a source is
	// read from ModulesDir under it.
	Root string `yaml:"-"`
	// ModulesDir is the directory a module listed without a source is read from, relative
	// to Root, set by [Load]: what the root's qory.yaml sets under exports.dir, else
	// modules. A stack built without [Load] reads modules.
	ModulesDir string `yaml:"-"`
}

// Workflow is the settings of one workflow of a stack, an entry of workflows.
type Workflow struct {
	// TimeLimitText is time_limit as written, a duration such as 7h or 45m.
	TimeLimitText string `yaml:"time_limit,omitempty"`
	// TimeLimit is TimeLimitText read as a duration, above zero, or 0 when the workflow
	// sets none. It is set as the stack is validated.
	TimeLimit time.Duration `yaml:"-"`
	// Access is access as written, the access level the workflow declares for what a
	// dispatched run of the workflow works on: read, write-no-push, or write, "" when the
	// workflow sets none. [Stack.AccessOf] reads "" as read.
	Access string `yaml:"access,omitempty"`
	// Tier is tier as written, the tier of [Tiers] that names the main session's model
	// and effort for a dispatched run of the workflow, "" when the workflow sets none.
	// Qory Apiary resolves it; qory reads it only to refuse a value that is no tier.
	Tier string `yaml:"tier,omitempty"`
	// Plan is plan as written, the artifact type whose launch version sets what the
	// workflow's session may push, "" when the workflow names none. Only a workflow with
	// access write names one. A module of the stack declares the scope check of the type
	// in its manifest's scope_checks, which the compose requires.
	Plan string `yaml:"plan,omitempty"`
	// HookPoints are the points of the workflow where a checkout extending the stack
	// may add workflow hooks, by the point's name; nil for none.
	HookPoints map[string]HookPoint `yaml:"hook_points,omitempty"`
}

// RuntimePin is the version a stack pins of a runtime, an entry of runtimes.
type RuntimePin struct {
	// Version is an exact version, see [runtimeinstall.ValidVersion].
	Version string `yaml:"version"`
	// From is the absolute path of the file that sets the pin. It is set as the stack is
	// validated.
	From string `yaml:"-"`
	// number is whether version in the file is a YAML number or date, such as 2.1 or
	// 2024-01-01, which the decoder reads into Version as a string. Set by [RuntimePin.UnmarshalYAML].
	number bool
}

// UnmarshalYAML reads an entry of runtimes and records in number whether version is a
// YAML number or date. A key a pin does not have is reported as the decoder reports an unknown
// field: the decode of the entry into plain runs without the decoder's check of keys.
func (r *RuntimePin) UnmarshalYAML(n *yaml.Node) error {
	type plain RuntimePin
	var unknown []string
	number := false
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			// An alias, version: *v, has the tag of the node it points to.
			if value.Kind == yaml.AliasNode {
				value = value.Alias
			}
			switch {
			case key.Value != "version":
				unknown = append(unknown, fmt.Sprintf("line %d: field %s not found in type stack.RuntimePin", key.Line, key.Value))
			case value.Kind == yaml.ScalarNode && (value.ShortTag() == "!!int" || value.ShortTag() == "!!float" ||
				value.ShortTag() == "!!timestamp"):
				number = true
			}
		}
	}
	if len(unknown) > 0 {
		return &yaml.TypeError{Errors: unknown}
	}
	var p plain
	if err := n.Decode(&p); err != nil {
		// The decoder's text has the type of p; the entry's type is stack.RuntimePin.
		var typeErr *yaml.TypeError
		if errors.As(err, &typeErr) {
			for i, e := range typeErr.Errors {
				typeErr.Errors[i] = strings.ReplaceAll(e, "stack.plain", "stack.RuntimePin")
			}
		}
		return err
	}
	*r = RuntimePin(p)
	r.number = number
	return nil
}

// Tiers are the tier names: the names a tier placeholder, ${qory:<tier>}, may take in an
// agent's model, and the values of a workflow's tier.
var Tiers = []string{"light", "balanced", "thinking"}

// IsTier reports whether name is one of [Tiers].
func IsTier(name string) bool {
	return slices.Contains(Tiers, name)
}

// TierList is the tiers for a message: "light, balanced, and thinking".
func TierList() string {
	return strings.Join(Tiers[:len(Tiers)-1], ", ") + ", and " + Tiers[len(Tiers)-1]
}

// The access levels a workflow declares, see [Workflow.Access].
const (
	AccessRead = "read"
	// AccessWriteNoPush is a writable checkout, no push step, no submit, and no version of
	// the artifact branch.
	AccessWriteNoPush = "write-no-push"
	AccessWrite       = "write"
)

// LowerAccess is the lower of the access levels a and b, in the order [AccessRead],
// [AccessWriteNoPush], [AccessWrite]. The empty level of documents with no target does
// not lower the other, and two empty levels give "". Any other value, such as "Read",
// counts as [AccessRead], also next to an empty level, so a value that no schema accepts
// never raises a run.
func LowerAccess(a, b string) string {
	rank := func(level string) int {
		switch level {
		case AccessWriteNoPush:
			return 2
		case AccessWrite:
			return 3
		}
		return 1
	}
	if a == "" && b == "" {
		return ""
	}
	if a == "" || b == "" {
		a, b = a+b, a+b
	}
	if rank(b) < rank(a) {
		a, b = b, a
	}
	if rank(a) == 1 {
		return AccessRead
	}
	return a
}

// workflowName is a workflow's name: at most 55 characters, so that [WorkflowEntry] is at
// most 64.
var workflowName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,54}$`)

// WorkflowEntry is the prefixed name of a skill or command that a harness ships for the
// workflow name: workflow-<name>. A harness may ship the plain name too, see
// [WorkflowEntries]. A stack and a process.yaml use name itself.
func WorkflowEntry(name string) string { return "workflow-" + name }

// WorkflowEntries are the names a skill or command of the workflow name may have, in the
// order a run looks them up: [WorkflowEntry], then name itself.
func WorkflowEntries(name string) []string { return []string{WorkflowEntry(name), name} }

// TimeLimitOf is the time_limit of the workflow name, 0 when the stack has no such
// workflow or the workflow sets none.
func (p *Stack) TimeLimitOf(name string) time.Duration {
	return p.Workflows[name].TimeLimit
}

// AccessOf is the access level the workflow name declares, [AccessRead],
// [AccessWriteNoPush] or [AccessWrite]; [AccessRead] when the stack has no such workflow
// or the workflow sets none.
func (p *Stack) AccessOf(name string) string {
	if a := p.Workflows[name].Access; a != "" {
		return a
	}
	return AccessRead
}

// Dir returns the directory that contains the stack file. A module's relative path source
// resolves against it.
func (p *Stack) Dir() string { return filepath.Dir(p.File) }

// SourceOf is the source a module entry reads from: its own, or <name> under
// [Stack.ModulesDir] for an entry with a name alone, which resolves against [Stack.Root];
// [Stack.DirOf] reports which. The relative form is what the report records, so it reads
// the same on every machine.
func (p *Stack) SourceOf(l Module) Source {
	if l.Source.Path == "" && l.Source.Git == "" {
		dir := p.ModulesDir
		if dir == "" {
			dir = exports.DefaultModulesDir
		}
		return Source{Path: filepath.Join(dir, l.Name)}
	}
	return l.Source
}

// ConfigFile is the name of the configuration file p was read from, [exports.FileName] or
// [exports.AltFileName], for a refusal that points to the checkout's document. A stack
// read from a stack file, or in memory, has the first.
func (p *Stack) ConfigFile() string {
	if base := filepath.Base(p.File); p.File != "" && base == exports.AltFileName {
		return base
	}
	return exports.FileName
}

// DirOf is the directory a module entry's relative path resolves against: the repository
// root for an entry with a name alone, else the stack's directory.
func (p *Stack) DirOf(l Module) string {
	if l.Source.Path == "" && l.Source.Git == "" {
		return p.Root
	}
	return p.Dir()
}

// Load reads and validates one stack file and records its absolute path in
// [Stack.File]. An unknown field is an error, so a misspelled key is reported rather than
// ignored, and so is a second YAML document in the file. The error from a missing or
// unreadable file is returned as it comes from the operating system, and a caller can
// match it with errors.Is and os.ErrNotExist; a decoding or validation error is prefixed
// with path. Each error prints a path segment that contains an access key secret or a
// run key as [exports.HideError] prints it, since -f and a base's path or cache
// directory put a name a person wrote in path.
func Load(path string) (*Stack, error) {
	p, err := loadFile(path)
	return p, exports.HideError(err)
}

// loadFile is [Load] with the path in its errors as written.
func loadFile(path string) (*Stack, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, exports.ReadError(path, err)
	}
	p := &Stack{}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(p); err != nil {
		return nil, exports.DecodeError(path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, exports.Refuse("Keep one YAML document in the stack file.", "A stack is one document, and the file contains more.", "The file is "+exports.FilePlace+".").In(exports.HidePath(path))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	p.File = abs
	if err := p.locate(); err != nil {
		return nil, err
	}
	if err := p.validate(false); err != nil {
		return nil, inPath(path, err)
	}
	// The repository delivering the stack states the qory it needs once, in its
	// qory.yaml; every stack it holds is held to that range beside its own.
	value, file, err := exports.Range(p.Root)
	if err != nil {
		return nil, err
	}
	if value != "" {
		c, err := ParseConstraint(value)
		if err != nil {
			return nil, inPath(file, err)
		}
		p.Qory = p.Qory.Join(c)
	}
	return p, nil
}

// locate sets [Stack.Root] and [Stack.ModulesDir] for the file the stack was read from:
// the repository root, and the modules directory its qory.yaml names, else modules.
func (p *Stack) locate() error {
	var err error
	if p.Root, err = checkout.Root(filepath.Dir(p.File)); err != nil {
		return err
	}
	p.ModulesDir, err = exports.ModulesDir(p.Root)
	return err
}

// NewCompose validates p as the document a checkout's qory.yaml contains under harness,
// read at path: the checkout's own stack when it sets a target, or, when it selects a
// stack under extends, the document that appends to that base.
func NewCompose(path string, p *Stack) (*Stack, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	p.File = abs
	if err := p.locate(); err != nil {
		return nil, err
	}
	if err := p.validate(true); err != nil {
		return nil, inPath(path, err)
	}
	return p, nil
}

// inPath is err, an error about the document at path, with path in front, or, when err
// itself is an [exports.InFile], with path inside its text, as [exports.AtPath] has it.
func inPath(path string, err error) error {
	var in exports.InFile
	if errors.As(err, &in) {
		return in.In(exports.HidePath(path))
	}
	return fmt.Errorf("%s: %w", exports.HidePath(path), err)
}

// extends reports whether the document names a stack to extend.
func (p *Stack) extends() bool {
	return p.Extends.Path != "" || p.Extends.Git != "" || p.Extends.Ref != "" || p.Extends.exported()
}

// validate checks the whole document before a caller sees it, so a document that reaches
// the compose is known to contain what the compose requires: a runtime for a stack that
// a checkout's qory.yaml defines, a base and no workflows for a document that extends
// one, no target for a stack file, at least one module unless the document extends a
// stack, each with a name or a source, no name twice, excludes and onlys over known
// kinds and parts, an exclude beside an only that lists entries the only does not, links
// that are one path segment and listed once, extensions that are maps, and an extending
// block over known kinds without hooks and servers. Whether a source contains a module,
// and whether its manifest defines the name the entry sets, is the compose's check.
func (p *Stack) validate(compose bool) error {
	current, err := exports.ResolveAPIVersion(p.APIVersion)
	if err != nil {
		return err
	}
	if current != p.APIVersion {
		p.RetiredAPIVersion, p.APIVersion = p.APIVersion, current
	}
	if compose {
		if p.extends() {
			if err := p.Extends.validate(); err != nil {
				return exports.Prefixed("extends", err)
			}
			if p.Extends.Module != "" {
				return exports.Refuse("Move the module from extends to modules, and select a stack under extends.", "extends selects module "+accesskey.ShownName(p.Extends.Module, false)+", and a checkout extends a stack.", "extends is in "+exports.FilePlace+".")
			}
		}
		if p.Extending != nil {
			return exports.Refuse("Remove extending from the harness section.", "extending belongs to the base stack, and a harness section that extends a stack does not set it.", "extending is in "+exports.FilePlace+".")
		}
		if p.extends() && len(p.Workflows) > 0 {
			return exports.Refuse("Remove workflows or extends.", "A harness section that extends a stack takes the workflows of its base.", "Both keys are in "+exports.FilePlace+".")
		}
		// A document extending a stack may leave the runtime to the machine's
		// configuration or the flag; its own stack names one.
		if len(p.Target.Runtimes) > 0 || !p.extends() {
			if err := p.Target.Runtimes.Validate(); err != nil {
				return err
			}
		}
	} else {
		if p.extends() {
			return exports.Refuse("Remove extends from the stack file.", "Only the harness section of a checkout's "+exports.FileName+" or "+exports.AltFileName+" extends a stack.", "extends is in "+exports.FilePlace+".")
		}
		if len(p.Target.Runtimes) > 0 || p.Target.Model != "" {
			return exports.Refuse("Remove target from the stack file.", "A stack is delivered to be extended, and the checkout extending it sets target under harness, beside extends.", "target is in "+exports.FilePlace+".")
		}
		if p.Tiers != nil {
			return exports.Refuse("Remove tiers from the stack file.", "A repository maps tiers under harness.tiers in its "+exports.FileName+" or "+exports.AltFileName+", and a machine maps them in the user's "+exports.FileName+" or "+exports.AltFileName+".", "tiers is in "+exports.FilePlace+".")
		}
	}
	if p.Extending != nil {
		for _, k := range p.Extending.Kinds {
			if !isKind(k) {
				return exports.Refuse("Set each entry of extending.kinds to a known kind.", "extending.kinds lists kind "+accesskey.ShownName(k, true)+".", "Kinds: "+strings.Join(Kinds, ", ")+".", "extending.kinds is in "+exports.FilePlace+".")
			}
			if k == "hooks" || k == "mcp" {
				return exports.Refuse(fmt.Sprintf("Remove %s from extending.kinds.", k), "An extending module never ships it, because the runtime executes it without the agent.", "extending.kinds is in "+exports.FilePlace+".")
			}
			if k == "files" {
				return exports.Refuse("Remove files from extending.kinds, and list the paths in extending.files.", "extending.files contains the paths that an extending module's files may sit under.", "extending.kinds is in "+exports.FilePlace+".")
			}
		}
		for _, key := range p.Extending.Settings {
			if key == "" || strings.HasPrefix(key, ".") || strings.HasSuffix(key, ".") {
				return exports.Refuse("Set each entry of extending.settings to a dotted key path.", "extending.settings lists "+accesskey.ShownName(key, true)+", which is not one.", "extending.settings is in "+exports.FilePlace+".")
			}
		}
		for _, prefix := range p.Extending.Files {
			if !filePrefix(prefix) {
				return exports.Refuse("Set each entry of extending.files to a <runtime>/<path> prefix.", "extending.files lists "+exports.Value(prefix, true)+". A prefix is relative, contains no dot segment, and starts with the runtime.", "extending.files is in "+exports.FilePlace+".")
			}
		}
		if p.Extending.Target != nil {
			if err := p.Extending.Target.validate(); err != nil {
				return err
			}
		}
		if p.Extending.Hooks != nil {
			if err := p.Extending.Hooks.validate(); err != nil {
				return err
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(p.Workflows)) {
		if !workflowName.MatchString(name) {
			return exports.Refuse("Rename the workflow "+accesskey.ShownName(name, true)+".", "A workflow name is a lower-case letter, then up to 54 lower-case letters, digits, and hyphens, 55 characters at most.", "workflows is in "+exports.FilePlace+".")
		}
		w := p.Workflows[name]
		if w.Access != "" && w.Access != AccessRead && w.Access != AccessWriteNoPush &&
			w.Access != AccessWrite {
			return exports.Refuse(
				fmt.Sprintf("Set workflows.%s.access to a valid level.", name),
				"The levels are read, write-no-push, and write.",
				fmt.Sprintf("The value is %s.", exports.Value(w.Access, true)),
				fmt.Sprintf("workflows.%s is in %s.", name, exports.FilePlace))
		}
		if w.Tier != "" && !IsTier(w.Tier) {
			return exports.Refuse(
				fmt.Sprintf("Set workflows.%s.tier to a valid tier.", name),
				"The tiers are "+TierList()+".",
				fmt.Sprintf("The value is %s.", exports.Value(w.Tier, true)),
				fmt.Sprintf("workflows.%s is in %s.", name, exports.FilePlace))
		}
		if w.Plan != "" {
			if !runapi.ArtifactName(w.Plan) {
				return exports.Refuse(
					fmt.Sprintf("Set workflows.%s.plan to an artifact type.", name),
					"An artifact type is lowercase letters and digits in groups joined by single hyphens, at most 64 bytes.",
					fmt.Sprintf("The value is %s.", exports.Value(w.Plan, true)),
					fmt.Sprintf("workflows.%s is in %s.", name, exports.FilePlace))
			}
			if p.AccessOf(name) != AccessWrite {
				return exports.Refuse(
					fmt.Sprintf("Remove `plan` from %s, or set its access to write.", name),
					fmt.Sprintf("Only a workflow with access write has a plan, and the access of %s is %s.", name, p.AccessOf(name)),
					fmt.Sprintf("workflows.%s is in %s.", name, exports.FilePlace))
			}
		}
		if w.TimeLimitText == "" {
			continue
		}
		d, err := time.ParseDuration(w.TimeLimitText)
		if err != nil || d <= 0 {
			return exports.Refuse(fmt.Sprintf("Set workflows.%s.time_limit to a duration above zero, such as 7h or 45m.", name), fmt.Sprintf("The value is %s.", exports.Value(w.TimeLimitText, true)), fmt.Sprintf("workflows.%s is in %s.", name, exports.FilePlace))
		}
		w.TimeLimit = d
		p.Workflows[name] = w
	}
	for _, name := range slices.Sorted(maps.Keys(p.RuntimePins)) {
		if !slices.Contains(runtimeinstall.Runtimes(), name) {
			return exports.Refuse(
				fmt.Sprintf("Remove %s from runtimes.", accesskey.ShownName(name, false)),
				"qory installs and pins only "+strings.Join(runtimeinstall.Runtimes(), " and ")+".",
				"runtimes is in "+exports.FilePlace+".")
		}
		pin := p.RuntimePins[name]
		if pin.number {
			return exports.Refuse(
				fmt.Sprintf("Put runtimes.%s.version in quotes, such as version: \"2.1.320\".", name),
				"Without quotes, YAML reads the version as a number or a date, and an exact version is a string.",
				fmt.Sprintf("The value is %s.", exports.Value(pin.Version, false)),
				fmt.Sprintf("runtimes.%s is in %s.", name, exports.FilePlace))
		}
		if !runtimeinstall.ValidVersion(pin.Version) {
			return exports.Refuse(
				fmt.Sprintf("Set runtimes.%s.version to an exact version, or remove runtimes.%s.", name, name),
				"An exact version starts with a digit, followed by up to 47 letters, digits, . or -. A runtime with no pin installs its latest version.",
				fmt.Sprintf("The value is %s.", exports.Value(pin.Version, true)),
				fmt.Sprintf("runtimes.%s is in %s.", name, exports.FilePlace))
		}
		pin.From = p.File
		p.RuntimePins[name] = pin
	}
	// A stack that a checkout's qory.yaml defines cannot be extended, so no hook can
	// reach a point it declares.
	if compose && p.hasHookPoints() {
		return hookErrorf("Remove hook_points from the workflows in {file}.", "A checkout adds workflow hooks to a stack it extends, so only a qory-stack.yaml declares hook points.")
	}
	if err := p.validateHookPoints(); err != nil {
		return err
	}
	if err := p.validateHooks(); err != nil {
		return err
	}
	if err := CheckFills(p.Bind, "bind"); err != nil {
		return err
	}
	// A document that extends a stack may append nothing and carry only what is the
	// repository's own beside the base, its extensions say; a stack, and a document
	// holding a checkout's own stack, names a module.
	if len(p.Modules) == 0 && (!compose || !p.extends()) {
		return exports.Refuse("List at least one module under modules.", "modules is empty, and a stack lists one or more modules.", "modules is in "+exports.FilePlace+".")
	}
	seen := map[string]bool{}
	links := map[string]string{}
	for i, l := range p.Modules {
		who := fmt.Sprintf("modules[%d]", i)
		if l.Name != "" {
			who = "module " + accesskey.ShownName(l.Name, false)
			if !segment(l.Name) {
				return exports.Refuse(fmt.Sprintf("Rename the name of modules[%d] to one path segment.", i), "A module name contains no slash, backslash, @, or leading dot, and the name is "+accesskey.ShownName(l.Name, true)+".", "modules is in "+exports.FilePlace+".")
			}
			if seen[l.Name] {
				return exports.Refuse(fmt.Sprintf("List module %s once in modules.", accesskey.ShownName(l.Name, false)), "modules lists it twice.", "modules is in "+exports.FilePlace+".")
			}
			seen[l.Name] = true
		}
		if l.Source.Path != "" || l.Source.Git != "" || l.Source.Ref != "" || l.Source.exported() {
			if err := l.Source.validate(); err != nil {
				var path *SourcePathError
				if errors.As(err, &path) {
					path.Module = strings.TrimPrefix(who, "module ")
					return path
				}
				return exports.Prefixed(who, err)
			}
			if l.Source.Stack != "" {
				return exports.Prefixed(who, exports.Refuse("Select a module in the source of "+exports.ModulePlace+", and move the stack to extends.", "The source selects stack "+accesskey.ShownName(l.Source.Stack, false)+", and a module's source selects a module.", "modules is in "+exports.FilePlace+"."))
			}
		} else if l.Name == "" {
			return exports.Prefixed(who, exports.Refuse("Add a name or a source to "+exports.ModulePlace+".", "A module has a name, a source, or both.", "modules is in "+exports.FilePlace+"."))
		}
		// The blocks are parsed on the stack's own entry, not on the loop's copy.
		if err := p.Modules[i].Exclude.parse("exclude"); err != nil {
			return exports.Prefixed(who, err)
		}
		if err := p.Modules[i].Only.parse("only"); err != nil {
			return exports.Prefixed(who, err)
		}
		if err := p.Modules[i].besideOnly(); err != nil {
			return exports.Prefixed(who, err)
		}
		if l.Link != "" {
			if strings.ContainsFunc(l.Link, unicode.IsControl) {
				return exports.Prefixed(who, exports.Refuse("Set the link of "+exports.ModulePlace+" to a name without control characters.", "The link name "+accesskey.ShownName(l.Link, true)+" contains a control character.", "modules is in "+exports.FilePlace+"."))
			}
			if !segment(l.Link) || l.Link == ".qory" {
				return exports.Prefixed(who, exports.Refuse("Set the link of "+exports.ModulePlace+" to one path segment.", "The link is "+accesskey.ShownName(l.Link, true)+". A link contains no slash, backslash, @, or leading dot.", "modules is in "+exports.FilePlace+"."))
			}
			if links[l.Link] != "" {
				return exports.Refuse(fmt.Sprintf("Give %s and %s different links.", links[l.Link], who), "Both link "+accesskey.ShownName(l.Link, false)+".", "modules is in "+exports.FilePlace+".")
			}
			links[l.Link] = who
		}
	}
	return nil
}

// besideOnly checks an exclude block that stands beside an only block: it names entries
// only, since only has already left every part it does not name out, and none that only
// names, since taking and leaving out one entry contradict. What it names has to be an
// entry only brings in, which the compose checks when it knows what the module ships.
func (l Module) besideOnly() error {
	if l.Only.Empty() || l.Exclude.Empty() {
		return nil
	}
	if l.Exclude.Instructions || l.Exclude.Settings.Set() || l.Exclude.Env.Set() {
		return exports.Refuse("Remove instructions, settings, and env from the exclude of "+exports.ModulePlace+".", "Only leaves every part it does not select out, so an exclude beside only selects entries that only brings in.", "The blocks are in "+exports.FilePlace+".")
	}
	for _, kind := range l.Exclude.KindNames() {
		for _, name := range l.Exclude.Kinds[kind] {
			if slices.Contains(l.Only.Kinds[kind], name) {
				entry := accesskey.ShownName(kind, false) + "/" + accesskey.ShownName(name, false)
				return exports.Refuse(fmt.Sprintf("Remove %s from the only or the exclude of %s.", entry, exports.ModulePlace), fmt.Sprintf("Both blocks list %s.", entry), "The blocks are in "+exports.FilePlace+".")
			}
		}
	}
	return nil
}

// validate checks that a source is one directory, one git ref or one export: a path
// source needs its path, a git source needs its ref, a path inside a git source stays
// inside it, an export is one name that is one path segment, and a git source names an
// export or a path, not both.
func (s Source) validate() error {
	if s.Module != "" && s.Stack != "" {
		return exports.Refuse("Remove source.module or source.stack from "+exports.ModulePlace+".", "A source selects one export.", "source is in "+exports.FilePlace+".")
	}
	for _, e := range []struct{ key, name string }{{"module", s.Module}, {"stack", s.Stack}} {
		if e.name != "" && !segment(e.name) {
			return exports.Refuse(fmt.Sprintf("Set source.%s of %s to one path segment.", e.key, exports.ModulePlace), "The value is "+accesskey.ShownName(e.name, true)+". An export's name contains no slash, backslash, @, or leading dot.", "source is in "+exports.FilePlace+".")
		}
	}
	if s.Git == "" {
		if s.Ref != "" {
			return exports.Refuse("Set source.git of "+exports.ModulePlace+", or remove source.ref.", "source.ref needs source.git.", "source is in "+exports.FilePlace+".")
		}
		if s.Path == "" {
			if s.exported() {
				return exports.Refuse("Set source.path of "+exports.ModulePlace+" to the repository that exports it.", "With an export selected, source.path is required.", "source is in "+exports.FilePlace+".")
			}
			return exports.Refuse("Set source.path of "+exports.ModulePlace+".", "source.path is required.", "source is in "+exports.FilePlace+".")
		}
		return nil
	}
	if s.Ref == "" {
		return exports.Refuse("Set source.ref of "+exports.ModulePlace+" to a branch, tag, or commit.", "source.ref is required with source.git.", "source is in "+exports.FilePlace+".")
	}
	if s.Path != "" && s.exported() {
		return exports.Refuse("Remove source.path or the export from "+exports.ModulePlace+".", "Both select the directory, and an export's directory is what the repository's "+exports.FileName+" or "+exports.AltFileName+" sets.", "source is in "+exports.FilePlace+".")
	}
	if s.Path != "" {
		clean := filepath.Clean(s.Path)
		if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return &SourcePathError{Path: s.Path}
		}
	}
	return nil
}

// SourcePathError is a git source's source.path that is not a directory inside the
// repository: an absolute path, the repository's root, or a path that leads out of it.
// Path is the path as written. Module is the module whose source it is, by its name or as
// modules[i] with no name, and "" for the source of extends.
type SourcePathError struct {
	Path, Module string
	file         string
}

// In is the error with file as the path of the document it is about, see [exports.InFile].
func (e *SourcePathError) In(file string) error {
	c := *e
	c.file = file
	return &c
}

func (e *SourcePathError) Error() string { return e.text(true) }

// Hidden is the error's text without the path, for a harness whose folders no text
// shows.
func (e *SourcePathError) Hidden() string { return e.text(false) }

// text is the refusal: the fix, and the cause with the path and the file when path is set.
// The hidden form has neither, as [SourcePathError.Hidden] says.
func (e *SourcePathError) text(path bool) string {
	of := "extends"
	switch {
	case strings.HasPrefix(e.Module, "modules["):
		of = e.Module
	case e.Module != "":
		of = "module " + accesskey.ShownName(e.Module, false)
	}
	cause := "source.path is not a directory inside the repository: it is absolute, the repository's root, or leads out of it."
	if path {
		cause = "source.path is " + exports.Value(e.Path, true) + ", which is not a directory inside the repository: it is absolute, the repository's root, or leads out of it."
	}
	lines := []string{cause}
	if path && e.file != "" {
		lines = append(lines, "source is in "+e.file+".")
	}
	return exports.Refuse("Set source.path of "+of+" to a folder inside its repository.", lines...).Error()
}

// BaseExtendsError is a base stack, read from File, that extends another stack,
// Extends: a stack does not extend another.
type BaseExtendsError struct {
	File    string
	Extends Source
}

func (e *BaseExtendsError) Error() string {
	return exports.Refuse("Set extends to a stack that does not extend another.", fmt.Sprintf("%s extends %s, and a stack does not extend another.", exports.HidePath(e.File), e.Extends.Shown())).Error()
}

// Hidden is the error's text with the stack the base extends left out, for a harness
// whose addresses no text shows.
func (e *BaseExtendsError) Hidden() string {
	return exports.Refuse("Set extends to a stack that does not extend another.", fmt.Sprintf("%s extends another stack, and a stack does not extend another.", exports.HidePath(e.File))).Error()
}

// decodeRefusal is a refusal of a value the decoder could not read, which unwraps to the
// decoder's error, so errors.As still matches a *yaml.TypeError under it. It is an
// [exports.InFile] as the refusal is.
type decodeRefusal struct {
	refusal *exports.Refusal
	err     error
}

func (d *decodeRefusal) Error() string { return d.refusal.Error() }
func (d *decodeRefusal) Unwrap() error { return d.err }

// In is the refusal with file at its [exports.FilePlace], and the same cause.
func (d *decodeRefusal) In(file string) error {
	r, _ := d.refusal.In(file).(*exports.Refusal)
	return &decodeRefusal{r, d.err}
}

// decoderLines is the cause lines of err, a decode error: what the decoder reports, one
// line for each value. A report that contains an access key secret or a run key is left
// out, and a line says so.
func decoderLines(err error) []string {
	if accesskey.ContainsSecret(err.Error()) {
		return []string{"qory cannot read the value here. The YAML decoder's message contains an access key secret, a run key, a run code, or an enrolment code, so qory does not print it."}
	}
	var lines []string
	for _, l := range strings.Split(err.Error(), "\n") {
		if l = strings.TrimSpace(l); l != "" && l != "yaml: unmarshal errors:" {
			lines = append(lines, "The decoder reports: "+strings.TrimSuffix(l, ".")+".")
		}
	}
	return lines
}

// segment reports whether name is one plain path segment: no separator of either kind, no
// leading dot, and no "@", which the compose uses to key an entry by its module. A module
// name becomes the directory name modules/<name> in the composed tree, so anything else
// would write outside it.
func segment(name string) bool {
	return name != "" && !strings.ContainsAny(name, `/\@`) && !strings.HasPrefix(name, ".")
}

// Referable are the kinds a reference and a binding may refer to, see the module package.
var Referable = []string{"agents", "commands", "skills"}

// isReferable reports whether k is a kind a role may be of.
func isReferable(k string) bool {
	for _, kind := range Referable {
		if k == kind {
			return true
		}
	}
	return false
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

func isKind(k string) bool {
	for _, kind := range Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// filePrefix reports whether s is a prefix an extending.files entry may hold: forward
// slashes, a trailing one allowed, no leading one, and at least the runtime segment, with
// no segment empty, "." or "..". [FileAllowed] does the matching.
func filePrefix(s string) bool {
	if s == "" || strings.HasPrefix(s, "/") || strings.Contains(s, "\\") {
		return false
	}
	for _, seg := range strings.Split(strings.TrimSuffix(s, "/"), "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// baseName is the name a checkout's qory.yaml lists base module l by: the name the base
// stack sets for it, else the export its source selects; "" for a module the base lists by
// a path or a git source alone, whose name only its manifest defines.
func baseName(l Module) string {
	if l.Name != "" {
		return l.Name
	}
	return l.Source.Module
}

// checkoutExclude checks entry l of a checkout's document, which file names, qory.yaml or
// harness.yaml, and which lists a module of the
// base by its name: the entry has an exclude and nothing beside it, and the exclude lists
// entries of the kinds and under the file prefixes extending lets an appended module
// ship, since a checkout leaves out only what its modules may replace. The base's
// hooks, MCP servers, instructions, settings and variables stay as the base composes
// them. Whether the base module composes each entry is the compose's check.
func checkoutExclude(l Module, extending *Extending, file string) error {
	name := accesskey.ShownName(l.Name, false)
	if l.Source.Path != "" || l.Source.Git != "" || l.Source.exported() || !l.Only.Empty() || l.Variant != "" || l.Link != "" {
		return exports.Errorf("List module %s with its name and exclude only.\n  It belongs to the base stack, and a checkout's %s cannot change its source, only, variant, or link.", name, file)
	}
	if l.Exclude.Empty() {
		return exports.Errorf("Add an exclude to module %s, or remove its entry.\n  It belongs to the base stack, and a checkout's %s lists a base module only to exclude entries from it.", name, file)
	}
	for _, part := range []struct {
		key string
		set bool
	}{{"instructions", l.Exclude.Instructions}, {"settings", l.Exclude.Settings.Set()}, {"env", l.Exclude.Env.Set()}} {
		if part.set {
			return exports.Errorf("Remove %s from the exclude of module %s.\n  It belongs to the base stack, and a checkout's %s excludes entries from a base module, not its instructions, settings, or env.", part.key, name, file)
		}
	}
	for _, kind := range l.Exclude.KindNames() {
		for _, entry := range l.Exclude.Kinds[kind] {
			if kind == "files" {
				if !FileAllowed(entry, extending.Files) {
					return exports.Errorf("Remove files/%s from the exclude of module %s.\n  A checkout excludes from a base module only files where an extending module may ship them, and the base stack lets an extending module ship files under %s.", exports.HidePath(entry), name, prefixesText(extending.Files))
				}
				continue
			}
			if !slices.Contains(extending.Kinds, kind) {
				return exports.Errorf("Remove %s/%s from the exclude of module %s.\n  A checkout excludes from a base module only entries of the kinds an extending module may ship, and the base stack lets an extending module ship %s.", accesskey.ShownName(kind, false), accesskey.ShownName(entry, false), name, kindsListText(extending.Kinds))
			}
		}
	}
	return nil
}

// kindsListText lists kinds for a message, "no entries" for none.
func kindsListText(kinds []string) string {
	if len(kinds) == 0 {
		return "no entries"
	}
	return strings.Join(accesskey.ShownNames(kinds), ", ")
}

// prefixesText lists file prefixes for a message, "no path" for none.
func prefixesText(prefixes []string) string {
	if len(prefixes) == 0 {
		return "no path"
	}
	return strings.Join(exports.Values(prefixes), ", ")
}

// FileAllowed reports whether a files entry name, <runtime>/<path>, is one of the
// prefixes or under one, by path segment: claude/rules and claude/rules/ both cover
// claude/rules/a/b.md and neither covers claude/rules-private/x.md, and a prefix that is
// a file's path covers that file only.
func FileAllowed(name string, prefixes []string) bool {
	for _, p := range prefixes {
		p = strings.TrimSuffix(p, "/")
		if name == p || strings.HasPrefix(name, p+"/") {
			return true
		}
	}
	return false
}

// Extend returns the stack the checkout's qory.yaml p composes on base: the base's
// modules first, marked [Module.Base], each with its source rewritten to resolve from p,
// then p's modules; p's target, since a base has none; both files' extensions; the
// base's bind in [Stack.BaseBind] and p's in [Stack.Bind], kept apart. The
// result's Qory is p's; the base's range is the caller's to keep and check, as
// [Base.Qory] does. It refuses a base that extends another and a base without an
// extending block. An entry of p that lists a base module by its name is no module of
// the result: its exclude becomes that base module's [Module.CheckoutExclude], under the
// rules of [checkoutExclude]. An extension key both files set is p's, and so is a
// runtime's pin, [Stack.RuntimePins]. The result's File and Root are p's.
func Extend(base, p *Stack) (*Stack, error) {
	if base.Extends.Path != "" || base.Extends.Git != "" {
		return nil, &BaseExtendsError{File: base.File, Extends: base.Extends}
	}
	if base.Extending == nil {
		return nil, exports.Refuse("Add an extending block to the base stack, or extend another stack.", "The stack at "+exports.HidePath(base.File)+" is closed: it declares no extending block, so nothing may extend it.")
	}
	out := *p
	out.Modules = nil
	for _, l := range base.Modules {
		l.Base = true
		// The name is taken before the source is rewritten for the checkout.
		l.BaseName = baseName(l)
		if l.Source.Git == "" {
			// A base module read by path: inside the base's repository, which is the
			// extends source's clone for a git base, so the module becomes a git source
			// at the same ref and gets the commit as its pin; a path relative to the
			// checkout's qory.yaml for a base on disk, so the report reads on any
			// machine.
			abs := base.SourceOf(l).Path
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(base.DirOf(l), abs)
			}
			rel, err := filepath.Rel(base.Root, abs)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return nil, exports.Refuse(fmt.Sprintf("Move module %s into the base stack's repository.", accesskey.ShownName(l.Name, false)), "The base stack "+exports.HidePath(base.File)+" lists the module outside that repository.")
			}
			if p.Extends.Git != "" {
				l.Source = Source{Git: p.Extends.Git, Ref: p.Extends.Ref, Path: filepath.ToSlash(rel)}
			} else if here, err := filepath.Rel(p.Dir(), abs); err == nil {
				l.Source = Source{Path: here}
			} else {
				l.Source = Source{Path: abs}
			}
		}
		out.Modules = append(out.Modules, l)
	}
	byName := map[string]int{}
	for i, l := range out.Modules {
		if l.BaseName != "" {
			byName[l.BaseName] = i
		}
	}
	for _, l := range p.Modules {
		if i, ok := byName[l.Name]; ok {
			if err := checkoutExclude(l, base.Extending, p.ConfigFile()); err != nil {
				return nil, err
			}
			out.Modules[i].CheckoutExclude = l.Exclude
			continue
		}
		// A module the base lists by a path alone is named by its manifest, and the
		// compose fails on a checkout module of the same name, as composed twice.
		out.Modules = append(out.Modules, l)
	}
	out.BaseBind = base.Bind
	out.Workflows = base.Workflows
	out.Extensions = map[string]any{}
	for key, v := range base.Extensions {
		out.Extensions[key] = v
	}
	for key, v := range p.Extensions {
		out.Extensions[key] = v
	}
	if len(out.Extensions) == 0 {
		out.Extensions = nil
	}
	out.RuntimePins = nil
	if len(base.RuntimePins)+len(p.RuntimePins) > 0 {
		out.RuntimePins = maps.Clone(base.RuntimePins)
		if out.RuntimePins == nil {
			out.RuntimePins = map[string]RuntimePin{}
		}
		maps.Copy(out.RuntimePins, p.RuntimePins)
	}
	return &out, nil
}
