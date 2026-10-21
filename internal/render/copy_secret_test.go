package render_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/stack"
)

// hiddenPhrase is what a refusal prints in place of a name that contains an access key
// secret.
const hiddenPhrase = "(a name that contains an access key secret)"

// wantHidden fails the test unless err is a refusal whose text contains hiddenPhrase and
// no qak_ in any letter case.
func wantHidden(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: no refusal", what)
		return
	}
	if strings.Contains(strings.ToLower(err.Error()), "qak_") || !strings.Contains(err.Error(), hiddenPhrase) {
		t.Errorf("%s: %v", what, err)
	}
}

// qakModule is the module qak_m with a skill qak_d, at a new directory.
func qakModule(t *testing.T) (m compose.Module, skill string) {
	t.Helper()
	m = appModule(t)
	m.Name = "qak_m"
	skill = filepath.Join(m.Dir, "skills", "qak_d")
	write(t, filepath.Join(skill, "SKILL.md"), "---\nname: qak_d\n---\n")
	return m, skill
}

// TestCopyRefusalsHideASecret is each copy refusal for a module named qak_m, about a
// path under skills/qak_d: the refusal prints the phrase for both and no qak_.
func TestCopyRefusalsHideASecret(t *testing.T) {
	copyEntry := func(m compose.Module, skill string) error {
		return render.CopyEntry(m, skill, filepath.Join(t.TempDir(), "qak_d"))
	}

	m, skill := qakModule(t)
	symlink(t, "..", filepath.Join(skill, "loop"))
	wantHidden(t, "a loop", copyEntry(m, skill))

	m, skill = qakModule(t)
	if out, err := exec.Command("mkfifo", filepath.Join(skill, "pipe")).CombinedOutput(); err != nil {
		t.Logf("mkfifo: %v\n%s", err, out)
	} else {
		wantHidden(t, "a pipe", copyEntry(m, skill))
	}

	m, skill = qakModule(t)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(outside, "qak_x"), "x\n")
	symlink(t, filepath.Join(outside, "qak_x"), filepath.Join(skill, "run.sh"))
	wantHidden(t, "a link out of the module", copyEntry(m, skill))

	m, skill = qakModule(t)
	sparse(t, filepath.Join(skill, "big.bin"), 64<<20+1)
	wantHidden(t, "a file over 64 MiB", copyEntry(m, skill))

	m, _ = qakModule(t)
	wantHidden(t, "a path outside the module", render.CopyEntry(m, filepath.Join(outside, "qak_x"), filepath.Join(t.TempDir(), "x")))

	m, _ = qakModule(t)
	render.SetMaxModuleBytes(t, 1)
	wantHidden(t, "the module's total", render.CopyModule(m, filepath.Join(t.TempDir(), "qak_m")))
}

// TestLinkFilesHidesASecret is a files entry of the copied module qak_m at the path of a
// skill qak_review the home links to another module: the build fails with the phrase in
// place of each name, and no qak_.
func TestLinkFilesHidesASecret(t *testing.T) {
	hermetic(t)
	core := appModule(t).Dir
	m, _ := qakModule(t)
	write(t, filepath.Join(core, "skills", "qak_review", "SKILL.md"), "---\nname: qak_review\n---\n")
	evil := filepath.Join(m.Dir, "files", "copilot", "workspace", ".github", "skills", "qak_review", "evil.sh")
	write(t, evil, "planted\n")
	res := &compose.Result{
		Stack:   &stack.Stack{},
		Modules: []compose.Module{{Name: "core", Dir: core}, m},
		Entries: []compose.Entry{
			{Kind: "files", Name: "copilot/workspace/.github/skills/qak_review/evil.sh", Module: "qak_m", Path: evil},
			{Kind: "skills", Name: "qak_review", Module: "core", Path: filepath.Join(core, "skills", "qak_review")},
		},
	}
	stage := t.TempDir()
	wantHidden(t, "the files entry", render.BuildAt(res, stage, stage, lookup(t, "copilot")))
	if _, err := os.Lstat(filepath.Join(core, "skills", "qak_review", "evil.sh")); err == nil {
		t.Error("the copy landed in module core's directory")
	}
}
