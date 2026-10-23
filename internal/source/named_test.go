package source_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/proxygit/proxygittest"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// namedOf is the Named of the stack core whose address is the folder core of url at
// ref.
func namedOf(t *testing.T, url, ref string) (*source.Named, stack.Source) {
	t.Helper()
	reference := url + "#" + ref + ":core"
	s, ok, err := stack.ParseReference(reference)
	if err != nil || !ok {
		t.Fatalf("%s: %v", reference, err)
	}
	return source.NewNamed("core", reference, s), s
}

// TestANamedStackHidesItsAddresses is the text of a compose of a named stack: the
// stack's reference, its URL with and without its ref, and a module's URL once the
// module is added, each show as the source's phrase, the longest first, only where
// nothing that continues a URL follows; a path in the stack's repository shows from the
// stack's folder; and a source as a compose's result writes it has its phrase.
func TestANamedStackHidesItsAddresses(t *testing.T) {
	url, tools := "https://code.example.com/team/stacks", "https://code.example.com/team/tools.git"
	n, _ := namedOf(t, url, "main")
	n.Add(stack.Source{Git: tools, Ref: "release-3", Path: "modules/tools"}, n.Module("tools"))
	for _, c := range []struct{ text, want string }{
		{"check -f " + url + "#main:core: x", "check -f the stack core: x"},
		{"module at " + url + "#main:core/modules/world", "module at modules/world in the stack core"},
		{"module at " + url + "#main:modules/world.", "module at the stack core."},
		{"read " + url + "#main:core", "read the stack core"},
		{"a link to " + url + "-other/x and " + url + ".github", "a link to " + url + "-other/x and " + url + ".github"},
		{"a link to " + url + ".git and " + url + "/tree/main/core.", "a link to the stack core and the stack core."},
		{"cloning " + url + ".git/info/refs: no", "cloning the stack core: no"},
		{"see " + tools + "/modules/tools (x)", "see the module tools (x)"},
		{"a link to " + url + ".", "a link to the stack core."},
		{tools + " at release-3 exports nothing", "the module tools exports nothing"},
		{"fetching " + tools + "#release-3: no", "fetching the module tools: no"},
		{"a link to " + url, "a link to the stack core"},
	} {
		if got := n.Hide(c.text); got != c.want {
			t.Errorf("%q\ngot  %q\nwant %q", c.text, got, c.want)
		}
	}
	for src, want := range map[string]string{
		url + "#main:modules/world":            "the stack core",
		tools + "#release-3:modules/tools":     "the module tools",
		tools + "#release-3 module tools":      "the module tools",
		"https://other.example/x#main:modules": "",
	} {
		if got, _ := n.Of(src); got != want {
			t.Errorf("Of(%s) is %q, want %q", src, got, want)
		}
	}
	if n.Phrase() != "the stack core" || n.Module("") != "a module of the stack core" {
		t.Errorf("phrases %q, %q", n.Phrase(), n.Module(""))
	}
	err := n.HideError(errors.New("read " + url))
	if err.Error() != "read the stack core" {
		t.Errorf("the error is %q", err)
	}
}

// TestANamedStackResolvesThroughTheProxyByNames resolves a named stack's source through
// the proxy: the fetch is reported with the source's name, no folder or file name of
// the cache contains the URL, and none outside a clone's .git contains the ref, and a
// path in the clone shows as the path in the repository in the source's phrase.
func TestANamedStackResolvesThroughTheProxyByNames(t *testing.T) {
	hermetic(t)
	s := proxygittest.Start(t)
	commit := s.Repository(t, "example-org/stacks", "stacks-r7", map[string]string{"core/qory-stack.yaml": "name: core\n"})
	n, ref := namedOf(t, s.URL("example-org/stacks"), "stacks-r7")
	cache := t.TempDir()
	var fetched []string
	g := &source.Git{Proxy: s.Proxy, Named: n, Fetched: func(_, _, name, c string, _ time.Duration) {
		fetched = append(fetched, name+" "+c)
	}}
	r, err := source.Resolve("", ref, source.Options{Cache: cache, Git: g, Name: n.Phrase()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(fetched, "\n") != "the stack core "+commit {
		t.Errorf("fetched %q", fetched)
	}
	filepath.WalkDir(cache, func(path string, d os.DirEntry, err error) error {
		inGit := strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator))
		if strings.Contains(path, "example-org") || strings.Contains(path, proxygittest.Host) || !inGit && strings.Contains(path, "stacks-r7") {
			t.Errorf("the cache has %s", path)
		}
		return nil
	})
	file := filepath.Join(r.Dir, "qory-stack.yaml")
	for text, want := range map[string]string{
		"read " + file + ": bad": "read qory-stack.yaml in the stack core: bad",
		"read " + filepath.Join(filepath.Dir(r.Dir), "README.md") + ".": "read the stack core.",
	} {
		if got := n.Hide(text); got != want {
			t.Errorf("%q shows as %q, want %q", text, got, want)
		}
	}
}

// TestANamedStacksFailedFetchShowsNoAddress is a ref the remote does not have, a fetch
// the proxy denies, and a source that is not https, of a named stack: each error shows
// the source's name, and none its URL, its ref or git's output.
func TestANamedStacksFailedFetchShowsNoAddress(t *testing.T) {
	hermetic(t)
	s := proxygittest.Start(t)
	s.Repository(t, "example-org/stacks", "main", map[string]string{"core/qory-stack.yaml": "name: core\n"})
	n, _ := namedOf(t, s.URL("example-org/stacks"), "main")
	g := &source.Git{Proxy: s.Proxy, Named: n}
	_, err := source.Resolve("", stack.Source{Git: s.URL("example-org/stacks"), Ref: "stacks-gone"}, source.Options{Cache: t.TempDir(), Git: g, Name: n.Phrase()})
	var gone *source.GoneError
	if !errors.As(err, &gone) || err.Error() != "the repository of the stack core has no branch or tag by the ref it is set to" {
		t.Errorf("a ref the remote does not have: %v", err)
	}
	denied := &source.Git{Proxy: s.Proxy, Named: n}
	denied.Proxy.URL = strings.Replace(denied.Proxy.URL, proxygittest.Password, "another-password", 1)
	_, err = source.Resolve("", stack.Source{Git: s.URL("example-org/stacks"), Ref: "main"}, source.Options{Cache: t.TempDir(), Git: denied, Name: n.Phrase()})
	var fetch *source.FetchError
	if !errors.As(err, &fetch) || err.Error() != "the fetch of the stack core failed" {
		t.Errorf("a denied fetch: %v", err)
	}
	_, err = source.Resolve("", stack.Source{Git: "git@code.example.com:team/tools.git", Ref: "main"}, source.Options{Cache: t.TempDir(), Git: g, Name: n.Module("tools")})
	var scheme *source.SchemeError
	if !errors.As(err, &scheme) || err.Error() != "use an https URL for the module tools: a named stack fetches its sources only over https, through the run's stack proxy" {
		t.Errorf("a source that is not https: %v", err)
	}
}
