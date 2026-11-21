package render

import (
	"slices"
	"strings"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/module"
)

// bashTool is Claude Code's shell tool.
const bashTool = "Bash"

// IsReviewAgent reports whether e is a member of a group role that the review_group of a
// workflow of the stack names: a reviewer of a review session, whose agent file gets no
// Bash, see [WithoutBash].
func IsReviewAgent(res *compose.Result, e compose.Entry) bool {
	if e.Kind != "agents" || res.Stack == nil {
		return false
	}
	for _, w := range res.Stack.Workflows {
		if w.ReviewGroup != "" && slices.Contains(res.Groups["agents/"+w.ReviewGroup], e.Name) {
			return true
		}
	}
	return false
}

// WithoutBash returns an agent file of Claude Code with no Bash: the tools key loses each
// Bash entry, including one with a rule such as Bash(git *), and disallowedTools lists
// Bash once, kept after the tools it lists already. An agent with no tools key inherits
// its tools, so it gets disallowedTools alone. A tools key that lists nothing but Bash is
// left as written, and disallowedTools then removes the tool the key lists; Claude Code
// does not start such an agent. Every other line of the file is kept; data that has no
// frontmatter gets one.
func WithoutBash(data []byte) []byte {
	doc, err := module.ParseDocument(data, "")
	if err != nil {
		return data
	}
	if tools, ok := toolList(doc.Front["tools"]); ok {
		kept := slices.DeleteFunc(slices.Clone(tools), isBash)
		if len(kept) != len(tools) && len(kept) > 0 {
			data = module.SetFront(data, "tools", strings.Join(kept, ", "))
		}
	}
	denied, _ := toolList(doc.Front["disallowedTools"])
	if !slices.ContainsFunc(denied, isBash) {
		data = module.SetFront(data, "disallowedTools", strings.Join(append(denied, bashTool), ", "))
	}
	return data
}

// isBash reports whether tool is the Bash tool, with or without a rule in parentheses.
func isBash(tool string) bool {
	return tool == bashTool || strings.HasPrefix(tool, bashTool+"(")
}

// toolList is the tools of a tools or disallowedTools value as Claude Code reads it, a
// comma-separated string or a list of strings, trimmed and without an empty item, and
// whether the value is one of the two. A comma inside parentheses belongs to the rule.
func toolList(v any) ([]string, bool) {
	var out []string
	switch x := v.(type) {
	case string:
		depth, start := 0, 0
		for i, r := range x + "," {
			switch {
			case r == '(':
				depth++
			case r == ')' && depth > 0:
				depth--
			case r == ',' && depth == 0:
				out = append(out, x[start:i])
				start = i + 1
			}
		}
	case []any:
		for _, item := range x {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
	default:
		return nil, false
	}
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	return slices.DeleteFunc(out, func(s string) bool { return s == "" }), true
}
