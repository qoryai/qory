package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenSuitesReadsHEADWithNoGit: the read of HEAD for the full suite runs no git and no
// program of the folder's configuration, whatever the setup commands or an earlier session left
// in .git. The start commit is the one of HEAD, and none where the files do not show it.
func TestOpenSuitesReadsHEADWithNoGit(t *testing.T) {
	id := strings.Repeat("12", 20)
	promisor := "[core]\n\trepositoryformatversion = 1\n[extensions]\n\tpartialClone = evil\n[remote \"evil\"]\n\tpromisor = true\n"
	appendTo := func(t *testing.T, path, text string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(text); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	put := func(t *testing.T, path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// missing is a plant of a HEAD at a missing commit, with config after the promisor's keys:
	// %[1]s is the marker's path and %[2]s the .git.
	missing := func(config string) func(t *testing.T, gitDir, marker, head string) string {
		return func(t *testing.T, gitDir, marker, _ string) string {
			appendTo(t, filepath.Join(gitDir, "config"), promisor+fmt.Sprintf(config, marker, gitDir))
			put(t, filepath.Join(gitDir, "HEAD"), id+"\n")
			return id
		}
	}
	for name, plant := range map[string]func(t *testing.T, gitDir, marker, head string) string{
		"none":       func(_ *testing.T, _, _, head string) string { return head },
		"uploadpack": missing("\turl = %[2]s\n\tuploadpack = touch %[1]s #\n"),
		"sshCommand": missing("\turl = ssh://example.invalid/%.0[2]s\n[core]\n\tsshCommand = touch %[1]s #\n"),
		"vcs none":   missing("\turl = x%.0[2]s\n\tvcs = none\n[alias]\n\tremote-none = \"!touch %[1]s #\"\n"),
		"bundleURI":  missing("\turl = %[2]s\n[fetch]\n\tbundleURI = %[2]s/b%.0[1]s\n"),
		"the alternates' refs": func(t *testing.T, gitDir, marker, head string) string {
			at := missing("\turl = %[2]s\n[core]\n\talternateRefsCommand = \"touch %[1]s #\"\n")(t, gitDir, marker, head)
			put(t, filepath.Join(gitDir, "objects", "info", "alternates"), filepath.Join(gitDir, "objects")+"\n")
			return at
		},
		"fsmonitor, a pager and an alias": missing("\turl = %[2]s\n[core]\n\tfsmonitor = touch %[1]s\n\tpager = touch %[1]s\n\thooksPath = %[2]s\n[alias]\n\trev-parse = \"!touch %[1]s\"\n"),
		"packed refs": func(t *testing.T, gitDir, _, _ string) string {
			put(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/packed\n")
			put(t, filepath.Join(gitDir, "packed-refs"), "# pack-refs with: peeled fully-peeled sorted \n"+id+" refs/heads/packed\n")
			return id
		},
		"no HEAD commit": func(t *testing.T, gitDir, _, _ string) string {
			put(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/unborn\n")
			return ""
		},
		"a commondir": func(t *testing.T, gitDir, _, _ string) string {
			put(t, filepath.Join(gitDir, "commondir"), "elsewhere\n")
			return ""
		},
		"reftable": func(t *testing.T, gitDir, _, _ string) string {
			appendTo(t, filepath.Join(gitDir, "config"), "[core]\n\trepositoryformatversion = 1\n[extensions]\n\trefStorage = reftable\n")
			return ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := workspaceRepo(t)
			gitDir, marker := filepath.Join(dir, ".git"), filepath.Join(t.TempDir(), "ran")
			head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
			if err != nil {
				t.Fatal(err)
			}
			want := plant(t, gitDir, marker, strings.TrimSpace(string(head)))
			api := newFakeAPI(t)
			api.withPlan(buildPlanText)
			spec := &runSpec{}
			spec.Dir = dir
			// Any git that runs makes the file ran-git.
			bin, ranGit := t.TempDir(), filepath.Join(t.TempDir(), "ran-git")
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\ntouch '"+ranGit+"'\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			r := openSuites(context.Background(), suiteSetup{
				workflow: "fix", plan: "build-plan",
				run: apiRun{api: api, dir: t.TempDir()}, spec: spec, runID: "r1", walled: true, fullSuite: true, said: func(string) {},
			})
			if r == nil || r.fault != nil || len(spec.After) == 0 {
				t.Fatalf("the suite does not run: %+v", r)
			}
			if got := spec.After[len(spec.After)-1]; got != want {
				t.Errorf("the start commit is %q, want %q", got, want)
			}
			// Every caller reads HEAD through headCommit.
			if got := headCommit(context.Background(), dir); got != want {
				t.Errorf("headCommit is %q, want %q", got, want)
			}
			if _, err := os.Lstat(ranGit); err == nil {
				t.Error("git ran")
			}
			if _, err := os.Lstat(marker); err == nil {
				t.Error("a program of the folder's configuration ran")
			}
		})
	}
}
