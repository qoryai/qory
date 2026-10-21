package cmd_test

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/hostgit"
)

// forgeParent rewrites the commit-graph file of the repository whose git directory is
// gitDir, written by git commit-graph write with generation version 1, so that it gives
// the commit child the first parent parent, and a generation one above parent's, and
// writes its checksum again. The objects stay as they are.
func forgeParent(t *testing.T, gitDir, child, parent string) {
	t.Helper()
	path := filepath.Join(gitDir, "objects", "info", "commit-graph")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data[:4]) != "CGPH" || data[5] != 1 {
		t.Fatalf("%s is no SHA-1 commit-graph", path)
	}
	const hashLen = 20
	chunks := map[string]uint64{}
	for i := 0; i < int(data[6]); i++ {
		at := 8 + 12*i
		chunks[string(data[at:at+4])] = binary.BigEndian.Uint64(data[at+4 : at+12])
	}
	if _, ok := chunks["GDA2"]; ok {
		t.Fatalf("%s holds corrected commit dates, which forgeParent does not rewrite", path)
	}
	fanout, oids, commits := chunks["OIDF"], chunks["OIDL"], chunks["CDAT"]
	count := int(binary.BigEndian.Uint32(data[fanout+255*4:]))
	index := func(oid string) int {
		want, err := hex.DecodeString(oid)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < count; i++ {
			at := int(oids) + i*hashLen
			if bytes.Equal(data[at:at+hashLen], want) {
				return i
			}
		}
		t.Fatalf("%s is not in %s", oid, path)
		return 0
	}
	entry := func(i int) int { return int(commits) + i*(hashLen+16) }
	c, p := entry(index(child)), entry(index(parent))
	binary.BigEndian.PutUint32(data[c+hashLen:], uint32(index(parent)))
	gen := binary.BigEndian.Uint64(data[p+hashLen+8:]) >> 34
	field := binary.BigEndian.Uint64(data[c+hashLen+8:])
	binary.BigEndian.PutUint64(data[c+hashLen+8:], (gen+1)<<34|field&(1<<34-1))
	sum := sha1.Sum(data[:len(data)-hashLen])
	copy(data[len(data)-hashLen:], sum[:])
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWorktreeRemoveReadsTheBranchAsItIsInTheObjects records a checkout, as a walled run
// there does, and gives the branch of its worktree a commit that nothing else has. The
// run then writes, in turn, a replace ref and a commit-graph, each of which gives the
// base's tip the branch's commit as its parent. With either, git on this machine reads every commit of the branch as on the
// base. remove without a terminal still finds the commit only on the branch and
// removes nothing, and git log through hostgit shows the commits as they are in the
// objects.
func TestWorktreeRemoveReadsTheBranchAsItIsInTheObjects(t *testing.T) {
	for _, c := range []struct {
		what  string
		write func(root, branch, tip string)
	}{
		{"a replace ref", func(root, branch, tip string) {
			forged := gitOut(t, root, "commit-tree", tip+"^{tree}", "-p", branch, "-m", "stack")
			runGit(t, root, "replace", tip, forged)
		}},
		{"a commit-graph", func(root, branch, tip string) {
			runGit(t, root, "-c", "commitGraph.generationVersion=1", "commit-graph", "write", "--reachable", "--no-progress")
			forgeParent(t, filepath.Join(root, ".git"), tip, branch)
		}},
	} {
		t.Run(c.what, func(t *testing.T) {
			root := worktreeRepo(t)
			if _, err := run(t, "wa", "feature", "--no-compose"); err != nil {
				t.Fatal(err)
			}
			wt := filepath.Join(filepath.Dir(root), "wt-feature")
			branch := commitOn(t, wt, "feature.txt", "work on feature")
			tip := gitOut(t, root, "rev-parse", "origin/main")
			parent := gitOut(t, root, "log", "-1", "--format=%P", tip)
			if err := cmd.RecordCheckout(root); err != nil {
				t.Fatal(err)
			}
			c.write(root, branch, tip)
			if got := gitOut(t, root, "rev-list", "feature", "--not", "--remotes"); got != "" {
				t.Fatalf("git still reads %s as only on the branch", got)
			}
			out, err := runNoTTY(t, "wr", "feature", "--offline")
			if err == nil || !strings.Contains(err.Error(), "branch feature has 1 commit that no remote branch") {
				t.Errorf("%v, want the commit only on the branch\n%s", err, out)
			}
			if _, err := os.Stat(wt); err != nil {
				t.Errorf("the worktree went: %v", err)
			}
			git, err := hostgit.Command(context.Background(), root, "log", "-1", "--format=%P", tip)
			if err != nil {
				t.Fatal(err)
			}
			logged, err := git.Output()
			if err != nil {
				t.Fatalf("git log: %v", err)
			}
			if got := strings.TrimSpace(string(logged)); got != parent {
				t.Errorf("the parent of the base's tip through hostgit is %q, want %q", got, parent)
			}
		})
	}
}
