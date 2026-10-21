package cmd

// StartThroughAgentEnv makes every walled run start its agent through the agent-env
// verb, as a dispatched run of Claude Code does, and returns what restores the default.
func StartThroughAgentEnv() func() {
	before := wrapsAgentEnv
	wrapsAgentEnv = func(_, walled bool) bool { return walled }
	return func() { wrapsAgentEnv = before }
}
