package cmd_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/qory/cmd"
)

// unknownDecisionResponse is the run API's 422 for a decision that is not the id of a
// person's note on the run's work item, as Qory Apiary words it.
const unknownDecisionResponse = `{"error":"unknown_decision","message":"Set decision to the id of a person's note ` +
	`on this work item, from notes in GET work-item, or leave decision out."}`

// textUnknownDecision is qory's text for that 422, for the id gd_0000000000000000.
const textUnknownDecision = "Set --decision to the id of a person's note on this work item, or drop the flag.\n" +
	`  "gd_0000000000000000" is not such an id. qory work-item show prints the open notes with their ids.`

func TestArtifactPushSendsTheDecision(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)

	path := filepath.Join(root, "checkout.png")
	if err := os.WriteFile(path, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, "artifact", "push", "screenshot", path, "--name", "checkout", "--decision", "gd_01jc4t9k2m8x5q7w")
	if err != nil {
		t.Fatal(err)
	}

	if out != "Pushed screenshot/checkout version 3.\n" {
		t.Errorf("out = %q", out)
	}

	if got := server.pushes[0]["decision"]; got != "gd_01jc4t9k2m8x5q7w" {
		t.Errorf("decision = %v, body = %v", got, server.pushes[0])
	}

	// A push without the flag has no decision member: the run API stores it as before.
	if _, err := run(t, "artifact", "push", "screenshot", path, "--name", "checkout"); err != nil {
		t.Fatal(err)
	}

	if _, has := server.pushes[1]["decision"]; has {
		t.Errorf("body = %v, want no decision", server.pushes[1])
	}
}

func TestArtifactPushWithdrawSendsTheDecision(t *testing.T) {
	inCheckout(t)
	server := startRunAPI(t)
	server.index = withdrawIndex()
	server.response = `{"version":1,"artifact":{"type":"demo-script","name":"scenario-2","version":{"id":"av_4","number":3}}}`

	if _, err := run(t, "artifact", "push", "demo-script", "--name", "scenario-2", "--withdraw", "--decision", "es_01jc4t9k2m8x5q7w"); err != nil {
		t.Fatal(err)
	}

	body := server.pushes[0]
	if body["decision"] != "es_01jc4t9k2m8x5q7w" || body["withdraw"] != true {
		t.Errorf("body = %v", body)
	}
}

// TestArtifactPushOfAnUnknownDecision is a 422 unknown_decision, for a push and for a
// withdrawal: exit 4 with qory's fix line, which names the flag, and no copy in the store.
func TestArtifactPushOfAnUnknownDecision(t *testing.T) {
	t.Run("push", func(t *testing.T) {
		root := inCheckout(t)
		server := startRunAPI(t)
		server.status = http.StatusUnprocessableEntity
		server.response = unknownDecisionResponse

		_, err := runIn(t, "text", "artifact", "push", "screenshot", "-", "--decision", "gd_0000000000000000")
		wantExit(t, err, cmd.ExitArtifactRefused, textUnknownDecision)

		if err != nil && err.Error() != textUnknownDecision {
			t.Errorf("err = %q, want %q", err, textUnknownDecision)
		}

		if _, err := os.Stat(filepath.Join(root, ".qory", "artifacts", "screenshot")); !os.IsNotExist(err) {
			t.Errorf("a copy of the refused push: %v", err)
		}
	})

	t.Run("withdrawal", func(t *testing.T) {
		inCheckout(t)
		server := startRunAPI(t)
		server.index = withdrawIndex()
		server.status = http.StatusUnprocessableEntity
		server.response = unknownDecisionResponse

		_, err := run(t, "artifact", "push", "demo-script", "--name", "scenario-2", "--withdraw", "--decision", "gd_0000000000000000")
		wantExit(t, err, cmd.ExitArtifactRefused, textUnknownDecision)
	})

	// Line 1 starts with the fix and fits in 100 characters behind "qory run: ".
	first, _, _ := strings.Cut(textUnknownDecision, "\n")
	if n := utf8.RuneCountInString("qory run: " + first); n > 100 {
		t.Errorf("line 1 has %d characters with its prefix, want at most 100", n)
	}
}

// TestArtifactPushOfAnUnknownDecisionShowsTheValueShort is line 2 of a 422
// unknown_decision for a long value, cut to 40 characters, and for a value with a key's
// prefix, which a phrase replaces.
func TestArtifactPushOfAnUnknownDecisionShowsTheValueShort(t *testing.T) {
	const runKey = "qrk_value-for-the-test-only"

	for name, c := range map[string]struct{ value, shown, hidden string }{
		"long":    {"gd_" + strings.Repeat("a", 60), `"gd_` + strings.Repeat("a", 37) + `" and more`, strings.Repeat("a", 38)},
		"run key": {runKey, "(a value that contains a run key)", "qrk_"},
	} {
		t.Run(name, func(t *testing.T) {
			inCheckout(t)
			server := startRunAPI(t)
			server.status = http.StatusUnprocessableEntity
			server.response = unknownDecisionResponse

			_, err := runIn(t, "text", "artifact", "push", "screenshot", "-", "--decision", c.value)
			want := "Set --decision to the id of a person's note on this work item, or drop the flag.\n  " +
				c.shown + " is not such an id. qory work-item show prints the open notes with their ids."
			wantExit(t, err, cmd.ExitArtifactRefused, want)

			if err != nil && strings.Contains(err.Error(), c.hidden) {
				t.Errorf("err = %q, want no %q", err, c.hidden)
			}
		})
	}
}

// TestArtifactPushOutsideARunKeepsNoDecision is --decision outside a run: as with --note,
// the store keeps the files and nothing else, and nothing is sent.
func TestArtifactPushOutsideARunKeepsNoDecision(t *testing.T) {
	root := inCheckout(t)
	t.Setenv("QORY_RUN_API", "")

	path := filepath.Join(root, "a.md")
	if err := os.WriteFile(path, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := run(t, "artifact", "push", "demo-video", path, "--decision", "gd_01jc4t9k2m8x5q7w"); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(root, ".qory", "artifacts", "demo-video", "main", "v1"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "a.md" {
		t.Errorf("v1 = %v, %v, want only a.md", entries, err)
	}
}

func TestArtifactPushHelpListsTheDecision(t *testing.T) {
	push, _, err := cmd.Root().Find([]string{"artifact", "push"})
	if err != nil {
		t.Fatal(err)
	}

	flag := push.Flags().Lookup("decision")
	if flag == nil || flag.Value.Type() != "string" || flag.DefValue != "" {
		t.Fatalf("--decision = %+v, want a string flag with no default", flag)
	}

	out, err := run(t, "artifact", "push", "--help")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out, "--decision string   the id of the person's note that asked for this version, from qory work-item show") {
		t.Errorf("help has no line for --decision:\n%s", out)
	}
}
