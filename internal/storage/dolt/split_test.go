package dolt

import (
	"testing"
)

func TestSplitStatements_ApostropheInComment(t *testing.T) {
	// Regression test: apostrophes in SQL comments (e.g., "agent A's drain")
	// must not enter string mode, which would swallow the semicolon terminator
	// and merge adjacent CREATE TABLE statements.
	sql := `CREATE TABLE IF NOT EXISTS inbox (
    id VARCHAR(255) PRIMARY KEY
);

-- agent A's drain should not break splitting
CREATE TABLE IF NOT EXISTS inbox_broadcast_ack (
    inbox_id VARCHAR(255) NOT NULL,
    PRIMARY KEY (inbox_id)
);

CREATE TABLE IF NOT EXISTS blocked_issues_cache (
    issue_id VARCHAR(255) PRIMARY KEY
);`

	stmts := splitStatements(sql)
	if len(stmts) != 3 {
		t.Fatalf("expected 3 statements, got %d", len(stmts))
	}

	for i, s := range stmts {
		if len(s) > 200 {
			t.Errorf("statement %d is suspiciously long (%d chars), possible merge:\n%.100s...", i, len(s), s)
		}
	}
}

func TestSplitStatements_QuotedSemicolon(t *testing.T) {
	// Semicolons inside quoted strings must NOT split statements.
	sql := `INSERT INTO config (` + "`key`" + `, value) VALUES ('greeting', 'hello; world');
INSERT INTO config (` + "`key`" + `, value) VALUES ('other', 'val');`

	stmts := splitStatements(sql)
	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(stmts))
	}
}

func TestSplitStatements_MultiLineCommentWithQuotes(t *testing.T) {
	// Multiple comments with quotes should all be handled correctly.
	sql := `-- It's important to test this
-- Don't forget about agent_name='all'
CREATE TABLE t1 (id INT);
-- Another comment: user's data
CREATE TABLE t2 (id INT);`

	stmts := splitStatements(sql)
	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(stmts))
	}
}
