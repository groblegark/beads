package rpc

import (
	"crypto/sha256"
	"fmt"
)

// GenerateAgentName produces a deterministic, human-friendly name from a session key.
// Names are adjective-noun pairs (e.g., "swift-fox", "keen-raven") that are memorable
// and fun — suitable for agent identities that persist until the session expires.
//
// The name is deterministic: the same sessionKey always produces the same name.
// The daemon's session registry adds numeric suffixes (-1, -2) if collisions occur.
func GenerateAgentName(sessionKey string) string {
	h := sha256.Sum256([]byte(sessionKey))
	adjIdx := (int(h[0])<<8 | int(h[1])) % len(adjectives)
	nounIdx := (int(h[2])<<8 | int(h[3])) % len(nouns)
	return fmt.Sprintf("%s-%s", adjectives[adjIdx], nouns[nounIdx])
}

// adjectives for agent names — positive, distinctive, easy to remember.
var adjectives = []string{
	"bold", "brave", "bright", "calm", "clear",
	"cool", "crisp", "deft", "eager", "fair",
	"fast", "fleet", "glad", "grand", "keen",
	"kind", "lush", "neat", "nimble", "plush",
	"prime", "proud", "pure", "quick", "rare",
	"sharp", "sleek", "smart", "snug", "soft",
	"spry", "stark", "stout", "sure", "swift",
	"tall", "tame", "tidy", "true", "vast",
	"vivid", "warm", "wary", "wise", "witty",
	"zesty", "agile", "apt", "arch", "avid",
}

// nouns for agent names — animals, natural things, short and distinctive.
var nouns = []string{
	"ant", "ape", "bat", "bear", "bee",
	"bird", "boar", "buck", "bull", "cat",
	"cod", "colt", "crab", "crow", "deer",
	"dove", "duck", "elk", "emu", "ewe",
	"fawn", "fish", "frog", "goat", "gull",
	"hare", "hawk", "hog", "ibis", "jay",
	"kite", "lark", "lion", "lynx", "mare",
	"mink", "mole", "moth", "newt", "orc",
	"orca", "owl", "ox", "pike", "puma",
	"ram", "rat", "raven", "ray", "robin",
	"rook", "seal", "shrew", "slug", "snail",
	"snake", "sole", "stag", "swan", "teal",
	"tern", "toad", "trout", "vole", "wasp",
	"wren", "wolf", "yak", "fox", "finch",
}
