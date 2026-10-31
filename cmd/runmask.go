package cmd

import (
	"io"
	"os"
	"strings"

	"github.com/qoryai/forager/gateway"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/secretmask"
	"github.com/qoryai/qory/internal/statusfd"
)

// maxCredentialFile is the most bytes qory run reads of a credential's file for the
// mask list: a token file is far shorter.
const maxCredentialFile = 64 << 10

// maskVariables adds the value of each of env, NAME=value, to the run's mask list: the
// values of --env and wall.env.
func maskVariables(env []string) {
	for _, kv := range env {
		if _, value, ok := strings.Cut(kv, "="); ok {
			secretmask.Default().Add(value)
		}
	}
}

// maskServerSecrets adds to the run's mask list the secrets of the server's variables,
// which qory read when it started, whether or not the run uses them: the access key's
// secret, the workspace's secret and the run key.
func maskServerSecrets() {
	v := config.TakenServerVariables()
	for _, s := range []string{v.AccessKeySecret, v.WorkspaceSecret, v.RunKey} {
		secretmask.Default().Add(strings.TrimSpace(s))
	}
}

// maskCredentials adds to the run's mask list the value of each of creds, the
// credentials of the gateway qory run starts in its process, that qory can read: the
// variable a credential's env is set to, and the file its file is set to, without the
// spaces around it, as the gateway reads them. An adapter's value comes from a program
// the gateway runs, and qory never reads it.
func maskCredentials(creds []gateway.Credential) {
	for _, c := range creds {
		if c.Env != "" {
			if v, ok := os.LookupEnv(c.Env); ok {
				secretmask.Default().Add(strings.TrimSpace(v))
			}
		}
		if c.File != "" {
			if v, ok := credentialFile(c.File); ok {
				secretmask.Default().Add(v)
			}
		}
	}
}

// credentialFile is the text of the credential file path, without the spaces around it,
// and false when it cannot be read. The gateway reports a file it cannot read.
func credentialFile(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxCredentialFile))
	defer clear(b)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// runStreams are the writers of a run's output: qory's lines, masked with the run's
// mask list, and stdout and stderr as qory run got them, for the session, which masks
// its copy of the agent's output with the same list. lines also writes each line to the
// status descriptor. Call flush when the run ends.
type runStreams struct {
	// out and errOut are stdout and stderr, masked, for qory's lines.
	out, errOut *secretmask.Writer
	// lines is errOut, with each line on the status descriptor as well: for qory's
	// lines that no ui prints.
	lines interface {
		io.Writer
		Flush() error
	}
	// rawOut and rawErr are stdout and stderr as qory run got them, for the session.
	rawOut, rawErr io.Writer
}

// newRunStreams returns the streams of a run whose standard output is stdout and whose
// standard error is stderr, masked with [secretmask.Default].
func newRunStreams(stdout, stderr io.Writer) runStreams {
	list := secretmask.Default()
	errOut := list.Writer(stderr)
	return runStreams{out: list.Writer(stdout), errOut: errOut, lines: statusfd.Tee(errOut), rawOut: stdout, rawErr: stderr}
}

// flush writes what each writer kept back.
func (s runStreams) flush() {
	s.lines.Flush()
	s.out.Flush()
	s.errOut.Flush()
}

// session returns the streams of the run's session: stdout and stderr as they are.
// Forager's session masks its copy of the agent's output and its setup lines with the
// run's masker, [secretmask.Set.Masker], on pipes and on a terminal, with a writer for
// each stream. A second pass would replace text inside a placeholder, such as a value
// inside qory-proxy-password, so qory's writers do not mask the session's output again.
func (s runStreams) session() (stdout, stderr io.Writer) {
	return s.rawOut, s.rawErr
}

// commandStreams are the writers of a command that talks to the server or a gateway and
// runs no session: qory run resend, qory gateway, qory run register and qory run end.
// Each prints every line, Forager's lines among them, through out and errOut. Forager
// does not mask those lines with this list, so qory masks each line as it is written; a
// line Forager masked keeps its ***. Call flush when the command ends.
type commandStreams struct {
	// out and errOut are stdout and stderr, masked with the command's mask list.
	out, errOut *secretmask.Writer
}

// newCommandStreams starts the command's mask list empty, adds the secrets of the
// server's variables to it ([maskServerSecrets]), and returns stdout and stderr masked
// with it, as qory run masks its lines. The command adds each other secret value to
// [secretmask.Default] as it reads the value.
func newCommandStreams(stdout, stderr io.Writer) commandStreams {
	secretmask.SetDefault(secretmask.New())
	maskServerSecrets()
	list := secretmask.Default()
	return commandStreams{out: list.Writer(stdout), errOut: list.Writer(stderr)}
}

// flush writes what each writer kept back.
func (s commandStreams) flush() {
	s.out.Flush()
	s.errOut.Flush()
}
