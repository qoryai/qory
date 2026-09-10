package render_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
)

// linkLayer sets the checkout-root name the profile links the named layer as, the way
// the profile loader does, and returns the layer.
func linkLayer(t *testing.T, res *compose.Result, layer, link string) compose.Layer {
	t.Helper()
	for i := range res.Layers {
		if res.Layers[i].Name == layer {
			res.Layers[i].Link = link
			return res.Layers[i]
		}
	}
	t.Fatalf("no layer %s in the fixture", layer)
	return compose.Layer{}
}

// readExclude returns the checkout's clone-local exclude file.
func readExclude(t *testing.T, root string) string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	return string(data)
}

// TestLayerLinkReachesTheLayerThroughTheHome is a profile that links the core layer as
// harness: the checkout gets a relative link resolving to the layer's directory through
// the home's layers/core, listed in the exclude file, and a later compose that drops the
// link from the profile takes it and its line back.
func TestLayerLinkReachesTheLayerThroughTheHome(t *testing.T) {
	res, root, home := composeFixture(t, "claude")
	claude := lookup(t, "claude")
	core := linkLayer(t, res, "core", "harness")
	if err := render.Build(res, home, claude); err != nil {
		t.Fatal(err)
	}
	if _, err := render.LinkInto(claude, res, root, home, false); err != nil {
		t.Fatal(err)
	}
	linked, err := render.LinkLayers(res, root, home, nil, false)
	if err != nil || len(linked.Skipped) != 0 || len(linked.Replaced) != 0 {
		t.Fatalf("linked=%+v err=%v", linked, err)
	}
	path := filepath.Join(root, "harness")
	target, err := os.Readlink(path)
	if err != nil || target != filepath.Join(".qory", "harness", "layers", "core") {
		t.Fatalf("harness links to %q (%v), want a relative link through the home", target, err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(core.Dir); resolved != want {
		t.Errorf("harness resolves to %s, want %s", resolved, want)
	}
	if _, err := os.Stat(filepath.Join(path, "scripts", "db.py")); err != nil {
		t.Errorf("the layer's own file is not reachable through the link: %v", err)
	}
	if !strings.Contains(readExclude(t, root), "/harness\n") {
		t.Errorf("exclude file lacks /harness:\n%s", readExclude(t, root))
	}
	// The same compose again is nothing new.
	linked, err = render.LinkLayers(res, root, home, []string{"harness"}, false)
	if err != nil || len(linked.Skipped) != 0 || len(linked.Replaced) != 0 {
		t.Fatalf("second link: linked=%+v err=%v", linked, err)
	}
	if !isLink(t, path) {
		t.Fatal("harness went in the second compose")
	}
	// The profile drops the link: the earlier compose's name is pruned.
	linkLayer(t, res, "core", "")
	if _, err := render.LinkLayers(res, root, home, []string{"harness"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); err == nil {
		t.Error("harness is still there after the profile dropped the link")
	}
	if strings.Contains(readExclude(t, root), "/harness\n") {
		t.Errorf("exclude file still holds /harness:\n%s", readExclude(t, root))
	}
}

// TestLayerLinkRefusesTheCheckoutsOwnPath is a repository with its own harness
// directory where a layer links: the link is hard, because permission rules and scripts
// name the path, so the compose refuses with the error the exit status is read from.
// With --force an untracked directory, and an untracked symlink, are refused with git's
// reason; once committed and unmodified, the directory is replaced and named.
func TestLayerLinkRefusesTheCheckoutsOwnPath(t *testing.T) {
	res, root, home := composeFixture(t, "claude")
	linkLayer(t, res, "core", "harness")
	own := filepath.Join(root, "harness", "README.md")
	write(t, own, "ours\n")
	if err := render.Build(res, home, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	_, err := render.LinkLayers(res, root, home, nil, false)
	var foreign *render.ForeignPathError
	if !errorsAs(err, &foreign) || foreign.Path != filepath.Join(root, "harness") || foreign.Reason != "" {
		t.Fatalf("err = %v, want a plain ForeignPathError for harness", err)
	}
	if data, _ := os.ReadFile(own); string(data) != "ours\n" {
		t.Errorf("the repository's own file was replaced: %q", data)
	}
	if strings.Contains(readExclude(t, root), "/harness\n") {
		t.Errorf("exclude file holds /harness for a link that was not written:\n%s", readExclude(t, root))
	}
	// Untracked, force does not help, and the error says why.
	_, err = render.LinkLayers(res, root, home, nil, true)
	if !errorsAs(err, &foreign) || foreign.Reason != "is not tracked in git" {
		t.Fatalf("force over an untracked directory: err = %v, want a refusal naming git's reason", err)
	}
	// An untracked symlink of the person's own at the path is refused too, force or not.
	if err := os.RemoveAll(filepath.Join(root, "harness")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "docs", "README.md"), "ours\n")
	if err := os.Symlink("docs", filepath.Join(root, "harness")); err != nil {
		t.Fatal(err)
	}
	_, err = render.LinkLayers(res, root, home, nil, false)
	if !errorsAs(err, &foreign) || foreign.Target != "docs" || foreign.Reason != "" {
		t.Fatalf("over a foreign symlink: err = %v, want a ForeignPathError naming its target", err)
	}
	_, err = render.LinkLayers(res, root, home, nil, true)
	if !errorsAs(err, &foreign) || foreign.Reason != "is not tracked in git" {
		t.Fatalf("force over an untracked symlink: err = %v, want a refusal naming git's reason", err)
	}
	if target, _ := os.Readlink(filepath.Join(root, "harness")); target != "docs" {
		t.Errorf("the person's own symlink changed: %q", target)
	}
	// Committed and unmodified, force replaces the directory and names it.
	if err := os.Remove(filepath.Join(root, "harness")); err != nil {
		t.Fatal(err)
	}
	write(t, own, "ours\n")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-q", "-m", "own")
	linked, err := render.LinkLayers(res, root, home, nil, true)
	if err != nil || len(linked.Replaced) != 1 || linked.Replaced[0] != "harness" || len(linked.Skipped) != 0 {
		t.Fatalf("force over a committed directory: linked=%+v err=%v", linked, err)
	}
	if target, err := os.Readlink(filepath.Join(root, "harness")); err != nil || target != filepath.Join(".qory", "harness", "layers", "core") {
		t.Fatalf("harness links to %q (%v), want the layer through the home", target, err)
	}
	if !strings.Contains(readExclude(t, root), "/harness\n") {
		t.Errorf("exclude file lacks /harness:\n%s", readExclude(t, root))
	}
}

// TestLayerLinkRefusesARuntimesPath is a profile that links a layer where a runtime
// links, or at the qory directory: the compose refuses with a plain error, and a link
// the same profile asks for elsewhere is not written first.
func TestLayerLinkRefusesARuntimesPath(t *testing.T) {
	for _, c := range []struct{ link, want string }{
		{"AGENTS.md", "link AGENTS.md is where the amp runtime links AGENTS.md"},
		{".claude", "link .claude is where the claude runtime links .claude"},
		{".qory", "link .qory is qory's own directory"},
	} {
		t.Run(c.link, func(t *testing.T) {
			res, root, home := composeFixture(t, "claude")
			linkLayer(t, res, "core", "harness")
			linkLayer(t, res, "nextjs", c.link)
			if err := render.Build(res, home, lookup(t, "claude")); err != nil {
				t.Fatal(err)
			}
			_, err := render.LinkLayers(res, root, home, nil, false)
			if err == nil || err.Error() != c.want {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			var foreign *render.ForeignPathError
			if errorsAs(err, &foreign) {
				t.Errorf("the refusal is a ForeignPathError, which the command layer reads as a path in the way")
			}
			if _, err := os.Lstat(filepath.Join(root, "harness")); err == nil {
				t.Error("harness was linked before the refusal")
			}
			if strings.Contains(readExclude(t, root), "/harness\n") {
				t.Errorf("exclude file holds /harness after the refusal:\n%s", readExclude(t, root))
			}
		})
	}
}

// TestUnlinkLayersTakesOnlyQorysLinks is qory harness remove in a checkout with a layer
// link, a link of the person's own, a directory and a name that holds nothing: only the
// layer link goes, with its exclude line, and the removal names it.
func TestUnlinkLayersTakesOnlyQorysLinks(t *testing.T) {
	res, root, home := composeFixture(t, "claude")
	linkLayer(t, res, "core", "harness")
	if err := render.Build(res, home, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	if _, err := render.LinkLayers(res, root, home, nil, false); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "docs", "README.md"), "ours\n")
	if err := os.Symlink("docs", filepath.Join(root, "own")); err != nil {
		t.Fatal(err)
	}
	removed, err := render.UnlinkLayers(root, []string{"harness", "own", "docs", "missing"})
	if err != nil || len(removed) != 1 || removed[0] != "harness" {
		t.Fatalf("unlink: removed=%v err=%v", removed, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "harness")); err == nil {
		t.Error("harness is still there")
	}
	if strings.Contains(readExclude(t, root), "/harness\n") {
		t.Errorf("exclude file still holds /harness:\n%s", readExclude(t, root))
	}
	if target, err := os.Readlink(filepath.Join(root, "own")); err != nil || target != "docs" {
		t.Errorf("the person's own link changed: %q %v", target, err)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "README.md")); err != nil {
		t.Errorf("the person's own directory changed: %v", err)
	}
}

// TestUnlinkLayerLinksFindsTheLinksWithoutTheReport is a person who ran rm -rf .qory
// before qory harness remove: the report that named the layer links is gone, and the
// remove still finds the link by its target, takes it with its exclude line, and leaves
// a symlink of the person's own alone.
func TestUnlinkLayerLinksFindsTheLinksWithoutTheReport(t *testing.T) {
	res, root, home := composeFixture(t, "claude")
	linkLayer(t, res, "core", "harness")
	if err := render.Build(res, home, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	if _, err := render.LinkLayers(res, root, home, nil, false); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "docs", "README.md"), "ours\n")
	if err := os.Symlink("docs", filepath.Join(root, "own")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(home)); err != nil {
		t.Fatal(err)
	}
	removed, err := render.UnlinkLayerLinks(root)
	if err != nil || len(removed) != 1 || removed[0] != "harness" {
		t.Fatalf("unlink: removed=%v err=%v", removed, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "harness")); err == nil {
		t.Error("harness is still there")
	}
	if strings.Contains(readExclude(t, root), "/harness\n") {
		t.Errorf("exclude file still holds /harness:\n%s", readExclude(t, root))
	}
	if target, err := os.Readlink(filepath.Join(root, "own")); err != nil || target != "docs" {
		t.Errorf("the person's own link changed: %q %v", target, err)
	}
}
