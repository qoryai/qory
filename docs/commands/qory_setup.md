## qory setup

Set up the repository, the machine's configuration, or your shell

### Synopsis

Set up the repository, the machine's configuration, or your shell.

setup repo writes the repository's qory.yaml and its own module; setup example writes the
hello example, a stack and two modules; setup machine writes the machine's qory.yaml; setup shell adds qory's completions and a function that follows a
worktree add and remove to your shell's rc file; setup completion prints the completion
script that function loads.

### Options

```
  -h, --help   help for setup
```

### SEE ALSO

* [qory](qory.md)	 - Compose the harness a runtime loads from modules
* [qory setup completion](qory_setup_completion.md)	 - Print the completion script; setup shell makes your shell source it
* [qory setup example](qory_setup_example.md)	 - Write the hello example into the current directory: a stack and two modules
* [qory setup machine](qory_setup_machine.md)	 - Write your qory.yaml in ~/.config/qory: how qory runs on this machine
* [qory setup repo](qory_setup_repo.md)	 - Write the repository's qory.yaml: its stack, its module, its worktree settings
* [qory setup shell](qory_setup_shell.md)	 - Add completions and a function that follows worktree add and remove to your shell

