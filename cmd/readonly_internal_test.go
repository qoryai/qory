package cmd

import (
	"strings"
	"testing"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/stack"
)

// TestARunTakesTheLowerOfItsTwoLevels is every combination of the workflow's access in
// the stack, read, write-no-push, write or undeclared, and access in the run's
// documents, read, write-no-push, write or empty as with no target. The run's level is
// the lower of the two in the order read, write-no-push, write, and only the level read
// is a read run. A level in the documents does not raise the workflow's, and one in the
// workflow does not raise the documents'. Documents with no access leave the workflow's
// level alone. A run not from a run code has no level and does not read only.
func TestARunTakesTheLowerOfItsTwoLevels(t *testing.T) {
	declared := map[string]map[string]stack.Workflow{
		"declared read":          {"hello": {Access: stack.AccessRead}},
		"declared write-no-push": {"hello": {Access: stack.AccessWriteNoPush}},
		"declared write":         {"hello": {Access: stack.AccessWrite}},
		"undeclared":             {},
	}
	for _, c := range []struct {
		declared, access, level string
	}{
		{"declared read", stack.AccessRead, stack.AccessRead},
		{"declared read", stack.AccessWriteNoPush, stack.AccessRead},
		{"declared read", stack.AccessWrite, stack.AccessRead},
		{"declared read", "", stack.AccessRead},
		{"declared write-no-push", stack.AccessRead, stack.AccessRead},
		{"declared write-no-push", stack.AccessWriteNoPush, stack.AccessWriteNoPush},
		{"declared write-no-push", stack.AccessWrite, stack.AccessWriteNoPush},
		{"declared write-no-push", "", stack.AccessWriteNoPush},
		{"declared write", stack.AccessRead, stack.AccessRead},
		{"declared write", stack.AccessWriteNoPush, stack.AccessWriteNoPush},
		{"declared write", stack.AccessWrite, stack.AccessWrite},
		{"declared write", "", stack.AccessWrite},
		{"undeclared", stack.AccessRead, stack.AccessRead},
		{"undeclared", stack.AccessWriteNoPush, stack.AccessRead},
		{"undeclared", stack.AccessWrite, stack.AccessRead},
		{"undeclared", "", stack.AccessRead},
	} {
		t.Run(c.declared+" with access "+c.access, func(t *testing.T) {
			r := &codeRun{workflow: "hello", access: c.access}
			if got := accessOf(r, declared[c.declared]); got != c.level {
				t.Errorf("accessOf is %q, want %q", got, c.level)
			}
			got, want := readsOnly(r, declared[c.declared]), c.level == stack.AccessRead
			if got != want {
				t.Errorf("readsOnly is %v, want %v", got, want)
			}
			branch := &preparation.WorkBranch{}
			wantPush := c.access == stack.AccessWrite && c.level == stack.AccessWrite
			if got := canPush(r, c.level, branch); got != wantPush {
				t.Errorf("canPush is %v, want %v", got, wantPush)
			}
		})
	}
	none := declared["declared read"]
	if readsOnly(nil, none) || accessOf(nil, declared["declared write"]) != "" {
		t.Error("a run not from a run code has a level")
	}
}

// TestTheNestedDockerLineShowsTheImage is line 2 of a read run on an image with nested
// Docker: it shows the image's name, its reference when the image has no name, and a
// phrase in place of a value that contains a secret.
func TestTheNestedDockerLineShowsTheImage(t *testing.T) {
	for _, c := range []struct {
		name string
		img  session.Image
		want string
	}{
		{"a name", session.Image{Name: "nested", Ref: "example.com/nested:1", Docker: true}, "image nested runs nested Docker"},
		{"a reference", session.Image{Ref: "example.com/nested:1", Docker: true}, "image example.com/nested:1 runs nested Docker"},
		{"a secret in the name", session.Image{Name: "qak_" + strings.Repeat("a", 8), Docker: true}, "image (a name that contains an access key secret) runs nested Docker"},
		{"a secret in the reference", session.Image{Ref: "example.com/qak_" + strings.Repeat("a", 8), Docker: true}, "image (a name that contains an access key secret) runs nested Docker"},
	} {
		t.Run(c.name, func(t *testing.T) {
			spec := &runSpec{}
			spec.Wall = &wall.Docker{}
			err := readOnlyCheckout(spec, "/checkout", "hello", c.img, startedRun)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("the line is %v, want %q in it", err, c.want)
			}
			if spec.WorkspaceReadOnly {
				t.Error("the refused run binds the workspace read-only")
			}
		})
	}
}

// TestTheStopsOfAReadCheckoutEndWithTheSubject is the three stops of a read checkout: each
// ends with "so the run did not start" for a read run and "so the review did not start"
// for a review join.
func TestTheStopsOfAReadCheckoutEndWithTheSubject(t *testing.T) {
	for _, c := range []struct {
		name string
		spec func() *runSpec
		img  session.Image
	}{
		{"no wall", func() *runSpec { return &runSpec{} }, session.Image{}},
		{"setup inside", func() *runSpec {
			s := &runSpec{}
			s.Wall, s.setupWhere = &wall.Docker{}, config.SetupInside
			return s
		}, session.Image{}},
		{"nested Docker", func() *runSpec { s := &runSpec{}; s.Wall = &wall.Docker{}; return s }, session.Image{Name: "nested", Docker: true}},
	} {
		for subject, want := range map[string]string{startedRun: "so the run did not start", startedReview: "so the review did not start"} {
			err := readOnlyCheckout(c.spec(), "/checkout", "hello", c.img, subject)
			if err == nil || !strings.Contains(strings.ReplaceAll(err.Error(), ",\n  so", ", so"), want) {
				t.Errorf("%s, %s: the line is %v, want %q in it", c.name, subject, err, want)
			}
		}
	}
}
