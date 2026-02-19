package main

// bd refinery — manage refinery agents across rigs (bd-utmxz).
//
// Refineries process merge queues for each rig. One refinery per rig.
// Commands: list, stop <rig>, start <rig>.

var refineryCmd, refinerySetup = buildRoleAgentCommands("refinery")

func init() {
	refinerySetup()
}
