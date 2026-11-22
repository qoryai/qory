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
// the working folder and the snapshot at the files folder, both read-only, and nothing is
// fetched.
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
	for _, want := range []string{"src=" + clone + ",dst=/qory-push-check/artifacts,readonly", "src=" + snapshot + ",dst=/qory-push-check/files,readonly"} {
		if !slices.Contains(f.e.mounts, "type=bind,"+want) {
			t.Errorf("the mounts %v lack %s", f.e.mounts, want)
		}
	}
	if f.e.pulled["app.go"] != "package app\n" || f.e.pushed["index.json"] == "" || len(f.fetched) != 0 {
		t.Errorf("the container got %v and %v, and fetched %v", f.e.pulled, f.e.pushed, f.fetched)
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

// TestScopeUserIsTheClonesOwner is the user of a scope check: this process's uid:gid for a
// process that is not root.
func TestScopeUserIsTheClonesOwner(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the rule for root needs a process that is not root to differ")
	}
	f := newFixture(t, "#!/bin/sh")
	f.c.User = "1000:1000"
	if got, want := f.c.ScopeUser(), fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()); got != want {
		t.Errorf("ScopeUser = %s, want %s", got, want)
	}
}
