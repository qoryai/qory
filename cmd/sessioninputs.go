package cmd

// setInputs sets the session's inputs.json, file with the bytes data, for the checks: the
// checks that later loads read them, and so does a checker that is loaded already, as
// session 1's is, since session 1 writes the file after its push checks load. The scope check's
// checker gets them the same way.
func (p *runPushChecks) setInputs(file string, data []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inputs, p.inputsData = file, data
	if p.checker != nil {
		p.checker.Inputs, p.checker.InputsData = file, data
	}
	if p.scope.checker != nil {
		p.scope.checker.Inputs, p.scope.checker.InputsData = file, data
	}
}
