package pushcheck_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// scopeFolders are a clone with one file and a snapshot with an index.
func scopeFolders(t *testing.T) (clone, snapshot string) {
	t.Helper()
	clone, snapshot = t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(clone, "app.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshot, "index.json"), []byte(`{"artifacts":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return clone, snapshot
}

// TestAScopeCheckRunsOnTheCloneAsTheGivenUser is a scope check that passes: the container
// runs the command with --base and --artifacts last, as the given user, with the clone as
// the writable working folder, and the home and the snapshot at the files folder
// read-only; nothing is fetched.
func TestAScopeCheckRunsOnTheCloneAsTheGivenUser(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	clone, snapshot := scopeFolders(t)
	base := strings.Repeat("a", 40)
	end, err := f.c.Scope(context.Background(), []string{f.program, "scope", "--plan"}, clone, snapshot, base, "1234:5678")
	if err != nil || end.TimedOut || end.NotStarted != "" || end.ExitCode != 0 || end.Lines != nil {
		t.Fatalf("got %+v, %v", end, err)
	}
	created := f.e.created(t)
	if i := slices.Index(created, "--user"); i < 0 || created[i+1] != "1234:5678" {
		t.Errorf("the container's user: %v", created)
	}
	if got := strings.Join(created[len(created)-8:], " "); got != f.program+" "+f.c.Image+" scope --plan --base "+base+" --artifacts /qory-push-check/files" {
		t.Errorf("the command: %s", got)
	}
	want := []string{"type=bind,src=" + f.c.Home + ",dst=" + f.c.Home + ",readonly", "type=bind,src=" + clone + ",dst=/qory-push-check/artifacts",
		"type=bind,src=" + snapshot + ",dst=/qory-push-check/files,readonly"}
	if !slices.Equal(f.e.mounts, want) {
		t.Errorf("the mounts are %q, want %q", f.e.mounts, want)
	}
	if f.e.pulled["app.go"] != "package app\n" || f.e.pushed["index.json"] == "" || len(f.fetched) != 0 {
		t.Errorf("the container got %v and %v, and fetched %v", f.e.pulled, f.e.pushed, f.fetched)
	}
}

// TestAScopeCheckGetsTheInputsQoryWroteReadOnlyBeforeBase is a checker with Inputs: the
// program gets --inputs and the path before --base, and the container mounts a copy of the
// bytes qory wrote read-only at that path, whose content is the host's, while the run's file
// stays as it was.
func TestAScopeCheckGetsTheInputsQoryWroteReadOnlyBeforeBase(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	inputs := filepath.Join(t.TempDir(), "in", "inputs.json")
	if err := os.MkdirAll(filepath.Dir(inputs), 0o700); err != nil {
		t.Fatal(err)
	}
	const data = `{"qory":{"start_commits":[{"path":"/w","commit":"c1"}]}}`
	if err := os.WriteFile(inputs, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	f.c.Inputs, f.c.InputsData = inputs, []byte(data)
	var mounted string
	f.e.create = func(io.Writer) error {
		for _, m := range f.e.mounts {
			if strings.HasSuffix(m, ",dst="+inputs+",readonly") {
				got, err := os.ReadFile(strings.TrimPrefix(strings.Split(m, ",")[1], "src="))
				if err != nil {
					return err
				}
				mounted = string(got)
			}
		}
		return nil
	}
	clone, snapshot := scopeFolders(t)
	base := strings.Repeat("a", 40)
	end, err := f.c.Scope(context.Background(), []string{f.program, "scope", "--plan"}, clone, snapshot, base, "1234:5678")
	if err != nil || end.ExitCode != 0 || end.NotStarted != "" {
		t.Fatalf("got %+v, %v", end, err)
	}
	created := f.e.created(t)
	if got, want := strings.Join(created[len(created)-10:], " "), f.program+" "+f.c.Image+" scope --plan --inputs "+inputs+" --base "+base+" --artifacts /qory-push-check/files"; got != want {
		t.Errorf("the command: %s, want %s", got, want)
	}
	if mounted != data {
		t.Errorf("the mounted copy has %q, want %q", mounted, data)
	}
	if len(f.e.mounts) != 4 {
		t.Errorf("the mounts are %q, want 4", f.e.mounts)
	}
	if got, err := os.ReadFile(inputs); err != nil || string(got) != data {
		t.Errorf("the run's file has %q, %v", got, err)
	}
}

// TestAScopeCheckMountsTheBytesQoryWroteNotAnotherFile is a checker whose file at Inputs on this
// machine has other bytes than InputsData: the container mounts the bytes qory wrote.
func TestAScopeCheckMountsTheBytesQoryWroteNotAnotherFile(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	inputs := filepath.Join(t.TempDir(), "inputs.json")
	if err := os.WriteFile(inputs, []byte(`{"changed":"on disk"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	const data = `{"qory":{}}`
	f.c.Inputs, f.c.InputsData = inputs, []byte(data)
	mounted := ""
	f.e.create = func(io.Writer) error {
		for _, m := range f.e.mounts {
			if strings.HasSuffix(m, ",dst="+inputs+",readonly") {
				got, _ := os.ReadFile(strings.TrimPrefix(strings.Split(m, ",")[1], "src="))
				mounted = string(got)
			}
		}
		return nil
	}
	clone, snapshot := scopeFolders(t)
	if _, err := f.c.Scope(context.Background(), []string{f.program, "scope"}, clone, snapshot, strings.Repeat("a", 40), "1:1"); err != nil {
		t.Fatal(err)
	}
	if mounted != data {
		t.Errorf("the mounted file has %q, want the bytes qory wrote, %q", mounted, data)
	}
}

// TestAScopeCheckOfARunWithNoInputsGetsNoInputsArgument is a checker with no Inputs: the
// program gets no --inputs, and the container has the three mounts of a scope check.
func TestAScopeCheckOfARunWithNoInputsGetsNoInputsArgument(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	clone, snapshot := scopeFolders(t)
	if _, err := f.c.Scope(context.Background(), []string{f.program, "scope"}, clone, snapshot, strings.Repeat("a", 40), "1:1"); err != nil {
		t.Fatal(err)
	}
	if line := strings.Join(f.e.created(t), " "); strings.Contains(line, "--inputs") || len(f.e.mounts) != 3 {
		t.Errorf("the create has --inputs or %d mounts: %s", len(f.e.mounts), line)
	}
}

// TestAScopeCheckEndsByItsExit is a check that prints findings and exits 1, one that exits
// 3 with no output, one past its limit, and one whose program does not start; and an
// engine that fails, which is an error. The lines are at most 50.
func TestAScopeCheckEndsByItsExit(t *testing.T) {
	clone, snapshot := scopeFolders(t)
	run := func(f *fixture) (string, error) {
		end, err := f.c.Scope(context.Background(), []string{f.program, "scope"}, clone, snapshot, strings.Repeat("b", 40), "1:1")
		return fmt.Sprintf("%v|%s|%d|%d|%s", end.TimedOut, end.NotStarted, end.ExitCode, len(end.Lines), strings.Join(end.Lines, "/")), err
	}
	f := newFixture(t, "#!/bin/sh")
	f.e.exit(1, "src/app.go is outside the plan\n", "")
	if got, err := run(f); got != "false||1|1|src/app.go is outside the plan" || err != nil {
		t.Errorf("exit 1: %s, %v", got, err)
	}
	f = newFixture(t, "#!/bin/sh")
	f.e.exit(1, strings.Repeat("a finding\n", 80), "")
	if got, _ := run(f); !strings.HasPrefix(got, "false||1|50|") {
		t.Errorf("80 findings: %s", got)
	}
	f = newFixture(t, "#!/bin/sh")
	f.e.exit(3, "", "")
	if got, err := run(f); got != "false||3|1|The check exited with 3 and printed nothing." || err != nil {
		t.Errorf("exit 3: %s, %v", got, err)
	}
	f = newFixture(t, "#!/bin/sh")
	f.c.Limit = 50 * time.Millisecond
	f.e.start = func(ctx context.Context, _, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}
	if got, err := run(f); got != "true||0|0|" || err != nil {
		t.Errorf("past the limit: %s, %v", got, err)
	}
	f = newFixture(t, "#!/bin/sh")
	f.e.exit(127, "", "[FATAL tini (7)] exec "+f.program+" failed: No such file or directory\n")
	if got, err := run(f); got != "false|exec "+f.program+" failed: No such file or directory|0|0|" || err != nil {
		t.Errorf("not started: %s, %v", got, err)
	}
	f = newFixture(t, "#!/bin/sh")
	f.e.create = func(stderr io.Writer) error {
		io.WriteString(stderr, "Cannot connect to the Docker daemon\n")
		return fmt.Errorf("exit status 1")
	}
	if _, err := run(f); err == nil || !strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
		t.Errorf("a failed engine: %v", err)
	}
}

// TestAPushCheckMountsEveryFolderReadOnly is a push check with the run's inputs: the home,
// the working folder, the pushed files and inputs.json are all read-only, as only a scope
// check's clone is writable.
func TestAPushCheckMountsEveryFolderReadOnly(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	f.c.Inputs, f.c.InputsData = filepath.Join(t.TempDir(), "inputs.json"), []byte(`{}`)
	if r, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"})); r != nil || err != nil {
		t.Fatalf("got %+v, %v", r, err)
	}
	if len(f.e.mounts) != 4 {
		t.Fatalf("the mounts are %q, want 4", f.e.mounts)
	}
	for _, m := range f.e.mounts {
		if !strings.HasSuffix(m, ",readonly") {
			t.Errorf("the mount %s is writable", m)
		}
	}
}
