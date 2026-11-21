package compose_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"gopkg.in/yaml.v3"
)

const fixtures = "../../contracts/harness/v1/fixtures"

// TestFixtures runs every fixture: the document in its qory.yaml, on the stack it
// extends when it extends one, composes to the expected entries, settings, instructions,
// push checks, scope checks, workflow plan types, workflow hooks, workflow access levels and
// runtime pins, or fails with the expected error text.
func TestFixtures(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join(fixtures, "*"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no fixtures under %s: %v", fixtures, err)
	}
	for _, dir := range dirs {
		dir, err := filepath.Abs(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.Base(dir), func(t *testing.T) {
			checkout := ""
			// A workflow hook's program is read from the commit at HEAD of the checkout,
			// so a fixture with hooks is composed from a copy that is a git checkout.
			if name := filepath.Base(dir); listsHooks(t, dir) {
				dir = realTemp(t)
				copyFixture(t, name, dir)
				commitAll(t, dir)
				checkout = dir
			}
			t.Chdir(dir)
			res, err := load(checkout)
			if want, readErr := os.ReadFile("expected/error.txt"); readErr == nil {
				if err == nil {
					t.Fatalf("composed without error; want:\n%s", want)
				}
				// A file of a stack the document extends is in the error by its absolute
				// path; the expected text has it relative to the fixture.
				got := strings.ReplaceAll(err.Error(), dir+string(filepath.Separator), "")
				if got != strings.TrimRight(string(want), "\n") {
					t.Fatalf("error:\n%s\nwant:\n%s", got, want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var b strings.Builder
			for _, e := range res.Entries {
				fmt.Fprintf(&b, "%s/%s %s\n", e.Kind, e.Name, e.Module)
			}
			expectText(t, "expected/entries.txt", b.String())
			// expected/settings/<runtime>/<file>.json holds the merged target file as JSON;
			// a TOML target is expected as <file>.toml.json.
			expected, _ := filepath.Glob("expected/settings/*/*.json")
			for _, file := range expected {
				runtime := filepath.Base(filepath.Dir(file))
				target := strings.TrimSuffix(filepath.Base(file), ".json")
				if strings.HasSuffix(target, ".toml") {
					target = strings.TrimSuffix(target, ".json")
				} else {
					target += ".json"
				}
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				var want map[string]any
				if err := json.Unmarshal(data, &want); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(res.Settings[runtime][target], want) {
					got, _ := json.MarshalIndent(res.Settings[runtime][target], "", "  ")
					t.Fatalf("settings %s/%s:\n%s\nwant:\n%s", runtime, target, got, data)
				}
			}
			n := 0
			for _, files := range res.Settings {
				n += len(files)
			}
			if n != len(expected) {
				t.Fatalf("%d merged settings files, want %d", n, len(expected))
			}
			if _, err := os.Stat("expected/AGENTS.md"); err == nil {
				expectText(t, "expected/AGENTS.md", res.Instructions)
			}
			// expected/references.txt lists the entries that reference others, and
			// expected/bind.txt the roles, each with the entry that fills it and the
			// layer that set it, one line per member of a group role, marked group, and
			// a member the checkout's bind replaced marked replaced; each when the
			// fixture has any.
			b.Reset()
			for _, e := range res.Entries {
				if len(e.References) > 0 {
					fmt.Fprintf(&b, "%s/%s: %s\n", e.Kind, e.Name, strings.Join(e.References, " "))
				}
			}
			if _, err := os.Stat("expected/references.txt"); err == nil || b.Len() > 0 {
				expectText(t, "expected/references.txt", b.String())
			}
			b.Reset()
			single := 0
			for _, r := range res.Roles {
				switch {
				case r.Replaced:
					fmt.Fprintf(&b, "%s %s %s group replaced\n", r.Role, r.Name, r.From)
					continue
				case r.Group:
					if !slices.Contains(res.Groups[r.Role], r.Name) {
						t.Fatalf("Groups %s is %v, and Roles has %s", r.Role, res.Groups[r.Role], r.Name)
					}
					fmt.Fprintf(&b, "%s %s %s group\n", r.Role, r.Name, r.From)
					continue
				}
				single++
				if res.Bind[r.Role] != r.Name {
					t.Fatalf("Bind %s is %s, and Roles has %s", r.Role, res.Bind[r.Role], r.Name)
				}
				fmt.Fprintf(&b, "%s %s %s\n", r.Role, r.Name, r.From)
			}
			if len(res.Bind) != single {
				t.Fatalf("Bind has %d roles, and Roles %d", len(res.Bind), single)
			}
			if _, err := os.Stat("expected/bind.txt"); err == nil || b.Len() > 0 {
				expectText(t, "expected/bind.txt", b.String())
			}
			// expected/egress.txt is the union of the declared hosts, each with the
			// modules that declared it, when any module has the key; an empty file
			// is a harness that declares and names nothing.
			b.Reset()
			declared := false
			byHost := map[string][]string{}
			for _, m := range res.Modules {
				declared = declared || m.Egress != nil
				for _, host := range m.Egress {
					byHost[host] = append(byHost[host], m.Name)
				}
			}
			hosts := make([]string, 0, len(byHost))
			for host := range byHost {
				hosts = append(hosts, host)
			}
			sort.Strings(hosts)
			for _, host := range hosts {
				fmt.Fprintf(&b, "%s %s\n", host, strings.Join(byHost[host], " "))
			}
			if _, err := os.Stat("expected/egress.txt"); err == nil || declared {
				expectText(t, "expected/egress.txt", b.String())
			}
			// expected/push-checks.txt is each push check, sorted by type, with its module,
			// its command and the types it reads, when any module declares one.
			b.Reset()
			for _, c := range res.PushChecks {
				fmt.Fprintf(&b, "%s %s %s", c.Type, c.Module, strings.Join(c.Command, " "))
				if len(c.Reads) > 0 {
					fmt.Fprintf(&b, " reads %s", strings.Join(c.Reads, " "))
				}
				b.WriteString("\n")
			}
			if _, err := os.Stat("expected/push-checks.txt"); err == nil || b.Len() > 0 {
				expectText(t, "expected/push-checks.txt", b.String())
			}
			// expected/submit-program.txt is the submit program's module and command, when a
			// module declares one.
			b.Reset()
			if res.SubmitProgram != nil {
				fmt.Fprintf(&b, "%s %s\n", res.SubmitProgram.Module, strings.Join(res.SubmitProgram.Command, " "))
			}
			if _, err := os.Stat("expected/submit-program.txt"); err == nil || b.Len() > 0 {
				expectText(t, "expected/submit-program.txt", b.String())
			}
			// expected/scope-checks.txt is the command of each scope check, sorted by type,
			// with its module and its words, when any module of the base stack declares
			// one. expected/plans.txt is each workflow that names a plan type, with it.
			b.Reset()
			for _, typ := range slices.Sorted(maps.Keys(res.ScopeChecks)) {
				fmt.Fprintf(&b, "%s %s %s\n", typ, res.ScopeChecks[typ].Module, strings.Join(res.ScopeChecks[typ].Command, " "))
			}
			if _, err := os.Stat("expected/scope-checks.txt"); err == nil || b.Len() > 0 {
				expectText(t, "expected/scope-checks.txt", b.String())
			}
			b.Reset()
			for _, name := range slices.Sorted(maps.Keys(res.Stack.Workflows)) {
				if plan := res.Stack.Workflows[name].Plan; plan != "" {
					fmt.Fprintf(&b, "%s %s\n", name, plan)
				}
			}
			if _, err := os.Stat("expected/plans.txt"); err == nil || b.Len() > 0 {
				expectText(t, "expected/plans.txt", b.String())
			}
			// expected/hooks.txt is each workflow hook, in the order of harness.hooks, with
			// its workflow, point, place in the session, kind, name, what it runs, whether
			// it is required and its timeout in seconds, when the checkout lists any.
			b.Reset()
			for _, h := range res.Hooks {
				runs := "run " + strings.Join(h.Run, " ")
				if h.Agent != "" {
					runs = "agent " + h.Agent
				}
				required := "required"
				if !h.Required {
					required = "optional"
				}
				fmt.Fprintf(&b, "%d %s %s %s %s %s %s %s %d\n", h.Index, h.Workflow, h.At, h.PointAt, h.Kind, h.Name, runs, required, h.TimeoutSeconds)
			}
			if _, err := os.Stat("expected/hooks.txt"); err == nil || b.Len() > 0 {
				expectText(t, "expected/hooks.txt", b.String())
			}
			// expected/access.txt is the access level of each workflow of the stack, as
			// a dispatched run reads it, when the stack has any workflows.
			b.Reset()
			for _, name := range slices.Sorted(maps.Keys(res.Stack.Workflows)) {
				fmt.Fprintf(&b, "%s %s\n", name, res.Stack.AccessOf(name))
			}
			if _, err := os.Stat("expected/access.txt"); err == nil || b.Len() > 0 {
				expectText(t, "expected/access.txt", b.String())
			}
			// expected/review-groups.txt is each workflow that sets review_group, with the
			// group and its members in order, when the stack has any.
			b.Reset()
			for _, name := range slices.Sorted(maps.Keys(res.Stack.Workflows)) {
				if group := res.Stack.Workflows[name].ReviewGroup; group != "" {
					fmt.Fprintf(&b, "%s %s %s\n", name, group, strings.Join(res.Groups["agents/"+group], " "))
				}
			}
			if _, err := os.Stat("expected/review-groups.txt"); err == nil || b.Len() > 0 {
				expectText(t, "expected/review-groups.txt", b.String())
			}
			// expected/runtimes.txt is the version each runtime is pinned to, with the file
			// of the layer that pins it, when the stack pins any.
			b.Reset()
			for _, name := range slices.Sorted(maps.Keys(res.Stack.RuntimePins)) {
				pin := res.Stack.RuntimePins[name]
				fmt.Fprintf(&b, "%s %s %s\n", name, pin.Version, strings.TrimPrefix(pin.From, dir+string(filepath.Separator)))
			}
			if _, err := os.Stat("expected/runtimes.txt"); err == nil || b.Len() > 0 {
				expectText(t, "expected/runtimes.txt", b.String())
			}
		})
	}
}

// fixVerbs are the verbs a refusal's fix line starts with.
var fixVerbs = []string{"Add", "Bind", "Change", "Commit", "Complete", "Declare", "Fill", "Fix", "Give", "Keep", "Move", "Remove", "Rename", "Set", "Shorten", "Ship", "Write"}

// printedLength is the length of line 1 of a refusal as qory prints it, after "✗ ".
func printedLength(line string) int { return utf8.RuneCountInString("✗ " + line) }

// TestRefusalsStartWithTheFix is every refusal a fixture expects written fix first: each
// line of expected/error.txt that is not indented starts a refusal, and is a fix line of
// at most 100 characters with the "✗ " that qory prints before it, and starts with a verb
// of fixVerbs. The lines after it,
// indented, are the cause and the values.
func TestRefusalsStartWithTheFix(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixtures, "*", "expected", "error.txt"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no expected refusals under %s: %v", fixtures, err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(filepath.Dir(filepath.Dir(file)))
		for i, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if strings.HasPrefix(line, " ") {
				continue
			}
			if n := printedLength(line); n > 100 {
				t.Errorf("%s: line %d is a fix line of %d characters with the \"✗ \"; shorten it to 100 or fewer", name, i+1, n)
			}
			if word, _, _ := strings.Cut(line, " "); !slices.Contains(fixVerbs, word) {
				t.Errorf("%s: line %d starts with %q; start the fix line with one of %s", name, i+1, word, strings.Join(fixVerbs, ", "))
			}
		}
	}
}

// listsHooks reports whether the qory.yaml of the fixture at dir has hooks under
// harness, whatever their value.
func listsHooks(t *testing.T, dir string) bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, config.FileName))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Harness struct {
			Hooks yaml.Node `yaml:"hooks"`
		} `yaml:"harness"`
	}
	// A fixture whose qory.yaml does not parse expects that refusal, before any hook.
	if yaml.Unmarshal(data, &doc) != nil {
		return false
	}
	return doc.Harness.Hooks.Kind != 0
}

// load composes the fixture in the working directory for the checkout at checkout, ""
// for that directory. It first checks the fixture's exports, as config.Load checks them
// at a repository root, so a fixture can expect an exports refusal, such as one of a
// module's process.yaml.
func load(checkout string) (*compose.Result, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	e, err := exports.Read(root)
	if err != nil {
		return nil, err
	}
	if e != nil {
		if err := e.Verify(); err != nil {
			return nil, err
		}
	}
	p, err := config.Compose(config.FileName)
	if err != nil {
		return nil, err
	}
	merged, base, err := compose.LoadBase(p, "", compose.Options{})
	if err != nil {
		return nil, err
	}
	return compose.ComposeWith(merged, compose.Options{Base: base, Checkout: checkout})
}

func expectText(t *testing.T, path, got string) {
	t.Helper()
	want, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s is missing; got:\n%s", path, got)
	}
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s:\n%s\nwant:\n%s", path, got, want)
	}
}
