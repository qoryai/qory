package exports

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/qoryai/forager/accesskey"
	"gopkg.in/yaml.v3"
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
// workspace type, the stack of the same repository its workflows run on, and the process
// document. qory checks the document's schema, that its stages are a list, and that each
// workflow of a stage is one the stack declares, and checks nothing else in it.
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
type processFile struct {
	APIVersion  string    `yaml:"apiVersion"`
	Name        string    `yaml:"name"`
	Summary     string    `yaml:"summary"`
	Description string    `yaml:"description"`
	Type        string    `yaml:"type"`
	Stack       string    `yaml:"stack"`
	Process     yaml.Node `yaml:"process"`
}

// checkProcess reads the process.yaml at path, in an exported module's folder, and
// checks its fields: each one is set, the texts are within their limits, stack is one of
// [Exports.Stacks], process is an object with schema qory.process/v1 and a list of
// stages, and every workflow of a stage's sequence, a loop's body included, is declared
// under workflows in the stack's qory-stack.yaml. The error starts with the file and
// then the fix; for undeclared workflows it has one line for each.
func (e *Exports) checkProcess(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return HideError(err)
	}
	shown := HidePath(path)
	if err := RefuseAliases(shown, data); err != nil {
		return err
	}
	var t processFile
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil && !errors.Is(err, io.EOF) {
		return DecodeError(shown, err)
	}
	if strings.TrimSpace(t.APIVersion) == "" {
		return fmt.Errorf("%s: add apiVersion: %s: the file has no apiVersion", shown, APIVersion)
	}
	if _, err := ResolveAPIVersion(t.APIVersion); err != nil {
		return fmt.Errorf("%s: %w", shown, HideValueError(err, ""))
	}
	for _, f := range []struct {
		key, value string
		limit      int
	}{{"name", t.Name, maxProcessName}, {"summary", t.Summary, maxProcessSummary}, {"description", t.Description, maxProcessDescription}} {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("%s: add %s, at most %d characters: the file has no %s", shown, f.key, f.limit, f.key)
		}
		if n := utf8.RuneCountInString(f.value); n > f.limit {
			return fmt.Errorf("%s: shorten %s to at most %d characters: %s has %d", shown, f.key, f.limit, f.key, n)
		}
	}
	if n := len(strings.Fields(t.Description)); n > maxDescriptionWords {
		return fmt.Errorf("%s: shorten description to at most %d words: description has %d", shown, maxDescriptionWords, n)
	}
	if strings.TrimSpace(t.Type) == "" {
		return fmt.Errorf("%s: add type, the workspace type the template sets up: the file has no type", shown)
	}
	if t.Stack == "" {
		return fmt.Errorf("%s: add stack, a name that exports.stacks lists: the file has no stack", shown)
	}
	if !contains(e.Stacks, t.Stack) {
		listed := "no stacks"
		if len(e.Stacks) > 0 {
			listed = strings.Join(accesskey.ShownNames(e.Stacks), ", ")
		}
		return fmt.Errorf("%s: set stack to a name that exports.stacks lists: stack is %s, and exports.stacks lists %s", shown, accesskey.ShownName(t.Stack, false), listed)
	}
	if t.Process.Kind == 0 || t.Process.Tag == "!!null" {
		return fmt.Errorf("%s: add process, the template's process document as an object: the file has no process", shown)
	}
	if t.Process.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: set process to an object, the template's process document: process is %s", shown, nodeKind(&t.Process))
	}
	schema, stages := value(&t.Process, "schema"), value(&t.Process, "stages")
	if schema == nil || schema.Tag == "!!null" {
		return fmt.Errorf("%s: add schema: %s under process: process has no schema", shown, ProcessSchema)
	}
	if schema.Kind != yaml.ScalarNode || schema.Value != ProcessSchema {
		return fmt.Errorf("%s: set process.schema to %s: process.schema is %s", shown, ProcessSchema, scalarText(schema))
	}
	if stages == nil || stages.Tag == "!!null" {
		return fmt.Errorf("%s: add stages under process, the list of the process's stages: process has no stages", shown)
	}
	if stages.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s: set process.stages to a list of stages: process.stages is %s", shown, nodeKind(stages))
	}
	return e.checkWorkflows(shown, t.Stack, stages)
}

// checkWorkflows checks that every workflow the stages run is declared under workflows in
// the qory-stack.yaml of the exported stack: each workflow entry of a stage's sequence,
// and each of a loop's body. Any other shape, such as a sequence that is not a list, is
// left unchecked. The error has one line per stage and undeclared workflow, each starting
// with shown, the process.yaml.
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
		if n := value(stage, "name"); n != nil && n.Kind == yaml.ScalarNode && strings.TrimSpace(n.Value) != "" {
			label = accesskey.ShownName(n.Value, false)
		}
		seen := map[string]bool{}
		for _, name := range stageWorkflows(stage) {
			if declared[name] || seen[name] {
				continue
			}
			seen[name] = true
			lines = append(lines, fmt.Sprintf("%s: declare %s under workflows in %s, or remove it from stage %s: stack %s does not declare it",
				shown, accesskey.ShownName(name, false), HidePath(stackFile), label, accesskey.ShownName(stackName, false)))
		}
	}
	if len(lines) > 0 {
		return errors.New(strings.Join(lines, "\n"))
	}
	return nil
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

// stageWorkflows returns the workflow names a stage runs, in order: the workflow of each
// entry of its sequence, and of each entry of a loop's body.
func stageWorkflows(stage *yaml.Node) []string {
	var names []string
	add := func(entry *yaml.Node) {
		if w := value(entry, "workflow"); w != nil && w.Kind == yaml.ScalarNode {
			names = append(names, w.Value)
		}
	}
	seq := value(stage, "sequence")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	for _, entry := range seq.Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}
		add(entry)
		loop := value(entry, "loop")
		if loop == nil || loop.Kind != yaml.MappingNode {
			continue
		}
		if body := value(loop, "body"); body != nil && body.Kind == yaml.SequenceNode {
			for _, b := range body.Content {
				if b.Kind == yaml.MappingNode {
					add(b)
				}
			}
		}
	}
	return names
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

// nodeKind is what a YAML value is, for a message: an object, a list, a string, a number
// or a boolean.
func nodeKind(n *yaml.Node) string {
	switch {
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
