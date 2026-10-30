package apiarydir_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/apiarydir"
)

func TestHostNameMapsTheServerToADirectoryName(t *testing.T) {
	for _, c := range []struct{ url, want string }{
		{"https://apiary.example", "apiary.example"},
		{"https://Apiary.Example/", "apiary.example"},
		{"https://apiary.example:443", "apiary.example"},
		{"https://apiary.example:8443", "apiary.example_8443"},
		{"http://127.0.0.1:4000", "http_127.0.0.1_4000"},
		{"http://localhost", "http_localhost"},
		{"http://LocalHost:80", "http_localhost"},
		{"http://localhost:4000", "http_localhost_4000"},
		{"https://localhost", "localhost"},
		{"https://localhost:4000", "localhost_4000"},
	} {
		got, err := apiarydir.HostName(c.url)
		if err != nil || got != c.want {
			t.Errorf("HostName(%q) = %q, %v; want %q", c.url, got, err, c.want)
		}
	}
}

func TestHostNameFailsForANameThatCouldLeaveTheDirectory(t *testing.T) {
	for _, url := range []string{
		"",
		"apiary.example",
		"ftp://apiary.example",
		"http://apiary.example",
		"https://user@apiary.example",
		"https://apiary.example/v1",
		"https://apiary.example?x=1",
		"https://apiary.example#x",
		"https://apiary.example#",
		"https://apiary.example/#",
		"http://localhost#",
		"http://[::1]",
		"http://[::1]:4000",
		"http://127.0.0.2",
		"http://localhost.example",
		"https://a..b",
		"https://..",
		"https://.apiary.example",
		"https://[::1]",
		"https://apiary_example",
		"https://apiary%2fexample",
		`https://apiary\example`,
	} {
		got, err := apiarydir.HostName(url)
		if err == nil {
			t.Errorf("HostName(%q) = %q, want an error", url, got)
			continue
		}
		if !strings.HasPrefix(err.Error(), "Give the server") {
			t.Errorf("HostName(%q): %q does not start with its fix", url, err)
		}
	}
}

// stored writes a diagnostics key for server under a new root and returns the root, the
// server's directory and the key.
func stored(t *testing.T, server string) (string, string, *accesskey.Key) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "qory", apiarydir.DirName)
	k := newKey(t)
	if err := apiarydir.Write(root, apiarydir.Settings{Server: server, AccessKeyID: "ak_0123456789abcdef", ApiaryPublicKey: pin(t)}, k); err != nil {
		t.Fatal(err)
	}
	host, err := apiarydir.HostName(server)
	if err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(root, host), k
}

func TestWriteStoresTheKeyPrivately(t *testing.T) {
	root, dir, k := stored(t, "https://apiary.example:8443")
	if filepath.Base(dir) != "apiary.example_8443" {
		t.Errorf("directory %s", dir)
	}
	for path, mode := range map[string]os.FileMode{
		root:                                     0o700 | os.ModeDir,
		dir:                                      0o700 | os.ModeDir,
		filepath.Join(dir, apiarydir.SecretFile): 0o600,
		filepath.Join(dir, apiarydir.SettingsFile): 0o600,
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode() != mode {
			t.Errorf("%s is %v, want %v", path, info.Mode(), mode)
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, apiarydir.SecretFile))
	if err != nil || string(b) != k.Secret()+"\n" {
		t.Errorf("access-key-secret is not the secret and a line ending")
	}
	var s map[string]any
	b, _ = os.ReadFile(filepath.Join(dir, apiarydir.SettingsFile))
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if s["version"] != float64(1) || s["server"] != "https://apiary.example:8443" || s["access_key_id"] != "ak_0123456789abcdef" {
		t.Errorf("apiary.json is %s", b)
	}
	if strings.Contains(string(b), k.Secret()) {
		t.Errorf("apiary.json contains the secret")
	}
	got, err := apiarydir.Read(root, filepath.Base(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret.PublicKey() != k.PublicKey() || got.AccessKeyID != "ak_0123456789abcdef" || got.Server != "https://apiary.example:8443" || len(got.Pin) != 1 {
		t.Errorf("Read returned %+v", got)
	}
	hosts, err := apiarydir.Hosts(root)
	if err != nil || len(hosts) != 1 || hosts[0] != "apiary.example_8443" {
		t.Errorf("Hosts = %v, %v", hosts, err)
	}
}

func TestWriteNeverReplacesASecret(t *testing.T) {
	root, _, _ := stored(t, "https://apiary.example")
	err := apiarydir.Write(root, apiarydir.Settings{Server: "https://apiary.example", AccessKeyID: "ak_0123456789abcdef", ApiaryPublicKey: pin(t)}, newKey(t))
	if !errors.Is(err, os.ErrExist) {
		t.Errorf("a second Write: %v, want an error that the secret exists", err)
	}
}

func TestReadWithoutAKeyIsErrNoKey(t *testing.T) {
	root := t.TempDir()
	if _, err := apiarydir.Read(root, "apiary.example"); !errors.Is(err, apiarydir.ErrNoKey) {
		t.Errorf("Read of no directory: %v", err)
	}
	_, dir, _ := stored(t, "https://apiary.example")
	if err := os.Remove(filepath.Join(dir, apiarydir.SettingsFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := apiarydir.Read(filepath.Dir(dir), "apiary.example"); !errors.Is(err, apiarydir.ErrNoKey) {
		t.Errorf("Read without apiary.json: %v", err)
	}
}

func TestReadFailsForASecretFileThatIsNotPrivate(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, dir, secret string)
		want  string
	}{
		{"group and others can read", func(t *testing.T, _, secret string) { chmod(t, secret, 0o644) }, "Run `chmod 600 "},
		{"group can read", func(t *testing.T, _, secret string) { chmod(t, secret, 0o640) }, "Run `chmod 600 "},
		{"directory open to others", func(t *testing.T, dir, _ string) { chmod(t, dir, 0o755) }, "Run `chmod 700 "},
		{"symbolic link", func(t *testing.T, dir, secret string) {
			moved := filepath.Join(dir, "elsewhere")
			if err := os.Rename(secret, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, secret); err != nil {
				t.Fatal(err)
			}
		}, "Replace the symbolic link "},
		{"directory", func(t *testing.T, _, secret string) {
			if err := os.Remove(secret); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(secret, 0o700); err != nil {
				t.Fatal(err)
			}
		}, "Remove "},
		{"not a secret", func(t *testing.T, _, secret string) {
			if err := os.WriteFile(secret, []byte("qak_short\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "Remove <dir>, then run `qory apiary enrol https://apiary.example <code>` again: "},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, dir, k := stored(t, "https://apiary.example")
			secret := filepath.Join(dir, apiarydir.SecretFile)
			c.setup(t, dir, secret)
			_, err := apiarydir.Read(root, "apiary.example")
			if err == nil {
				t.Fatal("Read succeeded")
			}
			if want := strings.ReplaceAll(c.want, "<dir>", dir); !strings.HasPrefix(err.Error(), want) {
				t.Errorf("%q does not start with %q", err, c.want)
			}
			if strings.Contains(err.Error(), k.Secret()) {
				t.Errorf("the error contains the secret")
			}
		})
	}
}

func TestReadFailsForSettingsOfAnotherServer(t *testing.T) {
	root, dir, _ := stored(t, "https://apiary.example")
	other := filepath.Join(root, "other.example")
	if err := os.Rename(dir, other); err != nil {
		t.Fatal(err)
	}
	_, err := apiarydir.Read(root, "other.example")
	if want := "Remove " + other + ", then run `qory apiary enrol <server> <code>` again: the server in "; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("Read of a directory whose apiary.json is another server's: %v", err)
	}
}

func TestReadFailsForANameOutsideTheDirectory(t *testing.T) {
	for _, host := range []string{"..", "../x", "a/b", "", "a..b", "x_", "http_", "http_apiary.example", "http_localhost_", "http_..", "http_../x"} {
		if _, err := apiarydir.Read(t.TempDir(), host); err == nil || errors.Is(err, apiarydir.ErrNoKey) {
			t.Errorf("Read(%q): %v, want an error", host, err)
		}
	}
}

func TestHostsListsServerDirectoriesAlone(t *testing.T) {
	root, _, _ := stored(t, "https://b.example")
	if err := apiarydir.Write(root, apiarydir.Settings{Server: "https://a.example", AccessKeyID: "ak_0123456789abcdef", ApiaryPublicKey: pin(t)}, newKey(t)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "a.example"), filepath.Join(root, "c.example")); err != nil {
		t.Fatal(err)
	}
	hosts, err := apiarydir.Hosts(root)
	if err != nil || strings.Join(hosts, ",") != "a.example,b.example" {
		t.Errorf("Hosts = %v, %v", hosts, err)
	}
	if hosts, err := apiarydir.Hosts(filepath.Join(root, "none")); err != nil || len(hosts) != 0 {
		t.Errorf("Hosts of no directory = %v, %v", hosts, err)
	}
}

func TestHostNameNeverPrintsACode(t *testing.T) {
	const code = "qecd_F1XT-0RE0-0000-0000-0000-0000-00.uoES-kuj1vk0sq0qoGlmAg"
	for _, url := range []string{
		"http://" + code,
		"http://" + code + ":4000",
		"ftp://" + code,
		"https://" + code + "/v1",
		"https://" + code + "?x=1",
		"https://" + code,
		"https://apiary.example:" + code,
	} {
		_, err := apiarydir.HostName(url)
		if err == nil {
			t.Errorf("HostName(%q) succeeded", url)
			continue
		}
		if strings.Contains(strings.ToLower(err.Error()), accesskey.CodePrefixDiagnostics) {
			t.Errorf("HostName(%q): the error contains the code: %v", url, err)
		}
	}
}

func TestHTTPAndHTTPSToLocalhostAreTwoDirectories(t *testing.T) {
	root, httpsDir, _ := stored(t, "https://localhost:4000")
	if err := apiarydir.Write(root, apiarydir.Settings{Server: "http://localhost:4000", AccessKeyID: "ak_0123456789abcdef", ApiaryPublicKey: pin(t)}, newKey(t)); err != nil {
		t.Fatalf("Write for http beside a key for https: %v", err)
	}
	hosts, err := apiarydir.Hosts(root)
	if err != nil || strings.Join(hosts, ",") != "http_localhost_4000,localhost_4000" {
		t.Errorf("Hosts = %v, %v", hosts, err)
	}
	for host, server := range map[string]string{filepath.Base(httpsDir): "https://localhost:4000", "http_localhost_4000": "http://localhost:4000"} {
		key, err := apiarydir.Read(root, host)
		if err != nil || key.Server != server {
			t.Errorf("Read(%q) = %+v, %v; want the key for %s", host, key, err, server)
		}
	}
}

func TestHostsCountsOnlyDirectoriesWithAKey(t *testing.T) {
	root, _, _ := stored(t, "https://a.example")
	moved := filepath.Join(root, "b.example")
	if err := os.Mkdir(moved, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moved, apiarydir.SecretFile+".old.1700000000"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "c.example"), 0o700); err != nil {
		t.Fatal(err)
	}
	hosts, err := apiarydir.Hosts(root)
	if err != nil || strings.Join(hosts, ",") != "a.example" {
		t.Errorf("Hosts = %v, %v; want a.example alone", hosts, err)
	}
}

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// pin is a pin of one new key.
func pin(t *testing.T) json.RawMessage {
	t.Helper()
	return json.RawMessage(`[{"alg":"ed25519","public_key":"` + newKey(t).PublicKey().String() + `"}]`)
}

// newKey generates an access key whose public key a pin reader takes: one without qak_
// or qrk_ in it: those readers return an error for a value that looks like a secret.
func newKey(t *testing.T) *accesskey.Key {
	t.Helper()
	for {
		k, err := accesskey.Generate()
		if err != nil {
			t.Fatal(err)
		}
		if !accesskey.ContainsSecret(k.PublicKey().String()) {
			return k
		}
	}
}
