package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"regexp"
	"strconv"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/exports"
)

// maxEngineProxyFile is the most qory reads of wall.engine_proxy_file: one line of
// three members is well under it.
const maxEngineProxyFile = 4 << 10

// engineProxyPassword is the length of the password in the engine proxy file: 32 random
// bytes in base64url with no padding.
const engineProxyPassword = 43

// enginePort is a port from 1 to 65535 as the file writes it, with no sign and no
// leading zero.
var enginePort = regexp.MustCompile(`^[1-9][0-9]{0,4}$`)

// engineProxyUID is the user the engine proxy file must belong to: the user qory runs
// as. A test sets another.
var engineProxyUID = os.Getuid

// readEngineProxy reads wall.engine_proxy_file at path, the address and the password of
// the container engine's proxy setting, which the machine's start unit writes, as one
// JSON object: {"version": 1, "address": "127.0.0.1:3129", "password": "<43
// characters>"}. The path is opened without following a link and without blocking, and
// the checks and the read are of the descriptor opened: a regular file, of the user qory
// runs as, mode 0400 or 0600, at most [maxEngineProxyFile] bytes, with exactly the
// three members once each, version 1, address a loopback IP and a port, and password 43
// characters of base64url with no padding, which decode to 32 bytes. Every refusal is
// input, contains the path and never the password or another byte of the file: the
// JSON decoder's error texts are left out, since they can quote the file.
func readEngineProxy(path string) (*session.EngineProxy, error) {
	shown := exports.Value(path, false)
	f, err := openEngineProxyFile(path)
	if err != nil {
		if engineProxyLink(err) {
			return nil, input(fmt.Errorf("set wall.engine_proxy_file to the file itself: %s is a link", shown))
		}
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return nil, input(fmt.Errorf("check wall.engine_proxy_file: qory cannot open %s: %w", shown, err))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, input(fmt.Errorf("check wall.engine_proxy_file: qory cannot read %s", shown))
	}
	if !info.Mode().IsRegular() {
		return nil, input(fmt.Errorf("set wall.engine_proxy_file to a regular file: %s is not one", shown))
	}
	if uid, ok := fileOwner(info); !ok || uid != engineProxyUID() {
		owner := "unknown"
		if ok {
			owner = "uid " + strconv.Itoa(uid)
		}
		return nil, input(fmt.Errorf("run chown %[1]d %[2]s, where %[1]d is the user qory runs as: its owner is %[3]s", engineProxyUID(), shown, owner))
	}
	if mode := modeBits(info.Mode()); mode != 0o400 && mode != 0o600 {
		return nil, input(fmt.Errorf("run chmod 0400 %s: its mode is %04o, and qory reads it only with no group or other bits", shown, mode))
	}
	b, err := io.ReadAll(io.LimitReader(f, maxEngineProxyFile+1))
	defer clear(b)
	if err != nil {
		return nil, input(fmt.Errorf("check wall.engine_proxy_file: qory cannot read %s", shown))
	}
	if len(b) > maxEngineProxyFile {
		return nil, input(fmt.Errorf("write only the engine proxy setting into %s: the file has more than %d bytes", shown, maxEngineProxyFile))
	}
	return engineProxyFrom(b, shown)
}

// engineProxyFrom reads the engine proxy file's bytes, b, as [readEngineProxy] does; shown
// is the file's path as a refusal prints it.
func engineProxyFrom(b []byte, shown string) (*session.EngineProxy, error) {
	notJSON := input(fmt.Errorf("write one JSON object with version, address and password into %s: the file is not one", shown))
	dec := json.NewDecoder(bytes.NewReader(b))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, notJSON
	}
	var version, address, password []byte
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok {
			return nil, notJSON
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, notJSON
		}
		switch key {
		case "version", "address", "password":
		default:
			// The member's name is the file's: it is not printed.
			return nil, input(fmt.Errorf("leave only version, address and password in %s: the file has another member", shown))
		}
		if seen[key] {
			return nil, input(fmt.Errorf("write %s once in %s: the file has it twice", key, shown))
		}
		seen[key] = true
		switch key {
		case "version":
			version = raw
		case "address":
			address = raw
		case "password":
			password = raw
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, notJSON
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, notJSON
	}
	for _, m := range []struct {
		key string
		raw []byte
	}{{"version", version}, {"address", address}, {"password", password}} {
		if m.raw == nil {
			return nil, input(fmt.Errorf("write %s into %s: the file has no such member", m.key, shown))
		}
	}
	if string(version) != "1" {
		return nil, input(fmt.Errorf("set version to 1 in %s: qory reads version 1 of the engine proxy file", shown))
	}
	var addr string
	if json.Unmarshal(address, &addr) != nil || !engineProxyAddr(addr) {
		return nil, input(fmt.Errorf("set address in %s to a loopback IP address and a port, such as 127.0.0.1:3129: the file has another value", shown))
	}
	var secret string
	if json.Unmarshal(password, &secret) != nil || !engineProxySecret(secret) {
		return nil, input(fmt.Errorf("set password in %s to 43 characters of base64url with no padding, 32 random bytes: the file has another value", shown))
	}
	return &session.EngineProxy{Addr: addr, Password: secret}, nil
}

// modeBits is m as chmod writes it, the permission bits with the setuid, setgid and
// sticky bits, st_mode & 07777.
func modeBits(m fs.FileMode) uint32 {
	bits := uint32(m.Perm())
	if m&fs.ModeSetuid != 0 {
		bits |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		bits |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		bits |= 0o1000
	}
	return bits
}

// engineProxyAddr reports whether addr is host:port with a loopback IP address,
// 127.0.0.0/8 or ::1, as host and a port from 1 to 65535.
func engineProxyAddr(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || !enginePort.MatchString(port) {
		return false
	}
	n, err := strconv.Atoi(port)
	ip := net.ParseIP(host)
	return err == nil && n <= 65535 && ip != nil && ip.IsLoopback()
}

// engineProxySecret reports whether p is [engineProxyPassword] characters of base64url
// with no padding, which decode to 32 bytes.
func engineProxySecret(p string) bool {
	if len(p) != engineProxyPassword {
		return false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(p)
	defer clear(b)
	return err == nil && len(b) == 32
}
