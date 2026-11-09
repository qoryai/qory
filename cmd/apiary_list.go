package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/integrations/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/ui"
)

// runListCommand is the command line of qory apiary run list, as its lines print it.
const runListCommand = "qory apiary run list"

// runStates are the states a run list selects by, and runStatesText the same list as the
// help and the errors print it.
var runStates = []string{"pending", "running", "completed", "failed", "cancelled", "lost"}

const runStatesText = "pending, running, completed, failed, cancelled or lost"

// The forms of the values of qory apiary run list, as runs.schema.json states them.
var (
	workspaceShape = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?/[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)
	workItemShape  = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	workflowShape  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,54}$`)
	limitShape     = regexp.MustCompile(`^[1-9][0-9]{0,2}$`)
)

// runListFlag is one flag of qory apiary run list: its name, the query parameter it sets
// and the value qory sends, after the check.
type runListFlag struct {
	name, parameter, value string
}

// runListFlags are the flags of qory apiary run list as given on the command line.
type runListFlags struct {
	workspace, workItem, workflow, state, since, limit, before string
	json                                                       bool
	// server is --server as given, and serverGiven whether it was given.
	server      string
	serverGiven bool
}

// newApiaryRunList builds qory apiary run list.
func newApiaryRunList() *cobra.Command {
	var f runListFlags
	c := &cobra.Command{
		Use:   "list",
		Short: "List the runs on a Qory Apiary server, the last created first",
		Long: `List the runs on a Qory Apiary server, the last created first, one line per run:
its id, its number, the workspace, the workflow, the work item's number, the state, when
it started and how long it ran. A - stands for a value the run does not have.

Each flag selects runs, and a run is listed when it matches every flag given. qory
checks each value before it sends the request. --since takes a duration, such as 1h,
which qory counts back from now, or a time, such as 2026-11-01T09:00:00Z.

When more runs match, the last line, on stderr, is the command that lists the next
page. Pass the id of a failed run to qory apiary run diagnose.

qory signs the request with the diagnostics key in ~/.config/qory/apiary/<host>, and
prints the list only after its signature verifies under the server key pinned there.
--json prints the list as Qory Apiary sent it, so a pipe or a file receives JSON alone.

--server selects the server by its URL. Without it, qory takes the one server it has a
diagnostics key for.

--verbose adds nothing here.

More: ` + apiaryDocs,
		Example: `  qory apiary run list
  qory apiary run list --state failed --since 24h
  qory apiary run list --workspace acme/web --work-item 7
  qory apiary run list --server https://apiary.example
  qory apiary run list --json > runs.json`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			f.serverGiven = cmd.Flags().Changed("server")
			return listRuns(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd, f)
		},
	}
	c.Flags().StringVar(&f.workspace, "workspace", "", "list the runs of one workspace, as `<org>/<workspace>`, such as acme/web")
	c.Flags().StringVar(&f.workItem, "work-item", "", "list the runs of the work item `<number>` in the workspace; needs --workspace")
	c.Flags().StringVar(&f.workflow, "workflow", "", "list the runs that start the workflow `<name>`, such as hello")
	c.Flags().StringVar(&f.state, "state", "", "list the runs in `<state>`: "+runStatesText)
	c.Flags().StringVar(&f.since, "since", "", "list the runs created at or after `<time>`: a duration back from now, such as 1h, or an RFC 3339 time")
	c.Flags().StringVar(&f.limit, "limit", "", "list at most `<number>` runs, from 1 to 100 (default 25)")
	c.Flags().StringVar(&f.before, "before", "", "list the runs after the run `<run id>`, for the next page")
	c.Flags().BoolVar(&f.json, "json", false, "print the list as Qory Apiary sent it, one JSON object")
	c.Flags().StringVar(&f.server, "server", "", serverUsage)
	return c
}

// listRuns checks the flags, sends the signed GET /v1/runs and prints the verified list
// to out: one line per run, or with --json the body as it came. The line for the next
// page and the line for an empty list go to report.
func listRuns(ctx context.Context, out, report io.Writer, cmd *cobra.Command, f runListFlags) error {
	given, err := checkRunListFlags(cmd, f, diagnosticsClock())
	if err != nil {
		return err
	}
	key, instance, err := diagnosticsKey(report, runListCommand, f.server, f.serverGiven)
	if err != nil {
		return err
	}
	q := url.Values{}
	for _, g := range given {
		q.Set(g.parameter, g.value)
	}
	if q.Get(contracts.RunListQueryLimit) == "" {
		q.Set(contracts.RunListQueryLimit, strconv.Itoa(contracts.RunListDefaultLimit))
	}
	status, b, err := signedGet(ctx, key, instance, "/v1/runs?"+q.Encode())
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return runListNotAccepted(key.Host, given, status, b)
	}
	list, err := readRunList(b)
	if err != nil {
		return fmt.Errorf("Check the run list with the instance admin of %s: the response does not follow runs.schema.json, so qory printed nothing.", key.Host)
	}
	if f.json {
		return writeLine(out, b)
	}
	if len(list.Runs) == 0 {
		fmt.Fprintln(report, noRunMatches(key.Host, given))
		return nil
	}
	u := ui.New(out)
	u.Title(key.Host, "runs")
	rows := make([][]string, 0, len(list.Runs))
	for _, r := range list.Runs {
		rows = append(rows, runListLine(r))
	}
	u.Table(rows)
	if list.Next != nil {
		fmt.Fprintln(report, "Next page: "+nextPageCommand(f, given, *list.Next))
	}
	return nil
}

// checkRunListFlags checks each flag cmd was given, in the order of the flags, and
// returns them with the value qory sends: --since as a time in UTC, a duration counted
// back from now. A wrong value is an input error whose first line is the fix.
func checkRunListFlags(cmd *cobra.Command, f runListFlags, now time.Time) ([]runListFlag, error) {
	var given []runListFlag
	add := func(name, parameter, value string, ok bool, fix string) error {
		if !cmd.Flags().Changed(name) {
			return nil
		}
		if !ok {
			return input(fmt.Errorf("%s\nYou passed %s.", fix, exports.Value(value, true)))
		}
		given = append(given, runListFlag{name: name, parameter: parameter, value: value})
		return nil
	}
	since, sinceOK := sinceTime(f.since, now)
	limit, _ := strconv.Atoi(f.limit)
	for _, err := range []error{
		add("workspace", contracts.RunListQueryWorkspace, f.workspace, workspaceShape.MatchString(f.workspace),
			"Pass --workspace as <org>/<workspace>, such as acme/web: two slugs of a-z, 0-9 and -."),
		add("work-item", contracts.RunListQueryWorkItem, f.workItem, workItemNumber(f.workItem),
			"Pass --work-item as the work item's number in the workspace, such as 7."),
		add("workflow", contracts.RunListQueryWorkflow, f.workflow, workflowShape.MatchString(f.workflow),
			"Pass --workflow as a workflow's name, such as hello: a-z, then a-z, 0-9 and -."),
		add("state", contracts.RunListQueryState, f.state, slices.Contains(runStates, f.state),
			"Pass --state as one of "+runStatesText+"."),
		add("since", contracts.RunListQuerySince, f.since, sinceOK,
			"Pass --since as a duration, such as 1h, or a time, such as 2026-11-01T09:00:00Z."),
		add("limit", contracts.RunListQueryLimit, f.limit, limitShape.MatchString(f.limit) && limit <= contracts.RunListMaxLimit,
			"Pass --limit as a number from 1 to 100."),
		add("before", contracts.RunListQueryBefore, f.before, runIDShape.MatchString(f.before),
			"Pass --before as a run id, rn_ and 16 characters."),
	} {
		if err != nil {
			return nil, err
		}
	}
	for i := range given {
		if given[i].name == "since" {
			given[i].value = since
		}
	}
	if cmd.Flags().Changed("work-item") && !cmd.Flags().Changed("workspace") {
		return nil, input(errors.New("Add --workspace <org>/<workspace>: a work item's number is unique only in its workspace."))
	}
	return given, nil
}

// workItemNumber reports whether v is a work item's number: decimal digits from 1, with
// no leading zero, at most the largest int64.
func workItemNumber(v string) bool {
	if !workItemShape.MatchString(v) {
		return false
	}
	_, err := strconv.ParseInt(v, 10, 64)
	return err == nil
}

// sinceTime is the value of --since that qory sends: v when it is an RFC 3339 time, in
// UTC, or now less v when v is a positive duration, in UTC to the second. ok is false
// for any other v, and for a time outside the years 1 to 9999 in UTC.
func sinceTime(v string, now time.Time) (since string, ok bool) {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		d, derr := time.ParseDuration(v)
		if derr != nil || d <= 0 {
			return "", false
		}
		t = now.Add(-d).Truncate(time.Second)
	}
	t = t.UTC()
	if t.Year() < 1 || t.Year() > 9999 {
		return "", false
	}
	return t.Format(time.RFC3339Nano), true
}

// runListSchema is runs.schema.json of the diagnostics contract, compiled once.
var runListSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return contracts.CompileDiagnostics("runs.schema.json")
})

// readRunList reads b, the body of a verified 200 to GET /v1/runs, after it passes
// runs.schema.json.
func readRunList(b []byte) (*contracts.RunList, error) {
	s, err := runListSchema()
	if err != nil {
		return nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if err := s.Validate(doc); err != nil {
		return nil, err
	}
	var list contracts.RunList
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	return &list, nil
}

// runListNotAccepted is the error of a verified refusal of GET /v1/runs from the server host,
// with the status status and the body b. For invalid_request, the first line contains
// the flag of the parameter that broke its rule, from given.
func runListNotAccepted(host string, given []runListFlag, status int, b []byte) error {
	ref := accesskey.ReadRefusal(status, b)
	if ref != nil && ref.Code == contracts.RunListInvalidRequest {
		var body contracts.RunListRefusal
		if json.Unmarshal(b, &body) == nil && body.Parameter != nil {
			for _, g := range runListSent(given) {
				if g.parameter == *body.Parameter {
					return fmt.Errorf("Check --%s %s: the request to %s failed with %s (%d) for that value.", g.name, g.value, host, ref.Code, status)
				}
			}
			return fmt.Errorf("Check the run list with the instance admin of %s: the request was not accepted: %s (%d): %s.", host, ref.Code, status, accesskey.ShownName(*body.Parameter, true))
		}
	}
	return notAccepted(host, "the run list", ref, status)
}

// runListSent is given with --limit at its default when it was not given: each flag
// whose parameter qory sends.
func runListSent(given []runListFlag) []runListFlag {
	for _, g := range given {
		if g.name == "limit" {
			return given
		}
	}
	return append(slices.Clone(given), runListFlag{name: "limit", parameter: contracts.RunListQueryLimit, value: strconv.Itoa(contracts.RunListDefaultLimit)})
}

// runListLine is the cells of one run's line: id, number, workspace, workflow, work item,
// state, started and duration, with - for a value the run does not have.
func runListLine(r contracts.RunListRow) []string {
	dash := func(v string) string {
		if v == "" {
			return "-"
		}
		return v
	}
	workItem, duration := "-", "-"
	if r.WorkItemNumber != 0 {
		workItem = strconv.FormatInt(r.WorkItemNumber, 10)
	}
	if r.DurationSeconds != nil {
		duration = shortDuration(*r.DurationSeconds)
	}
	return []string{
		r.ID,
		strconv.FormatInt(r.Number, 10),
		r.Organisation.Slug + "/" + r.Workspace.Slug,
		dash(r.Workflow),
		workItem,
		r.State,
		dash(r.StartedAt),
		duration,
	}
}

// shortDuration is s seconds as a run list shows it: 45s, 4m12s, or 1h02m from one hour
// on, without the seconds.
func shortDuration(s int64) string {
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh%02dm", s/3600, s%3600/60)
}

// nextPageCommand is the command that lists the page after the run next: --server as
// given in f, then every flag in given, --since as the time qory sent, then --before
// next.
func nextPageCommand(f runListFlags, given []runListFlag, next string) string {
	parts := []string{runListCommand}
	if f.serverGiven {
		parts = append(parts, "--server", f.server)
	}
	for _, g := range given {
		if g.name != "before" {
			parts = append(parts, "--"+g.name, g.value)
		}
	}
	return strings.Join(append(parts, "--before", next), " ")
}

// noRunMatches is the line for an empty list from the server host, for the flags given.
func noRunMatches(host string, given []runListFlag) string {
	var filters []string
	for _, g := range given {
		if g.name != "limit" {
			filters = append(filters, "--"+g.name+" "+g.value)
		}
	}
	if len(filters) == 0 {
		return "No action needed: " + host + " has no runs."
	}
	return "Change or remove a flag to list more runs: no run on " + host + " matches " + strings.Join(filters, " ") + "."
}
