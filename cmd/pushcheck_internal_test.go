package cmd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/qoryai/forager/wall"
)

// engineScript is a docker command whose context show prints the contexts in order,
// one a call, the last one again once they run out, and whose info prints the id of
// the engine the context selects, id-<DOCKER_CONTEXT>.
func engineScript(t *testing.T, contexts ...string) string {
	t.Helper()
	dir := t.TempDir()
	list := filepath.Join(dir, "contexts")
	content := ""
	for _, c := range contexts {
		content += c + "\n"
	}
	if err := os.WriteFile(list, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "docker")
	body := "#!/bin/sh\ncase \"$1\" in\ncontext)\n\thead -n 1 " + list + "\n\tif [ \"$(wc -l < " + list + ")\" -gt 1 ]; then sed -i.bak 1d " + list + "; fi ;;\ninfo) echo \"id-$DOCKER_CONTEXT\" ;;\nesac\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// unsetenv unsets name for the test.
func unsetenv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
}

// TestTheChecksEngineIsTheWalls is the engine of a run's push checks: with a selection
// the wall recorded, and an engine that has the id it had then, the wall's command by
// its absolute path with that selection; with none recorded, or with a selection that
// reaches another engine now, the wall's command with this process's environment.
func TestTheChecksEngineIsTheWalls(t *testing.T) {
	unsetenv(t, "DOCKER_HOST")
	unsetenv(t, "DOCKER_CONTEXT")
	t.Setenv("DOCKER_CONFIG", "/wall/config")
	ctx := context.Background()

	script := engineScript(t, "a")
	d := &wall.Docker{Command: script}
	if got := checkEngine(ctx, d); got.Command != script || got.Env != nil {
		t.Errorf("with no recorded selection: %+v, want the wall's command alone", got)
	}
	d.Engine(ctx)
	got := checkEngine(ctx, d)
	if got.Command != script || !slices.Contains(got.Env, "DOCKER_CONTEXT=a") || !slices.Contains(got.Env, "DOCKER_CONFIG=/wall/config") {
		t.Errorf("with the recorded selection: %+v, want the command and the selection", got)
	}

	// The context changed after the open: the new selection reaches another engine.
	other := engineScript(t, "a", "b")
	d = &wall.Docker{Command: other}
	d.Engine(ctx)
	if got := checkEngine(ctx, d); got.Command != other || got.Env != nil {
		t.Errorf("with another engine now: %+v, want the wall's command alone", got)
	}
}
