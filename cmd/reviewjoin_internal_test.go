package cmd

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"
)

func TestReadSessionSpec(t *testing.T) {
	base := session.SessionSpec{Args: []string{"claude", "--model", "m"}, WorkspaceReadOnly: false}
	got, err := readSessionSpec(base, "claude", false)
	if err != nil {
		t.Fatal(err)
	}
	if !got.WorkspaceReadOnly {
		t.Error("the workspace is not read-only")
	}
	i := slices.Index(got.Args, "--tools")
	if i < 0 || i+1 >= len(got.Args) {
		t.Fatalf("args %q have no --tools list", got.Args)
	}
	tools := strings.Split(got.Args[i+1], ",")
	want := []string{"Read", "Grep", "Glob", "Skill", "Write", "Agent"}
	if !slices.Equal(tools, want) {
		t.Errorf("tools %q, want %q: no Bash and no edit tool", tools, want)
	}
	if !slices.Equal(got.Args[:3], base.Args) {
		t.Errorf("args %q do not start with the given ones", got.Args)
	}
	if len(base.Args) != 3 || base.WorkspaceReadOnly {
		t.Errorf("the given spec changed: %+v", base)
	}
}

func TestReadSessionSpecRefusesNestedDockerAndOtherRuntimes(t *testing.T) {
	_, err := readSessionSpec(session.SessionSpec{}, "claude", true)
	var re *reasonError
	if !errors.As(err, &re) || re.code != "review_needs_no_nested_docker" ||
		err.Error() != "Remove the Docker daemon from the run's image.\n  A read-only review session cannot run beside an inner Docker daemon." {
		t.Errorf("nested Docker returned %v", err)
	}
	if _, err := readSessionSpec(session.SessionSpec{}, "codex", false); err == nil || !strings.HasPrefix(err.Error(), "Run the workflow on claude.") {
		t.Errorf("codex returned %v", err)
	}
}
