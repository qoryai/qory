package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/qoryai/forager/accesskey"
	"github.com/spf13/pflag"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/foragerdir"
)

// The help of qory run pool, as it was approved.
const (
	poolShort = "Claim this pool's runs from Qory Apiary and run them on this machine"
	poolLong  = "Run this machine as a node pool's program, until it is stopped. It claims the pool's dispatches from Qory Apiary with the pool's access key, and starts each with the command that runs on a machine a provisioner creates: qory run --run-code-file /dev/fd/3 --gateway <gateway_url>, with the run code in a file that no name links to. The pool's access key does not reach the run, and each run reports its outcome to Qory Apiary. While a run runs, the pool sends dispatched every 60 s. It stops the run when Qory Apiary ends the dispatch or the claim's max_lifetime_minutes pass. Once the run ends, the pool uploads the run's output as the dispatch's machine log: at most the last 768 KiB, with each line that contains qak_, qrk_, qecr_, qecn_, qecp_, qecg_ or qeci_ replaced. It releases a claim that sets a machine type, and a claim it could not start. The pool section of forager.yaml sets it: runs, registry, run_config. SIGINT or SIGTERM stops it: it claims no new run, stops the runs it started, and exits 0."
)

// poolMachine is a pool's machine: its server, the runs' configuration directory, the
// pool's folder and forager.yaml.
type poolMachine struct {
	srv         *fakeServer
	runs        string
	state, file string
}

// newPoolMachine makes a pool's machine in the environment [emptyDir] sets: a server
// whose discovery is of a node pool, forager.yaml with its URL and the pool section
// lines, the secret beside it, and a runs' configuration directory whose forager.yaml has
// a wall alone. lines may contain {runs}, its path; nil lines are a pool section of the
// registry and the runs' directory.
func newPoolMachine(t *testing.T, lines ...string) *poolMachine {
	t.Helper()
	emptyDir(t)
	m := &poolMachine{srv: newFakeServer(t, ""), runs: tempDir(t)}
	m.srv.node = testPool
	writeFile(t, filepath.Join(m.runs, "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\nwall:\n  adapter: docker\n  image: "+agentImage+"\n  images_allow: [localhost:5001/qory-env/]\n")
	if lines == nil {
		lines = []string{"registry: localhost:5001/qory-env", "run_config: {runs}"}
	}
	section := "pool:\n"
	for _, l := range lines {
		section += "  " + strings.ReplaceAll(l, "{runs}", m.runs) + "\n"
	}
	serverFile(t, m.srv, section)
	m.state = filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "pool")
	m.file = filepath.Join(string(configDir()), "forager.yaml")
	return m
}

// started records what qory run pool hands the loop, until the test ends.
func started(t *testing.T) *[]cmd.PoolStart {
	t.Helper()
	var got []cmd.PoolStart
	t.Cleanup(cmd.SetStartPool(func(s cmd.PoolStart) error {
		got = append(got, s)
		return nil
	}))
	return &got
}

// noKeyIn fails the test for each text that holds an access key secret, or the secret of
// k.
func noKeyIn(t *testing.T, k *accesskey.Key, texts ...string) {
	t.Helper()
	for _, text := range texts {
		if accesskey.ContainsSecret(text) || strings.Contains(text, k.Secret()) {
			t.Errorf("a secret was printed or returned: %q", text)
		}
	}
}

// TestRunPoolIsHidden is the verb: in the tree, hidden from qory run's help and so from
// the command reference, with the help as approved and --access-key-secret-fd its one
// flag.
func TestRunPoolIsHidden(t *testing.T) {
	c, _, _ := cmd.Root().Find([]string{"run", "pool"})
	if c == nil || c.Name() != "pool" || !c.Hidden {
		t.Fatal("qory run pool is not a hidden verb")
	}
	if c.Short != poolShort {
		t.Errorf("Short is %q, want %q", c.Short, poolShort)
	}
	if long := strings.Join(strings.Fields(c.Long), " "); long != poolLong {
		t.Errorf("Long is %q, want %q", long, poolLong)
	}
	var flags []string
	c.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) { flags = append(flags, f.Name) })
	if strings.Join(flags, " ") != "access-key-secret-fd" {
		t.Errorf("the flags are %v, want access-key-secret-fd alone", flags)
	}
	out, err := run(t, "run", "--help")
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, out, "pool")
	if _, err := os.Stat(filepath.Join("..", "docs", "commands", "qory_run_pool.md")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the command reference has a page of qory run pool: %v", err)
	}
}

// TestRunPoolStartsWithThePoolsSettings is qory run pool on a pool's machine: it hands
// the loop forager.yaml's pool section with its defaults, the runs' forager.yaml, the
// pool's folder under the state directory, made 0700, the instance it signs as, the
// signed discovery of a node pool, the access key and the pin, and holds the pool's lock
// while the loop runs. It prints nothing, and no secret anywhere. An entry of the runs'
// wall.images_allow with a slash after pool.registry covers it.
func TestRunPoolStartsWithThePoolsSettings(t *testing.T) {
	m := newPoolMachine(t)
	var lockedDuring error
	var got []cmd.PoolStart
	t.Cleanup(cmd.SetStartPool(func(s cmd.PoolStart) error {
		got = append(got, s)
		l, err := foragerdir.LockFile(filepath.Join(s.State, "pool.lock"))
		l.Release()
		lockedDuring = err
		return nil
	}))
	stdout, stderr, err := runSplit(t, "", "run", "pool")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if stdout != "" || stderr != "" {
		t.Errorf("printed %q and %q", stdout, stderr)
	}
	if len(got) != 1 {
		t.Fatalf("the loop started %d times", len(got))
	}
	s := got[0]
	if s.File != m.file || s.Pool.Runs != 1 || s.Pool.Registry != "localhost:5001/qory-env" || s.Pool.RunConfig != m.runs {
		t.Errorf("the loop got %s and the pool section %+v", s.File, s.Pool)
	}
	if s.Runs == nil || s.Runs.File != filepath.Join(m.runs, "qory", "forager.yaml") || s.Runs.Wall == nil || s.Runs.Wall.Image != agentImage {
		t.Errorf("the loop got the runs' forager.yaml %+v", s.Runs)
	}
	if s.State != m.state {
		t.Errorf("the pool's folder is %s, want %s", s.State, m.state)
	}
	if info, err := os.Stat(m.state); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the pool's folder: %v, %v", info, err)
	}
	if id, ok := configDir().ReadInstanceID(foragerdir.MachineID()); !ok || s.InstanceID != id || s.InstanceName == "" {
		t.Errorf("the loop signs as %s (%s), and instance-id holds %s", s.InstanceID, s.InstanceName, id)
	}
	if s.NodeID != testPool || s.RunURL == "" {
		t.Errorf("the discovery names %s and the run endpoint %q", s.NodeID, s.RunURL)
	}
	if s.Key == nil || s.Key.PublicKey() != m.srv.key.PublicKey() || len(s.Pin) != 1 || s.Pin[0].PublicKey != m.srv.signer.PublicKey().String() {
		t.Error("the loop got another key or pin")
	}
	if !errors.Is(lockedDuring, foragerdir.ErrLocked) {
		t.Errorf("the pool's lock was free while the loop ran: %v", lockedDuring)
	}
	l, err := foragerdir.LockFile(filepath.Join(m.state, "pool.lock"))
	if err != nil {
		t.Errorf("the pool's lock is held after the loop: %v", err)
	}
	l.Release()
	noKeyIn(t, m.srv.key, stdout, stderr)
	for path, content := range snapshot(t, m.state) {
		noKeyIn(t, m.srv.key, path, content)
	}

	// Set, runs is the file's; an entry of wall.images_allow without its slash covers
	// pool.registry too.
	m = newPoolMachine(t, "runs: 3", "registry: localhost:5001/qory-env", "run_config: {runs}")
	writeFile(t, filepath.Join(m.runs, "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\nwall:\n  adapter: docker\n  image: "+agentImage+"\n  images_allow: [localhost:5001/qory-env]\n")
	got = nil
	if _, err := run(t, "run", "pool"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Pool.Runs != 3 {
		t.Errorf("the loop got %+v", got)
	}
}

// TestRunPoolTakesTheKeyFromItsDescriptor signs with the secret --access-key-secret-fd
// gives, with no access-key-secret on the machine, and prints it nowhere.
func TestRunPoolTakesTheKeyFromItsDescriptor(t *testing.T) {
	m := newPoolMachine(t)
	if err := os.Remove(configDir().Path(foragerdir.SecretFile)); err != nil {
		t.Fatal(err)
	}
	got := started(t)
	var p [2]int
	if err := syscall.Pipe(p[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := syscall.Write(p[1], []byte(m.srv.key.Secret()+"\n")); err != nil {
		t.Fatal(err)
	}
	syscall.Close(p[1])
	out, err := run(t, "run", "pool", "--access-key-secret-fd", strconv.Itoa(p[0]))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(*got) != 1 || (*got)[0].Key.PublicKey() != m.srv.key.PublicKey() {
		t.Fatalf("the loop got %+v", *got)
	}
	noKeyIn(t, m.srv.key, out)
}

// TestRunPoolTurnsCoreDumpsOffFirst turns core dumps off before anything: before the
// access key's descriptor is read, while it is still open, and a failure to turn them
// off ends qory run pool before the loop starts.
func TestRunPoolTurnsCoreDumpsOffFirst(t *testing.T) {
	m := newPoolMachine(t)
	if err := os.Remove(configDir().Path(foragerdir.SecretFile)); err != nil {
		t.Fatal(err)
	}
	for _, refuse := range []bool{false, true} {
		got := started(t)
		var p [2]int
		if err := syscall.Pipe(p[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := syscall.Write(p[1], []byte(m.srv.key.Secret()+"\n")); err != nil {
			t.Fatal(err)
		}
		syscall.Close(p[1])
		calls, openThen := 0, false
		refusal := errors.New("core dumps could not be turned off: a test")
		restore := cmd.SetNoCoreDumps(func() error {
			calls++
			var st syscall.Stat_t
			openThen = syscall.Fstat(p[0], &st) == nil
			if refuse {
				return refusal
			}
			return nil
		})
		out, err := run(t, "run", "pool", "--access-key-secret-fd", strconv.Itoa(p[0]))
		restore()
		if calls != 1 || !openThen {
			t.Errorf("core dumps were turned off %d times, the descriptor open then %v", calls, openThen)
		}
		switch {
		case refuse && (!errors.Is(err, refusal) || len(*got) != 0):
			t.Errorf("a failure to turn core dumps off: %v, the loop got %d", err, len(*got))
		case !refuse && (err != nil || len(*got) != 1):
			t.Errorf("%v, the loop got %d\n%s", err, len(*got), out)
		}
		if refuse {
			syscall.Close(p[0])
		}
		noKeyIn(t, m.srv.key, out)
	}
}

// noCover ends the refusal of a run_config whose wall.images_allow does not cover
// pool.registry.
const noCover = ", whose forager.yaml's wall.images_allow does not cover pool.registry; a run could not start the image built for it"

// noFile, noWall, noImage and notPinned end the refusals of a run_config that holds no
// forager.yaml, whose forager.yaml sets no wall, whose wall sets no image, and whose
// wall.image is not pinned by its digest.
const (
	noFile    = ", which holds no qory/forager.yaml; each run starts behind the wall it sets"
	noWall    = ", whose forager.yaml sets no wall; each run starts behind the wall it sets"
	noImage   = ", whose forager.yaml's wall sets no image; give it one pinned by its digest, name@sha256: and 64 lower-case hexadecimal digits"
	notPinned = ", whose forager.yaml's wall.image is not pinned by its digest, name@sha256: and 64 lower-case hexadecimal digits, with no tag"
)

// agentImage is the runs' wall.image, pinned by its digest.
const agentImage = "registry.example.com/agent@sha256:1111111111111111111111111111111111111111111111111111111111111111"

// TestRunPoolRefusesWhatItCannotRunBy refuses, as input, before the loop starts and with
// no secret in what it prints: forager.yaml without a pool section or a server, a pool
// section with a key that earlier releases read, a run_config whose qory folder holds an access key or no forager.yaml, whose forager.yaml has a gateway
// section or does not read, sets no wall, a wall with no image, a wall.image not pinned
// by its digest, by an upper-case one or with a tag, or whose wall.images_allow does not
// cover pool.registry, a run_config that is no folder, and an access key that is not a
// node pool's.
func TestRunPoolRefusesWhatItCannotRunBy(t *testing.T) {
	for _, c := range []struct {
		name  string
		lines []string
		setup func(t *testing.T, m *poolMachine)
		want  func(m *poolMachine) string
	}{
		{"no pool section", nil, func(t *testing.T, m *poolMachine) {
			serverFile(t, m.srv, "")
		}, func(m *poolMachine) string { return m.file + " has no pool section" }},
		{"no server", nil, func(t *testing.T, m *poolMachine) {
			writeFile(t, m.file, "apiVersion: qory.dev/v1alpha1\npool:\n  registry: localhost:5001/qory-env\n  run_config: "+m.runs+"\n")
		}, func(*poolMachine) string { return "forager.yaml defines no server to claim the pool's runs from" }},
		{"a relative run_config", []string{"registry: localhost:5001/qory-env", "run_config: runs"}, nil,
			func(m *poolMachine) string { return m.file + ": pool.run_config is not an absolute path" }},
		{"prebuilds", []string{"registry: localhost:5001/qory-env", "prebuilds: 1", "run_config: {runs}"}, nil,
			func(m *poolMachine) string {
				return m.file + ": line 9: key \"prebuilds\" is not one forager.yaml reads"
			}},
		{"harness_ref", []string{"registry: localhost:5001/qory-env", "harness_ref: v1.2.3", "run_config: {runs}"}, nil,
			func(m *poolMachine) string {
				return m.file + ": line 9: key \"harness_ref\" is not one forager.yaml reads"
			}},
		{"harness_path", []string{"registry: localhost:5001/qory-env", "harness_path: /src/qory-github", "run_config: {runs}"}, nil,
			func(m *poolMachine) string {
				return m.file + ": line 9: key \"harness_path\" is not one forager.yaml reads"
			}},
		{"an access key in run_config", nil, func(t *testing.T, m *poolMachine) {
			if err := foragerdir.Dir(filepath.Join(m.runs, "qory")).WriteSecret(newKey(t)); err != nil {
				t.Fatal(err)
			}
		}, func(m *poolMachine) string {
			return m.file + ": pool.run_config is " + m.runs + ", which holds an access key; the runs' directory holds none"
		}},
		{"a key moved aside in run_config", nil, func(t *testing.T, m *poolMachine) {
			writeFile(t, filepath.Join(m.runs, "qory", foragerdir.OldPrefix+"1700000000"), "")
		}, func(m *poolMachine) string {
			return m.file + ": pool.run_config is " + m.runs + ", which holds an access key; the runs' directory holds none"
		}},
		{"run_config is the pool's", nil, func(t *testing.T, m *poolMachine) {
			serverFile(t, m.srv, "pool:\n  registry: localhost:5001/qory-env\n  run_config: "+filepath.Dir(string(configDir()))+"\n")
		}, func(m *poolMachine) string {
			return m.file + ": pool.run_config is " + filepath.Dir(string(configDir())) + ", which holds an access key; the runs' directory holds none"
		}},
		{"a gateway section in run_config", nil, func(t *testing.T, m *poolMachine) {
			writeFile(t, filepath.Join(m.runs, "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\ngateway:\n  egress: {mode: observe}\n")
		}, func(m *poolMachine) string {
			return m.file + ": pool.run_config is " + m.runs + ", whose forager.yaml has a gateway section; the runs' directory has none"
		}},
		{"run_config's forager.yaml does not read", nil, func(t *testing.T, m *poolMachine) {
			writeFile(t, filepath.Join(m.runs, "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\nwall: {}\n")
		}, func(m *poolMachine) string {
			return filepath.Join(m.runs, "qory", "forager.yaml") + ": wall.adapter is required, and docker is the one there is"
		}},
		{"run_config without forager.yaml", nil, func(t *testing.T, m *poolMachine) {
			if err := os.Remove(filepath.Join(m.runs, "qory", "forager.yaml")); err != nil {
				t.Fatal(err)
			}
		}, func(m *poolMachine) string { return m.file + ": pool.run_config is " + m.runs + noFile }},
		{"run_config is not there", nil, func(t *testing.T, m *poolMachine) {
			if err := os.RemoveAll(m.runs); err != nil {
				t.Fatal(err)
			}
		}, func(m *poolMachine) string {
			return m.file + ": pool.run_config is " + m.runs + ", which is not a folder"
		}},
		{"run_config is a file", nil, func(t *testing.T, m *poolMachine) {
			if err := os.RemoveAll(m.runs); err != nil {
				t.Fatal(err)
			}
			writeFile(t, m.runs, "not a folder\n")
		}, func(m *poolMachine) string {
			return m.file + ": pool.run_config is " + m.runs + ", which is not a folder"
		}},
		{"run_config without a wall section", nil, func(t *testing.T, m *poolMachine) {
			writeFile(t, filepath.Join(m.runs, "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\n")
		}, func(m *poolMachine) string { return m.file + ": pool.run_config is " + m.runs + noWall }},
		{"a wall.image by its name", nil, func(t *testing.T, m *poolMachine) {
			writeFile(t, filepath.Join(m.runs, "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\nwall:\n  adapter: docker\n  image: agent\n  images_allow: [localhost:5001/qory-env/]\n")
		}, func(m *poolMachine) string { return m.file + ": pool.run_config is " + m.runs + notPinned }},
		{"a wall.image by its tag", nil, func(t *testing.T, m *poolMachine) {
			writeFile(t, filepath.Join(m.runs, "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\nwall:\n  adapter: docker\n  image: registry.example.com/agent:1.0\n  images_allow: [localhost:5001/qory-env/]\n")
		}, func(m *poolMachine) string { return m.file + ": pool.run_config is " + m.runs + notPinned }},
		{"a wall without an image", nil, func(t *testing.T, m *poolMachine) {
			writeFile(t, filepath.Join(m.runs, "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\nwall:\n  adapter: docker\n")
		}, func(m *poolMachine) string { return m.file + ": pool.run_config is " + m.runs + noImage }},
		{"a wall.image by an upper-case digest", nil, func(t *testing.T, m *poolMachine) {
			writeFile(t, filepath.Join(m.runs, "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\nwall:\n  adapter: docker\n  image: registry.example.com/agent@sha256:"+strings.Repeat("A", 64)+"\n  images_allow: [localhost:5001/qory-env/]\n")
		}, func(m *poolMachine) string { return m.file + ": pool.run_config is " + m.runs + notPinned }},
		{"a wall.image by its tag and digest", nil, func(t *testing.T, m *poolMachine) {
			writeFile(t, filepath.Join(m.runs, "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\nwall:\n  adapter: docker\n  image: registry.example.com/agent:1.0@sha256:"+strings.Repeat("1", 64)+"\n  images_allow: [localhost:5001/qory-env/]\n")
		}, func(m *poolMachine) string { return m.file + ": pool.run_config is " + m.runs + notPinned }},
		{"run_config without wall.images_allow", nil, func(t *testing.T, m *poolMachine) {
			writeFile(t, filepath.Join(m.runs, "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\nwall:\n  adapter: docker\n  image: "+agentImage+"\n")
		}, func(m *poolMachine) string { return m.file + ": pool.run_config is " + m.runs + noCover }},
		{"a wall.images_allow that covers another registry", nil, func(t *testing.T, m *poolMachine) {
			writeFile(t, filepath.Join(m.runs, "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\nwall:\n  adapter: docker\n  image: "+agentImage+"\n  images_allow: [localhost:5001/qory-env-other/, localhost:5002/qory-env/]\n")
		}, func(m *poolMachine) string { return m.file + ": pool.run_config is " + m.runs + noCover }},
		{"a machine's key", nil, func(_ *testing.T, m *poolMachine) { m.srv.node = testNode },
			func(*poolMachine) string {
				return "this machine's key is not a pool's key; enrol one with a code made for the pool"
			}},
		{"a gateway's key", nil, func(_ *testing.T, m *poolMachine) { m.srv.node = testGatewayNode },
			func(*poolMachine) string {
				return "this machine's key is not a pool's key; enrol one with a code made for the pool"
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := newPoolMachine(t, c.lines...)
			if c.setup != nil {
				c.setup(t, m)
			}
			got := started(t)
			out, err := run(t, "run", "pool")
			if err == nil || err.Error() != c.want(m) {
				t.Errorf("%v, want %q", err, c.want(m))
			}
			if cmd.ExitCode(err) != cmd.ExitInput {
				t.Errorf("%v exits %d, want %d", err, cmd.ExitCode(err), cmd.ExitInput)
			}
			if len(*got) != 0 {
				t.Error("the loop started")
			}
			if err == nil {
				t.Fatal("qory run pool was not refused")
			}
			noKeyIn(t, m.srv.key, out, err.Error())
		})
	}
}

// noVia ends the refusal of a forager.yaml that sets gateway.server.via.
const noVia = ": gateway.server.via does nothing for qory run pool: the pool program reaches the server directly; remove gateway.server.via"

// TestRunPoolRefusesGatewayServerVia refuses, as input, a gateway.server.via in the
// pool's forager.yaml or in run_config's, with the one line, before the server is sent
// anything and before the loop starts.
func TestRunPoolRefusesGatewayServerVia(t *testing.T) {
	const via = "    via:\n      url: https://gateway.example.com:8443\n"
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, m *poolMachine)
		file  func(m *poolMachine) string
	}{
		{"in the pool's forager.yaml", func(t *testing.T, m *poolMachine) {
			serverFile(t, m.srv, via+"pool:\n  registry: localhost:5001/qory-env\n  run_config: "+m.runs+"\n")
		}, func(m *poolMachine) string { return m.file }},
		{"in run_config's forager.yaml", func(t *testing.T, m *poolMachine) {
			writeFile(t, filepath.Join(m.runs, "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\ngateway:\n  server:\n    url: https://apiary.example.com\n"+via+
				"wall:\n  adapter: docker\n  image: "+agentImage+"\n  images_allow: [localhost:5001/qory-env/]\n")
		}, func(m *poolMachine) string { return filepath.Join(m.runs, "qory", "forager.yaml") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := newPoolMachine(t)
			c.setup(t, m)
			got := started(t)
			out, err := run(t, "run", "pool")
			if want := c.file(m) + noVia; err == nil || err.Error() != want {
				t.Errorf("%v, want %q", err, want)
			}
			if cmd.ExitCode(err) != cmd.ExitInput {
				t.Errorf("%v exits %d, want %d", err, cmd.ExitCode(err), cmd.ExitInput)
			}
			if len(*got) != 0 {
				t.Error("the loop started")
			}
			m.srv.mu.Lock()
			sent := len(m.srv.instances)
			m.srv.mu.Unlock()
			if sent != 0 {
				t.Errorf("the server was sent %d requests", sent)
			}
			if err == nil {
				t.Fatal("qory run pool was not refused")
			}
			noKeyIn(t, m.srv.key, out, err.Error())
		})
	}
}

// TestRunPoolRunsOnceAtATime refuses a second qory run pool with the same state
// directory while the first holds the pool's lock, before it signs anything.
func TestRunPoolRunsOnceAtATime(t *testing.T) {
	m := newPoolMachine(t)
	if err := os.MkdirAll(m.state, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := foragerdir.LockFile(filepath.Join(m.state, "pool.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	got := started(t)
	before := len(m.srv.instances)
	out, err := run(t, "run", "pool")
	if want := "another qory run pool is running with " + m.state; err == nil || err.Error() != want || out != "" {
		t.Errorf("%v, printed %q, want %q", err, out, want)
	}
	if err == nil || cmd.ExitCode(err) == cmd.ExitInput || len(*got) != 0 {
		t.Errorf("%v exits %d, and the loop started %d times", err, cmd.ExitCode(err), len(*got))
	}
	if len(m.srv.instances) != before {
		t.Error("the server was sent a request")
	}
}
