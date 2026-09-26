package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/qoryai/runner/session"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/integration"
)

// integrationKey is the grammar of an integration's key, the integration contract's
// name: the runner's credential name without the dot, which a program's name, qory-<key>,
// would read as an extension.
var integrationKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ProgramPrefix is what an integration's key follows in the name of its program when
// the entry names none: qory-github for github, looked up on the PATH.
const ProgramPrefix = "qory-"

// RunnerIntegration is one entry of the integrations section: a program that speaks
// the integration contract, and the settings every role of it is started with.
type RunnerIntegration struct {
	// Key is the name the machine declares the integration under, and the name of what
	// it defines: the credential of its credential role.
	Key string
	// Program is the program as the entry names it, [ProgramPrefix] and the key when it
	// names none: an absolute path, or a name looked up on the PATH.
	Program string
	// Settings is the settings document as compact JSON, its keys in the file's order;
	// {} when the entry has none.
	Settings []byte
	// Path and Version are the program found and the version its description gives,
	// set by [Runner.Expand].
	Path, Version string
}

// readIntegrations reads the integrations section, a mapping from a key to an entry,
// in the file's order. The settings are written out as JSON here, and read against the
// program's description by [Runner.Expand], which runs the program.
func readIntegrations(path string, node *yaml.Node) ([]RunnerIntegration, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: integrations is a mapping from a name to an integration", path)
	}
	var out []RunnerIntegration
	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if !integrationKey.MatchString(key) {
			return nil, fmt.Errorf("%s: integrations: %q is not 1 to 64 of a-z, 0-9, underscore and dash, starting with a letter or a digit", path, key)
		}
		if seen[key] {
			return nil, fmt.Errorf("%s: integrations.%s is declared twice", path, key)
		}
		seen[key] = true
		in := RunnerIntegration{Key: key, Program: ProgramPrefix + key, Settings: []byte("{}")}
		entry := node.Content[i+1]
		if entry.ShortTag() == "!!null" {
			out = append(out, in)
			continue
		}
		if entry.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s: integrations.%s is a mapping: program and settings", path, key)
		}
		for k := 0; k+1 < len(entry.Content); k += 2 {
			value := entry.Content[k+1]
			switch name := entry.Content[k].Value; name {
			case "program":
				if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" || value.Value == "" {
					return nil, fmt.Errorf("%s: integrations.%s.program is a path or a name on the PATH", path, key)
				}
				if strings.ContainsRune(value.Value, '/') && !filepath.IsAbs(value.Value) {
					return nil, fmt.Errorf("%s: integrations.%s.program %q is not an absolute path; name a program by its absolute path or by a name on the PATH", path, key, value.Value)
				}
				in.Program = value.Value
			case "settings":
				if value.Kind != yaml.MappingNode {
					return nil, fmt.Errorf("%s: integrations.%s.settings is a mapping, the settings document", path, key)
				}
				var b bytes.Buffer
				if err := writeJSON(&b, value, "settings"); err != nil {
					return nil, fmt.Errorf("%s: integrations.%s.%w", path, key, err)
				}
				in.Settings = b.Bytes()
			default:
				return nil, fmt.Errorf("%s: integrations.%s: key %q is not one", path, key, name)
			}
		}
		out = append(out, in)
	}
	return out, nil
}

// writeJSON writes the YAML under n as compact JSON, a mapping's keys in the file's
// order. at is where n stands, for an error, which names the place and never the value.
// An alias is refused: the settings go on a command line, whole, and a few chained
// aliases expand to more than one can hold.
func writeJSON(b *bytes.Buffer, n *yaml.Node, at string) error {
	switch n.Kind {
	case yaml.AliasNode:
		return fmt.Errorf("%s: line %d: *%s is a YAML alias, which the settings may not hold; write the value out in full", at, n.Line, n.Value)
	case yaml.MappingNode:
		b.WriteByte('{')
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind == yaml.AliasNode || k.ShortTag() == "!!merge" {
				return fmt.Errorf("%s: line %d: a YAML alias or merge, which the settings may not hold; write the value out in full", at, k.Line)
			}
			if k.Kind != yaml.ScalarNode || k.ShortTag() != "!!str" {
				return fmt.Errorf("%s: line %d: a key that is not a string", at, k.Line)
			}
			if seen[k.Value] {
				return fmt.Errorf("%s.%s is given twice", at, k.Value)
			}
			seen[k.Value] = true
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, k.Value)
			b.WriteByte(':')
			if err := writeJSON(b, n.Content[i+1], at+"."+k.Value); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case yaml.SequenceNode:
		b.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeJSON(b, c, fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case yaml.ScalarNode:
		switch n.ShortTag() {
		case "!!null":
			b.WriteString("null")
		case "!!bool", "!!int", "!!float":
			var v any
			if err := n.Decode(&v); err != nil {
				return fmt.Errorf("%s is not a number or a boolean JSON holds", at)
			}
			if f, ok := v.(float64); ok && (math.IsInf(f, 0) || math.IsNaN(f)) {
				return fmt.Errorf("%s is not a number JSON holds", at)
			}
			out, err := json.Marshal(v)
			if err != nil {
				return fmt.Errorf("%s is not a value JSON holds", at)
			}
			b.Write(out)
		default:
			// A string, and a timestamp or anything else tagged, as it was written.
			writeString(b, n.Value)
		}
	default:
		return fmt.Errorf("%s: line %d: not a value the settings hold", at, n.Line)
	}
	return nil
}

// writeString writes s as a JSON string, with <, > and & as they are.
func writeString(b *bytes.Buffer, s string) {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	b.Truncate(b.Len() - 1) // the newline Encode ends with
}

// Expand describes every integration the file declares and adds the credentials they
// define to r.Credentials, after the file's own, in the section's order. For each it
// finds the program, runs <program> describe as [integration.Describe] does, and checks
// the settings against the description; a program that is not found or does not answer,
// settings the description refuses, and an integration that plays no role qory knows
// are errors naming the file and the key. The credential role defines the credential
// named by the key, with the adapter [integration.CredentialAdapter] gives, unless the
// file's credentials section defines that name itself: then the file's definition
// stands, and the integration's credential is not defined. A role qory does not know is
// left alone. Expand runs once; later calls do nothing.
func (r *Runner) Expand(ctx context.Context) error {
	if r == nil || r.expanded {
		return nil
	}
	r.expanded = true
	own := map[string]bool{}
	for _, c := range r.Credentials {
		own[c.Name] = true
	}
	for i := range r.Integrations {
		in := &r.Integrations[i]
		fail := func(format string, a ...any) error {
			return fmt.Errorf("%s: integrations.%s: %s", r.File, in.Key, fmt.Sprintf(format, a...))
		}
		found, err := exec.LookPath(in.Program)
		if err != nil {
			if filepath.IsAbs(in.Program) {
				return fail("%s is not a program this user may run", in.Program)
			}
			return fail("%s is not on the PATH; install it there, or name the program by its path with program", in.Program)
		}
		if found, err = filepath.Abs(found); err != nil {
			return fail("%v", err)
		}
		d, err := integration.Describe(ctx, found)
		if err != nil {
			return fail("%v", err)
		}
		if err := d.CheckSettings(in.Settings); err != nil {
			return fail("%v", err)
		}
		in.Path, in.Version = found, d.ProgramVersion
		if d.Credential == nil {
			return fail("%s plays no role qory knows, %s, and would define nothing", in.Program, strings.Join(d.Roles, ", "))
		}
		if own[in.Key] {
			continue
		}
		c := RunnerCredential{Name: in.Key, Adapter: integration.CredentialAdapter(found, in.Settings), Argument: d.Credential.Argument, Hosts: d.Credential.Hosts, Integration: in.Key}
		if err := (session.Credential{Name: c.Name, Adapter: c.Adapter, Argument: c.Argument, Hosts: c.Hosts}).Check(); err != nil {
			return fail("%v", err)
		}
		r.Credentials = append(r.Credentials, c)
	}
	return nil
}
