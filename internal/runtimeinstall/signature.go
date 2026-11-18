package runtimeinstall

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"
)

// npmKeys are npm's public keys for registry signatures, as npm publishes them at
// https://registry.npmjs.org/-/npm/v1/keys, read on 2026-11-18. qory never fetches
// them at run time: a new key of npm needs a new qory. expires is "" for a key with no
// expiry.
var npmKeys = []struct{ keyid, expires, key string }{
	{
		keyid:   "SHA256:jl3bwswu80PjjokCgh0o2w5c2U4LhQAE57gj9cz1kzA",
		expires: "2025-01-29T00:00:00.000Z",
		key:     "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE1Olb3zMAFFxXKHiIkQO5cJ3Yhl5i6UPp+IhuteBJbuHcA5UogKo0EWtlWwW6KSaKoTNEYL7JlCQiVnkhBktUgg==",
	},
	{
		keyid:   "SHA256:DhQ8wR5APBvFHLF/+Tc+AYvPOdTpcIDqOhxsBHRwC7U",
		expires: "",
		key:     "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEY6Ya7W++7aUPzvMTrezH6Ycx3c+HOKYCcNGybJZSCJq/fd7Qa8uuAKtdIkUQtQiEKERhAmE5lMMJhP8OkDOa2g==",
	},
}

// SigningKey is a public key with which npm signs a version of a package: ECDSA P-256,
// whose signature is over the SHA-256 digest of <package>@<version>:<integrity>.
type SigningKey struct {
	// KeyID is npm's id of the key, such as SHA256:<base64>.
	KeyID string
	// Expires is when the key expired; the zero time for a key with no expiry.
	Expires time.Time
	// Key is the public key.
	Key *ecdsa.PublicKey
}

// NpmKeys are npm's public keys for registry signatures that this qory has built in.
func NpmKeys() ([]SigningKey, error) {
	keys := make([]SigningKey, 0, len(npmKeys))
	for _, k := range npmKeys {
		der, err := base64.StdEncoding.DecodeString(k.key)
		if err != nil {
			return nil, fmt.Errorf("npm's key %s is not base64: %w", k.keyid, err)
		}
		pub, err := x509.ParsePKIXPublicKey(der)
		if err != nil {
			return nil, fmt.Errorf("npm's key %s is not a public key: %w", k.keyid, err)
		}
		ec, ok := pub.(*ecdsa.PublicKey)
		if !ok || ec.Curve.Params().Name != "P-256" {
			return nil, fmt.Errorf("npm's key %s is not an ECDSA P-256 key", k.keyid)
		}
		key := SigningKey{KeyID: k.keyid, Key: ec}
		if k.expires != "" {
			if key.Expires, err = time.Parse(time.RFC3339, k.expires); err != nil {
				return nil, fmt.Errorf("npm's key %s has the expiry %q: %w", k.keyid, k.expires, err)
			}
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// Signature is one of npm's signatures of a version of a package: dist.signatures in
// npm's document of the version, or the layer annotation dev.qory.runtime.signatures in
// the runtime cache of a registry.
type Signature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

// The limits of a list of signatures.
const (
	maxSignatures    = 16
	maxSignatureText = 1 << 10
)

// decodeSignatures reads b, a JSON array of signatures, each with a keyid and a sig, both
// non-empty strings of at most [maxSignatureText] bytes. The array has 1 to
// [maxSignatures] entries. strict refuses an entry with another member. The error is
// the cause, for a line of a refusal.
func decodeSignatures(b []byte, strict bool) ([]Signature, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, errors.New("it is not a JSON array")
	}
	switch {
	case len(raw) == 0:
		return nil, errors.New("it has no signature")
	case len(raw) > maxSignatures:
		return nil, fmt.Errorf("it has %d signatures, and qory reads at most %d", len(raw), maxSignatures)
	}
	sigs := make([]Signature, len(raw))
	for i, r := range raw {
		var s struct {
			KeyID *string `json:"keyid"`
			Sig   *string `json:"sig"`
		}
		d := json.NewDecoder(bytes.NewReader(r))
		if strict {
			d.DisallowUnknownFields()
		}
		if err := d.Decode(&s); err != nil {
			return nil, fmt.Errorf("its signature %d is not an object of keyid and sig", i+1)
		}
		if _, err := d.Token(); err != io.EOF {
			return nil, fmt.Errorf("its signature %d is not an object of keyid and sig", i+1)
		}
		switch {
		case s.KeyID == nil || *s.KeyID == "" || s.Sig == nil || *s.Sig == "":
			return nil, fmt.Errorf("its signature %d has no keyid or no sig", i+1)
		case len(*s.KeyID) > maxSignatureText || len(*s.Sig) > maxSignatureText:
			return nil, fmt.Errorf("its signature %d has a keyid or a sig of more than %d bytes", i+1, maxSignatureText)
		}
		sigs[i] = Signature{KeyID: *s.KeyID, Sig: *s.Sig}
	}
	return sigs, nil
}

// verify checks rel's signatures with keys at now: one signature by a key of keys that
// has not expired must verify over <rel.Signed>:<rel.Integrity>. what is the release as
// an error shows it, such as codex 0.170.0. A bad signature by a key qory has is
// refused first, then a key qory does not have, then an expired key.
func verify(rel Release, keys []SigningKey, now time.Time, what string) error {
	if len(rel.Signatures) == 0 {
		return fmt.Errorf(textRelease+"npm has no signature of %s", what)
	}
	digest := sha256.Sum256([]byte(rel.Signed + ":" + rel.Integrity))
	var unknown, expired, bad string
	for _, s := range rel.Signatures {
		i := slices.IndexFunc(keys, func(k SigningKey) bool { return k.KeyID == s.KeyID })
		switch {
		case i < 0:
			unknown = s.KeyID
		case !keys[i].Expires.IsZero() && !now.Before(keys[i].Expires):
			expired = s.KeyID
		default:
			sig, err := base64.StdEncoding.DecodeString(s.Sig)
			if err == nil && ecdsa.VerifyASN1(keys[i].Key, digest[:], sig) {
				return nil
			}
			bad = s.KeyID
		}
	}
	switch {
	case bad != "":
		return fmt.Errorf(textRelease+"npm's signature of %s with key %s does not verify", what, bad)
	case unknown != "":
		return fmt.Errorf("Update qory.\nnpm signed %s with key %s, which this qory does not have.", what, unknown)
	}
	return fmt.Errorf("Update qory.\nnpm signed %s with key %s, which expired on %s.", what, expired, keysExpiry(keys, expired))
}

// keysExpiry is the expiry date of the key keyid of keys, as 2006-01-02.
func keysExpiry(keys []SigningKey, keyid string) string {
	for _, k := range keys {
		if k.KeyID == keyid {
			return k.Expires.UTC().Format(time.DateOnly)
		}
	}
	return ""
}
