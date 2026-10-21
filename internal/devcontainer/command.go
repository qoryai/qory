package devcontainer

import (
	"fmt"
	"strings"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/qory/internal/integration"
)

// The words of a command's variables. The first %s is where the variable is: a
// lifecycle command, or a named command of one as [textNamed] has it.
const (
	textHostVariable = "%s uses %s, a value of the host; no value of the host reaches the container"
	textNoID         = "%s uses ${devcontainerId}, which is given no value here"
	textNoEnvName    = "%s uses ${containerEnv} without the name of a variable"
	// textNamed is a named command of a lifecycle command: the lifecycle command, then
	// the name.
	textNamed = "%s's command %s"
)

// Command is a lifecycle command, read: its member and what it runs. A command with no
// steps runs nothing.
type Command struct {
	// Member is the lifecycle command's member, such as postCreateCommand.
	Member string
	// Steps are what the command runs. The string and the array form are one step
	// without a name. The object form is one step per member that runs something, in
	// the order written, each with its name; the steps run in parallel.
	Steps []Step
	// Named says the command is of the object form.
	Named bool

	// expanded says [Command.Expand] made it, so [Run] runs it.
	expanded bool
}

// Step is one process a command runs: Line run as /bin/sh -c Line, or Args run
// directly, with no shell. Exactly one of the two is set.
type Step struct {
	// Name is the member's name in the object form, and "" in the others.
	Name string
	// Line is the string form, run with /bin/sh -c.
	Line string
	// Args is the array form: the program, then its arguments.
	Args []string
}

// Empty reports whether the command runs nothing: left out, null, empty, or an object
// of commands that each run nothing.
func (c Command) Empty() bool { return len(c.Steps) == 0 }

// label is where a step is, for an error: the lifecycle command, or its named command.
func (c Command) label(s Step) string {
	if c.Named {
		return fmt.Sprintf(textNamed, c.Member, integration.Printable(accesskey.ShownName(s.Name, false)))
	}
	return c.Member
}

// Vars are the values of the variables a command may use. The reference names each
// variable as ${name}, and the spec has a value "substituted at the time the value is
// applied":
//
//   - ${containerWorkspaceFolder}, "the path that the workspaces files can be found in
//     the container", is WorkspaceFolder;
//   - ${containerWorkspaceFolderBasename}, "name of the folder where the workspace
//     files can be found in the container", is WorkspaceFolderBasename;
//   - ${containerEnv:NAME}, "value of an existing environment variable inside the
//     container", is NAME's value in Env, the last when Env sets it more than once,
//     and ${containerEnv:NAME:default} is the default when Env does not set it. The
//     reference lists it for remoteEnv; the commands here run in the container, where
//     the variable is the container's;
//   - ${localEnv:NAME}, "value of an environment variable on the host machine", and
//     ${env:NAME}, which the reference implementation reads as ${localEnv:NAME}, are
//     refused, and so are ${localWorkspaceFolder} and ${localWorkspaceFolderBasename},
//     the "local folder that was opened in the devcontainer.json supporting service /
//     tool": no value of the host reaches the container;
//   - ${devcontainerId}, which lets "Features to refer to an identifier that is unique
//     to the dev container they are installed into", is derived by a tool on the host
//     and given no value here. It is refused.
//
// Any other ${...} is left as it is, as the reference implementation leaves it, so a
// shell's ${HOME} reaches the shell.
type Vars struct {
	// WorkspaceFolder is ${containerWorkspaceFolder}.
	WorkspaceFolder string
	// WorkspaceFolderBasename is ${containerWorkspaceFolderBasename}.
	WorkspaceFolderBasename string
	// Env is what ${containerEnv:NAME} reads, NAME=value entries.
	Env []string
}

// Expand returns the command with its variables replaced as [Vars] has them, in each
// step's line or each of its arguments, ready for [Run]. It refuses a variable of the
// host, ${devcontainerId}, and a ${containerEnv} without a name, naming the command and
// the variable's kind, never the text or a value.
func (c Command) Expand(v Vars) (Command, error) {
	out := c
	out.Steps = make([]Step, len(c.Steps))
	for i, s := range c.Steps {
		where := c.label(s)
		e := Step{Name: s.Name}
		if s.Args == nil {
			line, err := v.Expand(where, s.Line)
			if err != nil {
				return Command{}, err
			}
			e.Line = line
		} else {
			e.Args = make([]string, len(s.Args))
			for j, a := range s.Args {
				arg, err := v.Expand(where, a)
				if err != nil {
					return Command{}, err
				}
				e.Args[j] = arg
			}
		}
		out.Steps[i] = e
	}
	out.expanded = true
	return out, nil
}

// Expand is s with its variables replaced as [Vars] has them. where names the place s
// is for an error, such as a member. A variable is ${ up to the first } after it, line
// breaks between them included, and its name is what comes before the first colon in
// it, or all of it. In ${containerEnv:...}, NAME runs to the next colon, and the
// default is everything after that colon, further colons included; an empty NAME,
// as in ${containerEnv} or ${containerEnv::d}, is refused. A ${ with no } after it is
// left as it is. The reference implementation reads a variable otherwise in some of
// these cases; this is how Expand reads one.
func (v Vars) Expand(where, s string) (string, error) {
	var b strings.Builder
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			b.WriteString(s)
			return b.String(), nil
		}
		j := strings.IndexByte(s[i+2:], '}')
		if j < 0 {
			b.WriteString(s)
			return b.String(), nil
		}
		whole := s[i : i+2+j+1]
		name, rest, _ := strings.Cut(s[i+2:i+2+j], ":")
		value := whole
		switch name {
		case "containerWorkspaceFolder":
			value = v.WorkspaceFolder
		case "containerWorkspaceFolderBasename":
			value = v.WorkspaceFolderBasename
		case "containerEnv":
			key, fallback, _ := strings.Cut(rest, ":")
			if key == "" {
				return "", fmt.Errorf(textNoEnvName, where)
			}
			var ok bool
			if value, ok = lookup(v.Env, key); !ok {
				value = fallback
			}
		case "localEnv", "env":
			return "", fmt.Errorf(textHostVariable, where, "${"+name+":…}")
		case "localWorkspaceFolder", "localWorkspaceFolderBasename":
			return "", fmt.Errorf(textHostVariable, where, "${"+name+"}")
		case "devcontainerId":
			return "", fmt.Errorf(textNoID, where)
		}
		b.WriteString(s[:i])
		b.WriteString(value)
		s = s[i+len(whole):]
	}
}

// lookup is the value env sets name to, the last when it sets it more than once.
func lookup(env []string, name string) (string, bool) {
	value, found := "", false
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			value, found = v, true
		}
	}
	return value, found
}
