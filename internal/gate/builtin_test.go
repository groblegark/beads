package gate

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDecisionGate(t *testing.T) {
	g := DecisionGate()
	if g.ID != "decision" {
		t.Errorf("expected ID 'decision', got %q", g.ID)
	}
	if g.Hook != HookStop {
		t.Errorf("expected HookStop, got %q", g.Hook)
	}
	if g.Mode != GateModeStrict {
		t.Errorf("expected strict mode, got %q", g.Mode)
	}
	if g.AutoCheck == nil {
		t.Error("decision gate should have auto-check for decision-waiting marker")
	}
}

func TestCommitPushGate(t *testing.T) {
	g := CommitPushGate()
	if g.ID != "commit-push" {
		t.Errorf("expected ID 'commit-push', got %q", g.ID)
	}
	if g.Hook != HookStop {
		t.Errorf("expected HookStop, got %q", g.Hook)
	}
	if g.Mode != GateModeSoft {
		t.Errorf("expected soft mode, got %q", g.Mode)
	}
	if g.AutoCheck == nil {
		t.Error("commit-push gate should have auto-check")
	}
}

func TestBeadUpdateGate(t *testing.T) {
	g := BeadUpdateGate()
	if g.ID != "bead-update" {
		t.Errorf("expected ID 'bead-update', got %q", g.ID)
	}
	if g.Mode != GateModeSoft {
		t.Errorf("expected soft mode, got %q", g.Mode)
	}
	if g.AutoCheck == nil {
		t.Error("bead-update gate should have auto-check")
	}
}

func TestRegisterBuiltinGates(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltinGates(reg)

	if reg.Count() != 3 {
		t.Errorf("expected 3 built-in gates, got %d", reg.Count())
	}

	// All should be Stop gates
	stopGates := reg.GatesForHook(HookStop)
	if len(stopGates) != 3 {
		t.Errorf("expected 3 Stop gates, got %d", len(stopGates))
	}

	// Verify each exists
	for _, id := range []string{"decision", "commit-push", "bead-update"} {
		if reg.Get(id) == nil {
			t.Errorf("expected gate %q to be registered", id)
		}
	}
}

func TestCheckGitClean_CleanRepo(t *testing.T) {
	// Create a temporary git repo with no uncommitted changes
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")

	// Create and commit a file
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "file.txt")
	runGit(t, dir, "commit", "-m", "init")

	ctx := GateContext{WorkDir: dir}
	if !checkGitClean(ctx) {
		t.Error("clean repo should satisfy commit-push gate")
	}
}

func TestCheckGitClean_DirtyRepo(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")

	// Create and commit a file
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "file.txt")
	runGit(t, dir, "commit", "-m", "init")

	// Modify the file (dirty working tree)
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := GateContext{WorkDir: dir}
	if checkGitClean(ctx) {
		t.Error("dirty repo should NOT satisfy commit-push gate")
	}
}

func TestCheckGitClean_NotAGitRepo(t *testing.T) {
	dir := t.TempDir()

	ctx := GateContext{WorkDir: dir}
	if !checkGitClean(ctx) {
		t.Error("non-git dir should satisfy gate (fail-open)")
	}
}

func TestCheckNoBeadHooked_NoHook(t *testing.T) {
	t.Setenv("GT_HOOK_BEAD", "")

	ctx := GateContext{}
	if !checkNoBeadHooked(ctx) {
		t.Error("no hooked bead should auto-satisfy")
	}
}

func TestCheckNoBeadHooked_WithHook(t *testing.T) {
	t.Setenv("GT_HOOK_BEAD", "gt-abc123")

	ctx := GateContext{}
	if checkNoBeadHooked(ctx) {
		t.Error("hooked bead should NOT auto-satisfy")
	}
}

func TestCheckNoBeadHooked_ContextOverride(t *testing.T) {
	t.Setenv("GT_HOOK_BEAD", "")

	ctx := GateContext{HookBead: "gt-xyz"}
	if checkNoBeadHooked(ctx) {
		t.Error("HookBead in context should prevent auto-satisfy")
	}
}

func TestDecisionGate_AutoSatisfiedWhenWaiting(t *testing.T) {
	workDir := t.TempDir()
	sessionID := "decision-waiting-test"

	g := DecisionGate()
	ctx := GateContext{WorkDir: workDir, SessionID: sessionID}

	// Without decision-waiting marker, auto-check should return false
	if g.AutoCheck(ctx) {
		t.Error("decision gate should not auto-satisfy without decision-waiting marker")
	}

	// Write the decision-waiting marker (simulating bd decision create blocking)
	if err := MarkGate(workDir, sessionID, "decision-waiting"); err != nil {
		t.Fatal(err)
	}

	// Now auto-check should return true
	if !g.AutoCheck(ctx) {
		t.Error("decision gate should auto-satisfy when decision-waiting marker exists")
	}

	// Clean up the marker (simulating decision response received)
	ClearGate(workDir, sessionID, "decision-waiting")

	// Should go back to unsatisfied
	if g.AutoCheck(ctx) {
		t.Error("decision gate should not auto-satisfy after decision-waiting marker cleared")
	}
}

func TestDecisionGate_IntegrationWithEvaluateHook(t *testing.T) {
	workDir := t.TempDir()
	sessionID := "decision-eval-test"

	// Set up clean git repo and no hooked bead for other gates
	runGit(t, workDir, "init")
	runGit(t, workDir, "config", "user.email", "test@test.com")
	runGit(t, workDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(workDir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workDir, "add", "f.txt")
	runGit(t, workDir, "commit", "-m", "init")
	t.Setenv("GT_HOOK_BEAD", "")

	reg := NewRegistry()
	RegisterBuiltinGates(reg)

	// Without decision-waiting marker, should block
	resp, err := EvaluateHook(workDir, sessionID, HookStop, reg)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Decision != "block" {
		t.Errorf("expected block without decision-waiting marker, got %q", resp.Decision)
	}

	// Write decision-waiting marker — should now allow (agent is blocked on decision)
	if err := MarkGate(workDir, sessionID, "decision-waiting"); err != nil {
		t.Fatal(err)
	}

	resp, err = EvaluateHook(workDir, sessionID, HookStop, reg)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Decision != "allow" {
		t.Errorf("expected allow with decision-waiting marker, got %q (reason: %s)", resp.Decision, resp.Reason)
	}
}

func TestBuiltinGatesIntegration(t *testing.T) {
	workDir := t.TempDir()
	sessionID := "builtin-integration"

	// Set up a clean git repo
	runGit(t, workDir, "init")
	runGit(t, workDir, "config", "user.email", "test@test.com")
	runGit(t, workDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(workDir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workDir, "add", "f.txt")
	runGit(t, workDir, "commit", "-m", "init")

	// No hooked bead
	t.Setenv("GT_HOOK_BEAD", "")

	reg := NewRegistry()
	RegisterBuiltinGates(reg)

	// Evaluate Stop gates:
	// - decision: not marked → strict → should block
	// - commit-push: auto-check (clean repo) → satisfied
	// - bead-update: auto-check (no bead) → satisfied
	resp, err := EvaluateHook(workDir, sessionID, HookStop, reg)
	if err != nil {
		t.Fatal(err)
	}

	if resp.Decision != "block" {
		t.Errorf("expected block (decision gate unsatisfied), got %q", resp.Decision)
	}

	// Mark decision gate
	if err := MarkGate(workDir, sessionID, "decision"); err != nil {
		t.Fatal(err)
	}

	// Now should allow
	resp, err = EvaluateHook(workDir, sessionID, HookStop, reg)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Decision != "allow" {
		t.Errorf("expected allow after marking decision, got %q (reason: %s)", resp.Decision, resp.Reason)
	}
}

// runGit runs a git command in the given directory.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}
