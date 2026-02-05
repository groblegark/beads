package daemon

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// redisTestURL returns the Redis URL for testing, or skips the test.
func redisTestURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("BD_TEST_REDIS_URL")
	if url == "" {
		t.Skip("BD_TEST_REDIS_URL not set, skipping Redis tests")
	}
	return url
}

// newTestRedisStore creates a RedisWispStore for testing with a unique namespace.
func newTestRedisStore(t *testing.T) WispStore {
	t.Helper()
	url := redisTestURL(t)
	ns := fmt.Sprintf("bd-test-%d", time.Now().UnixNano())
	store, err := NewRedisWispStore(url, WithNamespace(ns), WithTTL(5*time.Minute))
	if err != nil {
		t.Fatalf("NewRedisWispStore() error = %v", err)
	}
	t.Cleanup(func() {
		store.Close()
	})
	return store
}

func TestRedisWispStore_Create(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	t.Run("creates wisp successfully", func(t *testing.T) {
		issue := &types.Issue{
			ID:    "test-wisp-001",
			Title: "Test Wisp",
		}

		err := store.Create(ctx, issue)
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		got, err := store.Get(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if got == nil {
			t.Fatal("Get() returned nil")
		}
		if got.Title != issue.Title {
			t.Errorf("Title = %v, want %v", got.Title, issue.Title)
		}
		if !got.Ephemeral {
			t.Error("Ephemeral = false, want true")
		}
	})

	t.Run("rejects duplicate ID", func(t *testing.T) {
		issue := &types.Issue{
			ID:    "test-wisp-002",
			Title: "Original",
		}

		if err := store.Create(ctx, issue); err != nil {
			t.Fatalf("first Create() error = %v", err)
		}

		duplicate := &types.Issue{
			ID:    "test-wisp-002",
			Title: "Duplicate",
		}

		err := store.Create(ctx, duplicate)
		if err == nil {
			t.Error("Create() should have failed for duplicate ID")
		}
	})

	t.Run("rejects nil issue", func(t *testing.T) {
		err := store.Create(ctx, nil)
		if err == nil {
			t.Error("Create(nil) should have failed")
		}
	})

	t.Run("rejects empty ID", func(t *testing.T) {
		issue := &types.Issue{
			Title: "No ID",
		}

		err := store.Create(ctx, issue)
		if err == nil {
			t.Error("Create() with empty ID should have failed")
		}
	})

	t.Run("sets timestamps", func(t *testing.T) {
		before := time.Now()
		issue := &types.Issue{
			ID:    "test-wisp-003",
			Title: "Timestamped",
		}

		if err := store.Create(ctx, issue); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		after := time.Now()

		got, _ := store.Get(ctx, issue.ID)
		if got.CreatedAt.Before(before) || got.CreatedAt.After(after) {
			t.Errorf("CreatedAt = %v, want between %v and %v", got.CreatedAt, before, after)
		}
		if got.UpdatedAt.Before(before) || got.UpdatedAt.After(after) {
			t.Errorf("UpdatedAt = %v, want between %v and %v", got.UpdatedAt, before, after)
		}
	})

	t.Run("does not mutate caller issue", func(t *testing.T) {
		issue := &types.Issue{
			ID:     "test-wisp-004",
			Title:  "Caller Issue",
			Labels: []string{"original"},
		}

		if err := store.Create(ctx, issue); err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		// Modify original after create
		issue.Title = "Modified by caller"
		issue.Labels = append(issue.Labels, "added")

		got, _ := store.Get(ctx, issue.ID)
		if got.Title != "Caller Issue" {
			t.Errorf("Stored issue was mutated: Title = %v, want Caller Issue", got.Title)
		}
		if len(got.Labels) != 1 || got.Labels[0] != "original" {
			t.Errorf("Stored issue was mutated: Labels = %v, want [original]", got.Labels)
		}
	})
}

func TestRedisWispStore_Get(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	t.Run("returns nil for nonexistent", func(t *testing.T) {
		got, err := store.Get(ctx, "nonexistent")
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if got != nil {
			t.Errorf("Get() = %v, want nil", got)
		}
	})

	t.Run("round-trips JSON correctly", func(t *testing.T) {
		issue := &types.Issue{
			ID:          "test-roundtrip",
			Title:       "Round Trip",
			Description: "Testing JSON serialization",
			Status:      types.StatusOpen,
			Priority:    2,
			Labels:      []string{"a", "b"},
			Dependencies: []*types.Dependency{
				{
					IssueID:     "test-roundtrip",
					DependsOnID: "parent-1",
					Type:        types.DepParentChild,
				},
			},
		}

		if err := store.Create(ctx, issue); err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		got, err := store.Get(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}

		if got.Title != issue.Title {
			t.Errorf("Title = %v, want %v", got.Title, issue.Title)
		}
		if got.Description != issue.Description {
			t.Errorf("Description = %v, want %v", got.Description, issue.Description)
		}
		if got.Status != issue.Status {
			t.Errorf("Status = %v, want %v", got.Status, issue.Status)
		}
		if got.Priority != issue.Priority {
			t.Errorf("Priority = %v, want %v", got.Priority, issue.Priority)
		}
		if len(got.Labels) != 2 || got.Labels[0] != "a" || got.Labels[1] != "b" {
			t.Errorf("Labels = %v, want [a b]", got.Labels)
		}
		if len(got.Dependencies) != 1 || got.Dependencies[0].DependsOnID != "parent-1" {
			t.Errorf("Dependencies not preserved correctly: %v", got.Dependencies)
		}
	})
}

func TestRedisWispStore_List(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	issues := []*types.Issue{
		{ID: "wisp-1", Title: "First", Status: types.StatusOpen, Priority: 1, Labels: []string{"bug"}},
		{ID: "wisp-2", Title: "Second", Status: types.StatusOpen, Priority: 2, Labels: []string{"feature"}},
		{ID: "wisp-3", Title: "Third", Status: types.StatusClosed, Priority: 1, Labels: []string{"bug", "urgent"}},
	}
	for _, issue := range issues {
		if err := store.Create(ctx, issue); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}

	t.Run("returns all with empty filter", func(t *testing.T) {
		got, err := store.List(ctx, types.IssueFilter{})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(got) != 3 {
			t.Errorf("List() returned %d issues, want 3", len(got))
		}
	})

	t.Run("filters by status", func(t *testing.T) {
		status := types.StatusOpen
		got, err := store.List(ctx, types.IssueFilter{Status: &status})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(got) != 2 {
			t.Errorf("List() returned %d issues, want 2", len(got))
		}
	})

	t.Run("filters by priority", func(t *testing.T) {
		priority := 1
		got, err := store.List(ctx, types.IssueFilter{Priority: &priority})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(got) != 2 {
			t.Errorf("List() returned %d issues, want 2", len(got))
		}
	})

	t.Run("filters by labels AND", func(t *testing.T) {
		got, err := store.List(ctx, types.IssueFilter{Labels: []string{"bug", "urgent"}})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(got) != 1 {
			t.Errorf("List() returned %d issues, want 1", len(got))
		}
		if len(got) > 0 && got[0].ID != "wisp-3" {
			t.Errorf("List() returned %s, want wisp-3", got[0].ID)
		}
	})

	t.Run("respects limit", func(t *testing.T) {
		got, err := store.List(ctx, types.IssueFilter{Limit: 2})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(got) != 2 {
			t.Errorf("List() returned %d issues, want 2", len(got))
		}
	})

	t.Run("filters by title search", func(t *testing.T) {
		got, err := store.List(ctx, types.IssueFilter{TitleSearch: "first"})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(got) != 1 {
			t.Errorf("List() returned %d issues, want 1", len(got))
		}
	})

	t.Run("filters by ID prefix", func(t *testing.T) {
		got, err := store.List(ctx, types.IssueFilter{IDPrefix: "wisp-"})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(got) != 3 {
			t.Errorf("List() returned %d issues, want 3", len(got))
		}
	})
}

func TestRedisWispStore_Update(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	t.Run("updates existing wisp", func(t *testing.T) {
		issue := &types.Issue{
			ID:    "wisp-update-1",
			Title: "Original",
		}

		if err := store.Create(ctx, issue); err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		issue.Title = "Updated"
		if err := store.Update(ctx, issue); err != nil {
			t.Fatalf("Update() error = %v", err)
		}

		got, _ := store.Get(ctx, issue.ID)
		if got.Title != "Updated" {
			t.Errorf("Title = %v, want Updated", got.Title)
		}
	})

	t.Run("fails for nonexistent", func(t *testing.T) {
		issue := &types.Issue{
			ID:    "nonexistent",
			Title: "Doesn't exist",
		}

		err := store.Update(ctx, issue)
		if err == nil {
			t.Error("Update() should have failed for nonexistent wisp")
		}
	})

	t.Run("updates timestamp", func(t *testing.T) {
		issue := &types.Issue{
			ID:    "wisp-update-2",
			Title: "Original",
		}

		if err := store.Create(ctx, issue); err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		original, _ := store.Get(ctx, issue.ID)
		originalUpdated := original.UpdatedAt

		time.Sleep(10 * time.Millisecond)

		issue.Title = "Changed"
		if err := store.Update(ctx, issue); err != nil {
			t.Fatalf("Update() error = %v", err)
		}

		got, _ := store.Get(ctx, issue.ID)
		if !got.UpdatedAt.After(originalUpdated) {
			t.Error("UpdatedAt was not updated")
		}
	})

	t.Run("rejects nil issue", func(t *testing.T) {
		err := store.Update(ctx, nil)
		if err == nil {
			t.Error("Update(nil) should have failed")
		}
	})

	t.Run("rejects empty ID", func(t *testing.T) {
		err := store.Update(ctx, &types.Issue{Title: "No ID"})
		if err == nil {
			t.Error("Update() with empty ID should have failed")
		}
	})
}

func TestRedisWispStore_Delete(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	t.Run("deletes existing wisp", func(t *testing.T) {
		issue := &types.Issue{
			ID:    "wisp-delete-1",
			Title: "To be deleted",
		}

		if err := store.Create(ctx, issue); err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		if err := store.Delete(ctx, issue.ID); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}

		got, _ := store.Get(ctx, issue.ID)
		if got != nil {
			t.Error("Wisp still exists after Delete()")
		}
	})

	t.Run("fails for nonexistent", func(t *testing.T) {
		err := store.Delete(ctx, "nonexistent")
		if err == nil {
			t.Error("Delete() should have failed for nonexistent wisp")
		}
	})

	t.Run("decrements count", func(t *testing.T) {
		issue := &types.Issue{
			ID:    "wisp-delete-2",
			Title: "Count test",
		}

		store.Create(ctx, issue)
		before := store.Count()

		store.Delete(ctx, issue.ID)
		after := store.Count()

		if after != before-1 {
			t.Errorf("Count after delete = %d, want %d", after, before-1)
		}
	})
}

func TestRedisWispStore_Count(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	if store.Count() != 0 {
		t.Errorf("Count() = %d, want 0 for new store", store.Count())
	}

	for i := 0; i < 5; i++ {
		issue := &types.Issue{
			ID:    fmt.Sprintf("wisp-count-%d", i),
			Title: fmt.Sprintf("Wisp %d", i),
		}
		store.Create(ctx, issue)
	}

	if store.Count() != 5 {
		t.Errorf("Count() = %d, want 5", store.Count())
	}
}

func TestRedisWispStore_Clear(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		issue := &types.Issue{
			ID:    fmt.Sprintf("wisp-clear-%d", i),
			Title: fmt.Sprintf("Wisp %d", i),
		}
		store.Create(ctx, issue)
	}

	store.Clear()

	if store.Count() != 0 {
		t.Errorf("Count() = %d after Clear(), want 0", store.Count())
	}
}

func TestRedisWispStore_Close(t *testing.T) {
	url := redisTestURL(t)
	ns := fmt.Sprintf("bd-test-close-%d", time.Now().UnixNano())
	store, err := NewRedisWispStore(url, WithNamespace(ns), WithTTL(5*time.Minute))
	if err != nil {
		t.Fatalf("NewRedisWispStore() error = %v", err)
	}

	ctx := context.Background()

	issue := &types.Issue{
		ID:    "wisp-close-1",
		Title: "Test",
	}
	store.Create(ctx, issue)

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if err := store.Create(ctx, &types.Issue{ID: "new", Title: "New"}); err == nil {
		t.Error("Create() should fail after Close()")
	}

	if _, err := store.Get(ctx, "wisp-close-1"); err == nil {
		t.Error("Get() should fail after Close()")
	}

	if _, err := store.List(ctx, types.IssueFilter{}); err == nil {
		t.Error("List() should fail after Close()")
	}
}

func TestRedisWispStore_Concurrent(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	const numGoroutines = 10
	const numOps = 50

	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		issue := &types.Issue{
			ID:    fmt.Sprintf("wisp-concurrent-%d", i),
			Title: fmt.Sprintf("Wisp %d", i),
		}
		store.Create(ctx, issue)
	}

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			wispID := fmt.Sprintf("wisp-concurrent-%d", id)

			for j := 0; j < numOps; j++ {
				_, _ = store.Get(ctx, wispID)
				_, _ = store.List(ctx, types.IssueFilter{})

				issue := &types.Issue{
					ID:    wispID,
					Title: fmt.Sprintf("Updated %d-%d", id, j),
				}
				_ = store.Update(ctx, issue)
			}
		}(i)
	}

	wg.Wait()

	if store.Count() != numGoroutines {
		t.Errorf("Count() = %d after concurrent ops, want %d", store.Count(), numGoroutines)
	}
}

func TestNewRedisWispStore_InvalidURL(t *testing.T) {
	_, err := NewRedisWispStore("not-a-url")
	if err == nil {
		t.Error("NewRedisWispStore() should fail with invalid URL")
	}
}

func TestNewRedisWispStore_UnreachableServer(t *testing.T) {
	_, err := NewRedisWispStore("redis://localhost:59999/0")
	if err == nil {
		t.Error("NewRedisWispStore() should fail when server is unreachable")
	}
}
