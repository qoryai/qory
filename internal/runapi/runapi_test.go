package runapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/runapi"
)

// The examples of Core's docs/contract/run-api.md.
const (
	snapshotExample = `{
  "version": 1,
  "work_item": {"id": "wi_1", "number": 12, "url": "https://example.test/wi_1"},
  "run": {"id": "run-1", "workflow": "capture-demo", "round": 2},
  "stage": {"id": "st_1", "name": "Build", "round": 2, "rounds": 3, "state": "running"},
  "inputs": [
    {"type": "acceptance-criteria", "required": true, "version": {"id": "av_1", "number": 2}}
  ],
  "outputs": [
    {"type": "demo-video", "required": true, "version": null, "check": "missing"}
  ],
  "verdict": {"verdict": "changes-requested", "gaps": "Add the empty cart.", "version": {"id": "av_2", "number": 1}},
  "gaps": "Add the empty cart.",
  "ticket": {"external_ref": "T-1", "url": "https://example.test/T-1", "title": "A title", "body": "A body",
             "filed_by": {"name": "Dana", "kind": "person"}, "filed_at": "2026-10-31T09:00:00Z", "collection": "c"},
  "instruction": {"text": "Go", "by": {"name": "dana"}, "at": "2026-10-31T09:00:00Z"},
  "comments": [],
  "comments_omitted": 0,
  "answers": [],
  "notes": [
    {"kind": "request_changes", "stage_id": "st_1", "by": {"name": "dana"},
     "note": "Add the empty cart.", "artifact_version_id": "av_1", "at": "2026-10-31T09:00:00Z"}
  ]
}`

	noStageExample = `{
  "version": 1,
  "work_item": {"id": "wi_1", "number": 12, "url": "https://example.test/wi_1"},
  "run": {"id": "run-1", "workflow": null, "round": null},
  "stage": {"id": null, "name": null, "round": null, "rounds": null, "state": null},
  "inputs": [],
  "outputs": [],
  "verdict": null,
  "gaps": null,
  "comments": [],
  "comments_omitted": 0,
  "answers": [],
  "notes": []
}`

	indexExample = `{
  "version": 1,
  "artifacts": [
    {
      "type": "demo-video",
      "name": "main",
      "stage_id": "st_1",
      "approved_version": "av_1",
      "newest_version": 2,
      "changed_after_approval": true,
      "versions": [
        {
          "id": "av_2",
          "number": 2,
          "writer": {"kind": "run", "run_id": "run-1"},
          "stage_id": "st_1",
          "note": null,
          "at": "2026-10-31T09:00:00Z",
          "files": [{"filename": "demo.mp4", "content_type": "video/mp4", "size": 1048576, "sha256": "abc"}],
          "approvals": [{"by": {"name": "dana"}, "stage_id": "st_1", "at": "2026-10-31T09:00:00Z"}]
        }
      ]
    }
  ]
}`

	pushedExample = `{"version": 1, "artifact": {"type": "screenshot", "name": "checkout", "version": {"id": "av_3", "number": 3}}}`
)

// serve returns a client whose base URL is on a test server with handler, as the gateway
// has it: <gateway>/v1/runs/run-1.
func serve(t *testing.T, handler http.HandlerFunc) *runapi.Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := runapi.New(server.URL+"/v1/runs/run-1", server.Client())
	if err != nil {
		t.Fatal(err)
	}

	return client
}

// respond returns a handler that writes status and body as JSON.
func respond(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func TestNewChecksTheBaseURL(t *testing.T) {
	for _, base := range []string{
		"",
		"gateway:8080/v1/runs/1",
		"ftp://gateway/v1/runs/1",
		"http://",
		"http://user:secret@gateway/v1/runs/1",
		"http://gateway/v1/runs/1?x=1",
		"http://gateway/v1/runs/1#x",
		"http://gateway/v1/runs/1?",
		"http://gateway/v1/runs/1#",
		"http://gateway\x7f/v1",
		"http://user:secret@gateway:notaport/v1",
		"http://user:s3/cret@gateway/v1/runs/1",
		"ftp://gateway/v1?token=secret",
	} {
		_, err := runapi.New(base, http.DefaultClient)
		if err == nil {
			t.Errorf("New(%q) returned no error", base)
			continue
		}

		if !strings.HasPrefix(err.Error(), "Set the run API's base URL") {
			t.Errorf("New(%q): %q does not start with the fix", base, err)
		}

		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "s3") {
			t.Errorf("New(%q): %q contains the password", base, err)
		}
	}

	if _, err := runapi.New("http://gateway/v1/runs/1", nil); err == nil {
		t.Error("New with a nil client returned no error")
	}
}

func TestATrailingSlashOnTheBaseURLIsTheSameURL(t *testing.T) {
	var path string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		respond(http.StatusOK, snapshotExample)(w, r)
	}))
	defer server.Close()

	client, err := runapi.New(server.URL+"/v1/runs/run-1/", server.Client())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.WorkItem(context.Background()); err != nil {
		t.Fatal(err)
	}

	if path != "/v1/runs/run-1/work-item" {
		t.Errorf("path = %q", path)
	}
}

func TestWorkItemReadsTheSnapshot(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/runs/run-1/work-item" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}

		// The container holds no key: every signature and identity header is the gateway's.
		for name := range r.Header {
			if strings.HasPrefix(name, "X-Qory") || name == "Authorization" {
				t.Errorf("the client sent %s", name)
			}
		}

		if accept := r.Header.Get("Accept"); accept != "" {
			t.Errorf("Accept = %q; the run API returns 406 for anything but JSON", accept)
		}

		respond(http.StatusOK, snapshotExample)(w, r)
	})

	snapshot, err := client.WorkItem(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if snapshot.WorkItem.ID != "wi_1" || snapshot.WorkItem.Number != 12 {
		t.Errorf("work item = %+v", snapshot.WorkItem)
	}

	if *snapshot.Run.Workflow != "capture-demo" || *snapshot.Run.Round != 2 || *snapshot.Stage.Name != "Build" {
		t.Errorf("run = %+v, stage = %+v", snapshot.Run, snapshot.Stage)
	}

	if snapshot.Inputs[0].Version.ID != "av_1" || snapshot.Outputs[0].Version != nil ||
		snapshot.Outputs[0].Check != "missing" {
		t.Errorf("inputs = %+v, outputs = %+v", snapshot.Inputs, snapshot.Outputs)
	}

	if snapshot.Verdict.Verdict != runapi.VerdictChangesRequested || *snapshot.Gaps != "Add the empty cart." {
		t.Errorf("verdict = %+v", snapshot.Verdict)
	}

	// A caller prints the whole work item, ticket and notes included, from Raw.
	if string(snapshot.Raw) != snapshotExample {
		t.Errorf("Raw = %s", snapshot.Raw)
	}
}

func TestWorkItemReadsAWorkItemInNoStage(t *testing.T) {
	snapshot, err := serve(t, respond(http.StatusOK, noStageExample)).WorkItem(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if snapshot.Stage.ID != nil || snapshot.Stage.Name != nil || snapshot.Run.Workflow != nil ||
		snapshot.Verdict != nil || snapshot.Gaps != nil {
		t.Errorf("snapshot = %+v", snapshot)
	}
}

func TestArtifactsReadsTheIndex(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/runs/run-1/artifacts" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}

		respond(http.StatusOK, indexExample)(w, r)
	})

	index, err := client.Artifacts(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	artifact := index.Artifacts[0]
	if artifact.Type != "demo-video" || *artifact.ApprovedVersion != "av_1" || artifact.NewestVersion != 2 ||
		!artifact.ChangedAfterApproval {
		t.Errorf("artifact = %+v", artifact)
	}

	version := artifact.Versions[0]
	if version.Writer.Kind != "run" || version.Writer.RunID != "run-1" || version.Note != nil ||
		version.Files[0].Size != 1048576 || version.Approvals[0].By.Name != "dana" {
		t.Errorf("version = %+v", version)
	}

	if string(index.Raw) != indexExample {
		t.Errorf("Raw = %s", index.Raw)
	}
}

func file(name string, content []byte) runapi.File {
	sum := sha256.Sum256(content)

	return runapi.File{Filename: name, Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}
}

func TestDownloadEscapesTheNameAndChecksTheBytes(t *testing.T) {
	content := []byte("the bytes")
	name := "a b?c#d%e é.png"

	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		want := "/v1/runs/run-1/artifacts/av_1/files/a%20b%3Fc%23d%25e%20%C3%A9.png"
		if r.RequestURI != want {
			t.Errorf("RequestURI = %q, want %q", r.RequestURI, want)
		}

		_, _ = w.Write(content)
	})

	var out bytes.Buffer
	if err := client.Download(context.Background(), "av_1", file(name, content), &out); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(out.Bytes(), content) {
		t.Errorf("bytes = %q", out.Bytes())
	}
}

func TestDownloadSendsNothingButOnePathSegment(t *testing.T) {
	client := serve(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("request sent: %s", r.RequestURI)
	})

	for _, c := range []struct{ version, name string }{
		{"", "a.png"}, {".", "a.png"}, {"..", "a.png"}, {"a/b", "a.png"},
		{"av_1", ""}, {"av_1", "."}, {"av_1", ".."}, {"av_1", "../a.png"}, {"av_1", `a\b.png`}, {"av_1", "a\x00.png"},
		{"av_1", "a\n.png"}, {"av_1", "a\x7f.png"},
	} {
		err := client.Download(context.Background(), c.version, runapi.File{Filename: c.name}, io.Discard)
		if err == nil || !strings.HasPrefix(err.Error(), "Pull the file by a version id") {
			t.Errorf("Download(%q, %q) = %v", c.version, c.name, err)
		}
	}
}

func TestDownloadFailsOnBytesThatDoNotMatchTheIndex(t *testing.T) {
	content := []byte("the bytes")

	for name, entry := range map[string]runapi.File{
		"shorter in the index": {Filename: "a", Size: 3, SHA256: file("a", content).SHA256},
		"longer in the index":  {Filename: "a", Size: 90, SHA256: file("a", content).SHA256},
		"another digest":       {Filename: "a", Size: int64(len(content)), SHA256: strings.Repeat("0", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			client := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(content) })

			err := client.Download(context.Background(), "av_1", entry, io.Discard)
			if err == nil || !strings.HasPrefix(err.Error(), "Pull a again.") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

// failingWriter is a destination that is full.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("no space left on device") }

func TestDownloadReportsAFailedWriteApartFromTheResponse(t *testing.T) {
	content := []byte("the bytes")
	client := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(content) })

	err := client.Download(context.Background(), "av_1", file("a", content), failingWriter{})
	if err == nil || !strings.HasPrefix(err.Error(), "Check where a is written, then pull it again.") ||
		!strings.Contains(err.Error(), "no space left on device") {
		t.Errorf("err = %v", err)
	}
}

// The gateway ends the connection when a file fails its check against Core's digest.
func TestDownloadFailsOnAConnectionThatEndsEarly(t *testing.T) {
	content := []byte("the bytes")

	client := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(content[:4])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	})

	err := client.Download(context.Background(), "av_1", file("a", content), io.Discard)
	if err == nil || !strings.HasPrefix(err.Error(), "Pull a again.") || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("err = %v", err)
	}
}

func TestDownloadReadsNoMoreThanTheIndexLists(t *testing.T) {
	entry := runapi.File{Filename: "a", Size: 3, SHA256: strings.Repeat("0", 64)}
	client := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(make([]byte, 1<<20)) })

	var out bytes.Buffer
	if err := client.Download(context.Background(), "av_1", entry, &out); err == nil {
		t.Fatal("no error")
	}

	if out.Len() > 4 {
		t.Errorf("wrote %d bytes", out.Len())
	}
}

func TestDownloadReportsAnErrorStatus(t *testing.T) {
	client := serve(t, respond(http.StatusNotFound, `{"error":"not_found"}`))

	err := client.Download(context.Background(), "av_1", file("a", nil), io.Discard)
	if !runapi.HasCode(err, runapi.CodeNotFound) {
		t.Fatalf("err = %v", err)
	}

	want := "Pull the artifact index again, and use a version id and a filename from it. " +
		"GET /artifacts/av_1/files/a returned 404 not_found."
	if err.Error() != want {
		t.Errorf("err = %q", err)
	}
}

func TestPushArtifactSendsTheBodyTheRunAPIReads(t *testing.T) {
	content := []byte{0xff, 0x00, 0xfe}

	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/runs/run-1/artifacts" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}

		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}

		raw, _ := io.ReadAll(r.Body)

		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}

		want := fmt.Sprintf(`{"type":"screenshot","name":"checkout","note":"After the fix.",`+
			`"files":[{"filename":"checkout.png","content_type":"image/png","content_base64":%q}]}`,
			base64.StdEncoding.EncodeToString(content))
		if string(raw) != want {
			t.Errorf("body = %s", raw)
		}

		respond(http.StatusCreated, pushedExample)(w, r)
	})

	pushed, err := client.PushArtifact(context.Background(), runapi.Push{
		Type: "screenshot", Name: "checkout", Note: "After the fix.",
		Files: []runapi.PushFile{{Filename: "checkout.png", ContentType: "image/png", Content: content}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if pushed.Type != "screenshot" || pushed.Name != "checkout" || pushed.Version.ID != "av_3" || pushed.Version.Number != 3 {
		t.Errorf("pushed = %+v", pushed)
	}
}

func TestPushArtifactLeavesOutAnEmptyNameAndNote(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)

		// A nil Content is an empty file: the run API returns 400 for null.
		want := `{"type":"screenshot","files":[{"filename":"a.png","content_type":"image/png","content_base64":""}]}`
		if string(raw) != want {
			t.Errorf("body = %s", raw)
		}

		respond(http.StatusCreated, pushedExample)(w, r)
	})

	_, err := client.PushArtifact(context.Background(), runapi.Push{
		Type: "screenshot", Files: []runapi.PushFile{{Filename: "a.png", ContentType: "image/png"}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPushArtifactSendsNothingOverTheLimit(t *testing.T) {
	client := serve(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("request sent: %s", r.RequestURI)
	})

	for name, content := range map[string][]byte{
		// Base64 makes 3 bytes 4: the content alone passes the limit.
		"files": make([]byte, runapi.MaxBody/4*3+3),
		// The encoded files fit, and the members around them pass the limit.
		"whole request": make([]byte, runapi.MaxBody/4*3),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := client.PushArtifact(context.Background(), runapi.Push{
				Type:  "demo-video",
				Files: []runapi.PushFile{{Filename: "a.mp4", ContentType: "video/mp4", Content: content}},
			})
			if !runapi.HasCode(err, runapi.CodePayloadTooLarge) {
				t.Fatalf("err = %v", err)
			}

			if !strings.HasPrefix(err.Error(), "Shrink the files of this version of demo-video") {
				t.Errorf("err = %q", err)
			}
		})
	}
}

func TestPushArtifactSendsNoMoreFilesThanTheRunAPITakes(t *testing.T) {
	client := serve(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("request sent: %s", r.RequestURI)
	})

	_, err := client.PushArtifact(context.Background(), runapi.Push{
		Type: "screenshot", Files: make([]runapi.PushFile, runapi.MaxFiles+1),
	})
	if !runapi.HasCode(err, runapi.CodeTooManyFiles) {
		t.Fatalf("err = %v", err)
	}

	if want := "Push at most 100 files in one version of screenshot. This version has 101."; err.Error() != want {
		t.Errorf("err = %q", err)
	}
}

func TestAnUnavailablePushSaysToReadTheIndexFirst(t *testing.T) {
	client := serve(t, respond(http.StatusServiceUnavailable, `{"error":"unavailable"}`))

	_, err := client.PushVerdict(context.Background(), runapi.VerdictPassed, "")

	want := "Read the artifact index, and try again if the version is not there. " +
		"POST /verdict may have stored it (503 unavailable)."
	if err == nil || err.Error() != want {
		t.Errorf("err = %v", err)
	}
}

func TestPushVerdictSendsTheBodyTheRunAPIReads(t *testing.T) {
	for _, c := range []struct{ gaps, want string }{
		{"Add the empty cart.", `{"verdict":"changes-requested","gaps":"Add the empty cart."}`},
		{"", `{"verdict":"changes-requested"}`},
		// Escaped, 20000 of these would pass the limit of the body.
		{"a < b & c", `{"verdict":"changes-requested","gaps":"a < b & c"}`},
	} {
		client := serve(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/runs/run-1/verdict" {
				t.Errorf("request = %s %s", r.Method, r.URL.Path)
			}

			if raw, _ := io.ReadAll(r.Body); string(raw) != c.want {
				t.Errorf("body = %s", raw)
			}

			respond(http.StatusCreated,
				`{"version": 1, "artifact": {"type": "verdict", "name": "main", "version": {"id": "av_4", "number": 4}}}`)(w, r)
		})

		pushed, err := client.PushVerdict(context.Background(), runapi.VerdictChangesRequested, c.gaps)
		if err != nil {
			t.Fatal(err)
		}

		if pushed.Type != "verdict" || pushed.Version.Number != 4 {
			t.Errorf("pushed = %+v", pushed)
		}
	}
}

func TestErrorsStartWithTheFix(t *testing.T) {
	const get = "GET /work-item"

	for _, c := range []struct {
		name   string
		status int
		header string
		body   string
		code   string
		want   string
	}{
		{
			"a message of the run API", http.StatusUnprocessableEntity, "",
			`{"error":"unknown_type","type":"branch","message":"Define the artifact type branch in the workspace's Artifact types, then run the round again."}`,
			runapi.CodeUnknownType,
			"Define the artifact type branch in the workspace's Artifact types, then run the round again.",
		},
		{
			"invalid request", http.StatusBadRequest, "", `{"error":"invalid_request","names":["files[0].filename"]}`,
			runapi.CodeInvalidRequest, "Fix files[0].filename in the request. GET /work-item returned 400 invalid_request.",
		},
		{
			"unauthorized", http.StatusUnauthorized, "", `{"error":"unauthorized"}`, runapi.CodeUnauthorized,
			"Check the access key of the run's gateway in Qory Apiary, and the clock of the gateway's host. " +
				"GET /work-item did not verify (401 unauthorized).",
		},
		{
			"an unknown run", http.StatusNotFound, "", `{"error":"not_found"}`, runapi.CodeNotFound,
			"Check the run id in the run API's base URL. GET /work-item returned 404 not_found: the run is unknown, " +
				"goes through another gateway, or has no work item.",
		},
		{
			"the error page", http.StatusNotFound, "", `<html>Not Found</html>`, "",
			"Turn on the feature workflow_model in Qory Apiary, and check that the run's gateway forwards the run API. " +
				"GET /work-item returned 404 with no error code.",
		},
		{
			"inputs", http.StatusConflict, "", `{"error":"inputs_invalid"}`, runapi.CodeInputsInvalid,
			"Fix the ticket or the work item. One of its values breaks the rules for a run's inputs " +
				"(GET /work-item returned 409 inputs_invalid).",
		},
		{
			"ticket", http.StatusGone, "", `{"error":"ticket_gone"}`, runapi.CodeTicketGone,
			"Take the ticket in again. The ticket of this work item is no longer in Qory Apiary " +
				"(GET /work-item returned 410 ticket_gone).",
		},
		{
			"too large", http.StatusRequestEntityTooLarge, "", `{"error":"payload_too_large"}`, runapi.CodePayloadTooLarge,
			"Shrink the request to at most 33554432 bytes encoded. GET /work-item returned 413 payload_too_large.",
		},
		{
			"rate limit", http.StatusTooManyRequests, "12", `{"error":"rate_limited"}`, runapi.CodeRateLimited,
			"Try again in 12 seconds. The gateway's access key is over its rate limit (GET /work-item returned 429 rate_limited).",
		},
		{
			"rate limit of one second", http.StatusTooManyRequests, "1", `{"error":"rate_limited"}`, runapi.CodeRateLimited,
			"Try again in 1 second. The gateway's access key is over its rate limit (GET /work-item returned 429 rate_limited).",
		},
		{
			"rate limit with no wait", http.StatusTooManyRequests, "", `{"error":"rate_limited"}`, runapi.CodeRateLimited,
			"Try again in a minute. The gateway's access key is over its rate limit (GET /work-item returned 429 rate_limited).",
		},
		{
			"an answer of the gateway's route", http.StatusBadGateway, "",
			`{"error":"answer_unverified","message":"Check the access key of the run's gateway."}`,
			runapi.CodeAnswerUnverified, "Check the access key of the run's gateway.",
		},
		{
			"unavailable", http.StatusServiceUnavailable, "", `{"error":"unavailable"}`, runapi.CodeUnavailable,
			"Try again. Qory Apiary could not complete GET /work-item (503 unavailable).",
		},
		{
			"another code", http.StatusTeapot, "", `{"error":"teapot"}`, "teapot",
			"Check the logs of the run's gateway. GET /work-item returned 418 teapot.",
		},
		{
			"no JSON", http.StatusBadGateway, "", `bad gateway`, "",
			"Check the logs of the run's gateway. GET /work-item returned 502.",
		},
		{
			"another success", http.StatusNoContent, "", ``, "",
			"Check the logs of the run's gateway. GET /work-item returned 204.",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			client := serve(t, func(w http.ResponseWriter, r *http.Request) {
				if c.header != "" {
					w.Header().Set("Retry-After", c.header)
				}

				respond(c.status, c.body)(w, r)
			})

			_, err := client.WorkItem(context.Background())

			var apiError *runapi.Error
			if !errors.As(err, &apiError) {
				t.Fatalf("err = %v", err)
			}

			if apiError.Status != c.status || apiError.Code != c.code || apiError.Request != get {
				t.Errorf("error = %+v", apiError)
			}

			if err.Error() != c.want {
				t.Errorf("err = %q\nwant  %q", err, c.want)
			}
		})
	}
}

func TestATooLargeVerdictHasTheVerdictLimit(t *testing.T) {
	client := serve(t, respond(http.StatusRequestEntityTooLarge, `{"error":"payload_too_large"}`))

	_, err := client.PushVerdict(context.Background(), runapi.VerdictPassed, "")

	want := "Shrink the request to at most 65536 bytes encoded. POST /verdict returned 413 payload_too_large."
	if err == nil || err.Error() != want {
		t.Errorf("err = %v", err)
	}
}

func TestAPushErrorHasItsMembers(t *testing.T) {
	client := serve(t, respond(http.StatusUnprocessableEntity,
		`{"error":"too_large","type":"demo-video","limit":209715200,"message":"Shrink the files of one version of demo-video to at most 209715200 bytes together."}`))

	_, err := client.PushArtifact(context.Background(), runapi.Push{Type: "demo-video"})

	var apiError *runapi.Error
	if !errors.As(err, &apiError) {
		t.Fatalf("err = %v", err)
	}

	if apiError.Type != "demo-video" || apiError.Limit != 209715200 || apiError.Code != runapi.CodeTooLarge {
		t.Errorf("error = %+v", apiError)
	}
}

func TestResponsesTheClientCannotRead(t *testing.T) {
	for name, c := range map[string]struct {
		body string
		want string
	}{
		"no JSON":         {`<html>`, "Check the run's gateway. The response to GET /work-item is not the run API's JSON"},
		"another shape":   {`{"version":1,"inputs":"none"}`, "Check the run's gateway. The response to GET /work-item is not the run API's JSON"},
		"newer version":   {`{"version":2}`, "Update qory. This qory reads version 1 of the run API, and GET /work-item returned version 2."},
		"no version":      {`{}`, "Check the run's gateway. The response to GET /work-item has no version."},
		"a long response": {`{"version":1,"x":"` + strings.Repeat("a", 16<<20) + `"}`, "Check the run's gateway. The response to GET /work-item is over"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := serve(t, respond(http.StatusOK, c.body)).WorkItem(context.Background())
			if err == nil || !strings.HasPrefix(err.Error(), c.want) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestAGatewayThatCannotBeReachedIsReported(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	base := server.URL + "/v1/runs/run-1"
	server.Close()

	client, err := runapi.New(base, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.WorkItem(context.Background())

	want := "Check that the run's gateway is reachable at " + base + ". GET /work-item failed: "
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("err = %v", err)
	}
}

func TestACancelledContextIsNotAGatewayError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := serve(t, respond(http.StatusOK, snapshotExample)).WorkItem(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}

	if !strings.HasPrefix(err.Error(), "Run the command again.") {
		t.Errorf("err = %q", err)
	}
}
