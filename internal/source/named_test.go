package source_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/proxygit/proxygittest"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// base is the phrase of the base of the workspace's harness.
const base = "the base of the workspace's harness"

// namedOf is the Named of the workspace's harness whose base is the folder core of url
// at ref, recorded.
func namedOf(url, ref string) (*source.Named, stack.Source) {
	s := stack.Source{Git: url, Ref: ref, Path: "core"}
	n := source.NewNamed("the workspace's harness")
	n.Add(s, n.Base())
	return n, s
}

// TestANamedHarnessHidesItsAddresses is the text of a compose of a harness whose
// addresses no text shows: the base's URL with and without its ref, and a module's URL,
// each show as the source's phrase, the longest first, only where nothing that
// continues a URL follows; a path in a source's repository shows from the source's
// folder; and a source as a compose's result writes it has its phrase.
func TestANamedHarnessHidesItsAddresses(t *testing.T) {
	url, tools := "https://code.example.com/team/stacks", "https://code.example.com/team/tools.git"
	n, _ := namedOf(url, "main")
	n.Add(stack.Source{Git: tools, Ref: "release-3", Path: "modules/tools"}, n.Module("tools"))
	for _, c := range []struct{ text, want string }{
		{"check " + url + "#main:core: x", "check " + base + ": x"},
		{"module at " + url + "#main:core/modules/world", "module at modules/world in " + base},
		{"module at " + url + "#main:modules/world.", "module at " + base + "."},
		{"read " + url + "#main:core", "read " + base},
		{"a link to " + url + "-other/x and " + url + ".github", "a link to " + url + "-other/x and " + url + ".github"},
		{"a link to " + url + ".git and " + url + "/tree/main/core.", "a link to " + base + " and " + base + "."},
		{"cloning " + url + ".git/info/refs: no", "cloning " + base + ": no"},
		{"see " + tools + "/modules/tools (x)", "see the module tools (x)"},
		{"a link to " + url + ".", "a link to " + base + "."},
		{tools + " at release-3 exports nothing", "the module tools exports nothing"},
		{"fetching " + tools + "#release-3: no", "fetching the module tools: no"},
		{"read " + tools + "#release-3:modules/tools/AGENTS.md", "read AGENTS.md in the module tools"},
		{"read " + tools + "#release-3:docs/README.md", "read the module tools"},
		{"a link to " + url, "a link to " + base},
	} {
		if got := n.Hide(c.text); got != c.want {
			t.Errorf("%q\ngot  %q\nwant %q", c.text, got, c.want)
		}
	}
	for src, want := range map[string]string{
		url + "#main:modules/world":            base,
		tools + "#release-3:modules/tools":     "the module tools",
		tools + "#release-3 module tools":      "the module tools",
		"https://other.example/x#main:modules": "",
	} {
		if got, _ := n.Of(src); got != want {
			t.Errorf("Of(%s) is %q, want %q", src, got, want)
		}
	}
	if n.Phrase() != "the workspace's harness" || n.Base() != base || n.Module("") != "a module of the workspace's harness" || n.Module("tools") != "the module tools" {
		t.Errorf("phrases %q, %q, %q, %q", n.Phrase(), n.Base(), n.Module(""), n.Module("tools"))
	}
	err := n.HideError(errors.New("read " + url))
	if err.Error() != "read "+base {
		t.Errorf("the error is %q", err)
	}
	path := fmt.Errorf("module more: %w", &stack.SourcePathError{Path: "../secret-folder-2"})
	var sp *stack.SourcePathError
	if err := n.HideError(path); err.Error() != "module more: source.path is not a directory inside the repository" || !errors.As(err, &sp) {
		t.Errorf("a source.path that leads out shows as %q", err)
	}
	other := "https://code.example.com/team/other-stacks"
	_, extends := stack.Extend(&stack.Stack{File: "/stacks/qory-stack.yaml", Extends: stack.Source{Git: other, Ref: "main"}}, &stack.Stack{})
	var be *stack.BaseExtendsError
	if extends == nil || !strings.Contains(extends.Error(), other) {
		t.Fatalf("a base that extends another shows as %v, want its address when shown", extends)
	}
	if err := n.HideError(extends); err.Error() != "/stacks/qory-stack.yaml extends another stack, and a stack does not extend another" || !errors.As(err, &be) {
		t.Errorf("a base that extends another shows as %q", err)
	}
}

// TestANamedHarnessResolvesThroughTheProxyByNames resolves the base of a harness whose
// addresses no text shows through the proxy: the fetch is reported with the source's
// name, no folder or file name of the cache contains the URL, and none outside a clone's
// .git contains the ref. A path in the clone shows as the path from the folder of the
// source whose folder contains it, in that source's phrase, and a path in no source's
// folder as the phrase of the base.
func TestANamedHarnessResolvesThroughTheProxyByNames(t *testing.T) {
	hermetic(t)
	s := proxygittest.Start(t)
	commit := s.Repository(t, "example-org/stacks", "stacks-r7", map[string]string{"core/qory-stack.yaml": "name: core\n", "modules/tools/AGENTS.md": "# Tools\n"})
	n, ref := namedOf(s.URL("example-org/stacks"), "stacks-r7")
	n.Add(stack.Source{Git: ref.Git, Ref: ref.Ref, Path: "modules/tools"}, n.Module("tools"))
	cache := t.TempDir()
	var fetched []string
	g := &source.Git{Proxy: s.Proxy, Named: n, Fetched: func(_, _, name, c string, _ time.Duration) {
		fetched = append(fetched, name+" "+c)
	}}
	r, err := source.Resolve("", ref, source.Options{Cache: cache, Git: g, Name: n.Base()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(fetched, "\n") != base+" "+commit {
		t.Errorf("fetched %q", fetched)
	}
	filepath.WalkDir(cache, func(path string, d os.DirEntry, err error) error {
		inGit := strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator))
		if strings.Contains(path, "example-org") || strings.Contains(path, proxygittest.Host) || !inGit && strings.Contains(path, "stacks-r7") {
			t.Errorf("the cache has %s", path)
		}
		return nil
	})
	clone := filepath.Dir(r.Dir)
	for text, want := range map[string]string{
		"read " + filepath.Join(r.Dir, "qory-stack.yaml") + ": bad":               "read qory-stack.yaml in " + base + ": bad",
		"read " + filepath.Join(clone, "README.md") + ".":                         "read " + base + ".",
		"read " + filepath.Join(clone, "modules", "tools", "AGENTS.md") + ": bad": "read AGENTS.md in the module tools: bad",
		"read " + filepath.Join(clone, "modules", "tools") + ": bad":              "read the module tools: bad",
	} {
		if got := n.Hide(text); got != want {
			t.Errorf("%q shows as %q, want %q", text, got, want)
		}
	}
}

// TestANamedHarnesssFailedFetchShowsNoAddress is a ref the remote does not have, a fetch
// the proxy denies, and a source that is not https, of a harness whose addresses no
// text shows: each error shows the source's name, and none its URL, its ref or git's
// output.
func TestANamedHarnesssFailedFetchShowsNoAddress(t *testing.T) {
	hermetic(t)
	s := proxygittest.Start(t)
	s.Repository(t, "example-org/stacks", "main", map[string]string{"core/qory-stack.yaml": "name: core\n"})
	n, _ := namedOf(s.URL("example-org/stacks"), "main")
	g := &source.Git{Proxy: s.Proxy, Named: n}
	_, err := source.Resolve("", stack.Source{Git: s.URL("example-org/stacks"), Ref: "stacks-gone"}, source.Options{Cache: t.TempDir(), Git: g, Name: n.Base()})
	var gone *source.GoneError
	if !errors.As(err, &gone) || err.Error() != "the repository of "+base+" has no branch or tag by the ref it is set to" {
		t.Errorf("a ref the remote does not have: %v", err)
	}
	denied := &source.Git{Proxy: s.Proxy, Named: n}
	denied.Proxy.URL = strings.Replace(denied.Proxy.URL, proxygittest.Password, "another-password", 1)
	_, err = source.Resolve("", stack.Source{Git: s.URL("example-org/stacks"), Ref: "main"}, source.Options{Cache: t.TempDir(), Git: denied, Name: n.Base()})
	var fetch *source.FetchError
	if !errors.As(err, &fetch) || err.Error() != "the fetch of "+base+" failed" {
		t.Errorf("a denied fetch: %v", err)
	}
	_, err = source.Resolve("", stack.Source{Git: "git@code.example.com:team/tools.git", Ref: "main"}, source.Options{Cache: t.TempDir(), Git: g, Name: n.Module("tools")})
	var scheme *source.SchemeError
	if !errors.As(err, &scheme) || err.Error() != "use an https URL for the module tools: a run from a control plane fetches git sources only over https" {
		t.Errorf("a source that is not https: %v", err)
	}
}
