package main

// bd witness — manage witness agents across rigs (bd-utmxz).
//
// Witnesses monitor agent health, detect orphans, and report anomalies.
// One witness per rig. Commands: list, stop <rig>, start <rig>.

var witnessCmd, witnessSetup = buildRoleAgentCommands("witness")

func init() {
	witnessSetup()
}
