// Command codexupstream is a stub of the Responses API for the tests of a real Codex run
// in package cmd, such as TestARealWalledCodexRunReachesTheModelThroughTheGateway. It runs
// in a container: it signs a certificate for the container's addresses with the
// authority in QORY_TEST_UPSTREAM_CA and QORY_TEST_UPSTREAM_CA_KEY, each DER in standard
// base64, serves HTTPS on 8443, and prints "upstream: listening" once it listens, with
// /tmp/upstream.jsonl made empty. With QORY_TEST_UPSTREAM_PLAIN=1 it serves plain HTTP on
// 127.0.0.1:8080 in place of HTTPS, for a Codex container in its network namespace, and
// needs no authority. It appends one JSON line per request to that file before it
// answers: a POST to /v1/responses with a streamed message with the text Hi, and every
// other request with 404. Each line has the method, the path, the SHA-256 of the
// Authorization header's bearer value, the chatgpt-account-id header, the text of each
// input message, and the output of each tool call in the input.
//
// QORY_TEST_UPSTREAM_SCRIPT, a JSON list of steps, makes the stub answer the requests to
// /v1/responses with one step each, in order, and with the message Hi after the last. A
// step is one of {"call": <tool>, "args": {...}, "ns": <namespace>}, a function call;
// {"custom": <tool>, "input": <text>}, a custom tool call such as apply_patch; and
// {"text": <text>}, a message. An argument "__SESSION__" becomes the session id of the
// last "session ID <n>" in a tool output of the request, as write_stdin needs.
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
	"regexp"
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
	Outputs []string `json:"outputs,omitempty"`
}

// step is one answer of the script, see the package's documentation.
type step struct {
	Call   string         `json:"call"`
	Args   map[string]any `json:"args"`
	NS     string         `json:"ns"`
	Custom string         `json:"custom"`
	Input  string         `json:"input"`
	Text   string         `json:"text"`
}

// stub logs each request and answers it, with the next step of script while one is left.
type stub struct {
	mu     sync.Mutex
	script []step
	next   int
}

// sessionID finds the session id in a tool output of exec_command.
var sessionID = regexp.MustCompile(`session ID (\d+)`)

func (s *stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	e := entry{Method: r.Method, Path: r.URL.Path, Account: r.Header.Get("chatgpt-account-id")}
	if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		sum := sha256.Sum256([]byte(v))
		e.Bearer = hex.EncodeToString(sum[:])
	}
	var req struct {
		Input []struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Output  json.RawMessage `json:"output"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	session := ""
	if json.Unmarshal(body, &req) == nil {
		for _, m := range req.Input {
			for _, c := range m.Content {
				if c.Text != "" {
					e.Texts = append(e.Texts, m.Role+": "+c.Text[:min(len(c.Text), 300)])
				}
			}
			if strings.HasSuffix(m.Type, "_output") && len(m.Output) > 0 {
				out := string(m.Output)
				var text string
				if json.Unmarshal(m.Output, &text) == nil {
					out = text
				}
				e.Outputs = append(e.Outputs, out)
				if id := sessionID.FindStringSubmatch(out); id != nil {
					session = id[1]
				}
			}
		}
	}
	s.write(e)
	if r.Method == http.MethodPost && r.URL.Path == "/v1/responses" {
		s.mu.Lock()
		var next *step
		if s.next < len(s.script) {
			next = &s.script[s.next]
			s.next++
		}
		n := s.next
		s.mu.Unlock()
		if next != nil {
			answerStep(w, *next, n, session)
			return
		}
		answer(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	io.WriteString(w, `{"error":{"message":"stub: not found","type":"invalid_request_error"}}`)
}

// write appends e to the log, before the answer, so the log has the request once the
// client has the answer.
func (s *stub) write(e entry) {
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
	stream(w, "resp_test_1", map[string]any{"type": "message", "role": "assistant", "id": "msg_test_1",
		"content": []any{map[string]any{"type": "output_text", "text": "Hi"}}})
}

// stream streams a Responses answer with the id given and one output item.
func stream(w http.ResponseWriter, id string, item map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	for _, e := range []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": id}},
		{"type": "response.output_item.done", "output_index": 0, "item": item},
		{"type": "response.completed", "response": map[string]any{"id": id,
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}},
	} {
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e["type"], b)
	}
}

// answerStep streams a Responses answer with the one output item of st, the script's
// step number n, with each argument "__SESSION__" replaced by session.
func answerStep(w http.ResponseWriter, st step, n int, session string) {
	id := fmt.Sprint(n)
	var item map[string]any
	switch {
	case st.Call != "":
		args := map[string]any{}
		for k, v := range st.Args {
			if v == "__SESSION__" {
				var sid int
				fmt.Sscan(session, &sid)
				v = sid
			}
			args[k] = v
		}
		b, _ := json.Marshal(args)
		item = map[string]any{"type": "function_call", "id": "fc_" + id, "call_id": "call_" + id, "name": st.Call, "arguments": string(b)}
		if st.NS != "" {
			item["namespace"] = st.NS
		}
	case st.Custom != "":
		item = map[string]any{"type": "custom_tool_call", "id": "ctc_" + id, "call_id": "call_" + id, "name": st.Custom, "input": st.Input}
	default:
		item = map[string]any{"type": "message", "role": "assistant", "id": "msg_" + id,
			"content": []any{map[string]any{"type": "output_text", "text": st.Text}}}
	}
	stream(w, "resp_"+id, item)
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
	s := &stub{}
	if script := os.Getenv("QORY_TEST_UPSTREAM_SCRIPT"); script != "" {
		if err := json.Unmarshal([]byte(script), &s.script); err != nil {
			fmt.Fprintln(os.Stderr, "upstream: the script:", err)
			os.Exit(1)
		}
	}
	var ln net.Listener
	var err error
	if os.Getenv("QORY_TEST_UPSTREAM_PLAIN") == "1" {
		ln, err = net.Listen("tcp", "127.0.0.1:8080")
	} else {
		var cert *tls.Certificate
		if cert, err = leaf(); err == nil {
			ln, err = tls.Listen("tcp", ":8443", &tls.Config{Certificates: []tls.Certificate{*cert}})
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "upstream:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(logFile, nil, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "upstream:", err)
		os.Exit(1)
	}
	fmt.Println("upstream: listening")
	fmt.Fprintln(os.Stderr, "upstream:", http.Serve(ln, s))
	os.Exit(1)
}
