package runtimeinstalltest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sync"

	"github.com/qoryai/qory/internal/runtimeinstall"
)

// KeyID is the id of the test's signing key, which stands in for npm's.
const KeyID = "SHA256:qory-test-key"

// testKey is the test's signing key: an ECDSA P-256 key made once per test binary.
var testKey = sync.OnceValue(func() *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	return k
})

// Keys is the key set of the stubs: the public half of the test's key, as [KeyID], with
// no expiry. Each stub signs with it, so an install with these keys accepts what the
// stubs publish.
func Keys() []runtimeinstall.SigningKey {
	return []runtimeinstall.SigningKey{{KeyID: KeyID, Key: &testKey().PublicKey}}
}

// Sign is the signature of message with the test's key, as npm writes one: base64 of the
// ASN.1 ECDSA signature of its SHA-256 digest.
func Sign(message string) string {
	digest := sha256.Sum256([]byte(message))
	sig, err := ecdsa.SignASN1(rand.Reader, testKey(), digest[:])
	if err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// Signatures are npm's signatures of signed, <package>@<version>, with integrity: one
// signature with the test's key.
func Signatures(signed, integrity string) []runtimeinstall.Signature {
	return []runtimeinstall.Signature{{KeyID: KeyID, Sig: Sign(signed + ":" + integrity)}}
}

// SignaturesJSON is [Signatures] as JSON, as the annotation dev.qory.runtime.signatures
// has it.
func SignaturesJSON(signed, integrity string) string {
	b, _ := json.Marshal(Signatures(signed, integrity))
	return string(b)
}
