package module

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// throughFixture is a checkout with ./harness a link to harness-real, whose AGENTS.md
// is an absolute link to harness-real/docs/agents.md and whose .git/config holds
// another text, and a directory outside whose AGENTS.md is an absolute link to its
// .git/config. It returns the checkout, ./harness, harness-real with every link resolved,
// and the outside directory.
func throughFixture(t *testing.T) (ck, harness, real, outside string) {
	t.Helper()
	ck, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real = filepath.Join(ck, "harness-real")
	outside = filepath.Join(ck, "outside")
	for path, body := range map[string]string{
		filepath.Join(real, "docs", "agents.md"):     "THE-AGENTS-FILE\n",
		filepath.Join(real, ".git", "config"):        "THE-GIT-CONFIG\n",
		filepath.Join(outside, ".git", "config"):     "OUTSIDE\n",
		filepath.Join(real, "settings", "x.json"):    "{}",
		filepath.Join(outside, "settings", "x.json"): "{}",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		filepath.Join(real, "AGENTS.md"):    filepath.Join(real, "docs", "agents.md"),
		filepath.Join(outside, "AGENTS.md"): filepath.Join(outside, ".git", "config"),
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	harness = filepath.Join(ck, "harness")
	if err := os.Symlink("harness-real", harness); err != nil {
		t.Fatal(err)
	}
	return ck, harness, real, outside
}

// TestReadThroughFollowsAnAbsoluteLinkInTheRootOnly is AGENTS.md, an absolute link into
// the module, read through the module's root while ./harness leads elsewhere since the
// listing, to a directory whose AGENTS.md links to its .git/config: the read returns the
// file AGENTS.md links to in the root, never another file's bytes.
func TestReadThroughFollowsAnAbsoluteLinkInTheRootOnly(t *testing.T) {
	_, harness, real, outside := throughFixture(t)
	root, err := os.OpenRoot(harness)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	read := ReadThrough(root, harness, real)
	if data, err := read(filepath.Join(harness, "AGENTS.md")); err != nil || string(data) != "THE-AGENTS-FILE\n" {
		t.Fatalf("before the re-point: %q, %v", data, err)
	}
	if err := os.Remove(harness); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, harness); err != nil {
		t.Fatal(err)
	}
	data, err := read(filepath.Join(harness, "AGENTS.md"))
	if err == nil && string(data) != "THE-AGENTS-FILE\n" {
		t.Errorf("after the re-point the read returned %q", data)
	}
}

// TestReadThroughRefusesAnAbsoluteLinkOutOfTheModule is an absolute link to a file
// outside the module, and any absolute link when the module's real path is not known:
// the read is refused.
func TestReadThroughRefusesAnAbsoluteLinkOutOfTheModule(t *testing.T) {
	_, harness, real, outside := throughFixture(t)
	if err := os.Symlink(filepath.Join(outside, ".git", "config"), filepath.Join(real, "out.md")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(harness)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if data, err := ReadThrough(root, harness, real)(filepath.Join(harness, "out.md")); err == nil {
		t.Errorf("a link out of the module read %q", data)
	}
	if data, err := ReadThrough(root, harness, "")(filepath.Join(harness, "AGENTS.md")); err == nil {
		t.Errorf("an absolute link with no real path read %q", data)
	}
}

// TestResolveInHidesASecret is a path under qak_d that leads out of the module, and one
// that passes a missing qak_ name: each refusal prints the phrase and no qak_.
func TestResolveInHidesASecret(t *testing.T) {
	_, harness, real, _ := throughFixture(t)
	root, err := os.OpenRoot(harness)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, rel := range []string{"qak_d/../../out", "qak_d/x"} {
		if err := os.MkdirAll(filepath.Join(real, "qak_d"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := ResolveIn(root, real, rel)
		if err == nil || strings.Contains(strings.ToLower(err.Error()), "qak_") || !strings.Contains(err.Error(), "(a name that contains an access key secret)") {
			t.Errorf("%s: %v", rel, err)
		}
	}
}
