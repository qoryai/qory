package cmd

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/proxygit"
	"github.com/qoryai/qory/internal/source"
)

// runSources is the folder of the run directory that the compose of a run from a
// control plane fetches its git sources into: the run's cache, which starts empty, so
// every source is fetched through this run's host proxy, under its policy and
// credentials.
const runSources = "sources"

// runLineError is an error qory run prints as one line, "qory run: <text>", with no
// title and no mark before it. Its text has no prefix; it unwraps to the error it
// carries, so [ExitCode] reads that error's status.
type runLineError struct{ err error }

func (e *runLineError) Error() string {
	return strings.TrimPrefix(e.err.Error(), "qory run: ")
}

func (e *runLineError) Unwrap() error { return e.err }

// textNoHostProxy is the line of a run whose host proxy did not open, with Forager's
// error after it.
const textNoHostProxy = "run again: the run's host proxy, through which qory fetches the run's code, did not open: %s"

// noHostProxy is [textNoHostProxy] for err, HostProxy's error, made printable with every
// URL's password and every secret hidden.
func noHostProxy(err error) error {
	return fmt.Errorf(textNoHostProxy, integration.Printable(exports.HideURLError(err).Error()))
}

// runValues is getenv for [preparation.Run]: the server's values of the run's system and
// repositories, as the run answer gave them, and "" for any other name.
func runValues(run openedRun) func(string) string {
	return func(name string) string {
		switch name {
		case preparation.EnvSystem:
			return run.System()
		case preparation.EnvRepositories:
			return run.Repositories()
		}
		return ""
	}
}

// proxyGit is how the compose of a run from a control plane fetches its git sources:
// through the run's host proxy h, with a line on stderr for each URL and ref it
// fetched, its commit and how long the fetch took.
func proxyGit(h hostProxy, stderr io.Writer) *source.Git {
	return &source.Git{Proxy: h.proxy, Fetched: func(src, ref, commit string, took time.Duration) {
		if len(commit) > 12 {
			commit = commit[:12]
		}
		fmt.Fprintf(stderr, "qory run: fetched %s#%s at %s in %.1fs\n", exports.URL(src), exports.Value(ref, false), commit, took.Seconds())
	}}
}

// proxyComposeError is err, a compose's error in a run from a control plane, as the line
// qory run prints: a git source that is not https, as [source.HTTPSFix] words it, and a
// ref the remote does not have, each an input error; a git source that could not be
// fetched, a failure of the fetch; anything else as it is. p is the run's host proxy,
// whose password no line contains.
func proxyComposeError(err error, p proxygit.Proxy) error {
	var scheme *source.SchemeError
	var gone *source.GoneError
	var fetch *source.FetchError
	switch {
	case errors.As(err, &scheme):
		return &runLineError{input(scheme)}
	case errors.As(err, &gone):
		cause := fmt.Errorf("%s has no branch or tag %s", exports.URL(gone.URL), exports.Value(gone.Ref, false))
		return &runLineError{input(errors.New(checkSource(gone.URL, gone.Ref, cause)))}
	case errors.As(err, &fetch):
		line := firstGitLine(fetch.Output)
		if line == "" && fetch.Err != nil {
			line = fetch.Err.Error()
		}
		return &runLineError{errors.New(checkSource(fetch.URL, fetch.Ref, errors.New(proxygit.Hide(line, &p))))}
	}
	return err
}

// checkSource is the line for a git source, src at ref, that the fetch through the run's
// gateway could not fetch, with cause, git's line or what the remote lacks, made
// printable with every URL's password and every secret hidden.
func checkSource(src, ref string, cause error) string {
	host := "its host"
	if u, err := url.Parse(src); err == nil && u.Hostname() != "" {
		host = integration.Printable(exports.Value(u.Hostname(), false))
	}
	return fmt.Sprintf("check that %s has the branch, tag or commit %s, that the run's policy allows %s and, for a private repository, that the run configuration has a credential that reads it. The fetch failed: %s",
		exports.URL(src), exports.Value(ref, false), host, integration.Printable(exports.HideURLError(cause).Error()))
}

// firstGitLine is the first line of git's output with text in it, trimmed.
func firstGitLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
