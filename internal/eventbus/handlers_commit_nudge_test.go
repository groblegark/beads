package eventbus

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommitNudgeHandler_Interface(t *testing.T) {
	h := &CommitNudgeHandler{}
	if h.ID() != "commit-nudge" {
		t.Fatalf("expected ID 'commit-nudge', got %q", h.ID())
	}
	if h.Priority() != 45 {
		t.Fatalf("expected priority 45, got %d", h.Priority())
	}
	handles := h.Handles()
	if len(handles) != 1 || handles[0] != EventStop {
		t.Fatalf("expected [Stop], got %v", handles)
	}
}

func TestCommitNudgeHandler_SkipsEmptyActor(t *testing.T) {
	h := &CommitNudgeHandler{}
	event := &Event{Type: EventStop, CWD: "/tmp"}
	result := &Result{}
	if err := h.Handle(context.Background(), event, result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) > 0 {
		t.Fatal("expected no warnings for empty actor")
	}
}

func TestCommitNudgeHandler_SkipsEmptyCWD(t *testing.T) {
	h := &CommitNudgeHandler{}
	event := &Event{Type: EventStop, Actor: "test-agent"}
	result := &Result{}
	if err := h.Handle(context.Background(), event, result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) > 0 {
		t.Fatal("expected no warnings for empty CWD")
	}
}

func TestCommitNudgeHandler_CleanRepo(t *testing.T) {
	dir := initTempGitRepo(t)
	h := &CommitNudgeHandler{cooldown: time.Millisecond}
	event := &Event{Type: EventStop, Actor: "test-agent", CWD: dir}
	result := &Result{}
	if err := h.Handle(context.Background(), event, result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) > 0 {
		t.Fatalf("expected no warnings for clean repo, got: %v", result.Warnings)
	}
}

func TestCommitNudgeHandler_DirtyRepo(t *testing.T) {
	dir := initTempGitRepo(t)
	// Create an uncommitted file.
	if err := os.WriteFile(filepath.Join(dir, "dirty.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	h := &CommitNudgeHandler{cooldown: time.Millisecond}
	event := &Event{Type: EventStop, Actor: "test-agent", CWD: dir}
	result := &Result{}
	if err := h.Handle(context.Background(), event, result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(result.Warnings))
	}
	if !strings.Contains(result.Warnings[0], "uncommitted changes") {
		t.Fatalf("warning should mention uncommitted changes: %s", result.Warnings[0])
	}
	if !strings.Contains(result.Warnings[0], "dirty.go") {
		t.Fatalf("warning should list dirty file: %s", result.Warnings[0])
	}
}

func TestCommitNudgeHandler_IgnoresNoisyFiles(t *testing.T) {
	dir := initTempGitRepo(t)
	// Create daemon/activity.json — should be filtered out.
	daemonDir := filepath.Join(dir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(daemonDir, "activity.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	// Create .beads/ file — should be filtered out.
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	h := &CommitNudgeHandler{cooldown: time.Millisecond}
	event := &Event{Type: EventStop, Actor: "test-agent", CWD: dir}
	result := &Result{}
	if err := h.Handle(context.Background(), event, result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) > 0 {
		t.Fatalf("expected no warnings when only noisy files are dirty, got: %v", result.Warnings)
	}
}

func TestCommitNudgeHandler_RateLimit(t *testing.T) {
	dir := initTempGitRepo(t)
	// Create dirty file.
	if err := os.WriteFile(filepath.Join(dir, "dirty.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	h := &CommitNudgeHandler{cooldown: 10 * time.Minute}
	event := &Event{Type: EventStop, Actor: "test-agent", CWD: dir}

	// First call: should nudge.
	result := &Result{}
	h.Handle(context.Background(), event, result)
	if len(result.Warnings) != 1 {
		t.Fatalf("expected 1 warning on first call, got %d", len(result.Warnings))
	}

	// Second call within cooldown: should NOT nudge.
	result = &Result{}
	h.Handle(context.Background(), event, result)
	if len(result.Warnings) != 0 {
		t.Fatalf("expected 0 warnings within cooldown, got %d", len(result.Warnings))
	}
}

func TestCommitNudgeHandler_NotGitRepo(t *testing.T) {
	dir := t.TempDir() // Not a git repo.
	h := &CommitNudgeHandler{cooldown: time.Millisecond}
	event := &Event{Type: EventStop, Actor: "test-agent", CWD: dir}
	result := &Result{}
	if err := h.Handle(context.Background(), event, result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) > 0 {
		t.Fatal("expected no warnings for non-git directory")
	}
}

func TestIsNoisyFile(t *testing.T) {
	tests := []struct {
		line  string
		noisy bool
	}{
		{" M daemon/activity.json", true},
		{"?? daemon/", true},
		{" M daemon/other.log", true},
		{"?? .beads/config.json", true},
		{"?? .beads/", true},
		{" M .beads/issues/test.md", true},
		{"?? deploy/", true},
		{" M deploy/slack/bot.yaml", true},
		{" M src/main.go", false},
		{"?? new_file.txt", false},
		{" M handlers.go", false},
		{"", false},
		{"XY", false},
	}
	for _, tt := range tests {
		got := isNoisyFile(tt.line)
		if got != tt.noisy {
			t.Errorf("isNoisyFile(%q) = %v, want %v", tt.line, got, tt.noisy)
		}
	}
}

// initTempGitRepo creates a temp directory with a git repo (initial commit).
func initTempGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	// Create initial commit so status works properly.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "initial")
	return dir
}
