package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/suite"
)

// The paths of the suite requests: POST suitesPath starts a suite, and GET suitesPath/<id>
// reads one.
const suitesPath = "/v1/suites"

// The codes of the answers of the suite paths.
const (
	routeMethod          = "method_not_allowed"
	routeBadBody         = "bad_request"
	routeFieldNotAllowed = "field_not_allowed"
	routeCommitMissing   = "commit_missing"
	routeBadCommit       = "bad_commit"
	routeCommitNotTaken  = "commit_not_taken"
	routeUnknownSuite    = "unknown_suite"
	routeNoBuildPlan     = "no_build_plan"
	routeNoFullCommand   = "no_full_command"
	routeRunEnding       = "run_ending"
	routeSuiteFolder     = "suite_unavailable"
)

// The texts of the answers of the suite paths. Each starts with its fix, in a first line of at
// most 100 characters; a name that the request sent is cut to [suiteNameMax] characters.
const (
	textRouteMethod         = "Use POST on /v1/suites and GET on /v1/suites/<id>: this path takes no other method."
	textRouteNotFoundSuites = "Check the request's path: qory serves only /v1/suites and /v1/suites/<id>."
	textRouteBadBody        = "Send a JSON object with the field commit as the body: qory could not read the body as one."
	textRouteField          = "Send only commit in the suite request: field %s is not allowed."
	textRouteCommitMissing  = "Send the field commit in the suite request: it has none."
	textRouteBadCommit      = "Send commit as the 40 or 64 lower-case hex digits of a commit id: the commit is not one."
	textRouteCommitNotTaken = "Send a commit of the session's checkout: qory could not take that commit from it."
	textRouteCloneTime      = "Send the request again: qory ran out of time taking that commit from the session's checkout."
	textRouteUnknownSuite   = "Use the suite id that POST /v1/suites returned: %s is unknown."
	textRouteNoBuildPlan    = "Push a build plan before the next run: this run started with none, so qory has no suite command."
	textRouteNoFullCommand  = "Add a full row with a command to Commands in the build plan: the plan of this run has none."
	textRouteRunEnding      = "Stop sending requests: the run is ending, and qory starts no suite."
	textRouteSuiteFolder    = "Start the run again: qory could not make the folder that the suites of the run need."
)

// suiteNameMax is how many characters of a name that a request sent a text shows: with the words
// around it, the first line stays within 100 characters.
const suiteNameMax = 36

// errSuiteFolder is why a run has no suite: qory could not make the folder that holds the clones.
var errSuiteFolder = errors.New("the folder of the suites was not made")

// errNoBuildPlan is why a run has no suite command: the run started with no build plan, or qory
// could not read it.
var errNoBuildPlan = errors.New("the run started with no build plan")

// suiteHost serves the suite paths of a walled run's route: the registry that runs the suites,
// or the reason that none can start. said prints a line of qory run.
type suiteHost struct {
	reg *suite.Registry
	// none is why reg is nil: [errNoBuildPlan], or an error of [suite.FullRow].
	none error
	said func(string)
	// saidNone is whether the line about none was printed: it is printed once.
	saidNone sync.Once
}

// serve answers a request of the route that passed the session's checks.
func (h *suiteHost) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch id, one := strings.CutPrefix(path, suitesPath+"/"); {
	case path == suitesPath:
		if r.Method != http.MethodPost {
			methodRefused(w)
			return
		}
		h.start(w, r)
	case one && id != "" && !strings.Contains(id, "/"):
		if r.Method != http.MethodGet {
			methodRefused(w)
			return
		}
		h.get(w, id)
	default:
		routeAnswer(w, http.StatusNotFound, routeNotFound, textRouteNotFoundSuites)
	}
}

// methodRefused answers a request of a method that its path does not take.
func methodRefused(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET, POST")
	routeAnswer(w, http.StatusMethodNotAllowed, routeMethod, textRouteMethod)
}

// start answers POST /v1/suites: the body is {"commit": <id>} and nothing else.
func (h *suiteHost) start(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil || len(body) > 1<<20 {
		routeAnswer(w, http.StatusBadRequest, routeBadBody, textRouteBadBody)
		return
	}
	commit, answered := suiteRequest(w, body)
	if answered {
		return
	}
	if h.none != nil {
		h.saidNone.Do(func() {
			text := textSuiteNotStarted
			if errors.Is(h.none, errSuiteFolder) {
				text = textSuiteNoFolder
			}
			h.said(fmt.Sprintf(text, shownError(h.none)))
		})
		switch {
		case errors.Is(h.none, errSuiteFolder):
			routeAnswer(w, http.StatusInternalServerError, routeSuiteFolder, textRouteSuiteFolder)
		case errors.Is(h.none, errNoBuildPlan):
			routeAnswer(w, http.StatusConflict, routeNoBuildPlan, textRouteNoBuildPlan)
		default:
			routeAnswer(w, http.StatusConflict, routeNoFullCommand, textRouteNoFullCommand)
		}
		return
	}
	snap, created, err := h.reg.Start(r.Context(), commit)
	switch {
	case errors.Is(err, context.Canceled):
		routeAnswer(w, http.StatusServiceUnavailable, routeRunEnding, textRouteRunEnding)
		return
	case err != nil:
		h.said("the full suite did not start: " + shownError(err))
		text := textRouteCommitNotTaken
		if errors.Is(err, suite.ErrCloneTime) {
			text = textRouteCloneTime
		}
		routeAnswer(w, http.StatusUnprocessableEntity, routeCommitNotTaken, text)
		return
	}
	state, status := string(snap.State), http.StatusOK
	if created {
		state, status = "started", http.StatusCreated
		h.said(fmt.Sprintf("started the full suite %s on %s", snap.ID, shortCommit(snap.Commit)))
	}
	routeJSON(w, status, struct {
		Suite  string `json:"suite"`
		Commit string `json:"commit"`
		State  string `json:"state"`
	}{snap.ID, snap.Commit, state})
}

// suiteRequest reads the body of a suite request and returns its commit. It writes the answer of
// a body that is no suite request and returns answered true. The first member that is not commit,
// in the order of the body, is the field the answer shows.
func suiteRequest(w http.ResponseWriter, body []byte) (commit string, answered bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		routeAnswer(w, http.StatusBadRequest, routeBadBody, textRouteBadBody)
		return "", true
	}
	var value json.RawMessage
	seen := false
	for dec.More() {
		tok, err := dec.Token()
		name, isName := tok.(string)
		if err != nil || !isName {
			routeAnswer(w, http.StatusBadRequest, routeBadBody, textRouteBadBody)
			return "", true
		}
		if name != "commit" {
			routeAnswer(w, http.StatusBadRequest, routeFieldNotAllowed, fmt.Sprintf(textRouteField, shownName(name)))
			return "", true
		}
		if dec.Decode(&value) != nil {
			routeAnswer(w, http.StatusBadRequest, routeBadBody, textRouteBadBody)
			return "", true
		}
		if seen || json.Unmarshal(value, &commit) != nil {
			routeAnswer(w, http.StatusBadRequest, routeBadCommit, textRouteBadCommit)
			return "", true
		}
		seen = true
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') || dec.More() {
		routeAnswer(w, http.StatusBadRequest, routeBadBody, textRouteBadBody)
		return "", true
	}
	switch {
	case !seen:
		routeAnswer(w, http.StatusBadRequest, routeCommitMissing, textRouteCommitMissing)
		return "", true
	case !suite.ValidCommit(commit):
		routeAnswer(w, http.StatusBadRequest, routeBadCommit, textRouteBadCommit)
		return "", true
	}
	return commit, false
}

// get answers GET /v1/suites/<id>.
func (h *suiteHost) get(w http.ResponseWriter, id string) {
	var snap suite.Snapshot
	found := false
	if h.reg != nil {
		snap, found = h.reg.Get(id)
	}
	if !found {
		routeAnswer(w, http.StatusNotFound, routeUnknownSuite, fmt.Sprintf(textRouteUnknownSuite, shownName(id)))
		return
	}
	routeJSON(w, http.StatusOK, suiteBody(snap))
}

// suiteBody is the body of GET /v1/suites/<id>: the suite, its commit and state, the counts or
// null, the minutes, and the last 64 KiB of the output. The file the stage with reviewers
// gets has the same body.
func suiteBody(s suite.Snapshot) any {
	return struct {
		Suite   string        `json:"suite"`
		Commit  string        `json:"commit"`
		State   string        `json:"state"`
		Counts  *suite.Counts `json:"counts"`
		Minutes int           `json:"minutes"`
		Output  string        `json:"output"`
	}{s.ID, s.Commit, string(s.State), s.Counts, s.Minutes, s.Output}
}

// routeJSON writes v as the body of an answer with status.
func routeJSON(w http.ResponseWriter, status int, v any) {
	body, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// shownName is a name that a request sent as a text shows it: printable, and cut to
// [suiteNameMax] characters.
func shownName(name string) string {
	return integration.PrintableWithin(name, suiteNameMax-3)
}

// shortCommit is the first 12 characters of commit, as a line shows it.
func shortCommit(commit string) string {
	return commit[:min(12, len(commit))]
}
