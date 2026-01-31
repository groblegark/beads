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
		getIssueFn: func(context.Context, string) (*types.Issue, error) { return stubIssue(), nil },
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
			// Implementation passes empty string for hash
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
		getIssueFn: func(context.Context, string) (*types.Issue, error) { return stubIssue(), nil },
	}
	summary := &stubSummarizer{summary: "short"}
	c := &Compactor{store: store, summarizer: summary, config: &Config{DryRun: true}}

	err := c.CompactTier1(context.Background(), "bd-123")
	// DryRun returns nil (no error) after fetching the issue
	if err != nil {
		t.Fatalf("expected nil error in dry-run mode, got %v", err)
	}
	if summary.calls != 0 {
		t.Fatalf("summarizer should not be used in dry run")
	}
}

func TestCompactTier1_GetIssueError(t *testing.T) {
	store := &stubStore{
		getIssueFn: func(context.Context, string) (*types.Issue, error) {
			return nil, errors.New("issue not found")
		},
	}
	c := &Compactor{store: store, config: &Config{}}

	err := c.CompactTier1(context.Background(), "bd-123")
	if err == nil || !strings.Contains(err.Error(), "failed to fetch issue") {
		t.Fatalf("expected fetch error, got %v", err)
	}
}

func TestCompactTier1_LargeSummaryStillApplied(t *testing.T) {
	// Implementation applies compaction regardless of whether summary is smaller
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
			// Comment shows negative reduction when summary is larger
			if !strings.Contains(comment, "Tier 1 compaction applied") {
				t.Fatalf("unexpected comment %q", comment)
			}
			return nil
		},
		markDirtyFn: func(context.Context, string) error { return nil },
	}
	summary := &stubSummarizer{summary: strings.Repeat("X", 40)}
	c := &Compactor{store: store, summarizer: summary, config: &Config{}}

	err := c.CompactTier1(context.Background(), "bd-123")
	// Implementation applies compaction even if summary is larger
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
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
	var mu sync.Mutex
	updated := make(map[string]int)
	applied := make(map[string]int)
	marked := make(map[string]int)
	store := &stubStore{
		getIssueFn: func(ctx context.Context, id string) (*types.Issue, error) {
			switch id {
			case "bd-1":
				issue := stubIssue()
				issue.ID = id
				return issue, nil
			case "bd-2":
				// Simulate an issue that fails to fetch
				return nil, errors.New("issue not found")
			default:
				return nil, fmt.Errorf("unexpected id %s", id)
			}
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

	if res := resMap["bd-1"]; res == nil || res.Err != nil || res.CompactedSize == 0 {
		t.Fatalf("expected success result for bd-1, got %+v", res)
	}
	if res := resMap["bd-2"]; res == nil || res.Err == nil || !strings.Contains(res.Err.Error(), "issue not found") {
		t.Fatalf("expected fetch error for bd-2, got %+v", res)
	}
	if updated["bd-1"] != 1 || applied["bd-1"] != 1 || marked["bd-1"] != 1 {
		t.Fatalf("expected store operations for bd-1 exactly once")
	}
	if updated["bd-2"] != 0 || applied["bd-2"] != 0 {
		t.Fatalf("bd-2 should not be processed")
	}
	if summary.calls != 1 {
		t.Fatalf("summarizer should run once; got %d", summary.calls)
	}
}
