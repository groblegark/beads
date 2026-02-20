package rpc

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// handleRecordCommit records an issue-commit association (bd-xxabm).
func (s *Server) handleRecordCommit(req *Request) Response {
	var args RecordCommitArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid record_commit args: %v", err),
		}
	}

	if args.IssueID == "" {
		return Response{Success: false, Error: "issue_id is required"}
	}
	if args.CommitSHA == "" {
		return Response{Success: false, Error: "commit_sha is required"}
	}

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}
	ctx, cancel := s.reqCtx(req)
	defer cancel()
	actor := s.reqActor(req)

	// Parse committed_at if provided
	var committedAt *time.Time
	if args.CommittedAt != "" {
		t, err := time.Parse(time.RFC3339, args.CommittedAt)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid committed_at format: %v", err),
			}
		}
		committedAt = &t
	}

	db := store.UnderlyingDB()
	if db == nil {
		return Response{Success: false, Error: "database not available"}
	}

	// Verify issue exists
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issues WHERE id = ?)`, args.IssueID).Scan(&exists); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("failed to verify issue: %v", err)}
	}
	if !exists {
		return Response{Success: false, Error: fmt.Sprintf("issue %q not found", args.IssueID)}
	}

	// Upsert: INSERT IGNORE to handle duplicate commit recordings gracefully
	_, err := db.ExecContext(ctx, `
		INSERT IGNORE INTO issue_commits (issue_id, commit_sha, repo_url, branch, author, message, committed_at, recorded_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, args.IssueID, args.CommitSHA, args.RepoURL, args.Branch, args.Author, args.Message, committedAt, actor)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to record commit: %v", err),
		}
	}

	// Emit mutation event
	s.emitMutation("commit", args.IssueID, args.Message, actor)

	record := CommitRecord{
		IssueID:   args.IssueID,
		CommitSHA: args.CommitSHA,
		RepoURL:   args.RepoURL,
		Branch:    args.Branch,
		Author:    args.Author,
		Message:   args.Message,
		RecordedBy: actor,
	}
	if committedAt != nil {
		record.CommittedAt = committedAt.Format(time.RFC3339)
	}
	record.RecordedAt = time.Now().UTC().Format(time.RFC3339)

	data, _ := json.Marshal(record)
	return Response{Success: true, Data: data}
}

// handleCommitsForIssue returns all commits associated with an issue (bd-xxabm).
func (s *Server) handleCommitsForIssue(req *Request) Response {
	var args CommitsForIssueArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid commits_for_issue args: %v", err),
		}
	}

	if args.IssueID == "" {
		return Response{Success: false, Error: "issue_id is required"}
	}

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}
	ctx, cancel := s.reqCtx(req)
	defer cancel()

	db := store.UnderlyingDB()
	if db == nil {
		return Response{Success: false, Error: "database not available"}
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 100
	}

	rows, err := db.QueryContext(ctx, `
		SELECT issue_id, commit_sha, repo_url, branch, author, message, committed_at, recorded_at, recorded_by
		FROM issue_commits
		WHERE issue_id = ?
		ORDER BY recorded_at DESC
		LIMIT ?
	`, args.IssueID, limit)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("failed to query commits: %v", err)}
	}
	defer rows.Close()

	records := scanCommitRows(rows)
	data, _ := json.Marshal(records)
	return Response{Success: true, Data: data}
}

// handleIssuesForCommit returns all issues associated with a commit SHA (bd-xxabm).
func (s *Server) handleIssuesForCommit(req *Request) Response {
	var args IssuesForCommitArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid issues_for_commit args: %v", err),
		}
	}

	if args.CommitSHA == "" {
		return Response{Success: false, Error: "commit_sha is required"}
	}

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}
	ctx, cancel := s.reqCtx(req)
	defer cancel()

	db := store.UnderlyingDB()
	if db == nil {
		return Response{Success: false, Error: "database not available"}
	}

	rows, err := db.QueryContext(ctx, `
		SELECT issue_id, commit_sha, repo_url, branch, author, message, committed_at, recorded_at, recorded_by
		FROM issue_commits
		WHERE commit_sha = ?
		ORDER BY recorded_at DESC
	`, args.CommitSHA)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("failed to query issues: %v", err)}
	}
	defer rows.Close()

	records := scanCommitRows(rows)
	data, _ := json.Marshal(records)
	return Response{Success: true, Data: data}
}

// scanCommitRows scans issue_commits rows into CommitRecord slice.
func scanCommitRows(rows *sql.Rows) []CommitRecord {
	var records []CommitRecord
	for rows.Next() {
		var r CommitRecord
		var committedAt, recordedAt sql.NullTime
		var repoURL, branch, author, message, recordedBy sql.NullString
		if err := rows.Scan(&r.IssueID, &r.CommitSHA, &repoURL, &branch, &author, &message, &committedAt, &recordedAt, &recordedBy); err != nil {
			continue
		}
		if repoURL.Valid {
			r.RepoURL = repoURL.String
		}
		if branch.Valid {
			r.Branch = branch.String
		}
		if author.Valid {
			r.Author = author.String
		}
		if message.Valid {
			r.Message = message.String
		}
		if committedAt.Valid {
			r.CommittedAt = committedAt.Time.Format(time.RFC3339)
		}
		if recordedAt.Valid {
			r.RecordedAt = recordedAt.Time.Format(time.RFC3339)
		}
		if recordedBy.Valid {
			r.RecordedBy = recordedBy.String
		}
		records = append(records, r)
	}
	if records == nil {
		records = []CommitRecord{}
	}
	return records
}
