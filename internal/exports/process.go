package exports

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/qoryai/forager/accesskey"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/primitives"
	"github.com/qoryai/qory/internal/runapi"
)

// ProcessFileName is the process template a factory module has at its folder root: the
// texts, the workspace type, the stack and the process that a workspace made from it
// starts with. The compose composes it as one of the module's other files.
const ProcessFileName = "process.yaml"

// ProcessSchema is the schema the process document in a process.yaml declares.
const ProcessSchema = "qory.process/v1"

// The limits of a process template's texts, in characters, and of its description in
// words.
const (
	maxProcessName        = 60
	maxProcessSummary     = 200
	maxProcessDescription = 1200
	maxDescriptionWords   = 80
)

// processFile is a process.yaml as written: the texts a list of templates shows, the
// workspace type, the stack of the same repository its workflows run on, the process
// document, and optionally the artifact types the workspaces use and the submit step of
// a run. In the document, qory checks the schema, that the stages are a list, that each
// workflow of a stage is a non-empty string of one line that the stack declares, and
// that each stage name has no line break or other control character, and checks nothing
// else in it. In artifact_types, qory checks each entry as [checkArtifactType] has it,
// and submit as [checkSubmit] has it.
//
//	apiVersion: qory.dev/v1alpha1
//	name: Support desk
//	summary: Agents draft answers to support tickets.
//	description: An agent drafts each answer, and a person checks it.
//	type: customer-care
//	stack: support
//	process:
//	  schema: qory.process/v1
//	  stages: [{name: Answer, sequence: [{workflow: draft-an-answer}]}]
//	artifact_types:
//	  - name: reply-draft
//	    base_view: document
//	    size_limit: 65536
//	    max_files: 1
type processFile struct {
	APIVersion    string    `yaml:"apiVersion"`
	Name          string    `yaml:"name"`
	Summary       string    `yaml:"summary"`
	Description   string    `yaml:"description"`
	Type          string    `yaml:"type"`
	Stack         string    `yaml:"stack"`
	Process       yaml.Node `yaml:"process"`
	ArtifactTypes yaml.Node `yaml:"artifact_types"`
	Submit        yaml.Node `yaml:"submit"`
}

// submitSends is each value submit.send may have: when the submit step sends the
// submission.
var submitSends = []string{"end_of_run", "after_gate"}

// readProcess returns the process.yaml at the root of the module folder dir, relative to
// [Exports.Root], with its path: nil data when the folder has no such file. The file is
// read through the folder opened as an [os.Root], as the compose reads a module's files,
// so a link inside the folder is followed and a link that leads outside it is refused
// without reading its target.
func (e *Exports) readProcess(dir string) ([]byte, string, error) {
	folder := filepath.Join(e.Root, dir)
	path := filepath.Join(folder, ProcessFileName)
	root, err := os.OpenRoot(folder)
	if err != nil {
		return nil, path, HideError(err)
	}
	defer root.Close()
	info, err := root.Stat(ProcessFileName)
	if errors.Is(err, fs.ErrNotExist) || err == nil && info.IsDir() {
		return nil, path, nil
	}
	var data []byte
	if err == nil {
		data, err = root.ReadFile(ProcessFileName)
	}
	if escapes(root, err) {
		return nil, path, processRefusal(HidePath(path), "Replace "+ProcessFileName+" with a file inside the module.", "It is a link to a place outside the module.")
	}
	if err != nil {
		return nil, path, HideError(err)
	}
	if data == nil {
		// An empty file is a file, which checkProcess refuses for its first field.
		data = []byte{}
	}
	return data, path, nil
}

// escapes reports whether err is the error root returns for a path that leads out of
// it, such as a link to a place outside the module. Package os does not export that
// error, so escapes takes it from a lookup of ".." in root, which fails with it before
// any file is read, as the render's copier does. A closed root fails that lookup with
// [os.ErrClosed], which is not the error escapes looks for.
func escapes(root *os.Root, err error) bool {
	if err == nil {
		return false
	}
	_, probe := root.Lstat("..")
	if errors.Is(probe, os.ErrClosed) {
		return false
	}
	sentinel := errors.Unwrap(probe)
	return sentinel != nil && errors.Is(err, sentinel)
}

// checkProcess checks data, the process.yaml at path in an exported module's folder,
// field by field: each one is set, the texts are within their limits, stack is one of
// [Exports.Stacks], process is an object with schema qory.process/v1 and a list of
// stages, every workflow of a stage's sequence, a loop's body included, is declared
// under workflows in the stack's qory-stack.yaml, as [Exports.checkWorkflows] checks,
// artifact_types, when it is set, is as [checkArtifactTypes] checks, and submit, when it
// is set, is as [checkSubmit] checks. Each refusal has the file in it; for the
// workflows and the artifact types the error has one refusal for each entry.
func (e *Exports) checkProcess(path string, data []byte) error {
	shown := HidePath(path)
	if err := RefuseAliases(shown, data); err != nil {
		return err
	}
	var t processFile
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil && !errors.Is(err, io.EOF) {
		// An unknown key is refused fix first; any other error as [DecodeError] has it.
		if m := UnknownKey.FindStringSubmatch(err.Error()); m != nil && !accesskey.ContainsSecret(m[2]) {
			at := ProcessFileName
			if line := strings.TrimSuffix(m[1], ": "); line != "" {
				at = line + " of " + ProcessFileName
			}
			return fmt.Errorf("Remove the key %q from %s.\n  %s has no key `%s`.\n  The file is %s.", m[2], at, ProcessFileName, m[2], shown)
		}
		return DecodeError(shown, err)
	}
	if strings.TrimSpace(t.APIVersion) == "" {
		return processRefusal(shown, "Add apiVersion: "+APIVersion+" to "+ProcessFileName+".", "The file has no apiVersion.")
	}
	if _, err := ResolveAPIVersion(t.APIVersion); err != nil {
		return HideValueError(AtPath(shown, err), "")
	}
	for _, f := range []struct {
		key, value string
		limit      int
	}{{"name", t.Name, maxProcessName}, {"summary", t.Summary, maxProcessSummary}, {"description", t.Description, maxProcessDescription}} {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("Add %s to %s, at most %d characters.\n  The file has no %s.\n  The file is %s.", f.key, ProcessFileName, f.limit, f.key, shown)
		}
		if n := utf8.RuneCountInString(f.value); n > f.limit {
			return fmt.Errorf("Shorten %s in %s to at most %d characters.\n  The %s has %d characters.\n  The file is %s.", f.key, ProcessFileName, f.limit, f.key, n, shown)
		}
	}
	if n := len(strings.Fields(t.Description)); n > maxDescriptionWords {
		return processRefusal(shown, fmt.Sprintf("Shorten description in %s to at most %d words.", ProcessFileName, maxDescriptionWords), fmt.Sprintf("The description has %d words.", n))
	}
	if strings.TrimSpace(t.Type) == "" {
		return processRefusal(shown, "Add type to "+ProcessFileName+", the workspace type the template sets up.", "The file has no type.")
	}
	if t.Stack == "" {
		return processRefusal(shown, "Add stack to "+ProcessFileName+", a name that exports.stacks lists.", "The file has no stack.")
	}
	if why := notOneLine(t.Stack); why != "" {
		// The refusal of a stack exports.stacks does not list prints the value, so a
		// value with a line break could start a line that looks like qory's.
		return processRefusal(shown, "Set stack in "+ProcessFileName+" to one line of text.", "The value "+why+".")
	}
	if !contains(e.Stacks, t.Stack) {
		listed := "no stacks"
		if len(e.Stacks) > 0 {
			listed = strings.Join(accesskey.ShownNames(e.Stacks), ", ")
		}
		return fmt.Errorf("Set stack in %s to a name that exports.stacks lists.\n  The value is %s, and exports.stacks lists %s.\n  The file is %s.", ProcessFileName, accesskey.ShownName(t.Stack, false), listed, shown)
	}
	if t.Process.Kind == 0 || t.Process.Tag == "!!null" {
		return processRefusal(shown, "Add process to "+ProcessFileName+", the template's process document as an object.", "The file has no process.")
	}
	if t.Process.Kind != yaml.MappingNode {
		return fmt.Errorf("Set process in %s to an object.\n  process is the template's process document, and the value is %s.\n  The file is %s.", ProcessFileName, nodeKind(&t.Process), shown)
	}
	schema, stages := value(&t.Process, "schema"), value(&t.Process, "stages")
	if schema == nil || schema.Tag == "!!null" {
		return processRefusal(shown, "Add schema: "+ProcessSchema+" under process.", "process has no schema.")
	}
	if schema.Kind != yaml.ScalarNode || schema.Value != ProcessSchema {
		return processRefusal(shown, "Set process.schema to "+ProcessSchema+".", "process.schema is "+scalarText(schema)+".")
	}
	if stages == nil || stages.Tag == "!!null" {
		return processRefusal(shown, "Add stages under process, the list of the process's stages.", "process has no stages.")
	}
	if stages.Kind != yaml.SequenceNode {
		return processRefusal(shown, "Set process.stages to a list of stages.", "process.stages is "+nodeKind(stages)+".")
	}
	if err := e.checkWorkflows(shown, t.Stack, stages); err != nil {
		return err
	}
	if err := checkArtifactTypes(shown, &t.ArtifactTypes); err != nil {
		return err
	}
	if err := checkOutputs(shown, stages, &t.ArtifactTypes); err != nil {
		return err
	}
	return checkSubmit(shown, &t.Submit, &t.ArtifactTypes)
}

// checkSubmit checks submit in the process.yaml shown, which may be left out: an object
// with type, an artifact type name, and send, one of [submitSends], and no other key.
// types, the file's artifact_types as [checkArtifactTypes] passed it, has a custom entry
// named type with base_view structured. The error is a [submitRefusal].
func checkSubmit(shown string, submit, types *yaml.Node) error {
	if submit.Kind == 0 {
		return nil
	}
	if submit.Kind != yaml.MappingNode {
		return submitRefusal(shown, "Set submit to an object with type and send, or remove it.", "submit is "+nodeKind(submit)+".")
	}
	seen := map[string]bool{}
	for j := 0; j+1 < len(submit.Content); j += 2 {
		key := submit.Content[j]
		if key.Kind != yaml.ScalarNode || key.Value != "type" && key.Value != "send" {
			return submitRefusal(shown, "Remove the key "+scalarText(key)+" from submit.", "submit has only type and send.")
		}
		if seen[key.Value] {
			return submitRefusal(shown, "Remove the second "+key.Value+" from submit.", "submit has the key "+key.Value+" twice.")
		}
		seen[key.Value] = true
	}
	sends := strings.Join(submitSends, " or ")
	typ, send := value(submit, "type"), value(submit, "send")
	if typ == nil {
		return submitRefusal(shown, "Add type to submit, the name of a structured entry of artifact_types.", "submit has no type.")
	}
	if typ.Kind == yaml.ScalarNode && typ.Tag != "!!str" && nodeKind(typ) == "a string" {
		// A date or a value with an explicit tag: unquoted, YAML reads it as no string.
		return submitRefusal(shown, "Set submit.type to a quoted string.", "YAML reads "+scalarText(typ)+" as a type that is not a string.")
	}
	if typ.Kind != yaml.ScalarNode || typ.Tag != "!!str" {
		return submitRefusal(shown, "Set submit.type to the name of a structured entry of artifact_types.", "submit.type is "+nodeKind(typ)+".")
	}
	if !runapi.ArtifactName(typ.Value) {
		return submitRefusal(shown, fmt.Sprintf("Set submit.type to a name such as reply-draft, at most %d bytes.", runapi.MaxArtifactName), "submit.type is "+scalarText(typ)+".")
	}
	if send == nil {
		return submitRefusal(shown, "Add send to submit, "+sends+".", "submit has no send.")
	}
	if send.Kind == yaml.ScalarNode && send.Tag != "!!str" && nodeKind(send) == "a string" {
		// A value with an explicit tag, such as !foo end_of_run: YAML reads it as no string.
		return submitRefusal(shown, "Set submit.send to a quoted string.", "YAML reads "+scalarText(send)+" as a type that is not a string.")
	}
	if send.Kind != yaml.ScalarNode || send.Tag != "!!str" {
		return submitRefusal(shown, "Set submit.send to "+sends+".", "submit.send is "+nodeKind(send)+".")
	}
	if !slices.Contains(submitSends, send.Value) {
		return submitRefusal(shown, "Set submit.send to "+sends+".", "submit.send is "+scalarText(send)+".")
	}
	return checkSubmitType(shown, typ.Value, types)
}

// checkSubmitType checks that types, a list of artifact types that [checkArtifactTypes]
// passed or nothing, has a custom entry named name with base_view structured. The name
// has the form of an artifact type name, so the message prints it bare.
func checkSubmitType(shown, name string, types *yaml.Node) error {
	const fix = "Set submit.type to a structured type of artifact_types."
	for _, entry := range types.Content {
		if n := value(entry, "name"); n == nil || n.Value != name {
			continue
		}
		view := value(entry, "base_view")
		switch {
		case view == nil:
			return submitRefusal(shown, fix, name+" is a standard type.")
		case view.Value != baseViewStructured:
			return submitRefusal(shown, fix, name+" has base_view "+view.Value+".")
		}
		return nil
	}
	return submitRefusal(shown, fix, "No entry of artifact_types is named "+name+".")
}

// stageLabel is the stage at index i for a message: its name as a name is shown when it
// has a text one, else its number from 1.
func stageLabel(i int, stage *yaml.Node) string {
	if n := value(stage, "name"); n != nil && n.Kind == yaml.ScalarNode && strings.TrimSpace(n.Value) != "" {
		return accesskey.ShownName(n.Value, false)
	}
	return strconv.Itoa(i + 1)
}

// checkOutputs checks outputs of each stage of stages, which [checkProcess] has checked
// is a list, in the process.yaml shown: a list of objects with type, the name of an entry
// of types, the artifact_types that [checkArtifactTypes] passed or nothing, and required,
// true or false, left out for false, and no other key. A stage without outputs passes.
// Any other key of a stage is left unchecked.
func checkOutputs(shown string, stages, types *yaml.Node) error {
	listed := map[string]bool{}
	for _, entry := range types.Content {
		if n := value(entry, "name"); n != nil {
			listed[n.Value] = true
		}
	}
	for i, stage := range stages.Content {
		outputs := value(stage, "outputs")
		if stage.Kind != yaml.MappingNode || outputs == nil {
			continue
		}
		label := stageLabel(i, stage)
		if outputs.Kind != yaml.SequenceNode {
			return processRefusal(shown, "Set outputs of the stage to a list, or remove it.", "outputs of stage "+label+" is "+nodeKind(outputs)+".")
		}
		once := map[string]bool{}
		for j, entry := range outputs.Content {
			at := fmt.Sprintf("entry %d of outputs of stage %s", j+1, label)
			if entry.Kind != yaml.MappingNode {
				return processRefusal(shown, "Set the entry of outputs to an object with type and required.", capital(at)+" is "+nodeKind(entry)+".")
			}
			seen := map[string]bool{}
			for k := 0; k+1 < len(entry.Content); k += 2 {
				key := entry.Content[k]
				if key.Kind != yaml.ScalarNode || key.Value != "type" && key.Value != "required" {
					return processRefusal(shown, "Remove the key "+scalarText(key)+" from the entry of outputs.", capital(at)+" has the key, and an output has only type and required.")
				}
				if seen[key.Value] {
					return processRefusal(shown, "Remove the second "+key.Value+" from the entry of outputs.", capital(at)+" has the key "+key.Value+" twice.")
				}
				seen[key.Value] = true
			}
			typ := value(entry, "type")
			switch {
			case typ == nil:
				return processRefusal(shown, "Add type to the entry of outputs, the name of an entry of artifact_types.", capital(at)+" has no type.")
			case typ.Kind != yaml.ScalarNode || typ.Tag != "!!str":
				return processRefusal(shown, "Set type of the entry of outputs to a quoted string, a name from artifact_types.", "type of "+at+" is "+nodeKind(typ)+".")
			case !runapi.ArtifactName(typ.Value):
				return processRefusal(shown, fmt.Sprintf("Set type of the entry of outputs to a name such as reply-draft, at most %d bytes.", runapi.MaxArtifactName), "type of "+at+" is "+scalarText(typ)+".")
			case !listed[typ.Value]:
				return processRefusal(shown, "Add "+typ.Value+" to artifact_types.", "Stage "+label+" has the output "+typ.Value+", and no entry of artifact_types is called "+typ.Value+".")
			}
			if req := value(entry, "required"); req != nil && !isBool(req) {
				return processRefusal(shown, "Set required of the entry of outputs to true or false.", "required of "+at+" is "+nodeKind(req)+".")
			}
			if once[typ.Value] {
				fix := fmt.Sprintf("List each output type of stage %s once: %s appears twice.", label, typ.Value)
				if len(fix) > 100 {
					fix = "List each output type of the stage once."
				}
				return processRefusal(shown, fix, capital(at)+" has the type "+typ.Value+", which an earlier entry has.")
			}
			once[typ.Value] = true
		}
	}
	return nil
}

// processRefusal is a refusal of the process.yaml shown, fix first: the fix, the cause,
// and the file on the last line, so the first line stays within 100 characters whatever
// the path.
func processRefusal(shown, fix, cause string) error {
	return fmt.Errorf("%s\n  %s\n  The file is %s.", fix, cause, shown)
}

// submitRefusal is a refusal of submit in the process.yaml shown, fix first: the fix,
// the cause, and the file on the last line, so the first line stays within 100
// characters whatever the path.
func submitRefusal(shown, fix, cause string) error {
	return fmt.Errorf("%s\n  %s\n  submit is in %s.", fix, cause, shown)
}

// checkWorkflows checks that every workflow the stages run is declared under workflows in
// the qory-stack.yaml of the exported stack: each workflow entry of a stage's sequence,
// and each of a loop's body. A workflow that is empty, null or not one line of text, and
// a stage name that is not one line of text, are refused too, so no line of the error
// is text from the file that a reader could take for a line of qory's. Any other shape,
// such as a sequence that is not a list, is left unchecked. The error has one line per
// refusal, each starting with shown, the process.yaml.
func (e *Exports) checkWorkflows(shown, stackName string, stages *yaml.Node) error {
	dir, _ := e.Stack(stackName)
	stackFile := filepath.Join(dir, StackFileName)
	declared, err := e.stackWorkflows(stackFile)
	if err != nil {
		return err
	}
	var lines []string
	for i, stage := range stages.Content {
		if stage.Kind != yaml.MappingNode {
			continue
		}
		label := strconv.Itoa(i + 1)
		if n := value(stage, "name"); n != nil && n.Kind == yaml.ScalarNode {
			if why := notOneLine(n.Value); why != "" {
				lines = append(lines, processRefusal(shown, fmt.Sprintf("Set the name of stage %d to one line of text.", i+1), "The name "+why+".").Error())
			} else if strings.TrimSpace(n.Value) != "" {
				label = accesskey.ShownName(n.Value, false)
			}
		}
		seen := map[string]bool{}
		for _, ref := range stageWorkflows(stage) {
			if why := badWorkflow(ref.name); why != "" {
				lines = append(lines, processRefusal(shown, fmt.Sprintf("Set the workflow of %s in stage %s to a workflow name.", ref.where, label), "The workflow "+why+".").Error())
				continue
			}
			name := ref.name.Value
			if declared[name] || seen[name] {
				continue
			}
			seen[name] = true
			workflow := accesskey.ShownName(name, false)
			lines = append(lines, fmt.Sprintf("Declare %s in stack %s, or remove it from stage %s.\n  Stage %s of %s runs %s, and stack %s does not declare it under workflows.\n  The stack is in %s.",
				workflow, accesskey.ShownName(stackName, false), label, label, shown, workflow, accesskey.ShownName(stackName, false), HidePath(stackFile)))
		}
	}
	if len(lines) > 0 {
		return errors.New(strings.Join(lines, "\n"))
	}
	return nil
}

// badWorkflow is why the workflow value n is no workflow name, for a message: it is
// null, not a string, empty, or not one line of text; "" for a value that is one. A
// number or a boolean is not a string: a quoted "123" is one.
func badWorkflow(n *yaml.Node) string {
	switch {
	case n.Tag == "!!null":
		return "is null"
	case n.Kind != yaml.ScalarNode, n.Tag == "!!int", n.Tag == "!!float", n.Tag == "!!bool":
		return "is " + nodeKind(n)
	case n.Tag != "!!str":
		return "is not a string"
	case strings.TrimSpace(n.Value) == "":
		return "is empty"
	}
	return notOneLine(n.Value)
}

// notOneLine is why v is not one line of text, for a message: it has a line break, another
// control character, or a character that [primitives.StopsLine] stops, such as U+200B or a
// tag character, which the message writes as a code point; "" for one line.
func notOneLine(v string) string {
	if strings.ContainsAny(v, "\n\r") {
		return "has a line break"
	}
	if strings.IndexFunc(v, unicode.IsControl) >= 0 {
		return "has a control character"
	}
	if i := strings.IndexFunc(v, primitives.StopsLine); i >= 0 {
		r, _ := utf8.DecodeRuneInString(v[i:])
		return fmt.Sprintf("has the character %U, which a line of text does not have", r)
	}
	return ""
}

// stackWorkflows returns the names under workflows in the stack file at file, relative to
// [Exports.Root]. The stack reader checks the rest of the file when a compose reads it.
func (e *Exports) stackWorkflows(file string) (map[string]bool, error) {
	shown := HidePath(file)
	data, err := os.ReadFile(filepath.Join(e.Root, file))
	if err != nil {
		return nil, HideError(err)
	}
	if err := RefuseAliases(shown, data); err != nil {
		return nil, err
	}
	var s struct {
		Workflows map[string]yaml.Node `yaml:"workflows"`
	}
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, DecodeError(shown, err)
	}
	names := make(map[string]bool, len(s.Workflows))
	for name := range s.Workflows {
		names[name] = true
	}
	return names, nil
}

// workflowRef is one workflow entry of a stage: where it is in the stage, as a message
// writes it, and the value of its workflow key.
type workflowRef struct {
	where string
	name  *yaml.Node
}

// stageWorkflows returns the workflow entries a stage runs, in order: each entry of its
// sequence that has a workflow key, and each such entry of a loop's body. Entries are
// counted from 1.
func stageWorkflows(stage *yaml.Node) []workflowRef {
	var refs []workflowRef
	seq := value(stage, "sequence")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	for i, entry := range seq.Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}
		if w := value(entry, "workflow"); w != nil {
			refs = append(refs, workflowRef{"entry " + strconv.Itoa(i+1), w})
		}
		loop := value(entry, "loop")
		if loop == nil || loop.Kind != yaml.MappingNode {
			continue
		}
		body := value(loop, "body")
		if body == nil || body.Kind != yaml.SequenceNode {
			continue
		}
		for j, b := range body.Content {
			if b.Kind != yaml.MappingNode {
				continue
			}
			if w := value(b, "workflow"); w != nil {
				refs = append(refs, workflowRef{fmt.Sprintf("entry %d in the loop of entry %d", j+1, i+1), w})
			}
		}
	}
	return refs
}

// value returns the value of key in the mapping m, nil when m has no such key or is not a
// mapping.
func value(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// scalarText is a YAML value for a message: a scalar quoted, as [Value] prints it, and
// any other value by its kind.
func scalarText(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return Value(n.Value, true)
	}
	return nodeKind(n)
}

// nodeKind is what a YAML value is, for a message: an object, a list, null, a string, a
// number or a boolean.
func nodeKind(n *yaml.Node) string {
	switch {
	case n.Tag == "!!null":
		return "null"
	case n.Kind == yaml.MappingNode:
		return "an object"
	case n.Kind == yaml.SequenceNode:
		return "a list"
	case n.Tag == "!!int" || n.Tag == "!!float":
		return "a number"
	case n.Tag == "!!bool":
		return "a boolean"
	}
	return "a string"
}

// ProcessSubmit returns typ and send of submit in the process.yaml at the root of the
// module folder dir, read through dir opened as an [os.Root], as [Exports.Verify] reads
// it. ok is false when the folder has no such file, and when the file cannot be read or
// decoded, has an alias, or has no submit whose type has the form of an artifact type
// name and whose send is end_of_run or after_gate. A run takes its submit
// settings from its run documents and nothing from this file, so ProcessSubmit returns
// no error; a caller prints typ and send as they are.
func ProcessSubmit(dir string) (typ, send string, ok bool) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", "", false
	}
	defer root.Close()
	data, err := root.ReadFile(ProcessFileName)
	if err != nil || RefuseAliases(ProcessFileName, data) != nil {
		return "", "", false
	}
	var f struct {
		Submit yaml.Node `yaml:"submit"`
	}
	if err := yaml.Unmarshal(data, &f); err != nil {
		return "", "", false
	}
	t, s := value(&f.Submit, "type"), value(&f.Submit, "send")
	if t == nil || s == nil || t.Kind != yaml.ScalarNode || t.Tag != "!!str" || s.Kind != yaml.ScalarNode || s.Tag != "!!str" {
		return "", "", false
	}
	if !runapi.ArtifactName(t.Value) || !slices.Contains(submitSends, s.Value) {
		return "", "", false
	}
	return t.Value, s.Value, true
}
