package cmd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
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
