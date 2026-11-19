package reviewfiles

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// copyRoot is a reviewer's copy with a file, a folder with Go files, a binary file, a link
// inside the copy, and links to a file and a folder outside it, which has a secret.
func copyRoot(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	root := filepath.Join(base, "copy")
	for path, text := range map[string]string{
		"outside/secret.txt":     "needle in the secret\n",
		"copy/README.md":         "line one\nline two\nline three\n",
		"copy/src/main.go":       "package main\n\nfunc main() { needle() }\n",
		"copy/src/lib/needle.go": "package lib\n\n// needle is a needle.\nfunc needle() {}\n",
		"copy/src/notes.txt":     "a needle in a text file\na(b literal\n",
		"copy/image.bin":         "needle\x00\x01\x02",
	} {
		full := filepath.Join(base, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		"copy/inside-link":    "README.md",
		"copy/secret-link":    filepath.Join(outside, "secret.txt"),
		"copy/up-link":        "../outside/secret.txt",
		"copy/src/folder-out": outside,
	} {
		if err := os.Symlink(target, filepath.Join(base, link)); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// reply is one response of the server.
type reply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// serve sends messages to a server on root and returns its replies, one per line.
func serve(t *testing.T, root string, messages ...string) ([]reply, error) {
	t.Helper()
	var out bytes.Buffer
	err := Serve(root, strings.NewReader(strings.Join(messages, "\n")+"\n"), &out)
	var replies []reply
	for line := range strings.Lines(out.String()) {
		var r reply
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("a reply is no JSON line: %q", line)
		}
		replies = append(replies, r)
	}
	return replies, err
}

// call is the text and the isError of tools/call of name with args on root.
func call(t *testing.T, root, name string, args any) (string, bool) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args}})
	if err != nil {
		t.Fatal(err)
	}
	replies, err := serve(t, root, string(data))
	if err != nil || len(replies) != 1 {
		t.Fatalf("serve: %v, %d replies", err, len(replies))
	}
	if replies[0].Error != nil {
		t.Fatalf("tools/call %s: error %d %s", name, replies[0].Error.Code, replies[0].Error.Message)
	}
	var result struct {
		Content []struct{ Type, Text string } `json:"content"`
		IsError bool                          `json:"isError"`
	}
	if err := json.Unmarshal(replies[0].Result, &result); err != nil || len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatalf("the result is %s", replies[0].Result)
	}
	if result.IsError {
		if first, _, _ := strings.Cut(result.Content[0].Text, "\n"); len(first) > 100 {
			t.Errorf("the error's line 1 has %d characters: %q", len(first), first)
		}
	}
	return result.Content[0].Text, result.IsError
}

func TestInitializeAndListTheTools(t *testing.T) {
	root := copyRoot(t)
	replies, err := serve(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"client","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":"two","method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":5,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(replies) != 5 {
		t.Fatalf("%d replies: the notification got one, or a request none", len(replies))
	}
	var init struct {
		ProtocolVersion string                `json:"protocolVersion"`
		ServerInfo      struct{ Name string } `json:"serverInfo"`
		Capabilities    map[string]any        `json:"capabilities"`
	}
	if err := json.Unmarshal(replies[0].Result, &init); err != nil {
		t.Fatal(err)
	}
	if init.ProtocolVersion != "2025-06-18" || init.ServerInfo.Name != "qory_review" || init.Capabilities["tools"] == nil {
		t.Errorf("initialize is %s", replies[0].Result)
	}
	var list struct {
		Tools []struct {
			Name        string
			InputSchema struct {
				Required   []string
				Properties map[string]any
			} `json:"inputSchema"`
			Annotations map[string]any
		}
	}
	if string(replies[1].ID) != `"two"` {
		t.Errorf("the id is %s", replies[1].ID)
	}
	if err := json.Unmarshal(replies[1].Result, &list); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
		if tool.Annotations["readOnlyHint"] != true {
			t.Errorf("%s has the annotations %v", tool.Name, tool.Annotations)
		}
	}
	if !slices.Equal(names, []string{"read_file", "list_directory", "search_in_files"}) {
		t.Errorf("the tools are %v", names)
	}
	props := func(i int) []string {
		var keys []string
		for k := range list.Tools[i].InputSchema.Properties {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		return keys
	}
	if !slices.Equal(props(0), []string{"end_line", "path", "start_line"}) || !slices.Equal(props(1), []string{"path"}) ||
		!slices.Equal(props(2), []string{"glob", "path", "pattern"}) {
		t.Errorf("the arguments are %v, %v, %v", props(0), props(1), props(2))
	}
	if string(replies[2].Result) != "{}" {
		t.Errorf("ping is %s", replies[2].Result)
	}
	if replies[3].Error == nil || replies[3].Error.Code != -32601 {
		t.Errorf("an unknown method: %+v", replies[3])
	}
	if !strings.Contains(string(replies[4].Result), `"protocolVersion":"2025-11-25"`) {
		t.Errorf("an unknown version: %s", replies[4].Result)
	}
}

func TestReadFile(t *testing.T) {
	root := copyRoot(t)
	if text, bad := call(t, root, "read_file", map[string]any{"path": "README.md"}); bad || text != "1\tline one\n2\tline two\n3\tline three\n" {
		t.Errorf("the whole file: %q", text)
	}
	if text, _ := call(t, root, "read_file", map[string]any{"path": "./README.md", "start_line": 2, "end_line": 2}); text != "2\tline two\n" {
		t.Errorf("line 2: %q", text)
	}
	if text, _ := call(t, root, "read_file", map[string]any{"path": "README.md", "start_line": 3, "end_line": 9}); text != "3\tline three\n" {
		t.Errorf("lines 3 to 9: %q", text)
	}
	if text, _ := call(t, root, "read_file", map[string]any{"path": "README.md", "start_line": 9}); text != "[README.md has 3 lines]" {
		t.Errorf("line 9: %q", text)
	}
	// A link that stays inside the copy is read.
	if text, bad := call(t, root, "read_file", map[string]any{"path": "inside-link", "end_line": 1}); bad || text != "1\tline one\n" {
		t.Errorf("a link inside the copy: %q", text)
	}
	for name, c := range map[string]struct {
		args map[string]any
		want string
	}{
		"start 0":   {map[string]any{"path": "README.md", "start_line": 0}, "Give start_line from 1"},
		"end first": {map[string]any{"path": "README.md", "start_line": 3, "end_line": 2}, "Give start_line from 1"},
		"no path":   {map[string]any{}, "Give read_file a path"},
		"a folder":  {map[string]any{"path": "src"}, "is a folder"},
		"binary":    {map[string]any{"path": "image.bin"}, "has a NUL byte"},
		"missing":   {map[string]any{"path": "none.md"}, "is missing"},
	} {
		if text, bad := call(t, root, "read_file", c.args); !bad || !strings.Contains(text, c.want) {
			t.Errorf("%s: %v %q", name, bad, text)
		}
	}
}

func TestReadFileKeepsToItsLimits(t *testing.T) {
	root := t.TempDir()
	big := bytes.Repeat([]byte("x"), MaxFileBytes+1)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	if text, bad := call(t, root, "read_file", map[string]any{"path": "big.txt"}); !bad || !strings.Contains(text, fmt.Sprintf("has %d bytes", MaxFileBytes+1)) {
		t.Errorf("an oversize file: %q", text)
	}
	var lines strings.Builder
	for n := range 40000 {
		fmt.Fprintf(&lines, "line %d with some words\n", n+1)
	}
	if err := os.WriteFile(filepath.Join(root, "long.txt"), []byte(lines.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	text, bad := call(t, root, "read_file", map[string]any{"path": "long.txt"})
	if bad || len(text) > MaxReadBytes+100 {
		t.Fatalf("a long file: %v, %d bytes", bad, len(text))
	}
	last := text[strings.LastIndex(strings.TrimSuffix(text, "\n"), "\n")+1:]
	if !strings.HasPrefix(last, fmt.Sprintf("[cut at %d bytes: read on with start_line ", MaxReadBytes)) {
		t.Errorf("the last line is %q", last)
	}
}

func TestListDirectory(t *testing.T) {
	root := copyRoot(t)
	text, bad := call(t, root, "list_directory", map[string]any{"path": "."})
	want := "[file] README.md\n[file] image.bin\n[link] inside-link\n[link] secret-link\n[directory] src\n[link] up-link\n"
	if bad || text != want {
		t.Errorf("the root: %q", text)
	}
	if text, _ := call(t, root, "list_directory", map[string]any{"path": "src/lib"}); text != "[file] needle.go\n" {
		t.Errorf("src/lib: %q", text)
	}
	if text, bad := call(t, root, "list_directory", map[string]any{"path": "README.md"}); !bad || !strings.Contains(text, "is no folder") {
		t.Errorf("a file: %q", text)
	}
}

func TestSearchInFiles(t *testing.T) {
	root := copyRoot(t)
	// The link to a folder outside the copy is not followed, and the binary file is skipped.
	text, bad := call(t, root, "search_in_files", map[string]any{"pattern": "needle"})
	want := "src/lib/needle.go:3:// needle is a needle.\nsrc/lib/needle.go:4:func needle() {}\nsrc/main.go:3:func main() { needle() }\nsrc/notes.txt:1:a needle in a text file\n"
	if bad || text != want {
		t.Errorf("needle: %q", text)
	}
	if text, _ := call(t, root, "search_in_files", map[string]any{"pattern": `^func \w+\(\)`, "path": "src", "glob": "*.go"}); text != "src/lib/needle.go:4:func needle() {}\nsrc/main.go:3:func main() { needle() }\n" {
		t.Errorf("a regular expression with a glob: %q", text)
	}
	if text, _ := call(t, root, "search_in_files", map[string]any{"pattern": "needle", "glob": "src/lib/*"}); text != "src/lib/needle.go:3:// needle is a needle.\nsrc/lib/needle.go:4:func needle() {}\n" {
		t.Errorf("a glob with a path: %q", text)
	}
	// An invalid expression is searched as literal text.
	if text, _ := call(t, root, "search_in_files", map[string]any{"pattern": "a(b"}); text != "src/notes.txt:2:a(b literal\n" {
		t.Errorf("a literal: %q", text)
	}
	if text, _ := call(t, root, "search_in_files", map[string]any{"pattern": "absent words"}); text != "[no line matches]" {
		t.Errorf("no match: %q", text)
	}
	for name, args := range map[string]map[string]any{
		"no pattern": {"pattern": ""},
		"a bad glob": {"pattern": "x", "glob": "[a"},
	} {
		if text, bad := call(t, root, "search_in_files", args); !bad || !strings.HasPrefix(text, "Give ") {
			t.Errorf("%s: %q", name, text)
		}
	}
}

func TestSearchInFilesKeepsToItsLimit(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("y", MaxMatchText+10)
	if err := os.WriteFile(filepath.Join(root, "many.txt"), []byte(strings.Repeat("hit "+long+"\n", MaxMatches+5)), 0o644); err != nil {
		t.Fatal(err)
	}
	text, _ := call(t, root, "search_in_files", map[string]any{"pattern": "hit"})
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != MaxMatches+1 || lines[MaxMatches] != fmt.Sprintf("[cut at %d lines: give a narrower pattern, path or glob]", MaxMatches) {
		t.Fatalf("%d lines, the last %q", len(lines), lines[len(lines)-1])
	}
	if want := "many.txt:1:" + string([]rune("hit " + long)[:MaxMatchText]) + "…"; lines[0] != want {
		t.Errorf("the first line has %d characters", len(lines[0]))
	}
}

func TestEveryToolRefusesAPathOutsideTheCopy(t *testing.T) {
	root := copyRoot(t)
	paths := map[string]string{
		"/etc/passwd":               "is absolute",
		"../outside/secret.txt":     "leaves the root through ..",
		"src/../../outside":         "leaves the root through ..",
		"..":                        "leaves the root through ..",
		"secret-link":               "goes through a link that points out of the root",
		"up-link":                   "goes through a link that points out of the root",
		"src/folder-out":            "goes through a link that points out of the root",
		"src/folder-out/secret.txt": "goes through a link that points out of the root",
		"src/\x00x":                 "has a NUL",
	}
	for p, want := range paths {
		for _, tool := range ToolNames {
			args := map[string]any{"path": p}
			if tool == SearchInFiles {
				args["pattern"] = "needle"
			}
			text, bad := call(t, root, tool, args)
			if !bad || !strings.HasPrefix(text, "Give a path relative to the copy's root, inside it.\n") || !strings.HasSuffix(text, want+".") {
				t.Errorf("%s %q: %v %q", tool, p, bad, text)
			}
			if strings.Contains(text, "secret") && strings.Contains(text, "needle in") {
				t.Errorf("%s %q returned the secret: %q", tool, p, text)
			}
		}
	}
}

func TestTheToolsWriteNothing(t *testing.T) {
	root := copyRoot(t)
	before := tree(t, root)
	call(t, root, "read_file", map[string]any{"path": "README.md"})
	call(t, root, "list_directory", map[string]any{"path": "src"})
	call(t, root, "search_in_files", map[string]any{"pattern": "needle"})
	if after := tree(t, root); after != before {
		t.Errorf("the copy changed:\n%s\n%s", before, after)
	}
}

// tree is each entry below root with its mode, size and change time.
func tree(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s %v %d %v\n", p, info.Mode(), info.Size(), info.ModTime())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestRequestsOfTheWrongForm(t *testing.T) {
	root := copyRoot(t)
	replies, err := serve(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write_file","arguments":{"path":"x"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"README.md","mode":"w"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":7}}}`,
		`[{"jsonrpc":"2.0","id":4,"method":"ping"}]`,
		`{"jsonrpc":"1.0","id":5,"method":"ping"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	codes := []int{}
	for _, r := range replies {
		if r.Error == nil {
			t.Fatalf("no error: %s", r.Result)
		}
		codes = append(codes, r.Error.Code)
	}
	if !slices.Equal(codes, []int{-32602, -32602, -32602, -32600, -32600}) {
		t.Errorf("the codes are %v", codes)
	}
	replies, err = serve(t, root, `{"jsonrpc":"2.0","id":1,`)
	if err == nil || len(replies) != 1 || replies[0].Error == nil || replies[0].Error.Code != -32700 {
		t.Errorf("a message that is no JSON: %v, %+v", err, replies)
	}
	if _, err := serve(t, filepath.Join(root, "README.md")); err == nil || !strings.HasPrefix(err.Error(), "Give qory review-files the folder of the reviewer's copy.\n") {
		t.Errorf("a root that is a file: %v", err)
	}
}

func TestCodexConfigEnablesTheThreeTools(t *testing.T) {
	text, err := CodexConfig("/qory/qory", "/review/commit")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, "[mcp_servers.qory_review]\n") {
		t.Errorf("the table starts %q", text)
	}
	var doc struct {
		MCPServers map[string]struct {
			Command      string   `toml:"command"`
			Args         []string `toml:"args"`
			EnabledTools []string `toml:"enabled_tools"`
		} `toml:"mcp_servers"`
	}
	meta, err := toml.Decode(text, &doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Undecoded()) != 0 {
		t.Errorf("the table has the keys %v too", meta.Undecoded())
	}
	server, ok := doc.MCPServers["qory_review"]
	if !ok || len(doc.MCPServers) != 1 {
		t.Fatalf("the servers are %v", doc.MCPServers)
	}
	if server.Command != "/qory/qory" || !slices.Equal(server.Args, []string{"review-files", "/review/commit"}) {
		t.Errorf("the server starts %s %v", server.Command, server.Args)
	}
	if !slices.Equal(server.EnabledTools, []string{"read_file", "list_directory", "search_in_files"}) {
		t.Errorf("enabled_tools is %v", server.EnabledTools)
	}
}
