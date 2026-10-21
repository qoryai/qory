package prebuild

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
)

// AllowedEnv are the variables of this process's environment that reach the programs
// the build runs, the devcontainer command and docker, and through them any build: the
// PATH they are found on, the folders they keep their files in, how docker reaches its
// engine, and the machine's proxies and certificate authorities, [ProxyEnv] and
// [CAEnv]. DOCKER_CONFIG names the original docker configuration, which the build
// reads, and a program gets the build's in its place, [dockerConfig]. No other
// variable reaches them, so no token or secret of the job does: the devcontainer
// command reads GITHUB_TOKEN and DEVCONTAINERS_OCI_AUTH, and a configuration's
// ${localEnv:...} reads any. BUILDX_BUILDER and BUILDX_CONFIG are left out, and the
// build's docker configuration holds no buildx folder, so docker build uses the
// default builder, which keeps the image it builds on the engine. DOCKER_BUILDKIT is
// left out too: the build sets it to 1, [BuildKit], whatever the job's value.
var AllowedEnv = append([]string{
	"PATH", "HOME", "TMPDIR",
	"DOCKER_HOST", "DOCKER_CONFIG", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH",
}, append(append([]string{}, ProxyEnv...), CAEnv...)...)

// BuildKit is DOCKER_BUILDKIT as every program of the build gets it: docker build
// builds with BuildKit, whose Dockerfile frontend the build checks, whatever the job
// sets.
const BuildKit = "DOCKER_BUILDKIT=1"

// ProxyEnv are the proxy variables, in both cases, as programs read either. Neither
// the devcontainer command nor docker turns one of these variables into a build
// argument or the image's environment, and the lifecycle commands' container gets
// none. docker does turn the proxies of its configuration into both, so the build's
// docker configuration has none: [dockerConfig].
var ProxyEnv = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
	"http_proxy", "https_proxy", "no_proxy", "all_proxy",
}

// CAEnv are the variables that name the certificate authorities a program trusts.
var CAEnv = []string{"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE"}

// allowed is the entries of env [AllowedEnv] names, in the order of env; a variable env
// sets twice keeps its last value, as a process reads it. A proxy variable whose value
// may carry user information, as [carriesUser] has it, holds a secret: it is left out,
// and its name is in withheld, once.
func allowed(env []string) (out, withheld []string) {
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !slices.Contains(AllowedEnv, name) {
			continue
		}
		if slices.Contains(ProxyEnv, name) && carriesUser(value) {
			if !slices.Contains(withheld, name) {
				withheld = append(withheld, name)
			}
			continue
		}
		out = append(out, kv)
	}
	return out, withheld
}

// carriesUser reports whether a proxy value may carry user information, user@ or
// user:password@: whether it holds an @ anywhere. Reading the URL's authority would
// miss what a program still reads as one, such as http:/user:pw@proxy:3128, or a
// password holding a / that is not encoded.
func carriesUser(url string) bool {
	return strings.Contains(url, "@")
}

// grace is how long a program stopped by its context has between SIGTERM and SIGKILL;
// a test shortens it.
var grace = 10 * time.Second

// cleanWait bounds each docker command that removes what the build made, which runs
// after the build's context ended too.
var cleanWait = 2 * time.Minute

// engine runs the programs of a build: each with the environment env alone, in a new
// process group that the context's end stops, and what each writes on its standard
// error to log.
type engine struct {
	env []string
	log io.Writer
}

// failed is a program that did not succeed: its name and first argument, then the
// status it exited with and the last line it wrote on its standard error.
type failed struct {
	what string
	code int
	last string
}

// Error names the program, its status and its last line.
func (f *failed) Error() string {
	if f.last == "" {
		return fmt.Sprintf("%s exited with status %d", f.what, f.code)
	}
	return fmt.Sprintf("%s exited with status %d: %s", f.what, f.code, f.last)
}

// run runs name with args, stdin as its standard input and stdout as its standard
// output. When ctx ends, the program's group gets SIGTERM, and SIGKILL [grace] later;
// run then returns ctx's error.
func (e engine) run(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...)
	c.Env = append([]string{}, e.env...)
	c.Stdin = stdin
	c.Stdout = stdout
	tail := &lastLine{}
	if e.log != nil {
		c.Stderr = io.MultiWriter(e.log, tail)
	} else {
		c.Stderr = tail
	}
	c.WaitDelay = grace
	group(c)
	err := c.Run()
	if ctx.Err() != nil {
		endGroup(c, time.Now().Add(grace))
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	what := name
	if len(args) > 0 {
		what += " " + args[0]
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &failed{what: what, code: ee.ExitCode(), last: tail.String()}
	}
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("no %s command on the PATH", name)
	}
	return fmt.Errorf("%s could not start: %s", what, integration.Printable(exports.HideError(err).Error()))
}

// output runs a program as [engine.run] does and returns its standard output.
func (e engine) output(ctx context.Context, name string, args ...string) ([]byte, error) {
	var out bytes.Buffer
	err := e.run(ctx, nil, &out, name, args...)
	return out.Bytes(), err
}

// clean runs a docker command that removes what the build made, within [cleanWait],
// whether or not ctx has ended. Its failure is left out: there is nothing left to do.
func (e engine) clean(ctx context.Context, args ...string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanWait)
	defer cancel()
	e.log = nil
	_ = e.run(ctx, nil, nil, "docker", args...)
}

// lastLine keeps the last line written to it that holds more than spaces, cut to one
// printable line.
type lastLine struct {
	buf  []byte
	last string
}

// Write keeps p's last complete line, and the rest for the next write.
func (l *lastLine) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimSpace(string(l.buf[:i])); line != "" {
			l.last = line
		}
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > 64<<10 {
		l.buf = l.buf[len(l.buf)-64<<10:]
	}
	return len(p), nil
}

// String is the last line, the rest after the last newline when it holds more than
// spaces.
func (l *lastLine) String() string {
	if line := strings.TrimSpace(string(l.buf)); line != "" {
		return integration.Printable(hidden(line))
	}
	return integration.Printable(hidden(l.last))
}

// hidden is a line a program wrote with a URL's password as (hidden) and each word with
// an access key secret or a run key as Forager's value phrase, as [exports.HideURLs] and
// [exports.HideValues] print them.
func hidden(line string) string {
	return exports.HideValues(exports.HideURLs(line))
}
