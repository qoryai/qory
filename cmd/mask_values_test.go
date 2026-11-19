package cmd_test

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/gateway"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/report"
)

// The tests of this file give a command an invented run key, maskKey, where a person may
// paste one by mistake, and check three things: no part of the key reaches stdout, stderr
// or the error; line 1 of the error is what qory printed before, with the phrase in place
// of the value; and a wrong value without a key prints as before, quoted where it was.
const (
	// maskLetters are the 40 letters after the run key's prefix, in an order no hex id
	// or path of the tests has.
	maskLetters = "zyxwvutsrqponmlkjihgzyxwvutsrqponmlkjihg"
	maskKey     = "qrk_" + maskLetters
	// keyPhrase is what qory prints in place of a value that contains a run key.
	keyPhrase = "(a value that contains a run key)"
)

// wantNoKey fails t when any of texts contains qrk_ in any letter case, or any 6 letters
// in a row of maskKey.
func wantNoKey(t *testing.T, texts ...string) {
	t.Helper()

	for _, text := range texts {
		if strings.Contains(strings.ToLower(text), "qrk_") {
			t.Errorf("the text has the key's prefix: %q", text)
		}

		for i := 0; i+6 <= len(maskLetters); i++ {
			if strings.Contains(text, maskLetters[i:i+6]) {
				t.Errorf("the text has %q of the key: %q", maskLetters[i:i+6], text)
			}
		}
	}
}

// wantMasked runs args, which contain the key, and checks that the command exits with
// status, that its error is want, and that no part of the key is printed.
func wantMasked(t *testing.T, status int, want string, args ...string) {
	t.Helper()

	stdout, stderr, err := runSplit(t, "", args...)
	wantError(t, err, status, want, args)
	wantNoKey(t, stdout, stderr, errorText(err))
}

// wantPlain runs args, which contain a wrong value with no key, and checks that the
// command exits with status, and that its error is want, as qory printed it before.
func wantPlain(t *testing.T, status int, want string, args ...string) {
	t.Helper()

	_, _, err := runSplit(t, "", args...)
	wantError(t, err, status, want, args)
}

// wantError checks err of args: status, and the text want.
func wantError(t *testing.T, err error, status int, want string, args []string) {
	t.Helper()

	if err == nil {
		t.Fatalf("%q: no error, want %q", args, want)
	}

	if got := cmd.ExitCode(err); got != status {
		t.Errorf("%q: exit status %d, want %d", args, got, status)
	}

	if err.Error() != want {
		t.Errorf("%q:\nerr  %q\nwant %q", args, err, want)
	}
}

// errorText is err's text, "" for nil.
func errorText(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

// TestMaskTheVerdictArgument is qory run verdict with a key as the verdict.
func TestMaskTheVerdictArgument(t *testing.T) {
	inCheckout(t)
	startRunAPI(t)

	const line = "Pass passed, changes-requested or escalate. The verdict is %s."

	wantMasked(t, cmd.ExitInput, strings.Replace(line, "%s", keyPhrase, 1), "run", "verdict", maskKey)
	wantPlain(t, cmd.ExitInput, strings.Replace(line, "%s", `"maybe"`, 1), "run", "verdict", "maybe")
}

// TestMaskTheArtifactTypeAndName is qory artifact push with a key as the type, and as
// --name.
func TestMaskTheArtifactTypeAndName(t *testing.T) {
	root := inCheckout(t)
	startRunAPI(t)
	file := filepath.Join(root, "demo.md")
	writeFile(t, file, "demo\n")

	const line = "Set the %s to lowercase letters and digits in groups joined by single hyphens, at most 64 bytes. The value is "

	for _, c := range []struct{ what, value string }{{"type", maskKey}, {"--name", maskKey}} {
		args := []string{"artifact", "push", c.value, file}
		plain := []string{"artifact", "push", "Demo_1", file}
		if c.what == "--name" {
			args = []string{"artifact", "push", "demo", file, "--name", c.value}
			plain = []string{"artifact", "push", "demo", file, "--name", "Demo_1"}
		}

		start := strings.Replace(line, "%s", c.what, 1)
		wantMasked(t, cmd.ExitInput, start+keyPhrase+".", args...)
		wantPlain(t, cmd.ExitInput, start+`"Demo_1".`, plain...)
	}
}

// TestMaskThePulledTypeOfAMissingVersion is qory artifact pull <type> --version N in a
// run, with a key as the type and no version N.
func TestMaskThePulledTypeOfAMissingVersion(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)
	pullFixture(server)
	index := filepath.Join(root, ".qory", "artifacts", "index.json")

	wantMasked(t, cmd.ExitInput, "Choose a version that "+index+" lists. "+keyPhrase+" has no version 9.", "artifact", "pull", maskKey, "--version", "9")
	wantPlain(t, cmd.ExitInput, "Choose a version that "+index+" lists. demo has no version 9.", "artifact", "pull", "demo", "--version", "9")
}

// TestMaskAStrayArgument is a command that takes no argument, given a key as one.
func TestMaskAStrayArgument(t *testing.T) {
	emptyDir(t)

	for _, verb := range [][]string{{"version"}, {"config"}, {"worktree", "list"}, {"setup", "example"}} {
		path := "qory " + strings.Join(verb, " ")
		wantMasked(t, cmd.ExitInput, "unknown command "+keyPhrase+` for "`+path+`"`, append(verb, maskKey)...)
		wantPlain(t, cmd.ExitInput, `unknown command "bogus" for "`+path+`"`, append(verb, "bogus")...)
	}
}

// TestMaskAFlagError is each error pflag prints a value of: a value an int, a bool or a
// duration flag does not read, and an unknown flag written with two dashes, one, or
// three.
func TestMaskAFlagError(t *testing.T) {
	emptyDir(t)

	for _, c := range []struct {
		args       func(value string) []string
		key, plain string
	}{
		{
			func(v string) []string { return []string{"worktree", "add", "--pr", v} },
			"invalid argument " + keyPhrase + ` for "--pr" flag: strconv.ParseInt: parsing ` + keyPhrase + ": invalid syntax",
			`invalid argument "bogus" for "--pr" flag: strconv.ParseInt: parsing "bogus": invalid syntax`,
		},
		{
			func(v string) []string { return []string{"version", "--json=" + v} },
			"invalid argument " + keyPhrase + ` for "--json" flag: strconv.ParseBool: parsing ` + keyPhrase + ": invalid syntax",
			`invalid argument "bogus" for "--json" flag: strconv.ParseBool: parsing "bogus": invalid syntax`,
		},
		{
			func(v string) []string { return []string{"run", "--timeout", v} },
			"invalid argument " + keyPhrase + ` for "--timeout" flag: time: invalid duration ` + keyPhrase,
			`invalid argument "bogus" for "--timeout" flag: time: invalid duration "bogus"`,
		},
		{
			func(v string) []string { return []string{"version", "--" + v} },
			"unknown flag: " + keyPhrase,
			"unknown flag: --bogus",
		},
		{
			func(v string) []string { return []string{"version", "-" + v} },
			"unknown shorthand flag: 'q' in " + keyPhrase,
			"unknown shorthand flag: 'b' in -bogus",
		},
		{
			func(v string) []string { return []string{"version", "---" + v} },
			"bad flag syntax: " + keyPhrase,
			"bad flag syntax: ---bogus",
		},
	} {
		wantMasked(t, cmd.ExitInput, c.key, c.args(maskKey)...)
		wantPlain(t, cmd.ExitInput, c.plain, c.args("bogus")...)
	}
}

// TestMaskAHelpTopic is qory help with a key as the topic: cobra's help command prints
// the topic itself, on the command's output when one is set, and exits 0.
func TestMaskAHelpTopic(t *testing.T) {
	emptyDir(t)

	for _, c := range []struct{ topic, line string }{
		{maskKey, "Unknown help topic [`" + keyPhrase + "`]\n"},
		{"bogus", "Unknown help topic [`bogus`]\n"},
	} {
		stdout, stderr, err := runSplit(t, "", "help", c.topic)
		if err != nil {
			t.Fatal(err)
		}

		if got, _, _ := strings.Cut(stdout+stderr, "Usage:"); got != c.line {
			t.Errorf("help %s: line 1 %q, want %q", c.topic, got, c.line)
		}

		if c.topic == maskKey {
			wantNoKey(t, stdout, stderr)
		}
	}
}

// TestMaskAPushedFile is qory artifact push with a key in the path of a file that is not
// there, of a folder, of two files with one name, and of a file given with --withdraw.
func TestMaskAPushedFile(t *testing.T) {
	root := inCheckout(t)
	startRunAPI(t)

	for _, name := range []string{maskKey, "plain"} {
		writeFile(t, filepath.Join(root, name, "demo.md"), "a\n")
		writeFile(t, filepath.Join(root, "one", name+".md"), "b\n")
		writeFile(t, filepath.Join(root, "two", name+".md"), "c\n")
	}

	for _, c := range []struct {
		args   func(name string) []string
		status int
		// shown is what the plain error prints of the path.
		shown string
		want  func(shown string) string
	}{
		{
			func(n string) []string {
				return []string{"artifact", "push", "demo", filepath.Join(root, n, "absent.md")}
			},
			cmd.ExitInput, filepath.Join(root, "plain", "absent.md"),
			func(s string) string { return "Pass a file that exists. stat " + s + ": no such file or directory" },
		},
		{
			func(n string) []string { return []string{"artifact", "push", "demo", filepath.Join(root, n)} },
			cmd.ExitInput, filepath.Join(root, "plain"),
			func(s string) string {
				return "Pass a file, not a directory or a device. " + s + " is neither a file nor a link to one."
			},
		},
		{
			func(n string) []string {
				return []string{"artifact", "push", "demo", filepath.Join(root, "one", n+".md"), filepath.Join(root, "two", n+".md")}
			},
			cmd.ExitInput, "plain.md",
			func(s string) string {
				return "Rename one of the files named " + s + ". The files of one version have different names."
			},
		},
		{
			func(n string) []string { return []string{"artifact", "push", "demo", n + ".json", "--withdraw"} },
			cmd.ExitArtifactRefused, "plain.json",
			func(s string) string {
				return "Pass either files or --withdraw.\n  A withdrawal stores no files, and the command has 1 file: " + s + "."
			},
		},
	} {
		wantMasked(t, c.status, c.want(keyPhrase), c.args(maskKey)...)
		wantPlain(t, c.status, c.want(c.shown), c.args("plain")...)
	}
}

// TestMaskAPointsFile is qory run verdict escalate --points with a key in the path of a
// file that is not there, too large, with a byte order mark, not UTF-8, and with a lone
// surrogate.
func TestMaskAPointsFile(t *testing.T) {
	root := inCheckout(t)
	startRunAPI(t)

	for _, c := range []struct {
		content string
		want    func(path string) string
	}{
		{"", func(p string) string {
			return "Pass a file that exists to --points. open " + p + ": no such file or directory"
		}},
		{strings.Repeat("a", 65537), func(p string) string { return "Shorten " + p + " to at most 65536 bytes. The file is larger." }},
		{"\xef\xbb\xbf" + verdictPoints, func(p string) string { return "Save " + p + " without a byte order mark." }},
		{"[{\"text\": \"The checks failed \xff.\", \"action\": \"run_again\"}]", func(p string) string { return "Save " + p + " as UTF-8." }},
		{`[{"text": "The checks failed \ud800.", "action": "run_again"}]`, func(p string) string {
			return `Remove the escape of a lone surrogate, such as \ud800, from ` + p + "."
		}},
	} {
		paths := map[string]string{}
		for _, name := range []string{maskKey, "plain"} {
			path := filepath.Join(root, name, "points.json")
			if err := os.RemoveAll(filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
			if c.content != "" {
				writeFile(t, path, c.content)
			}
			paths[name] = path
		}

		wantMasked(t, cmd.ExitInput, c.want(keyPhrase), "run", "verdict", "escalate", "--points", paths[maskKey])
		wantPlain(t, cmd.ExitInput, c.want(paths["plain"]), "run", "verdict", "escalate", "--points", paths["plain"])
	}
}

// TestMaskTheReasonOfARunEnd is qory run end with a key as --reason: a lower-case run
// key has the form of a starter's reason, so the server records it, and the line that
// says so prints the phrase in its place, with no _ turned into a space.
func TestMaskTheReasonOfARunEnd(t *testing.T) {
	starterServer(t, "https://gateway.example:8443", "")

	for _, c := range []struct{ id, reason, shown string }{
		{"0192f0c1-7d4e-7a2b-8c3d-000000000011", maskKey, keyPhrase},
		{"0192f0c1-7d4e-7a2b-8c3d-000000000012", "all_checks_passed", "all checks passed"},
	} {
		if _, stderr, err := runSplit(t, "", "run", "register", "--run-key-fd", newKeyPipe(t).fd(), "--run-id", c.id); err != nil {
			t.Fatalf("%v\n%s", err, stderr)
		}

		stdout, stderr, err := runSplit(t, "", "run", "end", c.id, "--outcome", "completed", "--reason", c.reason)
		if err != nil {
			t.Fatalf("%v\n%s", err, stderr)
		}

		if want := "qory run end: node " + testPool + ", instance " + c.id + "\n✓ the server records the run " + c.id + " as completed: " + c.shown + "\n"; stdout != "" || stderr != want {
			t.Errorf("stdout %q, stderr:\n%s\nwant:\n%s", stdout, stderr, want)
		}

		if c.reason == maskKey {
			wantNoKey(t, stdout, stderr)
		}
	}
}

// TestMaskTheListenHost is qory gateway with a key in the host of --listen, whose lookup
// fails: the address, and the host in the cause of the failure, print as the phrase.
func TestMaskTheListenHost(t *testing.T) {
	emptyDir(t)
	serverFile(t, newGatewayServer(t), "  listen: 127.0.0.1:0\n"+gatewayTLS)
	writeCertificate(t)

	for _, c := range []struct{ host, want string }{
		{maskKey + ".example", "listen on " + keyPhrase + ": lookup " + keyPhrase + ": no such host"},
		{"nowhere.example", "listen on nowhere.example:8443: lookup nowhere.example: no such host"},
	} {
		// The lookup fails as the resolver's would; no name is looked up.
		t.Cleanup(cmd.SetStartGateway(func(gateway.Config) error {
			return &net.OpError{Op: "listen", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: c.host, IsNotFound: true}}
		}))

		stdout, stderr, err := runSplit(t, "", "gateway", "--listen", c.host+":8443")
		if err == nil || err.Error() != c.want {
			t.Errorf("--listen %s: %v, want %q\n%s", c.host, err, c.want, stderr)
		}

		if c.host != "nowhere.example" {
			wantNoKey(t, stdout, stderr, errorText(err))
		}
	}
}

// TestMaskAMountPath is a walled run with a key in the path of a read-write --mount
// that contains qory's state directory: the mount, and the state directory under it,
// print with the phrase in place of the mount's path.
func TestMaskAMountPath(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, _ := fakeDocker(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")

	const want = "make the mount %[1]s read-only, with :ro on --mount or in wall.mounts: it contains qory's state directory %[1]s/state/qory, and a walled run may change any file in a read-write mount"

	for _, name := range []string{maskKey, "plain"} {
		mount := filepath.Join(tempDir(t), name)
		writeFile(t, filepath.Join(mount, "state", "keep"), "")
		t.Setenv("XDG_STATE_HOME", filepath.Join(mount, "state"))

		if name == maskKey {
			wantMasked(t, cmd.ExitInput, fmt.Sprintf(want, keyPhrase), "run", "claude", "--mount", mount)
		} else {
			wantPlain(t, cmd.ExitInput, fmt.Sprintf(want, mount), "run", "claude", "--mount", mount)
		}
	}
}

// TestMaskTheReportOfAHome is qory harness inspect with a key in --home, whose report
// is not JSON, or is a folder.
func TestMaskTheReportOfAHome(t *testing.T) {
	dir := emptyDir(t)

	for _, c := range []struct {
		make func(report string)
		want func(report string) string
	}{
		{
			func(r string) { writeFile(t, r, "{bad") },
			func(r string) string { return r + ": invalid character 'b' looking for beginning of object key string" },
		},
		{
			func(r string) { writeFile(t, filepath.Join(r, "keep"), "") },
			func(r string) string { return "read " + r + ": is a directory" },
		},
	} {
		for _, name := range []string{maskKey, "plain"} {
			home := filepath.Join(dir, name)
			report := home + "-report.json"
			if err := os.RemoveAll(report); err != nil {
				t.Fatal(err)
			}
			c.make(report)

			if name == maskKey {
				wantMasked(t, 1, c.want(keyPhrase), "harness", "inspect", "--home", home)
			} else {
				wantPlain(t, 1, c.want(report), "harness", "inspect", "--home", home)
			}
		}
	}
}

// TestMaskTheComposedFile is qory harness compose with a key in the path of -f: the
// error of a qory outside the file's range, and the rows of a range not checked and of
// a retired apiVersion.
func TestMaskTheComposedFile(t *testing.T) {
	root := newCheckout(t)

	stack := func(name, apiVersion, qory string) string {
		dir := filepath.Join(root, name)
		writeManifest(t, filepath.Join(dir, "modules", "app"), "app")
		writeFile(t, filepath.Join(dir, "modules", "app", "skills", "deploy", "SKILL.md"), "# deploy\n")
		file := filepath.Join(dir, "qory.yaml")
		writeFile(t, file, ownStack(strings.Replace(appStack, "qory.dev/v1alpha1", apiVersion, 1))+"qory: \""+qory+"\"\n")

		return file
	}

	t.Run("a qory outside the range", func(t *testing.T) {
		release(t, "0.2.1")

		const line = "%s: this qory is 0.2.1, and the stack wants >=0.3.0; install a qory in that range, or contact its owner"
		wantMasked(t, cmd.ExitVersion, fmt.Sprintf(line, keyPhrase), "harness", "compose", "-f", stack(maskKey, "qory.dev/v1alpha1", ">=0.3.0"))
		plain := stack("plain", "qory.dev/v1alpha1", ">=0.3.0")
		wantPlain(t, cmd.ExitVersion, fmt.Sprintf(line, plain), "harness", "compose", "-f", plain)
	})

	for _, c := range []struct{ name, apiVersion, row string }{
		{"a range not checked", "qory.dev/v1alpha1", "not checked against >=0.1.0 in %s)"},
		{"a retired apiVersion", "qory.ai/v1alpha1", "%s  (apiVersion qory.ai/v1alpha1; read as qory.dev/v1alpha1, the line to write)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, name := range []string{maskKey, "plain"} {
				stdout, stderr, err := runSplit(t, "", "harness", "compose", "-f", stack(name, c.apiVersion, ">=0.1.0"))
				if err != nil {
					t.Fatalf("%v\n%s", err, stderr)
				}

				shown := keyPhrase
				if name == "plain" {
					shown = filepath.Join("plain", "qory.yaml")
				} else {
					wantNoKey(t, stdout, stderr)
				}

				wants(t, stdout, fmt.Sprintf(c.row, shown))
			}
		})
	}
}

// TestMaskTheHomeAndFileOfARun is qory run with a key in --home, whose report is for
// another checkout or lies under a file, and with a key in the path of -f, whose range
// leaves out this qory.
func TestMaskTheHomeAndFileOfARun(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	walledForager(t)

	t.Run("--home", func(t *testing.T) {
		const line = "remove the harness report %[1]s, or give --home the home of the checkout %[2]s. The report is for that checkout, and %[3]s is not its home; a run uses only its checkout's home, so the run does not start"

		for _, name := range []string{maskKey, "plain"} {
			outside := filepath.Join(tempDir(t), name)
			if out, err := run(t, "hc", "--runtime", "claude", "--home", outside); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}

			home := filepath.Join(outside, checkout.Key(root))
			path := home + "-report.json"
			rep, err := report.Read(path)
			if err != nil {
				t.Fatal(err)
			}
			rep.Checkout = os.Getenv("HOME")
			if err := report.Write(path, rep); err != nil {
				t.Fatal(err)
			}

			if name == maskKey {
				wantMasked(t, cmd.ExitInput, fmt.Sprintf(line, keyPhrase, os.Getenv("HOME"), keyPhrase), "run", "claude", "--home", home)
			} else {
				wantPlain(t, cmd.ExitInput, fmt.Sprintf(line, path, os.Getenv("HOME"), home), "run", "claude", "--home", home)
			}
		}
	})

	t.Run("--home under a file", func(t *testing.T) {
		for _, name := range []string{maskKey, "plain"} {
			file := filepath.Join(tempDir(t), name)
			writeFile(t, file, "x")
			home := filepath.Join(file, "home")

			if name == maskKey {
				wantMasked(t, 1, "open "+keyPhrase+": not a directory", "run", "claude", "--home", home)
			} else {
				wantPlain(t, 1, "open "+home+"-report.json: not a directory", "run", "claude", "--home", home)
			}
		}
	})

	t.Run("-f", func(t *testing.T) {
		release(t, "0.2.1")

		const line = "%s: this qory is 0.2.1, and the stack wants >=0.3.0; install a qory in that range, or contact its owner"

		for _, name := range []string{maskKey, "plain"} {
			dir := filepath.Join(tempDir(t), name)
			writeManifest(t, filepath.Join(dir, "modules", "app"), "app")
			file := filepath.Join(dir, "qory.yaml")
			writeFile(t, file, ownStack(appStack)+"qory: \">=0.3.0\"\n")

			if name == maskKey {
				wantMasked(t, cmd.ExitVersion, fmt.Sprintf(line, keyPhrase), "run", "claude", "-f", file)
			} else {
				wantPlain(t, cmd.ExitVersion, fmt.Sprintf(line, file), "run", "claude", "-f", file)
			}
		}
	})
}
