package cmd

// A run the gateway closes itself. The gateway closes one for more than one reason: it
// could not take an event the session sent, or the session sent it nothing for three
// heartbeats. What the session hands qory, a close from the gateway with run_closed,
// is the same for each, so qory says nothing of its own about why: the gateway's report
// line says it, and Forager's refusal is passed on as its Error says it. "The server
// closed the run" is the server's alone.

// closedByGatewayBefore is a run the gateway closed before it started: Forager's
// refusal, as its Error says it.
func closedByGatewayBefore(err error) error { return err }

// closedByGateway is the end of a run the gateway closed while it ran: the gateway's
// report line has said why, and the run fails, as a run the server closes does.
func closedByGateway() error { return reported(&exitError{code: 1}) }
