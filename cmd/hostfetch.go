package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/proxygit"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
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

// textNoHostProxyFromCode is [textNoHostProxy] for a run from a run code. The code is
// used by then, so the run starts again only from the new run code that taking the work
// item in again makes, as for [textNoStackProxy].
const textNoHostProxyFromCode = "take the work item in again in Qory Apiary: the run's host proxy, through which qory fetches the run's repositories and git sources, did not open: %s" + codeUsed

// noHostProxy is [textNoHostProxy] for err, HostProxy's error, made printable with every
// URL's password and every secret hidden; [textNoHostProxyFromCode] for a run from a run
// code, fromCode.
func noHostProxy(err error, fromCode bool) error {
	text := textNoHostProxy
	if fromCode {
		text = textNoHostProxyFromCode
	}
	return fmt.Errorf(text, integration.Printable(exports.HideURLError(err).Error()))
}

// textNoStackProxy is the line of a run of a named stack whose stack proxy did not open,
// with the stack and Forager's error after it. The run code is used by then, so the run
// starts again only from the new run code that taking the work item in again makes.
const textNoStackProxy = "take the work item in again in Qory Apiary: the run's stack proxy, through which qory fetches %s, did not open: %s" + codeUsed

// noStackProxy is [textNoStackProxy] for n's stack and err, StackProxy's error, made
// printable with every URL's password and every secret hidden.
func noStackProxy(n *source.Named, err error) error {
	return fmt.Errorf(textNoStackProxy, n.Phrase(), integration.Printable(exports.HideURLError(err).Error()))
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
	return &source.Git{Proxy: h.proxy, Fetched: func(src, ref, _, commit string, took time.Duration) {
		if len(commit) > 12 {
			commit = commit[:12]
		}
		fmt.Fprintf(stderr, "qory run: fetched %s#%s at %s in %.1fs\n", exports.URL(src), exports.Value(ref, false), commit, took.Seconds())
	}}
}

// stackGit is how the compose of a named stack, n, fetches the stack and every git
// source it lists: through the run's stack proxy h, into folders named by a hash, with
// a line on stderr for each source it fetched, by the source's name, its commit and how
// long the fetch took; see [source.Named].
func stackGit(h hostProxy, stderr io.Writer, n *source.Named) *source.Git {
	return &source.Git{Proxy: h.proxy, Named: n, Fetched: func(_, _, name, commit string, took time.Duration) {
		if len(commit) > 12 {
			commit = commit[:12]
		}
		fmt.Fprintf(stderr, "qory run: fetched %s at %s in %.1fs\n", name, commit, took.Seconds())
	}}
}

// namedComposeError is err, a compose's error in a run of the named stack n, whose
// address is ref, as the line qory run prints: a git source that is not https and a ref
// a repository does not have, each an input error; a git source that could not be
// fetched, a failure of the fetch, with its cause and no output of git; anything else
// with every address and every path of a source's folder replaced by the source's
// name, as [source.Named.Hide] replaces them. An error of a source with no name, a
// module of the checkout's qory.yaml fetched through the run's host proxy, host, is the
// line [proxyComposeError] makes.
func namedComposeError(err error, n *source.Named, ref stack.Source, host proxygit.Proxy) error {
	var scheme *source.SchemeError
	var gone *source.GoneError
	var fetch *source.FetchError
	if unnamedSource(err) {
		return proxyComposeError(err, host)
	}
	switch {
	case errors.As(err, &scheme):
		return &runLineError{input(n.HideError(scheme))}
	case errors.As(err, &gone) && gone.URL == ref.Git && gone.Ref == ref.Ref:
		return &runLineError{input(fmt.Errorf("set the address of %s in Qory Apiary's list of stacks to a branch, tag or commit its repository has: %w", n.Phrase(), n.HideError(gone)))}
	case errors.As(err, &gone):
		return &runLineError{input(fmt.Errorf("set the ref of %s to a branch, tag or commit its repository has: %w", gone.Name, n.HideError(gone)))}
	case errors.As(err, &fetch):
		where := "its source in the stack's files"
		if fetch.URL == ref.Git {
			where = "its address in Qory Apiary's list of stacks"
		}
		if m := connectStatus.FindStringSubmatch(fetch.Output); m != nil && m[1] == "407" {
			return &runLineError{fmt.Errorf("take the work item in again in Qory Apiary: the run's stack proxy returned 407 Proxy Authentication Required to the password Forager set for the run, so the fetch of %s failed%s", fetch.Name, codeUsed)}
		}
		return &runLineError{fmt.Errorf("add the host of %s to gateway.stack_hosts in forager.yaml, or check %s: the fetch of %s through the run's stack proxy failed: %s", fetch.Name, where, fetch.Name, namedFetchCause(fetch))}
	}
	return n.HideError(err)
}

// unnamedSource reports whether err is the error of a git source resolved with no
// [source.Named]: a source that is not https, a ref the repository does not have, or a
// fetch that failed, with no name.
func unnamedSource(err error) bool {
	var scheme *source.SchemeError
	var gone *source.GoneError
	var fetch *source.FetchError
	switch {
	case errors.As(err, &scheme):
		return scheme.Name == ""
	case errors.As(err, &gone):
		return gone.Name == ""
	case errors.As(err, &fetch):
		return fetch.Name == ""
	}
	return false
}

// connectStatus and httpStatus match the status curl writes, in git's output, for a
// response of the proxy to the tunnel's CONNECT, and for a response of the host;
// gitExit matches the error of a git that exited otherwise than 0.
var (
	connectStatus = regexp.MustCompile(`CONNECT tunnel failed, response (\d{3})`)
	httpStatus    = regexp.MustCompile(`returned error: (\d{3})`)
	gitExit       = regexp.MustCompile(`^exit status (\d+)$`)
)

// namedFetchCause is the cause of fetch, a failed fetch of a named stack's source, in
// words that contain no part of git's output, which can contain the URL: the status the
// stack proxy or the host returned, the time a git ran past, or git's exit status.
func namedFetchCause(fetch *source.FetchError) string {
	text := ""
	if fetch.Err != nil {
		text = fetch.Err.Error()
	}
	if m := connectStatus.FindStringSubmatch(fetch.Output); m != nil {
		if m[1] == "403" {
			return "the stack proxy returned 403, as it does for a host that gateway.stack_hosts does not list"
		}
		return "the stack proxy returned " + m[1]
	}
	if m := httpStatus.FindStringSubmatch(fetch.Output); m != nil {
		return "the repository's host returned " + m[1]
	}
	if strings.HasPrefix(text, "git ") && strings.HasSuffix(text, " and was stopped") {
		// The text qory writes, with git's command and the time limit.
		return text
	}
	if m := gitExit.FindStringSubmatch(text); m != nil {
		return "git exited with status " + m[1] + "; qory prints none of git's output, which can contain the address"
	}
	return "qory prints none of git's output, which can contain the address"
}

// namedLines is the writer of the compose step of a named stack: it writes each whole
// line to w as [source.Named.Hide] prints it, so no line shows an address or a path of a
// source's folder. flush writes a last line without a line break.
type namedLines struct {
	w   io.Writer
	n   *source.Named
	buf []byte
}

// Write keeps p, and writes each line p ends to w, hidden.
func (l *namedLines) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := l.n.Hide(string(l.buf[:i+1]))
		l.buf = l.buf[i+1:]
		if _, err := io.WriteString(l.w, line); err != nil {
			return len(p), err
		}
	}
}

// Terminal is w's terminal, so a [ui.UI] on l colours its text as it would on w.
func (l *namedLines) Terminal() io.Writer {
	if t, ok := l.w.(interface{ Terminal() io.Writer }); ok {
		return t.Terminal()
	}
	return l.w
}

// flush writes a last line without a line break to w, hidden.
func (l *namedLines) flush() {
	if len(l.buf) > 0 {
		io.WriteString(l.w, l.n.Hide(string(l.buf)))
		l.buf = nil
	}
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
