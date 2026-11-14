// Command codexupstream is a stub of the Responses API for the test of a real Codex run
// behind the wall, TestARealWalledCodexRunReachesTheModelThroughTheGateway in package
// cmd. It runs in a container: it signs a certificate for the container's addresses with
// the authority in QORY_TEST_UPSTREAM_CA and QORY_TEST_UPSTREAM_CA_KEY, each DER in
// standard base64, serves HTTPS on 8443, and prints "upstream: listening" once it
// listens. It answers a POST to /v1/responses with a streamed message with the text Hi,
// and every other request with 404. It appends one JSON line per request to
// /tmp/upstream.jsonl: the method, the path, the SHA-256 of the Authorization header's
// bearer value, the chatgpt-account-id header, and the text of each input message.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// logFile is the file the stub appends each request to.
const logFile = "/tmp/upstream.jsonl"

// entry is one request as the stub logs it.
type entry struct {
	Method  string   `json:"method"`
	Path    string   `json:"path"`
	Bearer  string   `json:"bearer"`
	Account string   `json:"account"`
	Texts   []string `json:"texts,omitempty"`
}

// stub logs each request and answers it.
type stub struct{ mu sync.Mutex }

func (s *stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	e := entry{Method: r.Method, Path: r.URL.Path, Account: r.Header.Get("chatgpt-account-id")}
	if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		sum := sha256.Sum256([]byte(v))
		e.Bearer = hex.EncodeToString(sum[:])
	}
	var req struct {
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if json.Unmarshal(body, &req) == nil {
		for _, m := range req.Input {
			for _, c := range m.Content {
				if c.Text != "" {
					e.Texts = append(e.Texts, m.Role+": "+c.Text[:min(len(c.Text), 300)])
				}
			}
		}
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/responses" {
		answer(w)
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"message":"stub: not found","type":"invalid_request_error"}}`)
	}
	b, _ := json.Marshal(e)
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "upstream:", err)
		return
	}
	f.Write(append(b, '\n'))
	f.Close()
}

// answer streams a Responses answer with one assistant message, the text Hi.
func answer(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	for _, e := range []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": "resp_test_1"}},
		{"type": "response.output_item.done", "item": map[string]any{"type": "message", "role": "assistant", "id": "msg_test_1",
			"content": []any{map[string]any{"type": "output_text", "text": "Hi"}}}},
		{"type": "response.completed", "response": map[string]any{"id": "resp_test_1",
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}},
	} {
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e["type"], b)
	}
}

// leaf is a server certificate for the addresses of this machine, signed by the
// authority of the environment.
func leaf() (*tls.Certificate, error) {
	certDER, err1 := base64.StdEncoding.DecodeString(os.Getenv("QORY_TEST_UPSTREAM_CA"))
	keyDER, err2 := base64.StdEncoding.DecodeString(os.Getenv("QORY_TEST_UPSTREAM_CA_KEY"))
	if err := errors.Join(err1, err2); err != nil {
		return nil, err
	}
	ca, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		return nil, err
	}
	caKey, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("the authority's key is not ECDSA")
	}
	var ips []net.IP
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			ips = append(ips, n.IP)
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      pkix.Name{CommonName: "qory test upstream"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der, ca.Raw}, PrivateKey: key}, nil
}

func main() {
	cert, err := leaf()
	if err != nil {
		fmt.Fprintln(os.Stderr, "upstream:", err)
		os.Exit(1)
	}
	ln, err := tls.Listen("tcp", ":8443", &tls.Config{Certificates: []tls.Certificate{*cert}})
	if err != nil {
		fmt.Fprintln(os.Stderr, "upstream:", err)
		os.Exit(1)
	}
	fmt.Println("upstream: listening")
	fmt.Fprintln(os.Stderr, "upstream:", http.Serve(ln, &stub{}))
	os.Exit(1)
}
