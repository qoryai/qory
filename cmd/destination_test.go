//go:build unix

package cmd_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// The destinations of [destinationSession]: two repositories, a place with no address,
// a place whose address is no repository path, and an id that the inputs do not list.
const (
	webID      = "tg_web0000000000000"
	backID     = "tg_back000000000000"
	deskID     = "tg_desk000000000000"
	pathID     = "tg_path000000000000"
	unlistedID = "tg_zzzz000000000000"
)

// clones is a session of a run whose inputs list the destinations above, on the server s,
// which serves acme/web and acme/back at https://git.test through a stand-in of the
// wall's proxy, with the proxy and its certificate authority in the environment, as the
// wall gives them. root is the folder of the clones, which does not exist yet.
type clones struct {
	s    *proxygittest.Server
	root string
	home string
}

// destinationSession starts the server and sets up the session's environment.
func destinationSession(t *testing.T) *clones {
	t.Helper()
	emptyDir(t)
	s := proxygittest.Start(t)
	s.Repository(t, "acme/web", "main", map[string]string{"README.md": "web\n"})
	s.Repository(t, "acme/back", "main", map[string]string{"README.md": "back\n"})
	c := &clones{s: s, root: filepath.Join(tempDir(t), "qory-destinations"), home: tempDir(t)}
	t.Cleanup(cmd.SetDestinationsDir(c.root))
	t.Setenv("QORY_RUN_API", "http://gateway.invalid/v1/runs/run-1")
	t.Setenv("QORY_HARNESS_HOME", c.home)
	for _, name := range []string{"HTTPS_PROXY", "https_proxy"} {
		t.Setenv(name, s.Proxy.URL)
	}
	for _, name := range []string{"ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy", "HTTP_PROXY", "http_proxy"} {
		t.Setenv(name, "")
	}
	t.Setenv("GIT_SSL_CAINFO", s.Proxy.CABundle)
	c.inputs(t, []any{
		destinationOf(webID, "acme/web"),
		destinationOf(backID, "acme/back"),
		map[string]any{"id": deskID, "target": map[string]any{"collection": "desk-7"}},
		destinationOf(pathID, "acme/a b"),
	})
	return c
}

// destinationOf is a destination with the id and the address path on git.test.
func destinationOf(id, path string) map[string]any {
	return map[string]any{"id": id, "target": map[string]any{"collection": "C_" + id[3:7], "address": map[string]any{"system": proxygittest.Host, "path": path}}}
}

// inputs writes inputs.json into the home with the destinations, and without the member
// for nil.
func (c *clones) inputs(t *testing.T, destinations any) {
	t.Helper()
	in := map[string]any{"version": 1, "qory": map[string]any{"harness_extensions": map[string]any{}}}
	if destinations != nil {
		in["destinations"] = destinations
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(c.home, "inputs.json"), string(data))
}

// line is the line qory destination clone prints for id at path.
func (c *clones) line(id, path string) string {
	return id + " " + filepath.Join(c.root, proxygittest.Host, path) + "\n"
}

// wantLine fails unless err exits with status and its text is want, with a first line of
// at most 98 characters, 100 with the mark that qory prints before it, and no path of the
// machine, since a member reads it in Qory Apiary, and no URL.
func wantLine(t *testing.T, err error, status int, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %q", want)
	}
	if got := cmd.ExitCode(err); got != status {
		t.Errorf("exit status %d, want %d: %v", got, status, err)
	}
	if err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
	first, _, _ := strings.Cut(err.Error(), "\n")
	if n := utf8.RuneCountInString("✗ " + first); n > 100 {
		t.Errorf("line 1 has %d characters with its mark: %q", n, first)
	}
	if strings.Contains(first, "/") {
		t.Errorf("line 1 contains a path: %q", first)
	}
	for _, part := range []string{"://", proxygittest.Password} {
		if strings.Contains(err.Error(), part) {
			t.Errorf("error %q contains %s", err, part)
		}
	}
}

// TestDestinationCloneClonesEachIDOnce is a session that starts with nothing cloned:
// qory destination clone clones a listed id at depth one into the folder of its system
// and path and prints its line, and a second call for it clones nothing and prints the
// same line. A call with two ids prints a line for each, in the order given.
func TestDestinationCloneClonesEachIDOnce(t *testing.T) {
	c := destinationSession(t)
	if _, err := os.Lstat(c.root); !os.IsNotExist(err) {
		t.Fatalf("the folder of the clones exists before a call: %v", err)
	}
	out, err := run(t, "destination", "clone", webID)
	if err != nil {
		t.Fatal(err)
	}
	if want := c.line(webID, "acme/web"); out != want {
		t.Errorf("printed %q, want %q", out, want)
	}
	dir := filepath.Join(c.root, proxygittest.Host, "acme", "web")
	wantFile(t, filepath.Join(dir, "README.md"), "web\n")
	if got := gitOut(t, dir, "rev-list", "--count", "HEAD"); got != "1" {
		t.Errorf("the clone has %s commits, want 1 at depth one", got)
	}
	tunnels := c.s.Tunnels()
	if tunnels == 0 {
		t.Fatal("the clone did not go through the proxy")
	}

	out, err = run(t, "destination", "clone", webID)
	if err != nil {
		t.Fatal(err)
	}
	if want := c.line(webID, "acme/web"); out != want {
		t.Errorf("second call printed %q, want %q", out, want)
	}
	if got := c.s.Tunnels(); got != tunnels {
		t.Errorf("second call opened %d tunnels, want none", got-tunnels)
	}

	out, err = run(t, "destination", "clone", backID, webID)
	if err != nil {
		t.Fatal(err)
	}
	if want := c.line(backID, "acme/back") + c.line(webID, "acme/web"); out != want {
		t.Errorf("printed %q, want %q", out, want)
	}
	wantFile(t, filepath.Join(c.root, proxygittest.Host, "acme", "back", "README.md"), "back\n")
	wantFile(t, filepath.Join(c.root, "_cloned"), webID+"\n"+backID+"\n")
}

// TestDestinationCloneStopsBeforeAnyClone is each call that stops before it clones, exit
// 2, with its line: an id the inputs do not list, also after a listed one, and an
// argument that is no id, cut to the length of one; a place with no address; an address
// that is no repository path; a call outside a session; and inputs with no destinations.
func TestDestinationCloneStopsBeforeAnyClone(t *testing.T) {
	c := destinationSession(t)
	for _, k := range []struct {
		args []string
		want string
	}{
		{[]string{unlistedID}, "Use an id from destinations in the run's inputs: " + unlistedID + " is not one of them."},
		{[]string{webID, unlistedID}, "Use an id from destinations in the run's inputs: " + unlistedID + " is not one of them."},
		{[]string{strings.Repeat("x", 40)}, "Use an id from destinations in the run's inputs: xxxxxxxxxxxxxxxx... is not one of them."},
		{[]string{deskID}, "Clone only a destination with an address: " + deskID + " has none, so it has no repository."},
		{[]string{pathID}, "Clone only a repository: the address of " + pathID + " is not a repository path."},
	} {
		_, err := run(t, append([]string{"destination", "clone"}, k.args...)...)
		wantLine(t, err, cmd.ExitInput, k.want)
	}
	_, err := run(t, "destination", "clone")
	if cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("no id: %v (exit %d), want exit 2", err, cmd.ExitCode(err))
	}
	if n := c.s.Tunnels(); n != 0 {
		t.Errorf("the calls opened %d tunnels", n)
	}

	t.Setenv("QORY_HARNESS_HOME", "")
	_, err = run(t, "destination", "clone", webID)
	wantLine(t, err, cmd.ExitInput, "Run qory destination clone inside a run's session: QORY_HARNESS_HOME is not set.")
	t.Setenv("QORY_RUN_API", "")
	_, err = run(t, "destination", "clone", webID)
	wantLine(t, err, cmd.ExitInput, "Run qory destination clone inside a run's session: QORY_RUN_API is not set.")

	t.Setenv("QORY_RUN_API", "http://gateway.invalid/v1/runs/run-1")
	t.Setenv("QORY_HARNESS_HOME", c.home)
	c.inputs(t, nil)
	_, err = run(t, "destination", "clone", webID)
	wantLine(t, err, cmd.ExitInput, "Run qory destination clone only in a run whose inputs list destinations.")
	if err := os.Remove(filepath.Join(c.home, "inputs.json")); err != nil {
		t.Fatal(err)
	}
	_, err = run(t, "destination", "clone", webID)
	wantLine(t, err, cmd.ExitInput, "Run qory destination clone only in a run whose inputs list destinations.")
	if _, err := os.Lstat(c.root); !os.IsNotExist(err) {
		t.Errorf("a call that stopped made the folder of the clones: %v", err)
	}
}

// TestDestinationCloneStopsOnMalformedDestinations is inputs whose destinations break
// the shape of the explore inputs: exit 1, with the cause on line 2. An empty or null
// list, and a member whose name differs only in case, list no destinations: exit 2.
func TestDestinationCloneStopsOnMalformedDestinations(t *testing.T) {
	c := destinationSession(t)
	const none = "Run qory destination clone only in a run whose inputs list destinations."
	for _, destinations := range []any{[]any{}, json.RawMessage("null")} {
		c.inputs(t, destinations)
		_, err := run(t, "destination", "clone", webID)
		wantLine(t, err, cmd.ExitInput, none)
	}
	writeFile(t, filepath.Join(c.home, "inputs.json"), `{"Destinations": [{"id": "`+webID+`", "target": {"collection": "c"}}]}`)
	_, err := run(t, "destination", "clone", webID)
	wantLine(t, err, cmd.ExitInput, none)

	const fix = "qory destination clone: The run's destinations do not match their schema, so nothing was cloned.\n  "
	for _, k := range []struct {
		destinations any
		cause        string
	}{
		{"acme/web", "destinations is not an array of objects"},
		{[]any{destinationOf("tg_web000000000000i", "acme/web")}, "entry 1 does not match the schema of a destination at /id"},
		{[]any{destinationOf(webID, "acme/web"), destinationOf(webID, "acme/back")}, "entry 2 has the id of an entry before it"},
		{[]any{map[string]any{"id": webID, "target": map[string]any{}}}, "entry 1 does not match the schema of a destination at /target"},
		{[]any{map[string]any{"id": webID, "target": map[string]any{"collection": "c", "address": map[string]any{"system": "Git.test", "path": "acme/web"}}}}, "entry 1 does not match the schema of a destination at /target/address/system"},
		{[]any{destinationOf(webID, "acme/\x00web")}, "entry 1 does not match the schema of a destination at /target/address/path"},
		// The schema stops what the earlier checks of qory let through: a { in a path and
		// U+0000 in a collection. It stops two equal entries and more than 250 entries too.
		{[]any{destinationOf(backID, "acme/back"), destinationOf(webID, "acme/{web}")}, "entry 2 does not match the schema of a destination at /target/address/path"},
		{[]any{map[string]any{"id": webID, "target": map[string]any{"collection": "c\x00"}}}, "entry 1 does not match the schema of a destination at /target/collection"},
		{[]any{destinationOf(webID, "acme/web"), destinationOf(webID, "acme/web")}, "destinations does not match explore/v1 inputs.schema.json#/properties/destinations"},
		{slices.Repeat([]any{destinationOf(webID, "acme/web")}, 251), "destinations does not match explore/v1 inputs.schema.json#/properties/destinations"},
	} {
		c.inputs(t, k.destinations)
		_, err := run(t, "destination", "clone", webID)
		wantLine(t, err, 1, fix+k.cause)
	}
}

// TestDestinationCloneMatchesEachMemberByItsExactName checks that a member whose name
// differs only in case, after each member of an entry, changes nothing in the clone.
func TestDestinationCloneMatchesEachMemberByItsExactName(t *testing.T) {
	c := destinationSession(t)
	writeFile(t, filepath.Join(c.home, "inputs.json"), `{"destinations": [{"id": "`+webID+`", "ID": "`+backID+`",
		"target": {"collection": "c", "COLLECTION": "",
			"address": {"system": "`+proxygittest.Host+`", "SYSTEM": "Git.test", "path": "acme/web", "PATH": "acme/back"},
			"ADDRESS": null},
		"TARGET": {"collection": ""}}]}`)
	out, err := run(t, "destination", "clone", webID)
	if err != nil {
		t.Fatal(err)
	}
	if want := c.line(webID, "acme/web"); out != want {
		t.Errorf("printed %q, want %q", out, want)
	}
	wantFile(t, filepath.Join(c.root, proxygittest.Host, "acme", "web", "README.md"), "web\n")
	if _, err := os.Lstat(filepath.Join(c.root, proxygittest.Host, "acme", "back")); !os.IsNotExist(err) {
		t.Errorf("acme/back is cloned: %v", err)
	}
}

// TestDestinationCloneCountsAtMost100 is a session that cloned 100 destinations: a call
// that would make clone 101 stops before it clones, exit 2, and a call for a destination
// cloned before still prints its line.
func TestDestinationCloneCountsAtMost100(t *testing.T) {
	c := destinationSession(t)
	if _, err := run(t, "destination", "clone", webID); err != nil {
		t.Fatal(err)
	}
	var record strings.Builder
	record.WriteString(webID + "\n")
	for i := 1; i < 100; i++ {
		fmt.Fprintf(&record, "tg_%016d\n", i)
	}
	writeFile(t, filepath.Join(c.root, "_cloned"), record.String())
	tunnels := c.s.Tunnels()

	_, err := run(t, "destination", "clone", backID)
	wantLine(t, err, cmd.ExitInput, "Clone at most 100 destinations in one session: this call would make clone 101.")
	if got := c.s.Tunnels(); got != tunnels {
		t.Errorf("the call opened %d tunnels", got-tunnels)
	}
	if _, err := os.Lstat(filepath.Join(c.root, proxygittest.Host, "acme", "back")); !os.IsNotExist(err) {
		t.Errorf("clone 101 was made: %v", err)
	}
	out, err := run(t, "destination", "clone", webID)
	if err != nil || out != c.line(webID, "acme/web") {
		t.Errorf("a clone of before: %q, %v", out, err)
	}
}

// TestDestinationCloneStopsOnAFailedClone is a clone that fails, exit 1, with no URL in
// the line: a 401, a 403 or a 404 of the host has the line of the integration's access, and
// a 403 or a 407 of the proxy the line of the proxy. Nothing is left in the folder of the
// clones.
func TestDestinationCloneStopsOnAFailedClone(t *testing.T) {
	connection := func(id, status string) string {
		return "Give the integration access to " + id + " in Qory Apiary, then click Run Explore again.\n  The host returned " + status + " to the clone."
	}
	blocks := "qory destination clone: The proxy cannot reach " + webID + "'s host, so nothing was cloned."
	t.Run("403", func(t *testing.T) {
		c := destinationSession(t)
		c.s.Refuse("/acme/web.git/info/refs")
		_, err := run(t, "destination", "clone", webID)
		wantLine(t, err, 1, connection(webID, "403"))
		wantNoClone(t, c)
	})
	t.Run("401", func(t *testing.T) {
		c := destinationSession(t)
		c.s.Require(proxygittest.Credential{Prefix: "acme/web", Username: "u", Password: "p"})
		_, err := run(t, "destination", "clone", webID)
		wantLine(t, err, 1, connection(webID, "401"))
		wantNoClone(t, c)
	})
	t.Run("404", func(t *testing.T) {
		c := destinationSession(t)
		c.inputs(t, []any{destinationOf(webID, "acme/gone")})
		_, err := run(t, "destination", "clone", webID)
		wantLine(t, err, 1, connection(webID, "404"))
		wantNoClone(t, c)
	})
	t.Run("407", func(t *testing.T) {
		c := destinationSession(t)
		wrong := strings.Replace(c.s.Proxy.URL, proxygittest.Password, "wrong-password", 1)
		for _, name := range []string{"HTTPS_PROXY", "https_proxy"} {
			t.Setenv(name, wrong)
		}
		_, err := run(t, "destination", "clone", webID)
		wantLine(t, err, 1, blocks)
		wantNoClone(t, c)
	})
	t.Run("proxy 403", func(t *testing.T) {
		c := destinationSession(t)
		d := destinationOf(webID, "acme/web")
		d["target"].(map[string]any)["address"].(map[string]any)["system"] = "other.test"
		c.inputs(t, []any{d})
		_, err := run(t, "destination", "clone", webID)
		wantLine(t, err, 1, blocks)
		if c.s.Denied() == 0 {
			t.Error("the proxy denied no request")
		}
		wantNoClone(t, c)
	})
}

// TestDestinationCloneRetriesAFailedClone is a clone that failed with a 403 and a call
// for the same id once the host serves it: the second call clones and records the id.
func TestDestinationCloneRetriesAFailedClone(t *testing.T) {
	c := destinationSession(t)
	c.s.Refuse("/acme/web.git/info/refs")
	if _, err := run(t, "destination", "clone", webID); cmd.ExitCode(err) != 1 {
		t.Fatalf("%v (exit %d), want exit 1", err, cmd.ExitCode(err))
	}
	c.s.Refuse("")
	out, err := run(t, "destination", "clone", webID)
	if err != nil || out != c.line(webID, "acme/web") {
		t.Fatalf("the second call: %q, %v", out, err)
	}
	wantFile(t, filepath.Join(c.root, proxygittest.Host, "acme", "web", "README.md"), "web\n")
	wantFile(t, filepath.Join(c.root, "_cloned"), webID+"\n")
}

// TestDestinationCloneTakesAnIDTwice is a call with one id twice: one clone, one entry in
// the record, and the same line twice.
func TestDestinationCloneTakesAnIDTwice(t *testing.T) {
	c := destinationSession(t)
	out, err := run(t, "destination", "clone", webID, webID)
	if err != nil {
		t.Fatal(err)
	}
	if want := c.line(webID, "acme/web") + c.line(webID, "acme/web"); out != want {
		t.Errorf("printed %q, want %q", out, want)
	}
	wantFile(t, filepath.Join(c.root, "_cloned"), webID+"\n")
}

// TestDestinationCloneCallsInParallel is two calls for the same id at once: the record's
// lock lets one clone, the other finds the clone, both print the same line, and the record
// has the id once.
func TestDestinationCloneCallsInParallel(t *testing.T) {
	c := destinationSession(t)
	var wg sync.WaitGroup
	outs, errs := make([]string, 2), make([]error, 2)
	for i := range outs {
		wg.Go(func() { outs[i], errs[i] = run(t, "destination", "clone", webID) })
	}
	wg.Wait()
	for i := range outs {
		if errs[i] != nil || outs[i] != c.line(webID, "acme/web") {
			t.Errorf("call %d: %q, %v", i+1, outs[i], errs[i])
		}
	}
	wantFile(t, filepath.Join(c.root, "_cloned"), webID+"\n")
	entries, _ := os.ReadDir(c.root)
	if names := entryNames(entries); !slices.Equal(names, []string{"_cloned", proxygittest.Host}) {
		t.Errorf("the folder of the clones has %v", names)
	}
}

// entryNames is the names of entries.
func entryNames(entries []os.DirEntry) []string {
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// wantNoClone fails when the folder of the clones has an entry other than the record,
// or the record has an id.
func wantNoClone(t *testing.T, c *clones) {
	t.Helper()
	entries, err := os.ReadDir(c.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "_cloned" {
			t.Errorf("the folder of the clones has %s", e.Name())
		}
	}
	wantFile(t, filepath.Join(c.root, "_cloned"), "")
}

// TestDestinationCloneClonesIntoAFolderOnly is a folder of the clones that is a link:
// the call stops, exit 1, with the call's id on line 1, and clones nothing through the
// link.
func TestDestinationCloneClonesIntoAFolderOnly(t *testing.T) {
	c := destinationSession(t)
	elsewhere := tempDir(t)
	if err := os.Symlink(elsewhere, c.root); err != nil {
		t.Fatal(err)
	}
	_, err := run(t, "destination", "clone", webID)
	wantLine(t, err, 1, "qory destination clone: The machine cannot store "+webID+", so nothing was cloned.\n  "+c.root+": lstat: a link or a file, and qory clones only into folders")
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("the link's folder has %d entries", len(entries))
	}
}

// TestDestinationCloneStopsAtACorruptRecord is a record of the clones with a line that is
// not an id: the call stops before it clones, exit 1, with the clones in place of an id on
// line 1 and the cause on line 2.
func TestDestinationCloneStopsAtACorruptRecord(t *testing.T) {
	c := destinationSession(t)
	if err := os.Mkdir(c.root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(c.root, "_cloned"), "not an id\n")
	_, err := run(t, "destination", "clone", webID)
	wantLine(t, err, 1, "qory destination clone: The machine cannot store the clones, so nothing was cloned.\n  "+c.root+": _cloned has a line that is not an id")
	if n := c.s.Tunnels(); n != 0 {
		t.Errorf("the call opened %d tunnels", n)
	}
}

// TestDestinationCloneFollowsNoLinkBelowTheFolder is a link that something put in the
// folder of the clones, to a folder outside that has web/.git: at git.test/acme, above the
// clone's folder, at git.test/acme/web, the clone's folder, and at git.test/acme/web/.git.
// The call does not take the link for a clone, stops before it clones, exit 1, with the id
// on line 1 and the link's path on line 2, and writes nothing through the link.
func TestDestinationCloneFollowsNoLinkBelowTheFolder(t *testing.T) {
	for _, at := range []string{"acme", "acme/web", "acme/web/.git"} {
		t.Run(at, func(t *testing.T) {
			c := destinationSession(t)
			elsewhere := tempDir(t)
			if err := os.MkdirAll(filepath.Join(elsewhere, "web", ".git"), 0o700); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(c.root, proxygittest.Host, filepath.FromSlash(at))
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, link); err != nil {
				t.Fatal(err)
			}
			out, err := run(t, "destination", "clone", webID)
			wantLine(t, err, 1, "qory destination clone: The machine cannot store "+webID+", so nothing was cloned.\n  "+link+": lstat: a link or a file, and qory clones only into folders")
			if out != "" {
				t.Errorf("printed %q", out)
			}
			if n := c.s.Tunnels(); n != 0 {
				t.Errorf("the call opened %d tunnels", n)
			}
			if names := entryNames(mustReadDir(t, elsewhere)); !slices.Equal(names, []string{"web"}) {
				t.Errorf("the folder behind the link has %v", names)
			}
			if names := entryNames(mustReadDir(t, filepath.Join(elsewhere, "web"))); !slices.Equal(names, []string{".git"}) {
				t.Errorf("web behind the link has %v", names)
			}
			wantFile(t, filepath.Join(c.root, "_cloned"), "")
		})
	}
}

// TestDestinationCloneStopsAtAFolderThatIsNoClone is a folder with a file and no .git at
// the clone's place: the call stops before it clones, exit 1, with the id on line 1 and the
// folder's path on line 2, and the folder keeps its file.
func TestDestinationCloneStopsAtAFolderThatIsNoClone(t *testing.T) {
	c := destinationSession(t)
	dir := filepath.Join(c.root, proxygittest.Host, "acme", "web")
	writeFile(t, filepath.Join(dir, "notes.txt"), "notes\n")
	_, err := run(t, "destination", "clone", webID)
	wantLine(t, err, 1, "qory destination clone: The machine cannot store "+webID+", so nothing was cloned.\n  "+dir+": readdir: a folder with files and no clone")
	if n := c.s.Tunnels(); n != 0 {
		t.Errorf("the call opened %d tunnels", n)
	}
	wantFile(t, filepath.Join(dir, "notes.txt"), "notes\n")
	wantFile(t, filepath.Join(c.root, "_cloned"), "")
}

// TestDestinationCloneClonesIntoAnEmptyFolder is an empty folder at the clone's place: the
// clone takes its place, and the call prints the id's line.
func TestDestinationCloneClonesIntoAnEmptyFolder(t *testing.T) {
	c := destinationSession(t)
	dir := filepath.Join(c.root, proxygittest.Host, "acme", "web")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "destination", "clone", webID)
	if err != nil || out != c.line(webID, "acme/web") {
		t.Fatalf("%q, %v", out, err)
	}
	wantFile(t, filepath.Join(dir, "README.md"), "web\n")
	wantFile(t, filepath.Join(c.root, "_cloned"), webID+"\n")
}

// TestDestinationCloneNeedsGitOnPath is a session whose PATH has no git: the call stops,
// exit 1, with the line for the instance admin and the cause on line 2, opens no tunnel,
// and leaves nothing in the folder of the clones but the record.
func TestDestinationCloneNeedsGitOnPath(t *testing.T) {
	c := destinationSession(t)
	t.Setenv("PATH", tempDir(t))
	_, err := run(t, "destination", "clone", webID)
	wantLine(t, err, 1, "qory destination clone: The machine cannot store "+webID+", so nothing was cloned.\n  git is not on PATH")
	if n := c.s.Tunnels(); n != 0 {
		t.Errorf("the call opened %d tunnels", n)
	}
	wantNoClone(t, c)
}

// TestDestinationCloneStopsAtNestedDestinations is acme/deep, which commits a link sub to
// a folder outside, and the destination acme/deep/sub inside it: a call for either, in
// each order, stops before any clone, exit 1, with the two ids, and nothing reaches the
// folder that the link leads to. acme/deep2 does not nest with acme/deep.
func TestDestinationCloneStopsAtNestedDestinations(t *testing.T) {
	const outerID, subID, deep2ID = "tg_deep000000000000", "tg_deepsb0000000000", "tg_deep200000000000"
	nested := func(id, other string) string {
		return "qory destination clone: Destination " + id + " is inside another, so nothing was cloned.\n  " + id + " and " + other + " nest: one path lies inside the other."
	}
	for name, order := range map[string][]string{"outer first": {outerID, subID}, "inner first": {subID, outerID}} {
		t.Run(name, func(t *testing.T) {
			c := destinationSession(t)
			elsewhere := tempDir(t)
			c.s.RepositoryWithLinks(t, "acme/deep", "main", map[string]string{"README.md": "deep\n"}, map[string]string{"sub": elsewhere})
			c.s.Repository(t, "acme/deep2", "main", map[string]string{"README.md": "deep2\n"})
			c.inputs(t, []any{destinationOf(outerID, "acme/deep"), destinationOf(subID, "acme/deep/sub"), destinationOf(deep2ID, "acme/deep2")})
			for _, id := range order {
				other := map[string]string{outerID: subID, subID: outerID}[id]
				_, err := run(t, "destination", "clone", id)
				wantLine(t, err, 1, nested(id, other))
			}
			if n := c.s.Tunnels(); n != 0 {
				t.Errorf("the calls opened %d tunnels", n)
			}
			if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
				t.Errorf("the link's folder has %d entries", len(entries))
			}
			out, err := run(t, "destination", "clone", deep2ID)
			if err != nil || out != c.line(deep2ID, "acme/deep2") {
				t.Errorf("acme/deep2: %q, %v", out, err)
			}
		})
	}
}
