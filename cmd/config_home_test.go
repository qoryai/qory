package cmd_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/config"
)

// relativeLine is the refusal of a relative XDG_CONFIG_HOME.
const relativeLine = "qory: XDG_CONFIG_HOME is not an absolute path; qory reads its configuration only from an absolute one, so that it does not change with the working folder"

// filesUnder lists the paths under dir, relative to it, with each file's content.
func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if d.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel += "=" + string(data)
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestEveryCommandRefusesARelativeConfigHome sets XDG_CONFIG_HOME to cfg, a relative
// path, in a checkout a walled run has used, whose cfg/qory holds a qory.yaml, a
// forager.yaml and a runtime descriptor no reader could take: every command that reads
// the configuration directory refuses with the line that says why, and nothing under
// cfg is written or removed.
func TestEveryCommandRefusesARelativeConfigHome(t *testing.T) {
	root := newCheckout(t)
	cfg := filepath.Join(root, "cfg")
	writeFile(t, filepath.Join(cfg, "qory", config.FileName), "harness: [\n")
	writeFile(t, filepath.Join(cfg, "qory", config.ForagerFileName), "gateway: [\n")
	writeFile(t, filepath.Join(cfg, "qory", cmd.DescriptorsDir, "claude.yaml"), "launch: [\n")
	copyFixture(t, "two-modules", root)
	if err := cmd.RecordCheckout(root); err != nil {
		t.Fatal(err)
	}
	signer, err := accesskey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	before := filesUnder(t, cfg)
	t.Setenv("XDG_CONFIG_HOME", "cfg")
	for _, args := range [][]string{
		{"config"},
		{"harness", "compose"},
		{"harness", "launch"},
		{"run", "claude", "--local"},
		{"image", "check", "example.com/agent:1"},
		{"setup", "machine"},
		{"gateway"},
		{"access-key", "enrol", "https://apiary.example.com", "qecn_F1XT0RE0000000000000000000." + signer.Fingerprint()},
		{"worktree", "add", "feature"},
		{"setup", "repo", "--accept-checkout"},
	} {
		out, err := run(t, args...)
		if err == nil || err.Error() != relativeLine {
			t.Errorf("qory %s: %v, want %q\n%s", strings.Join(args, " "), err, relativeLine, out)
		}
	}
	if after := filesUnder(t, cfg); !slices.Equal(after, before) {
		t.Errorf("cfg holds %q, was %q", after, before)
	}
}

// TestAnAbsoluteOrUnsetConfigHomeStillWorks reads the user's qory.yaml under an
// absolute XDG_CONFIG_HOME, and under ~/.config when it is unset: qory config names the
// file each time.
func TestAnAbsoluteOrUnsetConfigHomeStillWorks(t *testing.T) {
	newCheckout(t)
	home := os.Getenv("HOME")
	for _, c := range []struct{ value, shown string }{
		{filepath.Join(home, "elsewhere"), "~/elsewhere/qory/qory.yaml"},
		{"", "~/.config/qory/qory.yaml"},
	} {
		t.Setenv("XDG_CONFIG_HOME", c.value)
		file := filepath.Join(home, strings.TrimPrefix(c.shown, "~/"))
		writeFile(t, file, "apiVersion: qory.dev/v1alpha1\nharness: {force: true}\n")
		out, err := run(t, "config")
		if err != nil {
			t.Fatalf("XDG_CONFIG_HOME=%q: %v\n%s", c.value, err, out)
		}
		if !slices.ContainsFunc(strings.Split(out, "\n"), func(line string) bool {
			return slices.Equal(strings.Fields(line), []string{"harness.force", "true", c.shown})
		}) {
			t.Errorf("XDG_CONFIG_HOME=%q: no harness.force row from %s\n%s", c.value, c.shown, out)
		}
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
	}
}
