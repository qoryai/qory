package cmd

// The cases of a run on Forager's local gateway that qory has no words of its own for
// yet. Each passes on what Forager says, unchanged, and is kept here, apart, so qory's
// own text for it replaces one function.

// codeRunIDUsed is the gateway's refusal of a run request whose run id a run of it
// already used.
const codeRunIDUsed = "run_id_used"

// runIDUsed is a run refused because the gateway has a run with its --run-id already:
// Forager's refusal, as its Error says it.
func runIDUsed(err error) error { return err }

// closedByGatewayBefore is a run the gateway closed before it started: Forager's
// refusal, as its Error says it. "The server closed the run" is the server's alone.
func closedByGatewayBefore(err error) error { return err }

// closedByGateway is the end of a run the gateway closed while it ran, such as one
// whose session it no longer heard from: the gateway's report line has said why, and
// the run fails, as a run the server closes does.
func closedByGateway() error { return reported(&exitError{code: 1}) }
