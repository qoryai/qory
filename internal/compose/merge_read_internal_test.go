package compose

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/module"
)

// TestAFailedReadOfASettingsFragmentPrintsTheModuleOnceWithItsFixFirst is a settings fragment
// that the compose cannot read when it merges it, as after another process changed the
// file since the module was read: the error the compose returns, with the caller's prefix,
// has the fix in line 1 and "module core" once, and matches the OS error.
func TestAFailedReadOfASettingsFragmentPrintsTheModuleOnceWithItsFixFirst(t *testing.T) {
	denied := &fs.PathError{Op: "open", Path: "/work/modules/core/settings/claude/settings.json", Err: errors.New("permission denied")}
	readFile := func(string) ([]byte, error) { return nil, denied }
	res := &Result{Settings: map[string]map[string]map[string]any{"claude": {"settings.json": {}}}, setBy: map[string]string{}}
	err := mergeFile(res, "claude", "settings.json", denied.Path, "core", nil, readFile)
	if err == nil {
		t.Fatal("no refusal")
	}
	// The compose prefixes each error of mergeFile with the module as the caller does.
	err = module.Prefixed("module core", err)
	const fix = "Check that you can read the settings fragment of module core."
	if first := strings.SplitN(err.Error(), "\n", 2)[0]; first != fix {
		t.Errorf("line 1 is %q, want %q", first, fix)
	}
	if n := strings.Count(err.Error(), "module core"); n != 1 {
		t.Errorf("module core is in the text %d times: %v", n, err)
	}
	if !errors.Is(err, denied) {
		t.Errorf("%v does not match the OS error", err)
	}
}
