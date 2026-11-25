// Package reviewfiles serves read-only file tools over MCP on a reviewer's copy, for a
// runtime that has no file tool with its shell off, such as Codex. The server,
// qory_review, speaks JSON-RPC 2.0 on standard input and output, one message per line, and
// has three tools: read_file, list_directory and search_in_files. Every path is relative to
// the copy's root; an absolute path, a .. that leaves the root and a link that points out of
// it each return a tool error. No tool writes, and each sets readOnlyHint, so a runtime that
// approves no call still runs it.
package reviewfiles

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ServerID is the server's id: the name of its table under mcp_servers in Codex's
// configuration, and the middle of each tool's name, mcp__qory_review__read_file.
const ServerID = "qory_review"

// The tools' names, as enabled_tools lists them.
const (
	ReadFile      = "read_file"
	ListDirectory = "list_directory"
	SearchInFiles = "search_in_files"
)

// ToolNames is the name of each tool, in the order tools/list lists them.
var ToolNames = []string{ReadFile, ListDirectory, SearchInFiles}

// The limits of the tools.
const (
	// MaxFileBytes is the largest file that read_file reads and search_in_files searches.
	MaxFileBytes = 8 << 20
	// MaxReadBytes is the most bytes of text that one read_file returns.
	MaxReadBytes = 256 << 10
	// MaxEntries is the most entries that one list_directory returns.
	MaxEntries = 2000
	// MaxMatches is the most lines that one search_in_files returns.
	MaxMatches = 200
	// MaxMatchText is the most characters of a matching line that search_in_files returns.
	MaxMatchText = 500
	// binaryProbe is how many bytes from a file's start a NUL marks it as no text.
	binaryProbe = 8000
)

// protocolVersions are the MCP versions that the server's result of an initialize can have,
// newest first. An initialize that asks for one of them gets that one in its result, and
// any other gets the first.
var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// The JSON-RPC error codes the server returns.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeNoMethod       = -32601
	codeInvalidParams  = -32602
)

// message is one JSON-RPC message from the client: a request when it has an id, a
// notification when it has none.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// response is one JSON-RPC response.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the error of a response.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve serves the tools on the folder root until in ends, reading requests from in and
// writing responses to out. It returns nil at the end of in, and an error when root is no
// folder, when a message is not JSON, or when a write to out fails.
func Serve(root string, in io.Reader, out io.Writer) error {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("Give qory review-files the folder of the reviewer's copy.\n%s is no folder.", strconv.Quote(root))
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("Give qory review-files a folder it can read.\n%w", err)
	}
	defer r.Close()
	s := &server{root: r}
	decoder := json.NewDecoder(in)
	w := bufio.NewWriter(out)
	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			_ = send(w, response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{codeParse, "Send each message as JSON: " + err.Error()}})
			return fmt.Errorf("Send qory review-files JSON-RPC messages.\nA message is not JSON: %w", err)
		}
		resp, ok := s.handle(raw)
		if !ok {
			continue
		}
		if err := send(w, resp); err != nil {
			return err
		}
	}
}

// send writes r as one line and flushes it.
func send(w *bufio.Writer, r response) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		return err
	}
	return w.Flush()
}

// server is the state of one connection: the copy's root.
type server struct{ root *os.Root }

// handle is the response to raw, and false for a notification, which gets none.
func (s *server) handle(raw json.RawMessage) (response, bool) {
	var m message
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(raw, &m) != nil || m.JSONRPC != "2.0" || m.Method == "" {
		return response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{codeInvalidRequest, "Send one JSON-RPC 2.0 request object with a method."}}, true
	}
	if len(m.ID) == 0 || string(m.ID) == "null" {
		return response{}, false
	}
	resp := response{JSONRPC: "2.0", ID: m.ID}
	switch m.Method {
	case "initialize":
		resp.Result = s.initialize(m.Params)
	case "ping":
		resp.Result = struct{}{}
	case "tools/list":
		resp.Result = map[string]any{"tools": tools()}
	case "tools/call":
		result, err := s.call(m.Params)
		if err != nil {
			resp.Error = err
		} else {
			resp.Result = result
		}
	default:
		resp.Error = &rpcError{codeNoMethod, "Call initialize, ping, tools/list or tools/call: the server has no method " + strconv.Quote(m.Method) + "."}
	}
	return resp, true
}

// initialize is the result of initialize: the protocol version, the tools capability and
// the server's name.
func (s *server) initialize(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	version := protocolVersions[0]
	if slices.Contains(protocolVersions, p.ProtocolVersion) {
		version = p.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": ServerID, "version": "1"},
		"instructions":    "Read-only file tools on the reviewer's copy. Give every path relative to the copy's root.",
	}
}

// tool is one tool as tools/list lists it.
type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations"`
}

// tools is the server's tools, each with readOnlyHint.
func tools() []tool {
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	line := func(description string) map[string]any {
		return map[string]any{"type": "integer", "minimum": 1, "description": description}
	}
	readOnly := map[string]any{"readOnlyHint": true}
	return []tool{
		{
			Name: ReadFile,
			Description: fmt.Sprintf("Read a text file of the copy, each line with its number and a tab before it. "+
				"start_line and end_line select lines, from 1. A file over %d bytes is not read, and one read returns at most %d bytes.",
				MaxFileBytes, MaxReadBytes),
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []string{"path"},
				"additionalProperties": false,
				"properties": map[string]any{
					"path":       text("The file's path, relative to the copy's root."),
					"start_line": line("The first line to read, from 1."),
					"end_line":   line("The last line to read."),
				},
			},
			Annotations: readOnly,
		},
		{
			Name:        ListDirectory,
			Description: fmt.Sprintf("List a folder of the copy: one entry per line, marked [file] or [directory], at most %d.", MaxEntries),
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []string{"path"},
				"additionalProperties": false,
				"properties": map[string]any{
					"path": text("The folder's path, relative to the copy's root; . for the root."),
				},
			},
			Annotations: readOnly,
		},
		{
			Name: SearchInFiles,
			Description: fmt.Sprintf("Search the text files of the copy for a pattern, an RE2 regular expression; a pattern "+
				"that is no valid expression is searched as literal text. Returns path:line:text, at most %d lines.", MaxMatches),
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []string{"pattern"},
				"additionalProperties": false,
				"properties": map[string]any{
					"pattern": text("The RE2 regular expression or the literal text to search for."),
					"path":    text("The folder or file to search, relative to the copy's root; the root when absent."),
					"glob":    text("Search only files whose name matches this glob, such as *.go; a glob with / matches the path from the root."),
				},
			},
			Annotations: readOnly,
		},
	}
}

// toolResult is the result of tools/call: one text, and whether it is a tool's error.
type toolResult struct {
	Content []map[string]string `json:"content"`
	IsError bool                `json:"isError"`
}

// call runs the tool whose name is in params. A tool's failure is a result with isError;
// an unknown tool or arguments of the wrong form are an error of the request.
func (s *server) call(params json.RawMessage) (toolResult, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return toolResult{}, &rpcError{codeInvalidParams, "Give tools/call a name and arguments."}
	}
	if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
		p.Arguments = json.RawMessage("{}")
	}
	args := func(v any) *rpcError {
		decoder := json.NewDecoder(bytes.NewReader(p.Arguments))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(v); err != nil {
			return &rpcError{codeInvalidParams, "Give " + p.Name + " the arguments that its input schema lists: " + err.Error()}
		}
		return nil
	}
	var text string
	var err error
	switch p.Name {
	case ReadFile:
		var a struct {
			Path      string `json:"path"`
			StartLine *int   `json:"start_line"`
			EndLine   *int   `json:"end_line"`
		}
		if e := args(&a); e != nil {
			return toolResult{}, e
		}
		text, err = s.readFile(a.Path, a.StartLine, a.EndLine)
	case ListDirectory:
		var a struct {
			Path string `json:"path"`
		}
		if e := args(&a); e != nil {
			return toolResult{}, e
		}
		text, err = s.listDirectory(a.Path)
	case SearchInFiles:
		var a struct {
			Pattern string `json:"pattern"`
			Path    string `json:"path"`
			Glob    string `json:"glob"`
		}
		if e := args(&a); e != nil {
			return toolResult{}, e
		}
		text, err = s.searchInFiles(a.Pattern, a.Path, a.Glob)
	default:
		return toolResult{}, &rpcError{codeInvalidParams, "Call read_file, list_directory or search_in_files: the server has no tool " + strconv.Quote(p.Name) + "."}
	}
	if err != nil {
		return toolResult{Content: []map[string]string{{"type": "text", "text": err.Error()}}, IsError: true}, nil
	}
	return toolResult{Content: []map[string]string{{"type": "text", "text": text}}}, nil
}

// pathError is the error of a tool for the path rel: the fix on line 1, then the path and
// what is wrong with it.
func pathError(fix, rel, what string) error {
	return fmt.Errorf("%s\n%s %s.", fix, shown(rel), what)
}

// shown is rel as an error prints it: quoted, and cut at 200 characters.
func shown(rel string) string {
	if utf8.RuneCountInString(rel) > 200 {
		rel = string([]rune(rel)[:200]) + "…"
	}
	return strconv.Quote(rel)
}

// resolve is rel as a path below the root that os.Root opens, "." for the root itself. It
// returns an error for an absolute path, a .. that leaves the root and a NUL. os.Root stops
// a link that points out of the root at the open, and [openError] states it; a .. that
// leaves the root would also reach os.Root as such a link, so resolve stops it first.
func (s *server) resolve(rel string) (string, error) {
	const fix = "Give a path relative to the copy's root, inside it."
	if strings.ContainsRune(rel, 0) {
		return "", pathError(fix, rel, "has a NUL")
	}
	if rel == "" {
		rel = "."
	}
	if strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) {
		return "", pathError(fix, rel, "is absolute")
	}
	clean := path.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", pathError(fix, rel, "leaves the root through ..")
	}
	return clean, nil
}

// openError is the error of an open or a stat of rel through the root.
func openError(rel string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return pathError("Give a path that the copy has: list_directory lists its entries.", rel, "is missing")
	case strings.Contains(err.Error(), "escapes from parent"):
		return pathError("Give a path relative to the copy's root, inside it.", rel, "goes through a link that points out of the root")
	}
	return pathError("Give a path that the copy can read.", rel, "cannot be read: "+firstLine(err.Error()))
}

// readText reads the regular file at clean below the root, of at most [MaxFileBytes], and
// returns its text, or an error for a folder, a special file, an oversize file and a file
// with a NUL in its first [binaryProbe] bytes.
func (s *server) readText(rel, clean string) ([]byte, error) {
	info, err := s.root.Stat(clean)
	if err != nil {
		return nil, openError(rel, err)
	}
	if info.IsDir() {
		return nil, pathError("Read a file: list_directory lists a folder.", rel, "is a folder")
	}
	if !info.Mode().IsRegular() {
		return nil, pathError("Read a regular file.", rel, "is no regular file")
	}
	if info.Size() > MaxFileBytes {
		return nil, pathError(fmt.Sprintf("Read a file of at most %d bytes, or search it.", MaxFileBytes), rel,
			fmt.Sprintf("has %d bytes", info.Size()))
	}
	f, err := s.root.Open(clean)
	if err != nil {
		return nil, openError(rel, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, openError(rel, err)
	}
	if len(data) > MaxFileBytes {
		return nil, pathError(fmt.Sprintf("Read a file of at most %d bytes, or search it.", MaxFileBytes), rel, "grew past the limit")
	}
	if bytes.IndexByte(data[:min(len(data), binaryProbe)], 0) >= 0 {
		return nil, pathError("Read a text file.", rel, "has a NUL byte, so it is no text")
	}
	return data, nil
}

// readFile is read_file: the lines from start to end, each with its number and a tab.
func (s *server) readFile(rel string, start, end *int) (string, error) {
	if rel == "" {
		return "", errors.New("Give read_file a path, relative to the copy's root.")
	}
	first, last := 1, 0
	if start != nil {
		first = *start
	}
	if end != nil {
		last = *end
	}
	if first < 1 || (end != nil && last < first) {
		return "", fmt.Errorf("Give start_line from 1, and end_line from start_line on.\nstart_line is %d and end_line is %d.", first, last)
	}
	clean, err := s.resolve(rel)
	if err != nil {
		return "", err
	}
	data, err := s.readText(rel, clean)
	if err != nil {
		return "", err
	}
	lines := strings.SplitAfter(strings.ToValidUTF8(string(data), "�"), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if first > len(lines) {
		return fmt.Sprintf("[%s has %d lines]", rel, len(lines)), nil
	}
	if end == nil || last > len(lines) {
		last = len(lines)
	}
	rows := make([]string, 0, last-first+1)
	size := 0
	for n := first; n <= last; n++ {
		row := strconv.Itoa(n) + "\t" + strings.TrimSuffix(lines[n-1], "\n") + "\n"
		rows = append(rows, row)
		size += len(row)
	}
	if size <= MaxReadBytes {
		return strings.Join(rows, ""), nil
	}
	// The rows and the note of the cut make at most MaxReadBytes: the room for the rows
	// leaves space for a note with the largest line number.
	note := func(n int) string {
		return fmt.Sprintf("[cut at %d bytes: read on with start_line %d]\n", MaxReadBytes, n)
	}
	room := MaxReadBytes - len(note(math.MaxInt64))
	var b strings.Builder
	for i, row := range rows {
		n := first + i
		if b.Len()+len(row) > room {
			if b.Len() == 0 {
				b.WriteString(cutBytes(row, room-1) + "\n")
				n++
			}
			b.WriteString(note(n))
			break
		}
		b.WriteString(row)
	}
	return b.String(), nil
}

// listDirectory is list_directory: one entry per line, [file] or [directory] and its name,
// in name order; a link is [link], and any other entry [other].
func (s *server) listDirectory(rel string) (string, error) {
	if rel == "" {
		return "", errors.New("Give list_directory a path, relative to the copy's root; . is the root.")
	}
	clean, err := s.resolve(rel)
	if err != nil {
		return "", err
	}
	info, err := s.root.Stat(clean)
	if err != nil {
		return "", openError(rel, err)
	}
	if !info.IsDir() {
		return "", pathError("List a folder: read_file reads a file.", rel, "is no folder")
	}
	entries, err := fs.ReadDir(s.root.FS(), clean)
	if err != nil {
		return "", openError(rel, err)
	}
	var b strings.Builder
	for i, e := range entries {
		if i == MaxEntries {
			fmt.Fprintf(&b, "[cut at %d entries: the folder has %d]\n", MaxEntries, len(entries))
			break
		}
		mark := "[other]"
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			mark = "[link]"
		case e.IsDir():
			mark = "[directory]"
		case e.Type().IsRegular():
			mark = "[file]"
		}
		name := e.Name()
		if strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			name = strconv.Quote(name)
		}
		b.WriteString(mark + " " + name + "\n")
	}
	if len(entries) == 0 {
		b.WriteString("[the folder is empty]\n")
	}
	return b.String(), nil
}

// searchInFiles is search_in_files: each line of a text file below rel that matches
// pattern, as path:line:text, at most [MaxMatches]. It follows no link and reads no file
// over [MaxFileBytes].
func (s *server) searchInFiles(pattern, rel, glob string) (string, error) {
	if pattern == "" {
		return "", errors.New("Give search_in_files a pattern: an RE2 regular expression or literal text.")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		re = regexp.MustCompile(regexp.QuoteMeta(pattern))
	}
	if glob != "" {
		if _, err := path.Match(glob, ""); err != nil {
			return "", fmt.Errorf("Give a glob such as *.go, in the form of Go's path.Match.\n%s is no glob.", shown(glob))
		}
	}
	clean, err := s.resolve(rel)
	if err != nil {
		return "", err
	}
	if _, err := s.root.Stat(clean); err != nil {
		return "", openError(rel, err)
	}
	var b strings.Builder
	matches := 0
	walkErr := fs.WalkDir(s.root.FS(), clean, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.Type().IsRegular() || !globMatch(glob, p) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > MaxFileBytes {
			return nil
		}
		data, err := fs.ReadFile(s.root.FS(), p)
		if err != nil || bytes.IndexByte(data[:min(len(data), binaryProbe)], 0) >= 0 {
			return nil
		}
		for n, line := range strings.Split(strings.ToValidUTF8(string(data), "�"), "\n") {
			if !re.MatchString(line) {
				continue
			}
			if matches == MaxMatches {
				fmt.Fprintf(&b, "[cut at %d lines: give a narrower pattern, path or glob]\n", MaxMatches)
				return fs.SkipAll
			}
			matches++
			line = strings.TrimSuffix(line, "\r")
			if utf8.RuneCountInString(line) > MaxMatchText {
				line = string([]rune(line)[:MaxMatchText]) + "…"
			}
			fmt.Fprintf(&b, "%s:%d:%s\n", p, n+1, line)
		}
		return nil
	})
	if walkErr != nil {
		return "", openError(rel, walkErr)
	}
	if matches == 0 {
		return "[no line matches]", nil
	}
	return b.String(), nil
}

// globMatch reports whether the file at p, below the root, matches glob: by its name, or by
// its path from the root when glob has a /. An empty glob matches every file.
func globMatch(glob, p string) bool {
	if glob == "" {
		return true
	}
	target := path.Base(p)
	if strings.Contains(glob, "/") {
		target = p
	}
	ok, _ := path.Match(glob, target)
	return ok
}

// cutBytes is text cut to at most limit bytes, at a character's start.
func cutBytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

// firstLine is text up to its first line break.
func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}
