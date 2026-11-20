package runtimeinstall_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/runtimeinstall"
	rt "github.com/qoryai/qory/internal/runtimeinstall/runtimeinstalltest"
)

// TestTheBuiltInKeysParse reads npm's keys built into qory: two ECDSA P-256 keys, the
// one that expired on 2025-01-29 and the one with no expiry.
func TestTheBuiltInKeysParse(t *testing.T) {
	keys, err := runtimeinstall.NpmKeys()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"SHA256:jl3bwswu80PjjokCgh0o2w5c2U4LhQAE57gj9cz1kzA": "2025-01-29",
		"SHA256:DhQ8wR5APBvFHLF/+Tc+AYvPOdTpcIDqOhxsBHRwC7U": "",
	}
	if len(keys) != len(want) {
		t.Fatalf("%d keys, want %d", len(keys), len(want))
	}
	for _, k := range keys {
		expires := ""
		if !k.Expires.IsZero() {
			expires = k.Expires.UTC().Format(time.DateOnly)
		}
		if e, ok := want[k.KeyID]; !ok || e != expires || k.Key == nil || k.Key.Curve.Params().Name != "P-256" {
			t.Errorf("the key %s expires %q, want %q", k.KeyID, expires, e)
		}
	}
}

// signedClaude is what npm signs for claude 2.1.320 for linux/amd64.
const signedClaude = "@anthropic-ai/claude-code-linux-x64@2.1.320"

// TestNpmsSignatureIsCheckedBeforeTheInstall publishes claude 2.1.320 on the npm stub
// with dist.signatures in each form qory refuses: none, an empty list, a key qory does
// not have, a signature that does not verify, alone or before a key qory does not have,
// a signature of another version or another checksum, and lists past the limits. Each
// install fails with its fix first, reads no tarball, and installs nothing.
func TestNpmsSignatureIsCheckedBeforeTheInstall(t *testing.T) {
	const what = "@anthropic-ai/claude-code-linux-x64 2.1.320"
	sig := func(message string) []runtimeinstall.Signature {
		return []runtimeinstall.Signature{{KeyID: rt.KeyID, Sig: rt.Sign(message)}}
	}
	many := make([]map[string]string, 17)
	for i := range many {
		many[i] = map[string]string{"keyid": rt.KeyID, "sig": "x"}
	}
	for _, c := range []struct {
		name string
		sign func(signed, integrity string) any
		want string
	}{
		{"no signatures", func(string, string) any { return nil },
			"Check the source of claude's releases.\n  npm has no signature of claude 2.1.320."},
		{"an empty list", func(string, string) any { return []any{} },
			"Check the source of claude's releases.\n  The dist.signatures of " + what + "\n  are not npm's signatures: it has no signature."},
		{"a key qory does not have", func(s, i string) any {
			return []runtimeinstall.Signature{{KeyID: "SHA256:another", Sig: rt.Sign(s + ":" + i)}}
		}, "Update qory.\n  npm signed claude 2.1.320 with a key that this qory does not have.\n  The key is SHA256:another."},
		{"a bad signature", func(string, string) any {
			return []runtimeinstall.Signature{{KeyID: rt.KeyID, Sig: "MEUCIQDnotasignature"}}
		}, "Check the source of claude's releases.\n  npm's signature of claude 2.1.320 does not verify.\n  The key is " + rt.KeyID + "."},
		{"a bad signature, then a key qory does not have", func(s, i string) any {
			return []runtimeinstall.Signature{{KeyID: rt.KeyID, Sig: "MEUCIQDnotasignature"}, {KeyID: "SHA256:another", Sig: rt.Sign(s + ":" + i)}}
		}, "Check the source of claude's releases.\n  npm's signature of claude 2.1.320 does not verify.\n  The key is " + rt.KeyID + "."},
		{"a signature of another version", func(_, i string) any { return sig("@anthropic-ai/claude-code-linux-x64@2.1.319:" + i) },
			"Check the source of claude's releases.\n  npm's signature of claude 2.1.320 does not verify.\n  The key is " + rt.KeyID + "."},
		{"a signature of another checksum", func(s, _ string) any { return sig(s + ":" + rt.Integrity([]byte("another tarball"))) },
			"Check the source of claude's releases.\n  npm's signature of claude 2.1.320 does not verify.\n  The key is " + rt.KeyID + "."},
		{"more than 16 signatures", func(string, string) any { return many },
			"Check the source of claude's releases.\n  The dist.signatures of " + what + "\n  are not npm's signatures: it has 17 signatures, and qory reads at most 16."},
		{"a keyid of more than 128 bytes", func(string, string) any {
			return []map[string]string{{"keyid": strings.Repeat("k", 129), "sig": "x"}}
		}, "Check the source of claude's releases.\n  The dist.signatures of " + what + "\n  are not npm's signatures: its signature 1 has a keyid of more than 128 bytes."},
		{"a keyid with a line break", func(string, string) any {
			return []map[string]string{{"keyid": "SHA256:a\nUpdate qory.", "sig": "x"}}
		}, "Check the source of claude's releases.\n  The dist.signatures of " + what + "\n  are not npm's signatures: its signature 1 has a keyid with a control character."},
		{"a sig of more than 1 KiB", func(string, string) any {
			return []map[string]string{{"keyid": rt.KeyID, "sig": strings.Repeat("s", 1025)}}
		}, "Check the source of claude's releases.\n  The dist.signatures of " + what + "\n  are not npm's signatures: its signature 1 has a sig of more than 1024 bytes."},
		{"upper-case names", func(s, i string) any {
			return []map[string]string{{"KEYID": rt.KeyID, "SIG": rt.Sign(s + ":" + i)}}
		}, "Check the source of claude's releases.\n  The dist.signatures of " + what + "\n  are not npm's signatures: its signature 1 has no keyid or no sig."},
		{"a member twice", func(s, i string) any {
			return []json.RawMessage{json.RawMessage(`{"keyid":"SHA256:another","keyid":"` + rt.KeyID + `","sig":"` + rt.Sign(s+":"+i) + `"}`)}
		}, "Check the source of claude's releases.\n  The dist.signatures of " + what + "\n  are not npm's signatures: its signature 1 is not an object of keyid and sig."},
		{"no sig", func(string, string) any { return []map[string]string{{"keyid": rt.KeyID}} },
			"Check the source of claude's releases.\n  The dist.signatures of " + what + "\n  are not npm's signatures: its signature 1 has no keyid or no sig."},
		{"an empty keyid", func(string, string) any { return []map[string]string{{"keyid": "", "sig": "x"}} },
			"Check the source of claude's releases.\n  The dist.signatures of " + what + "\n  are not npm's signatures: its signature 1 has no keyid or no sig."},
		{"a keyid that is no string", func(string, string) any { return []map[string]any{{"keyid": 1, "sig": "x"}} },
			"Check the source of claude's releases.\n  The dist.signatures of " + what + "\n  are not npm's signatures: its signature 1 is not an object of keyid and sig."},
		{"no list", func(string, string) any { return map[string]string{"keyid": rt.KeyID} },
			"Check the source of claude's releases.\n  The dist.signatures of " + what + "\n  are not npm's signatures: it is not a JSON array."},
	} {
		t.Run(c.name, func(t *testing.T) {
			npm := rt.StartNpm(t)
			npm.Publish("2.1.320")
			npm.SignWith(c.sign)
			folder := t.TempDir()
			_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
				Keys: rt.Keys(), Fetch: runtimeinstall.NpmFetch(http.DefaultClient, npm.URL(), "amd64"), Runtime: "claude", Folder: folder,
			})
			if err == nil || err.Error() != c.want {
				t.Fatalf("the error is %v, want %q", err, c.want)
			}
			if npm.TarballGets() != 0 {
				t.Errorf("%d tarballs read", npm.TarballGets())
			}
			if _, err := os.Lstat(filepath.Join(folder, "claude", "2.1.320")); !os.IsNotExist(err) {
				t.Errorf("the version's folder: %v", err)
			}
		})
	}
}

// expiredKeys are the stubs' keys, with the test's key expired on 2025-01-29.
func expiredKeys() []runtimeinstall.SigningKey {
	keys := rt.Keys()
	keys[0].Expires = time.Date(2025, 1, 29, 0, 0, 0, 0, time.UTC)
	return keys
}

// TestTheExpiredKeysFixFitsOnALine checks the first line of the refusal of a release
// signed only by an expired key, for each runtime that Install installs, pinned and not:
// the fix, which with "qory run: " before it has at most 100 characters.
func TestTheExpiredKeysFixFitsOnALine(t *testing.T) {
	for _, runtime := range runtimeinstall.Runtimes() {
		for pinned, fix := range map[bool]string{true: "Pin a newer version of " + runtime + ".", false: "Check the source of " + runtime + "'s releases."} {
			rel := runtimeinstall.Release{Version: "2.1.320", Signed: "p@2.1.320", Integrity: "sha512-x", Signatures: rt.Signatures("p@2.1.320", "sha512-x")}
			err := runtimeinstall.Verify(rel, expiredKeys(), time.Now(), runtime, pinned)
			if err == nil {
				t.Fatalf("%s: no error", runtime)
			}
			first, _, _ := strings.Cut(err.Error(), "\n")
			if first != fix || len("qory run: "+first) > 100 {
				t.Errorf("%s, pinned %v: the first line, with qory run: , has %d characters, want %q in at most 100", runtime, pinned, len("qory run: "+first), fix)
			}
		}
	}
}

// TestAnExpiredKeysFixDependsOnThePin installs claude 2.1.320, signed only by a key that
// expired on 2025-01-29, from the npm stub, with the version pinned and with no pin. A
// pinned install stops with the fix to pin a newer version; one with no pin, with the
// fix to check the source of claude's releases. The cause follows on indented lines, and
// neither install reads a tarball.
func TestAnExpiredKeysFixDependsOnThePin(t *testing.T) {
	const cause = "\n  npm signed claude 2.1.320 with a key that expired on 2025-01-29.\n  The key is " + rt.KeyID + "."
	for pin, want := range map[string]string{
		"2.1.320": "Pin a newer version of claude." + cause,
		"":        "Check the source of claude's releases." + cause,
	} {
		t.Run("pin "+pin, func(t *testing.T) {
			npm := rt.StartNpm(t)
			npm.Publish("2.1.320")
			_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
				Keys: expiredKeys(), Fetch: runtimeinstall.NpmFetch(http.DefaultClient, npm.URL(), "amd64"), Runtime: "claude", Version: pin, Folder: t.TempDir(),
			})
			if err == nil || err.Error() != want {
				t.Fatalf("the error is %v, want %q", err, want)
			}
			if npm.TarballGets() != 0 {
				t.Errorf("%d tarballs read", npm.TarballGets())
			}
		})
	}
}

// altered is sig, a base64 signature, with one byte of its decoded bytes changed.
func altered(t *testing.T, sig string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/2] ^= 0x01
	return base64.StdEncoding.EncodeToString(b)
}

// TestNpmsSignatureVerifies installs claude 2.1.320 whose dist.signatures have, beside
// the signature by the test's key, one by a key qory does not have and a member npm may
// add, or a second signature by the test's key with one byte changed, before or after
// it: the install takes the signature that verifies, and ignores the member. The test
// signs before the stub runs, since the stub calls its sign function from the stub's
// handler.
func TestNpmsSignatureVerifies(t *testing.T) {
	for _, c := range []struct {
		name string
		sign func(good, changed string) any
	}{
		{"a key qory does not have", func(good, _ string) any {
			return []map[string]string{
				{"keyid": "SHA256:another", "sig": "x"},
				{"keyid": rt.KeyID, "sig": good, "added": "by npm"},
			}
		}},
		{"one key, the altered signature first", func(good, changed string) any {
			return []runtimeinstall.Signature{{KeyID: rt.KeyID, Sig: changed}, {KeyID: rt.KeyID, Sig: good}}
		}},
		{"one key, the altered signature last", func(good, changed string) any {
			return []runtimeinstall.Signature{{KeyID: rt.KeyID, Sig: good}, {KeyID: rt.KeyID, Sig: changed}}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tarball := rt.PlainTarball(t, "2.1.320 amd64")
			integrity := rt.Integrity(tarball)
			good := rt.Sign(signedClaude + ":" + integrity)
			sigs := c.sign(good, altered(t, good))
			npm := rt.StartNpm(t)
			npm.PublishWith("x64", "2.1.320", tarball, integrity, "")
			npm.SetLatest("2.1.320")
			npm.SignWith(func(string, string) any { return sigs })
			got, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
				Keys: rt.Keys(), Fetch: runtimeinstall.NpmFetch(http.DefaultClient, npm.URL(), "amd64"), Runtime: "claude", Folder: t.TempDir(),
			})
			if err != nil {
				t.Fatal(err)
			}
			got.Release()
		})
	}
}

// npmRelease is a release that npm published, as testdata/npm-signatures.json records
// it from https://registry.npmjs.org/<package> on 2026-11-19.
type npmRelease struct {
	Runtime, Version, Arch, Signed, Integrity string
	Signatures                                []runtimeinstall.Signature
}

// npmReleases are the two releases of testdata/npm-signatures.json.
func npmReleases(t *testing.T) []npmRelease {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "npm-signatures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var releases []npmRelease
	if err := json.Unmarshal(b, &releases); err != nil {
		t.Fatal(err)
	}
	if len(releases) != 2 {
		t.Fatalf("%d releases, want 2", len(releases))
	}
	return releases
}

// TestTheBuiltInKeysVerifyNpmsSignatures checks npm's signatures of claude 2.1.327 and
// codex 0.170.0 for linux/amd64 with the keys built into qory, through the check that
// Install runs: what npm signs is the recorded <package>@<version>, and each signature
// verifies alone. One changed byte of a signature, or one changed character of the
// integrity, makes it fail.
func TestTheBuiltInKeysVerifyNpmsSignatures(t *testing.T) {
	keys, err := runtimeinstall.NpmKeys()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	for _, r := range npmReleases(t) {
		t.Run(r.Runtime, func(t *testing.T) {
			signed, ok := runtimeinstall.SignedName(r.Runtime, r.Version, r.Arch)
			if !ok || signed != r.Signed {
				t.Fatalf("qory signs %q (%v), npm signed %q", signed, ok, r.Signed)
			}
			if len(r.Signatures) != 2 {
				t.Fatalf("%d signatures, want 2", len(r.Signatures))
			}
			bad := "Check the source of " + r.Runtime + "'s releases.\n  npm's signature of " + r.Runtime + " " + r.Version + " does not verify.\n  The key is "
			for i, s := range r.Signatures {
				rel := runtimeinstall.Release{Version: r.Version, Signed: signed, Integrity: r.Integrity, Signatures: []runtimeinstall.Signature{s}}
				if err := runtimeinstall.Verify(rel, keys, now, r.Runtime, false); err != nil {
					t.Errorf("signature %d: %v", i+1, err)
				}
				changed := rel
				changed.Signatures = []runtimeinstall.Signature{{KeyID: s.KeyID, Sig: altered(t, s.Sig)}}
				if err := runtimeinstall.Verify(changed, keys, now, r.Runtime, false); err == nil || err.Error() != bad+s.KeyID+"." {
					t.Errorf("signature %d with a changed byte: %v, want %q", i+1, err, bad+s.KeyID+".")
				}
				integrity, first := []byte(r.Integrity), len("sha512-")
				integrity[first] = 'A'
				if r.Integrity[first] == 'A' {
					integrity[first] = 'B'
				}
				changed = rel
				changed.Integrity = string(integrity)
				if err := runtimeinstall.Verify(changed, keys, now, r.Runtime, false); err == nil || err.Error() != bad+s.KeyID+"." {
					t.Errorf("signature %d over a changed integrity: %v, want %q", i+1, err, bad+s.KeyID+".")
				}
			}
		})
	}
}

// TestAnInstallWithNoKeysUsesTheBuiltInKeys installs claude 2.1.320 from the npm stub,
// signed with the test's key, with Options.Keys unset, as qory run installs: the install
// checks the signature with the keys built into qory, which do not have the test's key,
// so it stops with Update qory. and reads no tarball.
func TestAnInstallWithNoKeysUsesTheBuiltInKeys(t *testing.T) {
	npm := rt.StartNpm(t)
	npm.Publish("2.1.320")
	_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
		Fetch: runtimeinstall.NpmFetch(http.DefaultClient, npm.URL(), "amd64"), Runtime: "claude", Folder: t.TempDir(),
	})
	want := "Update qory.\n  npm signed claude 2.1.320 with a key that this qory does not have.\n  The key is " + rt.KeyID + "."
	if err == nil || err.Error() != want {
		t.Fatalf("the error is %v, want %q", err, want)
	}
	if npm.TarballGets() != 0 {
		t.Errorf("%d tarballs read", npm.TarballGets())
	}
}

// TestAnInstallWithNoKeysPassesNpmsSignatures installs claude 2.1.327 and codex 0.170.0
// from the npm stub, each with the dist.integrity and dist.signatures that npm published,
// see testdata/npm-signatures.json, and with Options.Keys unset: npm's signature verifies
// with the keys built into qory, and the install reads the tarball. The stub's tarball is
// not npm's, so the install then stops at the checksum.
func TestAnInstallWithNoKeysPassesNpmsSignatures(t *testing.T) {
	for _, r := range npmReleases(t) {
		t.Run(r.Runtime, func(t *testing.T) {
			if r.Arch != "amd64" {
				t.Fatalf("the release is for %s, want amd64", r.Arch)
			}
			start := rt.StartNpm
			if r.Runtime == "codex" {
				start = rt.StartCodexNpm
			}
			npm := start(t)
			npm.PublishWith("x64", r.Version, []byte("not npm's tarball"), r.Integrity, "")
			npm.SetLatest(r.Version)
			npm.SignWith(func(string, string) any { return r.Signatures })
			_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
				Fetch: runtimeinstall.NpmFetch(http.DefaultClient, npm.URL(), r.Arch), Runtime: r.Runtime, Folder: t.TempDir(),
			})
			want := "Check the source of " + r.Runtime + "'s releases.\n  The tarball of " + r.Runtime + " " + r.Version + " does not match the checksum\n  " + r.Integrity + "."
			if err == nil || err.Error() != want {
				t.Fatalf("the error is %v, want %q", err, want)
			}
			if npm.TarballGets() != 1 {
				t.Errorf("%d tarballs read, want 1", npm.TarballGets())
			}
		})
	}
}

// TestTheRegistrysSignaturesAreReadStrictly publishes claude 2.1.320 in the registry's
// runtime cache with the annotation dev.qory.runtime.signatures in each form qory
// refuses: none, an empty list, a member other than keyid and sig, lists past the
// limits, no sig, and a signature that does not verify, or npm's signature with another
// integrity annotation. The cache's forms stop with the fix for the cache; the
// signature, with the fix for the source of the releases.
func TestTheRegistrysSignaturesAreReadStrictly(t *testing.T) {
	const cache = "Ask Qory Apiary's instance admin to check the registry's runtime cache\n" +
		"The manifest of claude 2.1.320 for linux/amd64 is not a runtime's:\n  "
	const bad = "its layer's dev.qory.runtime.signatures is not npm's signatures:\n  "
	text := func(v any) *string {
		b, _ := json.Marshal(v)
		s := string(b)
		return &s
	}
	none := ""
	many := make([]map[string]string, 17)
	for i := range many {
		many[i] = map[string]string{"keyid": rt.KeyID, "sig": "x"}
	}
	for _, c := range []struct {
		name       string
		signatures *string
		integrity  string
		want       string
	}{
		{"no annotation", &none, "", cache + "its layer has no dev.qory.runtime.signatures"},
		{"an empty list", text([]any{}), "", cache + bad + "it has no signature"},
		{"another member", text([]map[string]string{{"keyid": rt.KeyID, "sig": "x", "added": "y"}}), "", cache + bad + "its signature 1 is not an object of keyid and sig"},
		{"more than 16 signatures", text(many), "", cache + bad + "it has 17 signatures, and qory reads at most 16"},
		{"a sig of more than 1 KiB", text([]map[string]string{{"keyid": rt.KeyID, "sig": strings.Repeat("s", 1025)}}), "", cache + bad + "its signature 1 has a sig of more than 1024 bytes"},
		{"a keyid of more than 128 bytes", text([]map[string]string{{"keyid": strings.Repeat("k", 129), "sig": "x"}}), "", cache + bad + "its signature 1 has a keyid of more than 128 bytes"},
		{"a keyid with a line break", text([]map[string]string{{"keyid": "SHA256:a\nb", "sig": "x"}}), "", cache + bad + "its signature 1 has a keyid with a control character"},
		{"upper-case names", text([]map[string]string{{"KEYID": rt.KeyID, "SIG": "x"}}), "", cache + bad + "its signature 1 is not an object of keyid and sig"},
		{"a member twice", text([]json.RawMessage{json.RawMessage(`{"keyid":"a","keyid":"` + rt.KeyID + `","sig":"x"}`)}), "", cache + bad + "its signature 1 is not an object of keyid and sig"},
		{"no sig", text([]map[string]string{{"keyid": rt.KeyID}}), "", cache + bad + "its signature 1 has no keyid or no sig"},
		{"no JSON", text("x"), "", cache + bad + "it is not a JSON array"},
		{"a signature of another version", text(rt.Signatures("@anthropic-ai/claude-code-linux-x64@2.1.319", rt.Integrity(rt.PlainTarball(t, "2.1.320")))), "",
			"Check the source of claude's releases.\n  npm's signature of claude 2.1.320 does not verify.\n  The key is " + rt.KeyID + "."},
		{"a signature of the arm64 build", text(rt.Signatures("@anthropic-ai/claude-code-linux-arm64@2.1.320", rt.Integrity(rt.PlainTarball(t, "2.1.320")))), "",
			"Check the source of claude's releases.\n  npm's signature of claude 2.1.320 does not verify.\n  The key is " + rt.KeyID + "."},
		{"another integrity", text(rt.Signatures(signedClaude, rt.Integrity(rt.PlainTarball(t, "2.1.320")))), rt.Integrity([]byte("another tarball")),
			"Check the source of claude's releases.\n  npm's signature of claude 2.1.320 does not verify.\n  The key is " + rt.KeyID + "."},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFromRegistry(t)
			f.reg.Publish("2.1.320", true, map[string]rt.Layer{"amd64": {Tarball: rt.PlainTarball(t, "2.1.320"), Integrity: c.integrity, Signatures: c.signatures}})
			_, err := f.install(t, "amd64")
			if err == nil || err.Error() != c.want {
				t.Fatalf("the error is %v, want %q", err, c.want)
			}
			if f.reg.BlobGets() != 0 {
				t.Errorf("%d layers read", f.reg.BlobGets())
			}
			if got := files(t, f.folder); len(got) > 1 {
				t.Fatalf("files = %q, want nothing installed", got)
			}
		})
	}
}

// TestACachedInstallIsVerifiedAgain installs claude 2.1.320, then publishes the same
// tarball with a signature that does not verify: Install checks the signature on every
// fetch, before it looks for the version's folder, so the second install fails with the
// signature's refusal although the version's folder is complete, and leaves the folder.
func TestACachedInstallIsVerifiedAgain(t *testing.T) {
	npm := rt.StartNpm(t)
	npm.Publish("2.1.320")
	folder := t.TempDir()
	install := func() (runtimeinstall.Installed, error) {
		return runtimeinstall.Install(context.Background(), runtimeinstall.Options{
			Keys: rt.Keys(), Fetch: runtimeinstall.NpmFetch(http.DefaultClient, npm.URL(), "amd64"), Runtime: "claude", Folder: folder,
		})
	}
	first, err := install()
	if err != nil {
		t.Fatal(err)
	}
	first.Release()
	npm.SignWith(func(_, i string) any { return rt.Signatures("@anthropic-ai/claude-code-linux-x64@2.1.319", i) })
	_, err = install()
	want := "Check the source of claude's releases.\n  npm's signature of claude 2.1.320 does not verify.\n  The key is " + rt.KeyID + "."
	if err == nil || err.Error() != want {
		t.Fatalf("the second install: %v, want %q", err, want)
	}
	if npm.TarballGets() != 1 {
		t.Errorf("%d tarballs read, want 1", npm.TarballGets())
	}
	if _, err := os.Lstat(filepath.Join(first.Dir, "bin", "claude")); err != nil {
		t.Errorf("the version's folder: %v", err)
	}
}
