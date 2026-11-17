package cmd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/accesskey"
)

// fakeMountsEngine writes a program in place of docker whose ps prints ids, one a line, and
// whose inspect prints the mounts' sources of those ids, one a line; with fail set, ps
// exits 1.
func fakeMountsEngine(t *testing.T, ids, sources string, fail bool) string {
	t.Helper()
	program := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\ncase \"$1\" in\nps) "
	if fail {
		script += "echo 'Cannot connect to the Docker daemon' >&2; exit 1 ;;\n"
	} else {
		script += "printf '%s' '" + ids + "' ;;\n"
	}
	script += "inspect) printf '%s' '" + sources + "' ;;\nesac\n"
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return program
}

// TestEngineMountsListsTheRunningContainersMounts reads the mounts' sources of the
// running containers through the engine's program, none for no container, and returns an
// error when the engine does not answer.
func TestEngineMountsListsTheRunningContainersMounts(t *testing.T) {
	got, err := engineMounts(fakeMountsEngine(t, "aaa\nbbb\n", "/srv/a\n/srv/b\n\n/srv/c\n", false))(context.Background())
	if want := []string{"/srv/a", "/srv/b", "/srv/c"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("mounts = %v, %v, want %v", got, err, want)
	}
	got, err = engineMounts(fakeMountsEngine(t, "", "/srv/never\n", false))(context.Background())
	if err != nil || len(got) != 0 {
		t.Errorf("with no container: %v, %v, want none", got, err)
	}
	if _, err = engineMounts(fakeMountsEngine(t, "", "", true))(context.Background()); err == nil {
		t.Error("an engine that does not answer gave no error")
	}
}

// TestEngineMountsGivesTheEngineNoAccessKeyVariables runs the engine's program with the
// access key's variables and the run key set in this process: the program writes the
// names of its environment, never the values, and has none of the four, and has the
// others.
func TestEngineMountsGivesTheEngineNoAccessKeyVariables(t *testing.T) {
	secrets := []string{accesskey.EnvSecret, accesskey.EnvID, accesskey.EnvPin, accesskey.EnvRunKey}
	for _, name := range secrets {
		t.Setenv(name, "set-by-the-test")
	}
	t.Setenv("QORY_TEST_ENGINE_SEES", "1")
	dir := t.TempDir()
	names := filepath.Join(dir, "names")
	program := filepath.Join(dir, "docker")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nenv | sed 's/=.*//' >> "+names+"\ncase \"$1\" in\nps) echo abc ;;\nesac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := engineMounts(program)(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(names)
	if err != nil {
		t.Fatal(err)
	}
	seen := strings.Fields(string(data))
	if !slices.Contains(seen, "QORY_TEST_ENGINE_SEES") {
		t.Errorf("the engine's program did not get the process's environment: %d names", len(seen))
	}
	for _, name := range secrets {
		if slices.Contains(seen, name) {
			t.Errorf("the engine's program has the variable %s", name)
		}
	}
}

// TestEngineMountsFindsNoRunningContainerWithNoEngineProgram has no docker program on
// PATH: the machine has no running container, so there is no mount and no error.
func TestEngineMountsFindsNoRunningContainerWithNoEngineProgram(t *testing.T) {
	got, err := engineMounts("qory-test-no-such-engine")(context.Background())
	if err != nil || len(got) != 0 {
		t.Errorf("mounts = %v, %v, want none and no error", got, err)
	}
}
