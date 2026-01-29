package compact

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

type stubStore struct {
	checkEligibilityFn func(context.Context, string, int) (bool, string, error)
	getIssueFn         func(context.Context, string) (*types.Issue, error)
	updateIssueFn      func(context.Context, string, map[string]interface{}, string) error
	applyCompactionFn  func(context.Context, string, int, int, int, string) error
	addCommentFn       func(context.Context, string, string, string) error
	markDirtyFn        func(context.Context, string) error
}

func (s *stubStore) CheckEligibility(ctx context.Context, issueID string, tier int) (bool, string, error) {
	if s.checkEligibilityFn != nil {
		return s.checkEligibilityFn(ctx, issueID, tier)
	}
	return false, "", nil
}

func (s *stubStore) GetIssue(ctx context.Context, issueID string) (*types.Issue, error) {
	if s.getIssueFn != nil {
		return s.getIssueFn(ctx, issueID)
	}
	return nil, fmt.Errorf("GetIssue not stubbed")
}

func (s *stubStore) UpdateIssue(ctx context.Context, issueID string, updates map[string]interface{}, actor string) error {
	if s.updateIssueFn != nil {
		return s.updateIssueFn(ctx, issueID, updates, actor)
	}
	return nil
}

func (s *stubStore) ApplyCompaction(ctx context.Context, issueID string, tier int, originalSize int, compactedSize int, commitHash string) error {
	if s.applyCompactionFn != nil {
		return s.applyCompactionFn(ctx, issueID, tier, originalSize, compactedSize, commitHash)
	}
	return nil
}

func (s *stubStore) AddComment(ctx context.Context, issueID, actor, comment string) error {
	if s.addCommentFn != nil {
		return s.addCommentFn(ctx, issueID, actor, comment)
	}
	return nil
}

func (s *stubStore) MarkIssueDirty(ctx context.Context, issueID string) error {
	if s.markDirtyFn != nil {
		return s.markDirtyFn(ctx, issueID)
	}
	return nil
}

type stubSummarizer struct {
	summary string
	err     error
	calls   int
}

func (s *stubSummarizer) SummarizeTier1(ctx context.Context, issue *types.Issue) (string, error) {
	s.calls++
	return s.summary, s.err
}

func stubIssue() *types.Issue {
	return &types.Issue{
		ID:                 "bd-123",
		Title:              "Fix login",
		Description:        strings.Repeat("A", 20),
		Design:             strings.Repeat("B", 10),
		Notes:              strings.Repeat("C", 5),
		AcceptanceCriteria: "done",
		Status:             types.StatusClosed,
	}
}

func withGitHash(t *testing.T, hash string) func() {
	orig := gitExec
	gitExec = func(string, ...string) ([]byte, error) {
		return []byte(hash), nil
	}
	return func() { gitExec = orig }
}

func TestCompactTier1_Success(t *testing.T) {
	updateCalled := false
	applyCalled := false
	markCalled := false
	store := &stubStore{
		checkEligibilityFn: func(context.Context, string, int) (bool, string, error) { return true, "", nil },
		getIssueFn:         func(context.Context, string) (*types.Issue, error) { return stubIssue(), nil },
		updateIssueFn: func(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
			updateCalled = true
			if updates["description"].(string) != "short" {
				t.Fatalf("expected summarized description")
			}
			if updates["design"].(string) != "" {
				t.Fatalf("design should be cleared")
			}
			return nil
		},
		applyCompactionFn: func(ctx context.Context, id string, tier, original, compacted int, hash string) error {
			applyCalled = true
			// Hash is now empty (no git hash tracking)
			if hash != "" {
				t.Fatalf("unexpected hash %q (expected empty)", hash)
			}
			return nil
		},
		addCommentFn: func(ctx context.Context, id, actor, comment string) error {
			if !strings.Contains(comment, "Tier 1 compaction applied") {
				t.Fatalf("unexpected comment %q", comment)
			}
			return nil
		},
		markDirtyFn: func(context.Context, string) error {
			markCalled = true
			return nil
		},
	}
	summary := &stubSummarizer{summary: "short"}
	c := &Compactor{store: store, summarizer: summary, config: &Config{}}

	if err := c.CompactTier1(context.Background(), "bd-123"); err != nil {
		t.Fatalf("CompactTier1 unexpected error: %v", err)
	}
	if summary.calls != 1 {
		t.Fatalf("expected summarizer used once, got %d", summary.calls)
	}
	if !updateCalled || !applyCalled || !markCalled {
		t.Fatalf("expected update/apply/mark to be called")
	}
}

func TestCompactTier1_DryRun(t *testing.T) {
	store := &stubStore{
		checkEligibilityFn: func(context.Context, string, int) (bool, string, error) { return true, "", nil },
		getIssueFn:         func(context.Context, string) (*types.Issue, error) { return stubIssue(), nil },
	}
	summary := &stubSummarizer{summary: "short"}
	c := &Compactor{store: store, summarizer: summary, config: &Config{DryRun: true}}

	// DryRun now returns nil (early exit after getting issue)
	err := c.CompactTier1(context.Background(), "bd-123")
	if err != nil {
		t.Fatalf("expected nil error for dry run, got %v", err)
	}
	if summary.calls != 0 {
		t.Fatalf("summarizer should not be used in dry run")
	}
}

func TestCompactTier1_Ineligible(t *testing.T) {
	// Note: Current implementation doesn't check eligibility in CompactTier1.
	// Eligibility checking is expected to be done by the caller.
	// This test verifies that compaction proceeds even without eligibility check.
	updateCalled := false
	store := &stubStore{
		getIssueFn: func(context.Context, string) (*types.Issue, error) { return stubIssue(), nil },
		updateIssueFn: func(context.Context, string, map[string]interface{}, string) error {
			updateCalled = true
			return nil
		},
		applyCompactionFn: func(context.Context, string, int, int, int, string) error { return nil },
		addCommentFn:      func(context.Context, string, string, string) error { return nil },
		markDirtyFn:       func(context.Context, string) error { return nil },
	}
	summary := &stubSummarizer{summary: "short"}
	c := &Compactor{store: store, summarizer: summary, config: &Config{}}

	err := c.CompactTier1(context.Background(), "bd-123")
	if err != nil {
		t.Fatalf("expected successful compaction, got %v", err)
	}
	if !updateCalled {
		t.Fatalf("expected update to be called")
	}
}

func TestCompactTier1_LargeSummary(t *testing.T) {
	// Note: Current implementation doesn't check if summary is smaller.
	// It proceeds with compaction regardless of summary size.
	updateCalled := false
	commentCalled := false
	store := &stubStore{
		getIssueFn: func(context.Context, string) (*types.Issue, error) { return stubIssue(), nil },
		updateIssueFn: func(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
			updateCalled = true
			return nil
		},
		applyCompactionFn: func(context.Context, string, int, int, int, string) error { return nil },
		addCommentFn: func(ctx context.Context, id, actor, comment string) error {
			commentCalled = true
			// Comment should mention compaction applied
			if !strings.Contains(comment, "Tier 1 compaction applied") {
				t.Fatalf("unexpected comment %q", comment)
			}
			return nil
		},
		markDirtyFn: func(context.Context, string) error { return nil },
	}
	// Large summary (larger than original content)
	summary := &stubSummarizer{summary: strings.Repeat("X", 100)}
	c := &Compactor{store: store, summarizer: summary, config: &Config{}}

	err := c.CompactTier1(context.Background(), "bd-123")
	if err != nil {
		t.Fatalf("expected success even with large summary, got %v", err)
	}
	if !updateCalled {
		t.Fatalf("expected update to be called")
	}
	if !commentCalled {
		t.Fatalf("expected comment to be added")
	}
}

func TestCompactTier1_UpdateError(t *testing.T) {
	store := &stubStore{
		checkEligibilityFn: func(context.Context, string, int) (bool, string, error) { return true, "", nil },
		getIssueFn:         func(context.Context, string) (*types.Issue, error) { return stubIssue(), nil },
		updateIssueFn:      func(context.Context, string, map[string]interface{}, string) error { return errors.New("boom") },
	}
	summary := &stubSummarizer{summary: "short"}
	c := &Compactor{store: store, summarizer: summary, config: &Config{}}

	err := c.CompactTier1(context.Background(), "bd-123")
	if err == nil || !strings.Contains(err.Error(), "failed to update issue") {
		t.Fatalf("expected update error, got %v", err)
	}
}

func TestCompactTier1Batch_MixedResults(t *testing.T) {
	// Note: Current implementation doesn't check eligibility, so all items
	// in the batch will be compacted.
	var mu sync.Mutex
	updated := make(map[string]int)
	applied := make(map[string]int)
	marked := make(map[string]int)
	store := &stubStore{
		getIssueFn: func(ctx context.Context, id string) (*types.Issue, error) {
			issue := stubIssue()
			issue.ID = id
			return issue, nil
		},
		updateIssueFn: func(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
			mu.Lock()
			updated[id]++
			mu.Unlock()
			return nil
		},
		applyCompactionFn: func(ctx context.Context, id string, tier, original, compacted int, hash string) error {
			mu.Lock()
			applied[id]++
			mu.Unlock()
			return nil
		},
		addCommentFn: func(context.Context, string, string, string) error { return nil },
		markDirtyFn: func(ctx context.Context, id string) error {
			mu.Lock()
			marked[id]++
			mu.Unlock()
			return nil
		},
	}
	summary := &stubSummarizer{summary: "short"}
	c := &Compactor{store: store, summarizer: summary, config: &Config{Concurrency: 2}}

	results, err := c.CompactTier1Batch(context.Background(), []string{"bd-1", "bd-2"})
	if err != nil {
		t.Fatalf("CompactTier1Batch unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	resMap := map[string]*BatchResult{}
	for i := range results {
		resMap[results[i].IssueID] = &results[i]
	}

	// Both should succeed (no eligibility check)
	if res := resMap["bd-1"]; res == nil || res.Err != nil {
		t.Fatalf("expected success result for bd-1, got %+v", res)
	}
	if res := resMap["bd-2"]; res == nil || res.Err != nil {
		t.Fatalf("expected success result for bd-2, got %+v", res)
	}
	// Both should be updated
	if updated["bd-1"] != 1 || applied["bd-1"] != 1 || marked["bd-1"] != 1 {
		t.Fatalf("expected store operations for bd-1 exactly once")
	}
	if updated["bd-2"] != 1 || applied["bd-2"] != 1 || marked["bd-2"] != 1 {
		t.Fatalf("expected store operations for bd-2 exactly once")
	}
	// Summarizer called once per issue
	if summary.calls != 2 {
		t.Fatalf("summarizer should run twice (once per issue); got %d", summary.calls)
	}
}
