package cmd_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/report"
)

// missingHead makes HEAD's branch in the repository at d name a commit that is missing, with a
// promisor remote whose configuration, with %[1]s for marker and %[2]s for the .git, has a
// program that makes the file marker when git fetches the commit.
func missingHead(t *testing.T, d, config, marker string) {
	t.Helper()
	dotGit := filepath.Join(d, ".git")
	f, err := os.OpenFile(filepath.Join(dotGit, "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(f, "[core]\n\trepositoryformatversion = 1\n[extensions]\n\tpartialClone = evil\n[remote \"evil\"]\n\tpromisor = true\n"+config, marker, dotGit); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	head, err := os.ReadFile(filepath.Join(dotGit, "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	ref := filepath.Join(dotGit, strings.TrimSpace(strings.TrimPrefix(string(head), "ref: ")))
	if err := os.MkdirAll(filepath.Dir(ref), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ref, []byte(strings.Repeat("12", 20)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// lazyFetches are the configurations of [missingHead].
var lazyFetches = map[string]string{
	"uploadpack": "\turl = %[2]s\n\tuploadpack = touch %[1]s #\n",
	"sshCommand": "\turl = ssh://example.invalid/%.0[2]s\n[core]\n\tsshCommand = touch %[1]s #\n",
	"vcs none":   "\turl = x%.0[2]s\n\tvcs = none\n[alias]\n\tremote-none = \"!touch %[1]s #\"\n",
}

// runReport is the report of the harness that the test's one run composed outside its checkout.
func runReport(t *testing.T) report.Report {
	t.Helper()
	var paths []string
	if err := filepath.WalkDir(filepath.Dir(t.TempDir()), func(path string, _ os.DirEntry, err error) error {
		if strings.HasSuffix(path, "-report.json") {
			paths = append(paths, path)
		}
		return err
	}); err != nil || len(paths) != 1 {
		t.Fatalf("the reports %q, error %v", paths, err)
	}
	rep, err := report.Read(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestALaterSessionReadsNoObjectOfTheCheckout: before session 2 of a write-no-push run, which has
// no push step and so no check of the checkout's .git, qory runs no git in the checkout that
// reads an object, and no program of the checkout's configuration runs. The report of session 2
// has the worktree section of session 1, whatever the agent left in .git.
func TestALaterSessionReadsNoObjectOfTheCheckout(t *testing.T) {
	for name, config := range lazyFetches {
		t.Run(name, func(t *testing.T) {
			real, err := exec.LookPath("git")
			if err != nil {
				t.Skip("no git")
			}
			marker := filepath.Join(t.TempDir(), "ran")
			bin, log := t.TempDir(), filepath.Join(t.TempDir(), "log")
			p, requests := sharedPushRepo(t, laterAnswer(2, "claude", "example-provider-2", 30))
			change := p.change
			p.change = func(d map[string]any) {
				change(d)
				d["access"] = "write-no-push"
				d["harness"].(map[string]any)["harness"].(map[string]any)["workflows"] = map[string]any{"hello": map[string]any{"access": "write-no-push"}}
			}
			p.noOutput = true
			// Each git, with the folder it runs in.
			writeFile(t, filepath.Join(bin, "git"), "#!/bin/sh\necho \"$PWD $*\" >>'"+log+"'\nexec '"+real+"' \"$@\"\n")
			if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			var first, second report.Report
			joins := 0
			r := p.run(t, func(d string) {
				joins++
				rep := runReport(t)
				if joins == 1 {
					first = rep
					missingHead(t, d, config, marker)
					if err := os.Remove(log); err != nil {
						t.Fatal(err)
					}
					return
				}
				second = rep
				ran, err := os.ReadFile(log)
				if err != nil {
					t.Fatal(err)
				}
				for line := range strings.Lines(string(ran)) {
					if dir, _, _ := strings.Cut(line, " "); dir != d {
						continue
					}
					if !strings.HasSuffix(line, " remote get-url origin\n") {
						t.Errorf("before session 2 qory ran in the checkout: git %s", line)
					}
				}
			}, numbered())
			if joins != 2 || requests() != 2 {
				t.Fatalf("%d joins and %d next-session requests, want 2 and 2\n%s", joins, requests(), r.out)
			}
			if _, err := os.Lstat(marker); err == nil {
				t.Error("the program ran")
			}
			if first.Worktree == nil || second.Worktree == nil || *first.Worktree.Base != *second.Worktree.Base {
				t.Errorf("the worktree section of session 1 is %+v and of session 2 %+v, want the same one", first.Worktree, second.Worktree)
			}
		})
	}
}
