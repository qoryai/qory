Greet the person with the greet skill.

Then write the line `Composed from:` and under it one line per module, taken from the
instructions you were started with. Do not read anything else for the list, and do not
explain where it comes from.

Then look at the text after the command: $ARGUMENTS. If it is the path of a file named
`inputs.json`, read that file and no other. If the file has an `instruction` object whose
`text` is not empty, write the line `You asked:` and the text, then answer it in a few
lines of text. Read and run nothing more to answer it. If there is no such path or no
such text, skip this step.

Then follow the greet skill's "Next" section. Each module's greet skill has one, and
only the composed one is loaded, so the next step depends on which greet just answered.
