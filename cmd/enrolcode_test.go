package cmd_test

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/gateway"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/foragerdir"
)

// errGatewayStarted ends a qory gateway whose start a test replaces, once it has the
// gateway's configuration.
var errGatewayStarted = errors.New("the gateway started")

// codeFolder is a new folder for the enrolment code's file, mode 0700, by the path its
// links resolve to, so the path has no link in it.
func codeFolder(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeCode writes content into file, mode 0400, by a rename, so that a reader never
// sees the file half written.
func writeCode(t *testing.T, file, content string) {
	t.Helper()
	tmp := filepath.Join(filepath.Dir(file), ".code.tmp")
	if err := os.WriteFile(tmp, []byte(content), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, file); err != nil {
		t.Fatal(err)
	}
}

// codeGateway writes forager.yaml for qory gateway with srv as its server, with no
// access key id and no pin, gateway.server.enrolment_code_file set to file, and the
// gateway's certificate, in a folder mode 0700, with no access key. srv's codes are a
// gateway's.
func codeGateway(t *testing.T, srv *enrolServer, file string) {
	t.Helper()
	emptyDir(t)
	srv.kind = "gateway"
	if _, err := configDir().Ensure(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, foragerFile(), "apiVersion: qory.dev/v1alpha1\ngateway:\n  listen: 127.0.0.1:0\n"+gatewayTLS+"  server:\n    url: "+srv.URL+"\n    enrolment_code_file: "+file+"\n")
	writeCertificate(t)
}

// startedGateway makes qory gateway hand its configuration to the test in place of
// starting, and end with errGatewayStarted; it returns where the configuration goes.
func startedGateway(t *testing.T) *gateway.Config {
	t.Helper()
	got := &gateway.Config{}
	t.Cleanup(cmd.SetStartGateway(func(c gateway.Config) error {
		*got = c
		return errGatewayStarted
	}))
	return got
}

// enrolledLine is the result line of an enrolment from file, with the key in
// access-key-secret.
func enrolledLine(t *testing.T, file string) string {
	return "qory gateway: enrolled the access key ak_0123456789abcdef (fingerprint " + heldKey(t).Fingerprint() + ") with the code in " + file + "; the secret is in " + configDir().Path(foragerdir.SecretFile) + "\n"
}

// TestGatewayEnrolsFromTheCodeFile is qory gateway with no access key and
// gateway.server.enrolment_code_file set to a file that contains a gateway's code, in
// groups, with one newline: it enrols with the normalised code against gateway.server.url,
// writes access-key-secret and the key's id and pin into forager.yaml, keeping the
// setting, prints one result line and no code, leaves the file where it is, and starts
// with the key.
func TestGatewayEnrolsFromTheCodeFile(t *testing.T) {
	srv := newEnrolServer(t)
	file := filepath.Join(codeFolder(t), "code")
	codeGateway(t, srv, file)
	code := srv.code(1, false)
	writeCode(t, file, code+"\n")
	got := startedGateway(t)
	out, err := run(t, "gateway")
	if !errors.Is(err, errGatewayStarted) {
		t.Fatalf("%v, want the gateway started\n%s", err, out)
	}
	sent := srv.sent()
	normal, _ := accesskey.NormaliseCode(code)
	if len(sent) != 1 || sent[0].Code != normal {
		t.Fatalf("sent %+v, want one enrolment with %s", sent, normal)
	}
	key := heldKey(t)
	if got.Server == nil || got.Server.AccessKeyID != "ak_0123456789abcdef" || got.Server.AccessKey == nil || got.Server.AccessKey.PublicKey() != key.PublicKey() {
		t.Errorf("the gateway started with %+v, want the enrolled key", got.Server)
	}
	if mode(t, configDir().Path(foragerdir.SecretFile)) != 0o600 {
		t.Errorf("access-key-secret is mode %v, want 0600", mode(t, configDir().Path(foragerdir.SecretFile)))
	}
	wants(t, readForagerFile(t), "access_key_id: ak_0123456789abcdef", "apiary_public_key:", "enrolment_code_file: "+file)
	wants(t, out, enrolledLine(t, file))
	lacks(t, out, "waiting for the enrolment code", "access key fingerprint", "stored secrets", code, normal, code[len("qecg_"):strings.Index(code, ".")])
	if !exists(file) {
		t.Error("qory removed the code's file")
	}
}

// TestGatewayWaitsForTheCodeFile is qory gateway started before the code's file exists,
// and before its folder does: it prints the line with the file's path once, looks again
// until the file is there, then enrols and starts.
func TestGatewayWaitsForTheCodeFile(t *testing.T) {
	srv := newEnrolServer(t)
	folder := filepath.Join(codeFolder(t), "enrolment")
	file := filepath.Join(folder, "code")
	codeGateway(t, srv, file)
	t.Cleanup(cmd.SetEnrolmentCodePoll(5 * time.Millisecond))
	startedGateway(t)
	out, done := gatewayInBackground(t)
	const waiting = "qory gateway: waiting for the enrolment code at "
	untilOutput(t, out, waiting)
	time.Sleep(50 * time.Millisecond)
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	writeCode(t, file, srv.code(1, false)+"\n")
	select {
	case err := <-done:
		if !errors.Is(err, errGatewayStarted) {
			t.Fatalf("%v, want the gateway started\n%s", err, out)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("qory gateway did not enrol:\n%s", out)
	}
	text := out.String()
	if n := strings.Count(text, waiting); n != 1 {
		t.Errorf("the waiting line is printed %d times, want once:\n%s", n, text)
	}
	wants(t, text, waiting+file+"; Qory Apiary writes it once this gateway is installed\n", enrolledLine(t, file))
	if sent := srv.sent(); len(sent) != 1 {
		t.Errorf("sent %d enrolments, want 1", len(sent))
	}
}

// TestGatewayStopsWhileItWaitsForTheCodeFile is SIGTERM while qory gateway waits for the
// code's file: it prints that it stops and that it stopped, exits 0, and sends nothing.
func TestGatewayStopsWhileItWaitsForTheCodeFile(t *testing.T) {
	srv := newEnrolServer(t)
	file := filepath.Join(codeFolder(t), "code")
	codeGateway(t, srv, file)
	t.Cleanup(cmd.SetEnrolmentCodePoll(5 * time.Millisecond))
	startedGateway(t)
	out, done := gatewayInBackground(t)
	untilOutput(t, out, "qory gateway: waiting for the enrolment code at ")
	err := stopGateway(t, done)
	if err != nil || cmd.ExitCode(err) != 0 {
		t.Fatalf("%v (exit %d), want nil\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out.String(), "qory gateway: stopping\n", "the gateway stopped\n")
	if sent := srv.sent(); len(sent) != 0 {
		t.Errorf("sent %d enrolments, want none", len(sent))
	}
	if exists(configDir().Path(foragerdir.SecretFile)) {
		t.Error("a stopped gateway wrote access-key-secret")
	}
}

// gatewayInBackground runs qory gateway in the background and returns its output and
// what its end returns.
func gatewayInBackground(t *testing.T) (*syncBuffer, <-chan error) {
	t.Helper()
	out := &syncBuffer{}
	root := cmd.Root()
	root.SetArgs([]string{"gateway"})
	root.SetOut(out)
	root.SetErr(out)
	done := make(chan error, 1)
	go func() { done <- root.Execute() }()
	return out, done
}

// untilOutput waits until out contains want, and fails the test after 30 seconds.
func untilOutput(t *testing.T, out *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(out.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("the output lacks %q:\n%s", want, out)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestGatewayStopsOnACodeFileOutsideTheRules is each code's file qory gateway stops on,
// exit 2, with its error, before it makes a key or sends anything: a link, a path through
// a link, a folder, a folder every user may write, an empty file, a file of more than
// 1 KiB, a file with more than the code, a file with no code, a node's code and a run
// code. No error contains the code.
func TestGatewayStopsOnACodeFileOutsideTheRules(t *testing.T) {
	const fix = "set gateway.server.enrolment_code_file to "
	for _, c := range []struct {
		name string
		// setup makes the code's file in folder, writing code, a gateway's, and returns
		// the path qory is given.
		setup func(t *testing.T, folder, code string) string
		want  func(folder, file string) string
	}{
		{"a link", func(t *testing.T, folder, code string) string {
			writeCode(t, filepath.Join(folder, "real"), code+"\n")
			if err := os.Symlink("real", filepath.Join(folder, "code")); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(folder, "code")
		}, func(_, file string) string { return fix + "the file itself, not a link: " + file + " is a link" }},
		{"a path through a link", func(t *testing.T, folder, code string) string {
			if err := os.Mkdir(filepath.Join(folder, "real"), 0o700); err != nil {
				t.Fatal(err)
			}
			writeCode(t, filepath.Join(folder, "real", "code"), code+"\n")
			if err := os.Symlink("real", filepath.Join(folder, "link")); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(folder, "link", "code")
		}, func(folder, file string) string {
			return fix + "a path with no link in it, such as " + filepath.Join(folder, "real", "code") + ": " + file + " leads through a link"
		}},
		{"a folder", func(t *testing.T, folder, _ string) string {
			if err := os.Mkdir(filepath.Join(folder, "code"), 0o700); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(folder, "code")
		}, func(_, file string) string { return fix + "a regular file: " + file + " is not one" }},
		{"a folder every user may write", func(t *testing.T, folder, code string) string {
			writeCode(t, filepath.Join(folder, "code"), code+"\n")
			if err := os.Chmod(folder, 0o777); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(folder, "code")
		}, func(folder, _ string) string {
			return "remove the write permission of other users from " + folder + ": every user may write it, and only root and the user running qory may write gateway.server.enrolment_code_file and its folders, except a folder that root owns with the sticky bit set"
		}},
		{"an empty file", func(t *testing.T, folder, _ string) string {
			writeCode(t, filepath.Join(folder, "code"), "")
			return filepath.Join(folder, "code")
		}, func(_, file string) string {
			return "write the gateway's enrolment code (qecg_…) into " + file + ": the file is empty"
		}},
		{"a newline alone", func(t *testing.T, folder, _ string) string {
			writeCode(t, filepath.Join(folder, "code"), "\n")
			return filepath.Join(folder, "code")
		}, func(_, file string) string {
			return "write the gateway's enrolment code (qecg_…) into " + file + ": the file is empty"
		}},
		{"more than 1 KiB", func(t *testing.T, folder, code string) string {
			writeCode(t, filepath.Join(folder, "code"), code+strings.Repeat("0", 1025))
			return filepath.Join(folder, "code")
		}, func(_, file string) string {
			return "write only the gateway's enrolment code into " + file + ", with at most one newline after it: the file contains more"
		}},
		{"two lines", func(t *testing.T, folder, code string) string {
			writeCode(t, filepath.Join(folder, "code"), code+"\n\n")
			return filepath.Join(folder, "code")
		}, func(_, file string) string {
			return "write only the gateway's enrolment code into " + file + ", with at most one newline after it: the file contains more"
		}},
		{"no code", func(t *testing.T, folder, _ string) string {
			writeCode(t, filepath.Join(folder, "code"), "hello\n")
			return filepath.Join(folder, "code")
		}, func(_, file string) string {
			return "write a gateway's enrolment code (qecg_…) into " + file + ": the file does not contain an enrolment code of Qory Apiary"
		}},
		{"a node's code", func(t *testing.T, folder, code string) string {
			writeCode(t, filepath.Join(folder, "code"), accesskey.CodePrefixNode+strings.TrimPrefix(code, accesskey.CodePrefixGateway)+"\n")
			return filepath.Join(folder, "code")
		}, func(_, file string) string {
			return "write a gateway's enrolment code (qecg_…) into " + file + ": the file contains the enrolment code of an access key of kind node"
		}},
		{"a run code", func(t *testing.T, folder, code string) string {
			writeCode(t, filepath.Join(folder, "code"), accesskey.CodePrefixRun+strings.TrimPrefix(code, accesskey.CodePrefixGateway)+"\n")
			return filepath.Join(folder, "code")
		}, func(_, file string) string {
			return "write a gateway's enrolment code (qecg_…) into " + file + ": the file contains the enrolment code of an access key of kind run"
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := newEnrolServer(t)
			srv.kind = "gateway"
			folder := codeFolder(t)
			code := srv.code(1, false)
			file := c.setup(t, folder, code)
			t.Cleanup(func() { os.Chmod(folder, 0o700) })
			codeGateway(t, srv, file)
			startedGateway(t)
			out, err := run(t, "gateway")
			if want := c.want(folder, file); err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
				t.Errorf("%v (exit %d)\nwant %q", err, cmd.ExitCode(err), want)
			}
			stoppedBeforeAKey(t, srv, out, err, code)
		})
	}
}

// stoppedBeforeAKey fails the test when a stopped qory gateway sent an enrolment, made
// a key, or printed the code.
func stoppedBeforeAKey(t *testing.T, srv *enrolServer, out string, err error, code string) {
	t.Helper()
	if sent := srv.sent(); len(sent) != 0 {
		t.Errorf("sent %d enrolments, want none", len(sent))
	}
	for _, name := range []string{foragerdir.SecretFile, foragerdir.NewSecretFile} {
		if exists(configDir().Path(name)) {
			t.Errorf("a stopped gateway wrote %s", name)
		}
	}
	text := out
	if err != nil {
		text += err.Error()
	}
	lacks(t, text, "listening on", code[len("qecg_"):strings.Index(code, ".")], "F1XT0RE0")
}

// TestGatewayStopsOnACodeFileAGroupMayWrite is a code's file that its group may write,
// or in a folder its group may write, whatever the group: qory gateway stops, exit 2,
// before a key is made. Under root the folder's group is root's, gid 0, which the rule
// of an integration's program accepts and this rule does not.
func TestGatewayStopsOnACodeFileAGroupMayWrite(t *testing.T) {
	for _, c := range []struct {
		name string
		// writable is the path given the group's write permission, the file or its folder.
		writable func(folder, file string) string
		mode     os.FileMode
	}{
		{"the folder", func(folder, _ string) string { return folder }, 0o770},
		{"the file", func(_, file string) string { return file }, 0o460},
	} {
		t.Run(c.name, func(t *testing.T) {
			folder := codeFolder(t)
			file := filepath.Join(folder, "code")
			srv := newEnrolServer(t)
			srv.kind = "gateway"
			code := srv.code(1, false)
			writeCode(t, file, code+"\n")
			path := c.writable(folder, file)
			if os.Geteuid() == 0 {
				if err := os.Chown(path, -1, 0); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(path, c.mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(folder, 0o700) })
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			gid := strconv.FormatUint(uint64(info.Sys().(*syscall.Stat_t).Gid), 10)
			group := gid
			if g, err := user.LookupGroupId(gid); err == nil {
				group = g.Name + " (" + gid + ")"
			}
			codeGateway(t, srv, file)
			startedGateway(t)
			out, err := run(t, "gateway")
			want := "remove the group's write permission from " + path + ": the group " + group + " may write it, and only root and the user running qory may write gateway.server.enrolment_code_file and its folders, except a folder that root owns with the sticky bit set"
			if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
				t.Errorf("%v (exit %d)\nwant %q", err, cmd.ExitCode(err), want)
			}
			stoppedBeforeAKey(t, srv, out, err, code)
		})
	}
}

// TestGatewayStopsOnACodeFileAnotherUserOwns is a code's file another user owns: qory
// gateway stops, exit 2, before a key is made. Only root can give a file to another user, so the test
// needs root.
func TestGatewayStopsOnACodeFileAnotherUserOwns(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("only root can give a file to another user")
	}
	srv := newEnrolServer(t)
	srv.kind = "gateway"
	code := srv.code(1, false)
	file := filepath.Join(codeFolder(t), "code")
	writeCode(t, file, code+"\n")
	if err := os.Chown(file, 65532, 65532); err != nil {
		t.Fatal(err)
	}
	codeGateway(t, srv, file)
	startedGateway(t)
	out, err := run(t, "gateway")
	if err == nil || !strings.HasPrefix(err.Error(), "make root or the user running qory the owner of "+file+": it is owned by ") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("%v (exit %d), want the error for the owner", err, cmd.ExitCode(err))
	}
	stoppedBeforeAKey(t, srv, out, err, code)
}

// TestGatewayWithAKeyIgnoresTheCodeFile is qory gateway with an access key in
// access-key-secret and gateway.server.enrolment_code_file set to a file that does not
// exist: it waits for nothing, enrols nothing, and starts with the key it has.
func TestGatewayWithAKeyIgnoresTheCodeFile(t *testing.T) {
	emptyDir(t)
	srv := newGatewayServer(t)
	serverFile(t, srv, "    enrolment_code_file: "+filepath.Join(codeFolder(t), "code")+"\n  listen: 127.0.0.1:0\n"+gatewayTLS)
	writeCertificate(t)
	got := startedGateway(t)
	out, err := run(t, "gateway")
	if !errors.Is(err, errGatewayStarted) {
		t.Fatalf("%v, want the gateway started\n%s", err, out)
	}
	if got.Server == nil || got.Server.AccessKey == nil || got.Server.AccessKey.PublicKey() != srv.key.PublicKey() {
		t.Errorf("the gateway started with %+v, want the key it had", got.Server)
	}
	lacks(t, out, "enrolment code", "enrolled")
}

// TestGatewayWithNoKeyAndNoCodeFileStops is qory gateway with an access key id, no
// secret and no gateway.server.enrolment_code_file: it stops as before, exit 2.
func TestGatewayWithNoKeyAndNoCodeFileStops(t *testing.T) {
	srv := newEnrolServer(t)
	codeGateway(t, srv, "code")
	writeFile(t, foragerFile(), strings.Replace(readForagerFile(t), "    enrolment_code_file: code\n", "    access_key_id: ak_0123456789abcdef\n", 1))
	startedGateway(t)
	_, err := run(t, "gateway")
	if want := "no access key secret for the server " + srv.URL + ": qory access-key enrol makes one, or set QORY_ACCESS_KEY_SECRET"; err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("%v (exit %d)\nwant %q", err, cmd.ExitCode(err), want)
	}
	if sent := srv.sent(); len(sent) != 0 {
		t.Errorf("sent %d enrolments, want none", len(sent))
	}
}

// TestRunStopsOnTheCodeFile is gateway.server.enrolment_code_file in the forager.yaml of
// qory run, with --local too, qory run pool, qory run register and qory run end: each
// stops, exit 2, before it reaches the server.
func TestRunStopsOnTheCodeFile(t *testing.T) {
	const setting = "    enrolment_code_file: /run/qory/enrolment/code\n"
	want := func(command string) string {
		return "remove gateway.server.enrolment_code_file from " + foragerFile() + ": qory gateway alone enrols from it, and " + command + " does not"
	}
	t.Run("qory run", func(t *testing.T) {
		root := newCheckout(t)
		copyFixture(t, "two-modules", root)
		composedForFake(t, root, fakeRuntime(t))
		srv := newFakeServer(t, "")
		serverFile(t, srv, setting)
		t.Cleanup(cmd.SetStartGateway(func(gateway.Config) error { return errGatewayStarted }))
		for _, args := range [][]string{{"run"}, {"run", "--local"}} {
			out, err := run(t, args...)
			if err == nil || err.Error() != want("qory run") || cmd.ExitCode(err) != cmd.ExitInput {
				t.Errorf("%v: %v (exit %d)\nwant %q\n%s", args, err, cmd.ExitCode(err), want("qory run"), out)
			}
		}
		if targets := srvTargets(srv); len(targets) != 0 {
			t.Errorf("the server got %v", targets)
		}
		if ids := recorded(t, root); len(ids) != 0 {
			t.Errorf("a stopped run left records %v", ids)
		}
	})
	t.Run("qory run pool", func(t *testing.T) {
		m := newPoolMachine(t)
		pin := "    apiary_public_key: " + pinLine(m.srv.signer) + "\n"
		writeFile(t, m.file, strings.Replace(readForagerFile(t), pin, pin+setting, 1))
		got := started(t)
		_, err := run(t, "run", "pool")
		if err == nil || err.Error() != want("qory run pool") || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%v (exit %d)\nwant %q", err, cmd.ExitCode(err), want("qory run pool"))
		}
		if len(*got) != 0 {
			t.Error("the loop started")
		}
		if targets := srvTargets(m.srv); len(targets) != 0 {
			t.Errorf("the server got %v", targets)
		}
	})
	for _, c := range []struct {
		command string
		args    func() []string
	}{
		{"qory run register", func() []string { return []string{"run", "register", "--run-key-fd", newKeyPipe(t).fd()} }},
		{"qory run end", func() []string { return []string{"run", "end", "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d"} }},
	} {
		t.Run(c.command, func(t *testing.T) {
			srv := starterServer(t, "https://gateway.example:8443", setting)
			_, _, err := runSplit(t, "", c.args()...)
			if err == nil || err.Error() != want(c.command) || cmd.ExitCode(err) != cmd.ExitInput {
				t.Errorf("%v (exit %d)\nwant %q", err, cmd.ExitCode(err), want(c.command))
			}
			if targets, _ := srv.sent(); len(targets) != 0 {
				t.Errorf("the server got %v", targets)
			}
		})
	}
}
