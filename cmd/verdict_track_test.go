package cmd_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/qory/cmd"
)

// storedVerdict is the answer of the run API to a verdict that it stores.
const storedVerdict = `{"version":1,"artifact":{"type":"verdict","name":"main","version":{"id":"av_4","number":4}}}`

// trackReason is a reason that passes, and trackBody is the verdict with it.
const (
	trackReason = "The change touches one file."
	trackBody   = `{"verdict":"passed","track":{"id":"small-change","reason":"The change touches one file."}}`
)

// TestRunVerdictSendsTheTrack sends the track as the member track of the verdict, with
// each kind of verdict, and a verdict without the pair sends none.
func TestRunVerdictSendsTheTrack(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)
	server.response = storedVerdict

	points := writePoints(t, root, verdictPoints)

	for _, args := range [][]string{
		{"run", "verdict", "passed", "--track", "small-change", "--track-reason", trackReason},
		{"run", "verdict", "changes-requested", "--track", "standard", "--track-reason", trackReason},
		{"run", "verdict", "escalate", "--points", points, "--track", "standard", "--track-reason", trackReason},
		{"run", "verdict", "passed"},
	} {
		out, err := run(t, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}

		if out != "Stored the verdict "+args[2]+" as version 4.\n" {
			t.Errorf("%v: out = %q", args, out)
		}
	}

	want := []string{
		trackBody,
		`{"verdict":"changes-requested","track":{"id":"standard","reason":"The change touches one file."}}`,
		strings.TrimSuffix(verdictPointsBody, "}") + `,"track":{"id":"standard","reason":"The change touches one file."}}`,
		`{"verdict":"passed"}`,
	}
	if !slices.Equal(server.verdicts, want) {
		t.Errorf("bodies = %q, want %q", server.verdicts, want)
	}
}

// TestRunVerdictAcceptsTheLimitsOfATrack sends an id of 40 characters and a reason of 300,
// in characters and not in bytes.
func TestRunVerdictAcceptsTheLimitsOfATrack(t *testing.T) {
	inCheckout(t)
	server := startRunAPI(t)
	server.response = storedVerdict

	id := "a" + strings.Repeat("b", 19) + "-" + strings.Repeat("c", 19)
	reason := strings.Repeat("é", 300)

	if _, err := run(t, "run", "verdict", "passed", "--track", id, "--track-reason", reason); err != nil {
		t.Fatal(err)
	}

	want := `{"verdict":"passed","track":{"id":"` + id + `","reason":"` + reason + `"}}`
	if !slices.Equal(server.verdicts, []string{want}) {
		t.Errorf("bodies = %q, want %q", server.verdicts, want)
	}
}

// TestRunVerdictOutsideARunKeepsTheTrack keeps the same document as a run sends.
func TestRunVerdictOutsideARunKeepsTheTrack(t *testing.T) {
	root := inCheckout(t)
	t.Setenv("QORY_RUN_API", "")

	_, err := run(t, "run", "verdict", "passed", "--track", "small-change", "--track-reason", trackReason)
	if err != nil {
		t.Fatal(err)
	}

	wantFile(t, filepath.Join(root, ".qory", "artifacts", "verdict", "main", "v1", "verdict.json"), trackBody)
}

// TestRunVerdictChecksItsTrack stops each wrong id, reason or pair of the track flags with
// exit 2 and an error whose line 1 has at most 98 characters and the fix, before anything
// is sent: in a run, no verdict reaches the run API, and outside a run, the store keeps
// none.
func TestRunVerdictChecksItsTrack(t *testing.T) {
	root := inCheckout(t)

	pair := func(id, reason string) []string {
		return []string{"run", "verdict", "passed", "--track", id, "--track-reason", reason}
	}

	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"id alone", []string{"run", "verdict", "passed", "--track", "small-change"},
			"Pass --track-reason <text> with --track: a track needs the reason for it."},
		{"reason alone", []string{"run", "verdict", "passed", "--track-reason", trackReason},
			"Pass --track <id> with --track-reason: a reason needs the track it is for."},
		{"empty reason alone", []string{"run", "verdict", "passed", "--track-reason", ""},
			"Pass --track <id> with --track-reason: a reason needs the track it is for."},
		{"id too long", pair("a"+strings.Repeat("b", 40), trackReason),
			"Set --track to lower-case words joined by hyphens, such as small-change, at most 40 characters."},
		{"id in capitals", pair("Small-change", trackReason),
			"Set --track to lower-case words joined by hyphens, such as small-change, at most 40 characters."},
		{"id with an underscore", pair("small_change", trackReason),
			"Set --track to lower-case words joined by hyphens, such as small-change, at most 40 characters."},
		{"id with a double hyphen", pair("small--change", trackReason),
			"Set --track to lower-case words joined by hyphens, such as small-change, at most 40 characters."},
		{"id with a trailing hyphen", pair("small-", trackReason),
			"Set --track to lower-case words joined by hyphens, such as small-change, at most 40 characters."},
		{"id starting with a digit", pair("1small", trackReason),
			"Set --track to lower-case words joined by hyphens, such as small-change, at most 40 characters."},
		{"empty id", pair("", trackReason),
			"Set --track to lower-case words joined by hyphens, such as small-change, at most 40 characters."},
		{"empty reason", pair("small-change", ""),
			"Set --track-reason to one sentence on one line, 1 to 300 characters. The reason is empty."},
		{"reason too long", pair("small-change", strings.Repeat("é", 301)),
			"Shorten --track-reason to at most 300 characters. The reason has 301."},
		{"reason with a line break", pair("small-change", "One file.\nOne more."),
			"Write --track-reason on one line. The reason has a line break (U+000A)."},
		{"reason with a tab", pair("small-change", "One\tfile."),
			"Write --track-reason on one line. The reason has a tab (U+0009)."},
		{"reason with a zero-width space", pair("small-change", "One​file."),
			"Remove the character U+200B from --track-reason. The verdict format excludes it."},
		{"reason with a tag character", pair("small-change", "One\U000E0041file."),
			"Remove the character U+E0041 from --track-reason. The verdict format excludes it."},
		{"the flag --track-kind", []string{"run", "verdict", "passed", "--track", "small-change", "--track-reason", trackReason,
			"--track-kind", "A comment"}, "unknown flag: --track-kind"},
		{"track flag on another command", []string{"run", "verdict", "passed", "--track-id", "x"}, "unknown flag: --track-id"},
		{"track flag on push", []string{"artifact", "push", "demo", "-", "--track", "small-change"}, "unknown flag: --track"},
	} {
		for _, inRun := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s in a run %v", c.name, inRun), func(t *testing.T) {
				var server *runAPIServer
				if inRun {
					server = startRunAPI(t)
				} else {
					t.Setenv("QORY_RUN_API", "")
				}

				_, err := run(t, c.args...)
				wantExit(t, err, cmd.ExitInput, c.want)

				if err == nil {
					return
				}

				line := firstLine(err.Error())
				if line != c.want {
					t.Errorf("line 1 = %q, want %q", line, c.want)
				}

				if n := utf8.RuneCountInString(line); n > 98 {
					t.Errorf("line 1 has %d characters: %q", n, line)
				}

				if server != nil && len(server.verdicts) != 0 {
					t.Errorf("sent %q", server.verdicts)
				}
			})
		}
	}

	if _, err := os.Stat(filepath.Join(root, ".qory", "artifacts", "verdict")); !os.IsNotExist(err) {
		t.Errorf("the store has a verdict: %v", err)
	}
}

// TestRunVerdictShowsTheValueOfAWrongTrackID shows a long id cut on line 2, so line 1
// stays within 100 characters.
func TestRunVerdictShowsTheValueOfAWrongTrackID(t *testing.T) {
	inCheckout(t)
	startRunAPI(t)

	id := "A" + strings.Repeat("b", 80)

	_, err := run(t, "run", "verdict", "passed", "--track", id, "--track-reason", trackReason)
	wantExit(t, err, cmd.ExitInput, "Set --track to lower-case words")

	if want := "\n  The value is \"" + id[:40] + "\" and more."; err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Errorf("err = %v, want it to end with %q", err, want)
	}
}

// TestRunVerdictMasksAKeyInAWrongTrackID shows a wrong id with a run key's prefix as a
// phrase on line 2, so no part of the key reaches the output or the error, and a plain
// wrong id quoted.
func TestRunVerdictMasksAKeyInAWrongTrackID(t *testing.T) {
	// A fake run key for the test: qrk_ and 40 letters.
	const letters = "abcdefghijklmnopqrstuvwxyzabcdefghijklmn"
	const runKey = "qrk_" + letters
	const line1 = "Set --track to lower-case words joined by hyphens, such as small-change, at most 40 characters.\n"

	for name, c := range map[string]struct{ id, want string }{
		"run key":   {runKey, line1 + "  The value is (a value that contains a run key)."},
		"plain bad": {"Small-change", line1 + `  The value is "Small-change".`},
	} {
		t.Run(name, func(t *testing.T) {
			inCheckout(t)
			server := startRunAPI(t)

			out, err := run(t, "run", "verdict", "passed", "--track", c.id, "--track-reason", trackReason)
			wantExit(t, err, cmd.ExitInput, c.want)

			if err == nil || err.Error() != c.want {
				t.Errorf("err = %v, want %q", err, c.want)
			}

			if len(server.verdicts) != 0 {
				t.Errorf("sent %q", server.verdicts)
			}

			if c.id != runKey {
				return
			}

			// out is stdout and stderr together, and qory prints err on stderr.
			for _, text := range []string{out, fmt.Sprint(err)} {
				if strings.Contains(strings.ToLower(text), "qrk_") {
					t.Errorf("the text has the key's prefix: %q", text)
				}

				for i := 0; i+6 <= len(letters); i++ {
					if strings.Contains(text, letters[i:i+6]) {
						t.Errorf("the text has %q of the key: %q", letters[i:i+6], text)
					}
				}
			}
		})
	}
}

// TestRunVerdictExitsWith4ForARefusedVerdict gives each 422 invalid_artifact of the verdict
// exit 4, with line 1 of at most 98 characters that has the fix, then Qory Apiary's text
// on an indented line. Every other error keeps its status.
func TestRunVerdictExitsWith4ForARefusedVerdict(t *testing.T) {
	notOffered := "Set track.id to a track that the gate of Explore offers (fast, standard), " +
		"or leave track out of the verdict. The track small-chnge is not offered."
	notOpen := "Push the questions first, or point at an open question in verdict.json: " +
		"point 2 is about question q9, which is not open."
	short := "Remove the point that has no question from verdict.json."
	line98 := "Fix verdict.json: " + strings.Repeat("a", 98-len("Fix verdict.json: "))

	for _, c := range []struct {
		name     string
		names    string
		message  string
		wantLine string
		args     []string
	}{
		{"a track the gate does not offer", "track", notOffered,
			"Set --track to a track that the gate of the stage offers, or leave out the track flags.",
			[]string{"--track", "small-chnge", "--track-reason", trackReason}},
		{"a gate with no track", "track", "Leave track out of the verdict: the gate of Explore offers no track.",
			"Set --track to a track that the gate of the stage offers, or leave out the track flags.",
			[]string{"--track", "small-change", "--track-reason", trackReason}},
		{"a point of a long text", "files[0]", notOpen, "Fix the refused part of the verdict, then run the command again.", nil},
		{"a point of a short text", "files[0]", short, short, nil},
		{"the member track.id", "track.id", short, "Set --track to a track that the gate of the stage offers, or leave out the track flags.", nil},
		{"a member called tracks", "tracks", short, short, nil},
		{"a member called trackx", "trackx", short, short, nil},
		{"a line 1 of 98 characters", "files[0]", line98, line98, nil},
		{"a line 1 of 99 characters", "files[0]", line98 + "x", "Fix the refused part of the verdict, then run the command again.", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			inCheckout(t)
			server := startRunAPI(t)
			server.status = http.StatusUnprocessableEntity
			server.response = fmt.Sprintf(`{"error":"invalid_artifact","names":[%q],"message":%q}`, c.names, c.message)

			_, err := run(t, append([]string{"run", "verdict", "passed"}, c.args...)...)
			wantExit(t, err, cmd.ExitArtifactRefused, c.wantLine)

			if err == nil {
				return
			}

			line := firstLine(err.Error())
			if line != c.wantLine {
				t.Errorf("line 1 = %q, want %q", line, c.wantLine)
			}

			if n := utf8.RuneCountInString(line); n > 98 {
				t.Errorf("line 1 has %d characters: %q", n, line)
			}

			if line != c.message && !strings.HasSuffix(err.Error(), "\n  "+c.message) {
				t.Errorf("err = %q, want Qory Apiary's text on the next line", err)
			}
		})
	}
}

// TestRunVerdictKeepsTheOtherExitStatuses leaves every refusal of the run API that is not
// 422 invalid_artifact as it was: 413 exits 4, and the rest exit 1.
func TestRunVerdictKeepsTheOtherExitStatuses(t *testing.T) {
	for _, c := range []struct {
		code   string
		status int
		exit   int
	}{
		{"payload_too_large", http.StatusRequestEntityTooLarge, cmd.ExitArtifactRefused},
		{"run_ended", http.StatusConflict, 1},
		{"unavailable", http.StatusServiceUnavailable, 1},
		{"invalid_request", http.StatusBadRequest, 1},
		{"unknown_type", http.StatusUnprocessableEntity, cmd.ExitArtifactRefused},
	} {
		t.Run(c.code, func(t *testing.T) {
			inCheckout(t)
			server := startRunAPI(t)
			server.status = c.status
			server.response = fmt.Sprintf(`{"error":%q,"message":"The line of the run API."}`, c.code)

			_, err := run(t, "run", "verdict", "passed")
			if err == nil || cmd.ExitCode(err) != c.exit {
				t.Errorf("exit status = %d, want %d, err = %v", cmd.ExitCode(err), c.exit, err)
			}
		})
	}
}

// TestRunVerdictFirstLinesFitWithThePrefix measures every line 1 that qory writes for the
// track flags: with the "✗ " that qory prints before it, each fits in 100 characters.
func TestRunVerdictFirstLinesFitWithThePrefix(t *testing.T) {
	long := "A" + strings.Repeat("b", 99)
	reason := "The change touches one file."

	for _, args := range [][]string{
		{"--track", "small-change"},
		{"--track-reason", reason},
		{"--track", long, "--track-reason", reason},
		{"--track", "small-change", "--track-reason", ""},
		{"--track", "small-change", "--track-reason", strings.Repeat("a", 99999)},
		{"--track", "small-change", "--track-reason", "a\nb"},
		{"--track", "small-change", "--track-reason", "a\tb"},
		{"--track", "small-change", "--track-reason", "a\u200bb"},
	} {
		inCheckout(t)
		t.Setenv("QORY_RUN_API", "")

		_, err := run(t, append([]string{"run", "verdict", "passed"}, args...)...)
		if err == nil {
			t.Errorf("%q: no error", args)

			continue
		}

		if line := firstLine(err.Error()); utf8.RuneCountInString("✗ "+line) > 100 {
			t.Errorf("%q: line 1 has %d characters with the prefix: %q", args, utf8.RuneCountInString("✗ "+line), line)
		}
	}

	for _, line := range []string{
		"Set --track to a track that the gate of the stage offers, or leave out the track flags.",
		"Fix the refused part of the verdict, then run the command again.",
	} {
		if n := utf8.RuneCountInString("✗ " + line); n > 100 {
			t.Errorf("%q has %d characters with the prefix", line, n)
		}
	}
}
