package hostgit_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/hostgit"
)

// TestWithoutUserinfoMasksTheUserAndThePassword is each form of a remote's URL git
// takes, with a user and a password of odd characters each form lets through to the
// host git takes: the row masks them and shows that host, and nothing of either prints.
// For each form git parses itself, git fetch-pack --diag-url, which connects nowhere,
// says the host the row must show.
func TestWithoutUserinfoMasksTheUserAndThePassword(t *testing.T) {
	// http and https end the host at the first of / ? and #, and do not decode it.
	httpUser, httpPassword := "us3r:@%2F \x01é", "pa55:@%2F \x01ñ"
	// ssh://, git:// and file:// are decoded first, and end the host at the first /.
	sshUser, sshPassword := "us3r:@%40?# \x01é", "pa55:@%3A?# \x01ñ"
	// The scp-like form ends the host at the first :, and takes no password.
	scpUser := "us3r@%2F?# \x01é"
	cases := []struct {
		url, want, host string
		parts           []string
	}{
		{"https://" + httpUser + ":" + httpPassword + "@git.example.com/acme/shop.git", "https://***@git.example.com/acme/shop.git", "", []string{"us3r", "pa55", "%2F", "é", "ñ", "\x01"}},
		{"https://" + httpUser + ":" + httpPassword + "@git.example.com:8443/acme/shop.git", "https://***@git.example.com:8443/acme/shop.git", "", []string{"us3r", "pa55", "%2F", "é", "ñ", "\x01"}},
		{"ssh://" + sshUser + ":" + sshPassword + "@git.example.com:2222/acme/shop", "ssh://***@git.example.com:2222/acme/shop", "git.example.com:2222", []string{"us3r", "pa55", "%", "?", "#", "é", "ñ", "\x01"}},
		{"git+ssh://" + sshUser + "@git.example.com/acme/shop", "git+ssh://***@git.example.com/acme/shop", "git.example.com", []string{"us3r", "%", "?", "#", "é", "\x01"}},
		{"git://" + sshUser + ":" + sshPassword + "@git.example.com/acme/shop.git", "git://***@git.example.com/acme/shop.git", "git.example.com", []string{"us3r", "pa55", "%", "?", "#", "é", "ñ", "\x01"}},
		{"file://" + sshUser + ":" + sshPassword + "@localhost/srv/git/shop.git", "file://***@localhost/srv/git/shop.git", "localhost", []string{"us3r", "pa55", "%", "?", "#", "é", "ñ", "\x01"}},
		{scpUser + "@git.example.com:acme/shop.git", "***@git.example.com:acme/shop.git", "git.example.com", []string{"us3r", "%2F", "?", "#", "é", "\x01"}},
		{"ssh://[" + "us3r@git.example.com:2222]/acme/shop", "ssh://***@git.example.com:2222]/acme/shop", "git.example.com", []string{"us3r"}},
		{"us3r@[::1]:acme/shop.git", "***@[::1]:acme/shop.git", "::1", []string{"us3r"}},
		{"https::https://a-token@git.example.com/acme/shop.git", "https::https://***@git.example.com/acme/shop.git", "", []string{"a-token"}},
		// git takes a transport that starts with a digit, and an empty one, for a remote
		// helper's.
		{"0x::https://a-user:a-password@git.example.com/x", "0x::https://***@git.example.com/x", "", []string{"a-user", "a-password"}},
		{"::https://a-user:a-password@git.example.com/x", "::https://***@git.example.com/x", "", []string{"a-user", "a-password"}},
		{"https://a-token@git.example.com/acme/shop.git", "https://***@git.example.com/acme/shop.git", "", []string{"a-token"}},
		{"git@git.example.com:acme/shop.git", "***@git.example.com:acme/shop.git", "git.example.com", []string{"git@"}},
		// git ends the host before the @ these hide behind: the row shows the host git
		// reaches, and masks nothing it does not take for a user.
		{"prod-box:x@github.com:org/repo", "prod-box:x@github.com:org/repo", "prod-box", nil},
		{"a-user:a-password@git.example.com:acme/shop.git", "a-user:a-password@git.example.com:acme/shop.git", "a-user", nil},
		{"ssh://evil.example%2F@github.com/acme/shop.git", "ssh://evil.example/@github.com/acme/shop.git", "evil.example", nil},
		{"git://evil.example%2F@github.com/acme/shop.git", "git://evil.example/@github.com/acme/shop.git", "evil.example", nil},
		{"https://evil.example?@github.com/acme/shop.git", "https://evil.example?@github.com/acme/shop.git", "", nil},
		{"https://evil.example#@github.com/acme/shop.git", "https://evil.example#@github.com/acme/shop.git", "", nil},
		{"https://git.example.com/acme/shop.git", "https://git.example.com/acme/shop.git", "", nil},
		{"git.example.com:acme/shop.git", "git.example.com:acme/shop.git", "git.example.com", nil},
		{"file:///srv/git/shop.git", "file:///srv/git/shop.git", "", nil},
		{"/srv/git/u@shop.git", "/srv/git/u@shop.git", "", nil},
		{"./u@x:shop.git", "./u@x:shop.git", "", nil},
	}
	scratch := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", scratch).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for _, c := range cases {
		got := hostgit.WithoutUserinfo(c.url)
		if got != c.want {
			t.Errorf("WithoutUserinfo(%q) = %q, want %q", c.url, got, c.want)
		}
		for _, part := range c.parts {
			if strings.Contains(got, part) {
				t.Errorf("WithoutUserinfo(%q) = %q holds %q", c.url, got, part)
			}
		}
		if c.host == "" {
			continue
		}
		diag := exec.Command("git", "fetch-pack", "--diag-url", c.url)
		diag.Dir = scratch
		out, err := diag.CombinedOutput()
		if err != nil {
			t.Fatalf("git fetch-pack --diag-url %q: %v\n%s", c.url, err, out)
		}
		var host, port string
		for _, line := range strings.Split(string(out), "\n") {
			if v, ok := strings.CutPrefix(line, "Diag: userandhost="); ok {
				host = v
			} else if v, ok := strings.CutPrefix(line, "Diag: hostandport="); ok {
				host = v
			} else if v, ok := strings.CutPrefix(line, "Diag: port="); ok && v != "NONE" {
				port = v
			}
		}
		if i := strings.LastIndexByte(host, '@'); i >= 0 {
			host = host[i+1:]
		}
		if port != "" {
			host += ":" + port
		}
		if host != c.host && !strings.HasPrefix(host, c.host+":") {
			t.Errorf("git takes the host of %q for %q, the case says %q", c.url, host, c.host)
		}
		if !strings.Contains(got, c.host) {
			t.Errorf("WithoutUserinfo(%q) = %q does not show the host %q git takes", c.url, got, c.host)
		}
	}
}

// TestShownKeyMasksTheURLOfAKey is a url, http, credential and lfs key whose subsection
// is a URL that names a user and a password, a remote named by such a URL, and an
// includeIf key whose condition matches one: each shows with them masked. A subsection
// that is no URL shows as it is. Another key, and
// one with no subsection, shows as Shown shows it.
func TestShownKeyMasksTheURLOfAKey(t *testing.T) {
	for key, want := range map[string]string{
		"url.https://a-user:a-password@git.example.com/.insteadof":                           "url.https://***@git.example.com/.insteadof",
		"http.https://a-user:a-password@git.example.com/.extraheader":                        "http.https://***@git.example.com/.extraheader",
		"credential.https://a-user@git.example.com.username":                                 "credential.https://***@git.example.com.username",
		"url.a-user@git.example.com:.pushinsteadof":                                          "url.***@git.example.com:.pushinsteadof",
		"includeif.hasconfig:remote.*.url:https://a-user:a-password@git.example.com/**.path": "includeif.hasconfig:remote.*.url:https://***@git.example.com/**.path",
		"includeif.HasConfig:Remote.*.URL:https://a-user:a-password@git.example.com/**.path": "includeif.HasConfig:Remote.*.URL:https://***@git.example.com/**.path",
		"includeif.gitdir:~/work/.path":                                                      "includeif.gitdir:~/work/.path",
		"lfs.https://a-user:a-password@git.example.com/acme/shop.git/info/lfs.access":        "lfs.https://***@git.example.com/acme/shop.git/info/lfs.access",
		"remote.https://a-user:a-password@git.example.com/acme/shop.git.url":                 "remote.https://***@git.example.com/acme/shop.git.url",
		"branch.feature/a@b.remote":                                                          "branch.feature/a@b.remote",
		"remote.origin.url":                                                                  "remote.origin.url",
		"alias.a\x1bb":                                                                       `"alias.a\x1bb"`,
		"core.editor":                                                                        "core.editor",
	} {
		if got := hostgit.ShownKey(key); got != want {
			t.Errorf("ShownKey(%q) = %q, want %q", key, got, want)
		}
	}
}

// TestARefusedKeyShowsItsURLMasked sets an includeIf key whose condition matches a URL
// that names a user and a password in a recorded repository: the refusal names the key
// with them masked. Only the configuration is read.
func TestARefusedKeyShowsItsURLMasked(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	record(t, dir)
	git(t, dir, "config", "includeIf.hasconfig:remote.*.url:https://a-user:a-password@git.example.com/**.path", "/etc/acme.gitconfig")
	refusedKey(t, run(t, dir), dir, "includeif.hasconfig:remote.*.url:https://***@git.example.com/**.path")
}
