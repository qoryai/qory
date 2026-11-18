package pushcheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/relay"
)

// The limits of the submit program's run.
const (
	// ProgramLimit is how long the submit program may run, from the start of its container.
	ProgramLimit = 120 * time.Second
	// RelayConnections is the most connections that the program's relay takes. The program
	// opens one connection for each call to the forge, and makes at most two calls for each of
	// the 20 repositories it handles.
	RelayConnections = 50
	// maxProgramFile is the most that qory reads of the file that the program leaves.
	maxProgramFile = 4 << 20
)

// The paths inside the submit program's container, and the name of the file in the first.
// ProgramWorkDir is the only folder the program can write; the files of [ProgramNetDir],
// the relay's socket and the CA bundle, are read-only to it.
const (
	ProgramWorkDir = "/qory-submit/work"
	ProgramNetDir  = "/qory-submit/net"
	SubmissionFile = "submission.json"
	caFile         = "ca.pem"
)

// Program is one run of the submit program.
type Program struct {
	// Command is the program's absolute path in the home, then its arguments.
	Command []string
	// Submission is the content of the file that the program reads and rewrites. It holds
	// no token or secret.
	Submission []byte
	// ProxyURL is the push proxy's URL, http://qory:<password>@<host>:<port>, which the
	// relay forwards to and whose password the program sends. CABundle is the path of the
	// bundle that signs the proxy's certificates, "" for none.
	ProxyURL, CABundle string
}

// ProgramEnd is how a run of the submit program ended.
type ProgramEnd struct {
	// Submission is the file as the program left it; nil when the file is not there or is
	// not a regular file that qory reads, with FileErr saying why.
	Submission []byte
	FileErr    string
	// Exit is the program's exit status; TimedOut a program that ran past [ProgramLimit] and
	// was stopped; StartErr the engine's line for a program that did not start, "" for none.
	Exit     int
	TimedOut bool
	StartErr string
	// Stdout and Stderr are what the program printed, cut at 64 KiB each.
	Stdout, Stderr []byte
}

// RunProgram runs the submit program of p in a new container from the wall's default image,
// with no network, as the run's user, with the home read-only at its path. The container
// sees the submission file in a folder it can write, and the relay's socket and the CA bundle
// in a read-only folder. The relay forwards the socket to the push proxy and ends with the
// program's container. It returns how the program ended and the file it left. A failure of
// the engine or of a temporary folder is an error; so is a ctx that ended. The container and
// the folders are removed in every case.
func (c *Checker) RunProgram(ctx context.Context, p Program) (ProgramEnd, error) {
	proxy, err := url.Parse(p.ProxyURL)
	if err != nil || proxy.Host == "" || proxy.User == nil {
		return ProgramEnd{}, errors.New("the push proxy's address is not http://user:password@host:port")
	}
	password, _ := proxy.User.Password()
	// The folder is in the system's temporary folder, not in [Checker.Dir]: a unix socket's
	// path has at most 104 bytes on macOS and 108 on Linux, which the run directory's path
	// can pass.
	root, err := os.MkdirTemp("", "qory-submit-")
	if err != nil {
		return ProgramEnd{}, fmt.Errorf("making a temporary folder for the submit program failed: %w", exports.HideError(err))
	}
	defer os.RemoveAll(root)
	work, net := filepath.Join(root, "work"), filepath.Join(root, "net")
	// The folder is this user's alone. A run's user that is another user passes through the
	// root, which it cannot list, and writes the work folder; the socket and the files of the
	// net folder are open to it.
	socket, workMode, netMode, fileMode := os.FileMode(0o600), os.FileMode(0o700), os.FileMode(0o700), os.FileMode(0o600)
	if c.otherUser() {
		socket, workMode, netMode, fileMode = 0o666, 0o777, 0o755, 0o666
		if err := os.Chmod(root, 0o711); err != nil {
			return ProgramEnd{}, fmt.Errorf("opening the submit program's folder to the run's user failed: %w", exports.HideError(err))
		}
	}
	for dir, mode := range map[string]os.FileMode{work: workMode, net: netMode} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return ProgramEnd{}, fmt.Errorf("making a folder for the submit program failed: %w", exports.HideError(err))
		}
		if err := os.Chmod(dir, mode); err != nil {
			return ProgramEnd{}, fmt.Errorf("making a folder for the submit program failed: %w", exports.HideError(err))
		}
	}
	file := filepath.Join(work, SubmissionFile)
	if err := writeNew(file, p.Submission); err != nil {
		return ProgramEnd{}, fmt.Errorf("writing the submission for the submit program failed: %w", exports.HideError(err))
	}
	if err := os.Chmod(file, fileMode); err != nil {
		return ProgramEnd{}, fmt.Errorf("writing the submission for the submit program failed: %w", exports.HideError(err))
	}
	env := []string{
		"QORY_SUBMISSION_FILE=" + ProgramWorkDir + "/" + SubmissionFile,
		"QORY_PROXY_SOCKET=" + ProgramNetDir + "/" + relay.SocketName,
		"QORY_PROXY_PASSWORD=" + password,
	}
	if p.CABundle != "" {
		bundle, err := os.ReadFile(p.CABundle)
		if err != nil {
			return ProgramEnd{}, fmt.Errorf("reading the CA bundle for the submit program failed: %w", exports.HideError(err))
		}
		copied := filepath.Join(net, caFile)
		if err := writeNew(copied, bundle); err != nil {
			return ProgramEnd{}, fmt.Errorf("copying the CA bundle for the submit program failed: %w", exports.HideError(err))
		}
		if err := os.Chmod(copied, 0o644); err != nil {
			return ProgramEnd{}, fmt.Errorf("copying the CA bundle for the submit program failed: %w", exports.HideError(err))
		}
		env = append(env, "SSL_CERT_FILE="+ProgramNetDir+"/"+caFile)
	}
	// The password goes through a file that no container mounts, not through the command
	// line, which any user of this machine can read.
	envFile := filepath.Join(root, "env")
	if err := writeNew(envFile, []byte(strings.Join(env, "\n")+"\n")); err != nil {
		return ProgramEnd{}, fmt.Errorf("writing the submit program's environment failed: %w", exports.HideError(err))
	}
	if err := os.Chmod(envFile, 0o600); err != nil {
		return ProgramEnd{}, fmt.Errorf("writing the submit program's environment failed: %w", exports.HideError(err))
	}
	forward, err := relay.Start(net, proxy.Host, RelayConnections, socket)
	if err != nil {
		return ProgramEnd{}, fmt.Errorf("opening the relay to the push proxy failed: %w", exports.HideError(err))
	}
	defer forward.Close()
	name, err := containerName()
	if err != nil {
		return ProgramEnd{}, err
	}
	var mounts []string
	for _, m := range []struct {
		src, dst string
		readonly bool
	}{{c.Home, c.Home, true}, {work, ProgramWorkDir, false}, {net, ProgramNetDir, true}} {
		mount, err := bindMountMode(m.src, m.dst, m.readonly)
		if err != nil {
			return ProgramEnd{}, err
		}
		mounts = append(mounts, "--mount", mount)
	}
	create := []string{"create", "--name", name, "--network", "none", "--user", c.User,
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--init", "--pull", "never",
		"--no-healthcheck", "--workdir", ProgramWorkDir, "--env-file", envFile}
	if c.Label != "" {
		create = append(create, "--label", c.Label)
	}
	create = append(create, c.limits()...)
	create = append(create, mounts...)
	if c.Runtime != "" {
		create = append(create, "--runtime", c.Runtime)
	}
	program := p.Command[0]
	create = append(create, "--entrypoint", program, c.Image)
	create = append(create, p.Command[1:]...)
	end, err := c.execute(ctx, "the submit program", name, program, create, c.ProgramLimit, ProgramLimit)
	// The relay ends with the program, before the file is read.
	forward.Close()
	if err != nil {
		return ProgramEnd{}, err
	}
	out := ProgramEnd{Exit: end.exitCode, TimedOut: end.timedOut, StartErr: end.startErr, Stdout: end.stdout, Stderr: end.stderr}
	out.Submission, out.FileErr = readProgramFile(work)
	return out, nil
}

// readProgramFile reads submission.json of dir, which the program could write as a link to a
// file of this machine: the read stays inside dir, and takes a regular file of at most
// [maxProgramFile] bytes alone. A file it cannot take is nil with the cause.
func readProgramFile(dir string) ([]byte, string) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "the folder cannot be opened"
	}
	defer root.Close()
	f, err := root.Open(SubmissionFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "the file is not there"
	}
	if err != nil {
		return nil, "the file cannot be opened"
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, "the file is not a regular file"
	}
	data, err := io.ReadAll(io.LimitReader(f, maxProgramFile+1))
	switch {
	case err != nil:
		return nil, "the file cannot be read"
	case len(data) > maxProgramFile:
		return nil, fmt.Sprintf("the file has more than %d bytes", maxProgramFile)
	}
	return data, ""
}

// bindMountMode is [bindMount] for a mount that readonly sets read-only or not.
func bindMountMode(src, dst string, readonly bool) (string, error) {
	mount, err := bindMount(src, dst)
	if err != nil || readonly {
		return mount, err
	}
	return strings.TrimSuffix(mount, ",readonly"), nil
}
