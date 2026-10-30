// Package apiarydir is where qory keeps a diagnostics key on this computer: an access
// key of kind diagnostics, which reads the diagnostics of runs from one Qory Apiary
// server. Each server has one directory, <user dir>/apiary/<host>, where <user dir> is
// [config.UserDir] and <host> is [HostName] of the server's URL:
//
//	access-key-secret  the key's secret, one line, mode 0600
//	apiary.json        the server's URL, the key's id and the server's public keys, mode 0600
//	instance-id        this computer's instance id for the server, mode 0600
//	locks/key.lock     the lock [Write] takes exclusively and [Read] takes shared
//
// Every directory is mode 0700. The files are written as [foragerdir.Dir] writes them:
// created with O_CREAT|O_EXCL|O_NOFOLLOW and an exact mode, and synced. A diagnostics key
// is never read from or written to forager.yaml or its directory, so a node's access key
// and a diagnostics key never share a file.
//
// [Write] stores a key, and [Read] reads one back. qory apiary enrol stores a key in steps
// instead: [Prepare], the secret in access-key-secret.new, then [WriteSettings] once
// Qory Apiary has enrolled the key; [HasKey] reports a key already there.
//
// [Read] returns an error for a secret file that is a symbolic link, is not a regular
// file, or grants anything to the group or to others, with an error whose first line
// starts with the fix. No error of this package contains
// the secret.
package apiarydir

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/foragerdir"
)

// The names of the directory and its files.
const (
	// DirName is the directory under [config.UserDir] that contains one directory per
	// server.
	DirName = "apiary"
	// SettingsFile is the server's URL, the key's id and the server's public keys, as
	// JSON, mode 0600.
	SettingsFile = "apiary.json"
	// SecretFile is the key's secret, one line, mode 0600.
	SecretFile = foragerdir.SecretFile
)

// SettingsVersion is the version of apiary.json this package writes and reads.
const SettingsVersion = 1

// maxSettingsFile is the most of apiary.json that is read.
const maxSettingsFile = 64 << 10

// maxSecretFile is the most of access-key-secret that is read: a secret is one line of
// 47 characters.
const maxSecretFile = 4 << 10

// Settings is apiary.json:
//
//	{"version":1,"server":"https://apiary.example","access_key_id":"ak_...","apiary_public_key":[{"alg":"ed25519","public_key":"..."}]}
//
// ApiaryPublicKey is the pin as [accesskey.ParsePin] reads it: a JSON list.
type Settings struct {
	Version         int             `json:"version"`
	Server          string          `json:"server"`
	AccessKeyID     string          `json:"access_key_id"`
	ApiaryPublicKey json.RawMessage `json:"apiary_public_key"`
}

// Key is a diagnostics key read by [Read]: its directory, its settings and its secret.
type Key struct {
	// Dir is the server's directory, <user dir>/apiary/<host>.
	Dir foragerdir.Dir
	// Host is the directory's name, [HostName] of Server.
	Host        string
	Server      string
	AccessKeyID string
	Pin         accesskey.Pin
	Secret      *accesskey.Key
}

// Root returns <user dir>/apiary, the directory of every server's directory. It is an
// error when the user's configuration directory cannot be found.
func Root() (string, error) {
	dir, err := config.UserDir()
	if err != nil {
		return "", err
	}
	if dir == "" {
		return "", errors.New("Set HOME or XDG_CONFIG_HOME: qory keeps diagnostics keys under the user's configuration directory, and neither variable is set.")
	}
	return filepath.Join(dir, DirName), nil
}

// hostShape is a host name as a directory name takes it.
var hostShape = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

// httpPrefix starts the directory name of a server reached over http. The name of a
// server reached over https is <host> or <host>_<port>, with a port of digits, so it never
// starts with http_ followed by localhost or 127.0.0.1.
const httpPrefix = "http_"

// HostName returns the name of the directory of the server at raw: its host in lower
// case, followed by _<port> when the URL gives a port other than the scheme's default,
// and after http_ when the URL is http. It returns an error for a URL with a "#", user
// information, a path other than "/" or a query; for a scheme other than https, or http
// to a host other than localhost or 127.0.0.1; and for a host that is empty or has a
// character outside [a-z0-9.-] or "..", so the name cannot leave the apiary directory.
// No error prints a part of the URL that contains a secret or a code.
func HostName(raw string) (string, error) {
	if strings.Contains(raw, "#") {
		return "", errors.New("Give the server as a URL of its host alone, such as https://apiary.example: the URL has a \"#\".")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Host == "" {
		return "", fmt.Errorf("Give the server as an https URL, such as https://apiary.example: %s is not one.", exports.Value(raw, true))
	}
	switch {
	case u.Scheme != "https" && u.Scheme != "http":
		return "", fmt.Errorf("Give the server as an https URL, such as https://apiary.example: %s is not https.", exports.Value(u.Scheme+"://"+u.Host, false))
	case u.Scheme == "http" && !loopback(u.Hostname()):
		return "", fmt.Errorf("Give the server as an https URL, such as https://apiary.example: http is allowed only to localhost or 127.0.0.1, and %s is neither.", exports.Value(u.Hostname(), false))
	case u.User != nil:
		return "", errors.New("Give the server as a URL without user information, such as https://apiary.example.")
	case (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery:
		return "", fmt.Errorf("Give the server as a URL of its host alone, such as https://apiary.example: %s has a path or a query.", exports.Value(u.Scheme+"://"+u.Host, false))
	}
	host := strings.ToLower(u.Hostname())
	if !hostShape.MatchString(host) || strings.Contains(host, "..") {
		return "", fmt.Errorf("Give the server by a host name of letters, digits, dots and hyphens, with no two dots in a row: %s is not one.", exports.Value(u.Hostname(), true))
	}
	prefix := ""
	if u.Scheme == "http" {
		prefix = httpPrefix
	}
	port := u.Port()
	if port == "" || (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		return prefix + host, nil
	}
	if strings.Trim(port, "0123456789") != "" {
		return "", fmt.Errorf("Give the server with a port of digits: %s is not one.", exports.Value(port, true))
	}
	return prefix + host + "_" + port, nil
}

// loopback reports whether host is localhost, in any letter case, or 127.0.0.1: the
// hosts a server URL may reach over http.
func loopback(host string) bool {
	return strings.EqualFold(host, "localhost") || host == "127.0.0.1"
}

// Hosts returns the names of the server directories under root that contain a
// diagnostics key or a part of one, as [HasKey] reports, sorted: each directory, not a
// link, whose name [HostName] can return. A root that does not exist has none.
func Hosts(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsDir() && validName(e.Name()) && HasKey(foragerdir.Dir(filepath.Join(root, e.Name()))) {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	return out, nil
}

// validName reports whether name is a name [HostName] returns: <host> or <host>_<port>,
// or either after http_ when the host is localhost or 127.0.0.1.
func validName(name string) bool {
	if rest, ok := strings.CutPrefix(name, httpPrefix); ok && validHost(rest) {
		host, _, _ := strings.Cut(rest, "_")
		if loopback(host) {
			return true
		}
	}
	return validHost(name)
}

// validHost reports whether name is <host> or <host>_<port>, with a host [hostShape]
// takes, no "..", and a port of digits.
func validHost(name string) bool {
	host, port, found := strings.Cut(name, "_")
	if !hostShape.MatchString(host) || strings.Contains(host, "..") {
		return false
	}
	return !found || (port != "" && strings.Trim(port, "0123456789") == "")
}

// Write stores the diagnostics key k for the server s.Server under root: [Prepare],
// then access-key-secret, which must not exist, [WriteSettings] and the instance id.
// It returns an error for settings whose server, key id or pin do not read, before it
// writes anything.
func Write(root string, s Settings, k *accesskey.Key) error {
	if _, err := settingsJSON(s); err != nil {
		return err
	}
	dir, _, lock, err := Prepare(root, s.Server)
	if err != nil {
		return err
	}
	defer lock.Release()
	if err := dir.WriteSecret(k); err != nil {
		return err
	}
	if err := WriteSettings(dir, s); err != nil {
		return err
	}
	_, _, err = dir.InstanceID(foragerdir.MachineID())
	return err
}

// Prepare makes root and the directory of the server at server mode 0700, and takes the
// key lock of that directory exclusively. It returns the directory, its name, which is
// [HostName] of server, and the lock, which the caller releases.
func Prepare(root, server string) (foragerdir.Dir, string, *foragerdir.Lock, error) {
	host, err := HostName(server)
	if err != nil {
		return "", "", nil, err
	}
	const whose = "the directory of diagnostics keys"
	if err := foragerdir.MakePrivateDir(root, whose); err != nil {
		return "", "", nil, err
	}
	dir := foragerdir.Dir(filepath.Join(root, host))
	if err := foragerdir.MakePrivateDir(string(dir), whose); err != nil {
		return "", "", nil, err
	}
	lock, err := dir.LockKey(true)
	if err != nil {
		return "", "", nil, fmt.Errorf("the key lock in %s: %w", dir, err)
	}
	return dir, host, lock, nil
}

// HasKey reports whether dir contains a diagnostics key or a part of one:
// access-key-secret or apiary.json, as a file or as anything else.
func HasKey(dir foragerdir.Dir) bool {
	for _, name := range []string{SecretFile, SettingsFile} {
		if _, err := os.Lstat(dir.Path(name)); err == nil {
			return true
		}
	}
	return false
}

// WriteSettings replaces apiary.json of dir with s, version [SettingsVersion], in one
// rename. It returns an error for settings whose server is not the server of dir, or
// whose key id or pin do not read, before it writes anything. The caller holds the key
// lock.
func WriteSettings(dir foragerdir.Dir, s Settings) error {
	b, err := settingsJSON(s)
	if err != nil {
		return err
	}
	if host, _ := HostName(s.Server); host != filepath.Base(string(dir)) {
		return fmt.Errorf("the server %s is not the server of the directory %s", exports.Value(s.Server, true), dir)
	}
	return dir.ReplaceFile(SettingsFile, b)
}

// settingsJSON returns s as apiary.json contains it, version [SettingsVersion] and a
// line ending, and an error for a server, a key id or a pin that does not read.
func settingsJSON(s Settings) ([]byte, error) {
	if _, err := HostName(s.Server); err != nil {
		return nil, err
	}
	if err := accesskey.CheckID(s.AccessKeyID); err != nil {
		return nil, err
	}
	if _, err := accesskey.ParsePin(s.ApiaryPublicKey); err != nil {
		return nil, err
	}
	s.Version = SettingsVersion
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ErrNoKey is the error of a server directory that contains no diagnostics key: no
// directory, no access-key-secret or no apiary.json.
var ErrNoKey = errors.New("no diagnostics key")

// EnrolAgain is the fix of a diagnostics key in dir that does not read or that the
// server does not accept, for the server at server, or for any server when server is
// empty. dir is removed first, since enrol stops at a directory with a key.
func EnrolAgain(dir foragerdir.Dir, server string) string {
	if server == "" {
		server = "<server>"
	}
	return "Remove " + string(dir) + ", then run `qory apiary enrol " + server + " <code>` again"
}

// Read reads the diagnostics key of the server directory host under root, holding the key
// lock shared when the directory can be written. A directory without the key's files is
// [ErrNoKey]. Every other error starts with its fix, and none contains the secret.
func Read(root, host string) (*Key, error) {
	if !validName(host) {
		return nil, fmt.Errorf("Pass --server <url>: %s is not the name of a server directory.", exports.Value(host, true))
	}
	dir := foragerdir.Dir(filepath.Join(root, host))
	for _, name := range []string{"", SecretFile, SettingsFile} {
		if _, err := os.Lstat(dir.Path(name)); errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNoKey
		}
	}
	if err := checkDir(string(dir)); err != nil {
		return nil, err
	}
	lock, err := dir.LockKey(false)
	if err != nil && !errors.Is(err, foragerdir.ErrReadOnly) {
		return nil, fmt.Errorf("Check that %s can be written: the key lock: %w", dir, err)
	}
	defer lock.Release()
	s, pin, err := readSettings(dir, host)
	if err != nil {
		return nil, err
	}
	secret, err := readSecret(dir, s.Server)
	if err != nil {
		return nil, err
	}
	return &Key{Dir: dir, Host: host, Server: s.Server, AccessKeyID: s.AccessKeyID, Pin: pin, Secret: secret}, nil
}

// checkDir returns an error for a server directory that is a link, is not a directory, or grants
// anything to the group or to others, with the fix first.
func checkDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("Replace the symbolic link %s with the directory itself, mode 0700: qory reads a diagnostics key only from a directory.", dir)
	case !info.IsDir():
		return fmt.Errorf("Remove %s, and enrol the diagnostics key again: it is not a directory.", dir)
	case info.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("Run `chmod 700 %s`: the directory is mode %04o, which grants access to the group or others, and it contains a diagnostics key.", dir, info.Mode().Perm())
	}
	return nil
}

// checkFile returns an error for a file of a server directory that is a link, is not a regular file,
// or grants anything to the group or to others, with the fix first. The read that
// follows checks the file again on the descriptor it reads from.
func checkFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("Replace the symbolic link %s with the file itself, mode 0600: qory reads a diagnostics key only from a regular file.", path)
	case !info.Mode().IsRegular():
		return fmt.Errorf("Remove %s, and enrol the diagnostics key again: it is not a regular file.", path)
	case info.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("Run `chmod 600 %s`, and enrol a new diagnostics key if anyone else could have read the file: it is mode %04o, which grants access to the group or others.", path, info.Mode().Perm())
	}
	return nil
}

// readSecret reads access-key-secret of dir, the directory of the server at server,
// and returns an error for the published fixture key.
func readSecret(dir foragerdir.Dir, server string) (*accesskey.Key, error) {
	path := dir.Path(SecretFile)
	if err := checkFile(path); err != nil {
		return nil, err
	}
	b, err := dir.ReadFile(SecretFile, maxSecretFile, "an access key secret")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnrolAgain(dir, server), err)
	}
	defer clear(b)
	line := bytes.TrimSuffix(bytes.TrimSuffix(b, []byte("\n")), []byte("\r"))
	k, err := accesskey.ParseSecret(string(line))
	if err != nil {
		return nil, fmt.Errorf("%s: %s is not an access key secret, one line of %s and 43 characters of base64url.", EnrolAgain(dir, server), path, accesskey.SecretPrefix)
	}
	if k.PublicKey().Fixture() {
		return nil, fmt.Errorf("%s: %s contains the published fixture key of the Forager contract, whose secret anyone can read.", EnrolAgain(dir, server), path)
	}
	return k, nil
}

// readSettings reads apiary.json of dir, the directory host, and returns an error for settings of
// another version, a server whose directory is not host, a key id that does not read
// and a pin that [accesskey.ParsePin] does not read.
func readSettings(dir foragerdir.Dir, host string) (*Settings, accesskey.Pin, error) {
	path := dir.Path(SettingsFile)
	again := EnrolAgain(dir, "")
	if err := checkFile(path); err != nil {
		return nil, nil, err
	}
	b, err := dir.ReadFile(SettingsFile, maxSettingsFile, "64 KiB")
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", again, err)
	}
	var s Settings
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, nil, fmt.Errorf("%s: %s is not JSON of version, server, access_key_id and apiary_public_key.", again, path)
	}
	if s.Version != SettingsVersion {
		return nil, nil, fmt.Errorf("%s with this qory: %s is version %d, and this qory reads version %d.", again, path, s.Version, SettingsVersion)
	}
	if got, err := HostName(s.Server); err != nil || got != host {
		return nil, nil, fmt.Errorf("%s: the server in %s is not the server of the directory %s.", again, path, host)
	}
	again = EnrolAgain(dir, s.Server)
	if err := accesskey.CheckID(s.AccessKeyID); err != nil {
		return nil, nil, fmt.Errorf("%s: %s: access_key_id: %w", again, path, err)
	}
	pin, err := accesskey.ParsePin(s.ApiaryPublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %s: apiary_public_key: %w", again, path, err)
	}
	return &s, pin, nil
}
