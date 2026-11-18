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
	"strconv"
	"strings"
	"time"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/relay"
	"github.com/qoryai/qory/internal/render"
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
//
// The proxy's password reaches the program as the variable QORY_PROXY_PASSWORD, through an env
// file that qory creates with mode 0600 and no container mounts. The engine keeps the variable
// in the container's configuration, so anyone who can run docker inspect on this host reads it
// until the container is removed. The program's contract reads the variable, so the exposure
// stays; docs/run.md says so.
func (c *Checker) RunProgram(ctx context.Context, p Program) (ProgramEnd, error) {
	proxy, err := url.Parse(p.ProxyURL)
	if err != nil || proxy.Host == "" || proxy.User == nil {
		return ProgramEnd{}, errors.New("the push proxy's address is not http://user:password@host:port")
	}
	password, _ := proxy.User.Password()
	// The folder is in the system's temporary folder, not in [Checker.Dir]: a unix socket's
	// path has at most 104 bytes on macOS and 108 on Linux, which the run directory's path
	// can pass. os.MkdirTemp makes it mode 0700 with a random name, so no other user lists it
	// or reaches a path below it. The engine binds work and net from here, whatever their
	// modes, so the container's user needs no access to the folders above them.
	root, err := os.MkdirTemp("", "qory-submit-")
	if err != nil {
		return ProgramEnd{}, fmt.Errorf("making a temporary folder for the submit program failed: %w", exports.HideError(err))
	}
	defer os.RemoveAll(root)
	work, net := filepath.Join(root, "work"), filepath.Join(root, "net")
	for _, dir := range []string{work, net} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return ProgramEnd{}, fmt.Errorf("making a folder for the submit program failed: %w", exports.HideError(err))
		}
	}
	access := c.programAccess(work)
	file := filepath.Join(work, SubmissionFile)
	if err := access.writeFile(file, p.Submission, access.file); err != nil {
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
		if err := access.writeFile(filepath.Join(net, caFile), bundle, access.ca); err != nil {
			return ProgramEnd{}, fmt.Errorf("copying the CA bundle for the submit program failed: %w", exports.HideError(err))
		}
		env = append(env, "SSL_CERT_FILE="+ProgramNetDir+"/"+caFile)
	}
	// The password goes through a file that no container mounts, not through the command
	// line, which any user of this machine can read. The file is created with mode 0600, so
	// it never has a wider mode, and it is not given to the run's user.
	envFile := filepath.Join(root, "env")
	if err := writeNewMode(envFile, []byte(strings.Join(env, "\n")+"\n"), 0o600); err != nil {
		return ProgramEnd{}, fmt.Errorf("writing the submit program's environment failed: %w", exports.HideError(err))
	}
	forward, err := relay.Start(net, proxy.Host, RelayConnections, access.socket)
	if err != nil {
		return ProgramEnd{}, fmt.Errorf("opening the relay to the push proxy failed: %w", exports.HideError(err))
	}
	defer forward.Close()
	if err := access.finish(work, net, forward.Path); err != nil {
		return ProgramEnd{}, fmt.Errorf("opening the submit program's folders to the run's user failed: %w", exports.HideError(err))
	}
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
// file of this machine, or as a pipe: the read stays inside dir, takes a regular file of at
// most [maxProgramFile] bytes alone, and never waits for a writer. The kind of the file is
// read with Lstat before the file is opened, and the open does not block, so a pipe, a link or
// a device there never stops the run. A file it cannot take is nil with the cause.
func readProgramFile(dir string) ([]byte, string) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "the folder cannot be opened"
	}
	defer root.Close()
	info, err := root.Lstat(SubmissionFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "the file is not there"
	}
	if err != nil {
		return nil, "the file cannot be opened"
	}
	if !info.Mode().IsRegular() {
		return nil, "the file is not a regular file"
	}
	f, err := root.OpenFile(SubmissionFile, os.O_RDONLY|render.OpenFlags, 0)
	if err != nil {
		return nil, "the file cannot be opened"
	}
	defer f.Close()
	// The file may be swapped for another kind after the Lstat: the open did not block, and
	// the opened file is checked again.
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
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

// programAccess is how the folders of the submit program's container are given to the run's
// user. When this process can give each folder and file to that user (chown), they keep the
// modes 0700 and 0600, and only that user and this one open them. Otherwise, with a run's user
// that is another user and no right to give files to it (widen), the folders and files get the
// wider modes that user needs, below the folder of [Checker.RunProgram], which is mode 0700 and
// which no other user opens. The engine mounts work and net from there as they are.
type programAccess struct {
	// uid and gid are the run's user; chown is set when this process gives paths to it.
	uid, gid int
	chown    bool
	// widen is whether the modes below are wider than 0700 and 0600.
	widen bool
	// file is the mode of submission.json, ca of the CA bundle and socket of the relay's
	// socket; work and net are the modes of the folders.
	file, ca, socket, work, net os.FileMode
}

// programAccess chooses the modes for the run's user, with probe, a folder this process made,
// as the test of whether it can give a path to that user.
func (c *Checker) programAccess(probe string) *programAccess {
	a := &programAccess{file: 0o600, ca: 0o600, socket: 0o600, work: 0o700, net: 0o700}
	if !c.otherUser() {
		return a
	}
	uid, gid, ok := c.numericUser()
	if ok && os.Lchown(probe, uid, gid) == nil {
		a.uid, a.gid, a.chown = uid, gid, true
		return a
	}
	a.widen = true
	a.file, a.ca, a.socket, a.work, a.net = 0o666, 0o644, 0o666, 0o777, 0o755
	return a
}

// numericUser is the run's user as a user id and a group id; the group is -1, which leaves
// it as it is, when the user has none. A user given by name is not numeric.
func (c *Checker) numericUser() (uid, gid int, ok bool) {
	u, g, hasGroup := strings.Cut(c.User, ":")
	uid, err := strconv.Atoi(u)
	if err != nil {
		return 0, 0, false
	}
	gid = -1
	if hasGroup {
		if gid, err = strconv.Atoi(g); err != nil {
			return 0, 0, false
		}
	}
	return uid, gid, true
}

// writeFile writes a new file at path with mode: it is created with mode 0600, and given to the
// run's user or widened to mode after the content is written.
func (a *programAccess) writeFile(path string, content []byte, mode os.FileMode) error {
	if err := writeNewMode(path, content, 0o600); err != nil {
		return err
	}
	return a.give(path, mode)
}

// give gives path to the run's user when this process can, and otherwise sets mode on it.
func (a *programAccess) give(path string, mode os.FileMode) error {
	if a.chown {
		return os.Lchown(path, a.uid, a.gid)
	}
	if a.widen {
		return os.Chmod(path, mode)
	}
	return nil
}

// finish gives the folders and the socket to the run's user, or sets their modes, after the
// socket exists: a socket cannot be made in a folder that another user owns.
func (a *programAccess) finish(work, net, socket string) error {
	for path, mode := range map[string]os.FileMode{work: a.work, net: a.net, socket: a.socket} {
		if err := a.give(path, mode); err != nil {
			return err
		}
	}
	return nil
}
