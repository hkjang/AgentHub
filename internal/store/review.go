package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// A code review's result.
//
// The other runners end with an answer and the evaluator judges it. A review
// ends with a list of observations that each point at a file and a line, and
// carry a severity somebody has to act on. Keeping that as prose would make the
// console show a paragraph where it could show the eleven things to look at,
// sorted by how much they matter.

// ReviewSeverities and ReviewCategories are the review engine's own vocabularies,
// stored verbatim rather than mapped onto something of the platform's invention:
// a finding has to mean the same thing on both sides of the boundary. They are
// exported because the API validates against them and the database checks them,
// and three copies of a list is three chances to drift.
var (
	ReviewSeverities = []string{"critical", "high", "medium", "low"}
	ReviewCategories = []string{"bug", "security", "performance", "maintainability", "test", "style", "documentation", "other"}
)

// ReviewFinding is one located observation.
type ReviewFinding struct {
	ID           string     `json:"id"`
	RunID        string     `json:"runId"`
	TaskID       string     `json:"taskId,omitempty"`
	AgentID      string     `json:"agentId"`
	OwnerID      string     `json:"ownerId"`
	FilePath     string     `json:"filePath"`
	StartLine    int        `json:"startLine"`
	EndLine      int        `json:"endLine"`
	Severity     string     `json:"severity"`
	Category     string     `json:"category"`
	Message      string     `json:"message"`
	ExistingCode string     `json:"existingCode,omitempty"`
	Suggestion   string     `json:"suggestion,omitempty"`
	Status       string     `json:"status"`
	DecidedBy    *string    `json:"decidedBy,omitempty"`
	DecidedAt    *time.Time `json:"decidedAt,omitempty"`
	Source       string     `json:"source"`
	CreatedAt    time.Time  `json:"createdAt"`
}

// ReviewRun is what the review covered — the claim its findings rest on.
//
// A run with no findings and a run that failed to read anything both produce an
// empty list, and they mean opposite things. This is how the console tells them
// apart without asking anybody to read a log.
type ReviewRun struct {
	RunID         string    `json:"runId"`
	Mode          string    `json:"mode"`
	BaseRef       string    `json:"baseRef"`
	HeadRef       string    `json:"headRef"`
	ResolvedBase  string    `json:"resolvedBase"`
	ResolvedHead  string    `json:"resolvedHead"`
	FilesSelected int       `json:"filesSelected"`
	FilesReviewed int       `json:"filesReviewed"`
	FilesFailed   int       `json:"filesFailed"`
	SessionID     string    `json:"sessionId"`
	EngineVersion string    `json:"engineVersion"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
}

const reviewFindingColumns = `id,run_id,COALESCE(task_id,''),agent_id,owner_id,file_path,start_line,end_line,severity,category,message,existing_code,suggestion,status,decided_by,decided_at,source,created_at`

func scanReviewFinding(row pgx.Row) (ReviewFinding, error) {
	var item ReviewFinding
	err := row.Scan(&item.ID, &item.RunID, &item.TaskID, &item.AgentID, &item.OwnerID, &item.FilePath,
		&item.StartLine, &item.EndLine, &item.Severity, &item.Category, &item.Message,
		&item.ExistingCode, &item.Suggestion, &item.Status, &item.DecidedBy, &item.DecidedAt,
		&item.Source, &item.CreatedAt)
	return item, err
}

// SaveReviewRun records what one review covered.
func (s *Store) SaveReviewRun(ctx context.Context, item ReviewRun) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO review_runs
		(run_id,mode,base_ref,head_ref,resolved_base,resolved_head,files_selected,files_reviewed,files_failed,session_id,engine_version,status)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT(run_id) DO UPDATE SET mode=excluded.mode,base_ref=excluded.base_ref,head_ref=excluded.head_ref,
			resolved_base=excluded.resolved_base,resolved_head=excluded.resolved_head,files_selected=excluded.files_selected,
			files_reviewed=excluded.files_reviewed,files_failed=excluded.files_failed,session_id=excluded.session_id,
			engine_version=excluded.engine_version,status=excluded.status`,
		item.RunID, item.Mode, item.BaseRef, item.HeadRef, item.ResolvedBase, item.ResolvedHead,
		item.FilesSelected, item.FilesReviewed, item.FilesFailed, item.SessionID, item.EngineVersion, item.Status)
	return err
}

// SaveReviewFindings writes one review's findings.
//
// The whole set is written or none of it is: a console showing four of eleven
// findings with nothing to say the rest were lost is worse than one showing that
// the review failed.
func (s *Store) SaveReviewFindings(ctx context.Context, items []ReviewFinding) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, item := range items {
		if item.ID == "" {
			item.ID = uuid.NewString()
		}
		if item.Source == "" {
			item.Source = "open-code-review"
		}
		var taskID *string
		if item.TaskID != "" {
			taskID = &item.TaskID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO review_findings
			(id,run_id,task_id,agent_id,owner_id,file_path,start_line,end_line,severity,category,message,existing_code,suggestion,source)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			item.ID, item.RunID, taskID, item.AgentID, item.OwnerID, item.FilePath,
			item.StartLine, item.EndLine, item.Severity, item.Category, item.Message,
			item.ExistingCode, item.Suggestion, item.Source); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ReviewFindings lists one review's findings, worst first.
//
// Severity order is spelled out rather than sorted alphabetically, which would
// put critical after high and low before medium — an ordering that reads as a
// bug in the console every time somebody notices it.
func (s *Store) ReviewFindings(ctx context.Context, runID, ownerID string, admin bool) ([]ReviewFinding, error) {
	query := `SELECT ` + reviewFindingColumns + ` FROM review_findings WHERE run_id=$1`
	args := []any{runID}
	if !admin {
		query += ` AND owner_id=$2`
		args = append(args, ownerID)
	}
	query += ` ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 ELSE 3 END, file_path, start_line`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ReviewFinding{}
	for rows.Next() {
		item, err := scanReviewFinding(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ReviewRunByID reads what a review covered.
func (s *Store) ReviewRunByID(ctx context.Context, runID string) (ReviewRun, error) {
	var item ReviewRun
	err := s.pool.QueryRow(ctx, `SELECT run_id,mode,base_ref,head_ref,resolved_base,resolved_head,
		files_selected,files_reviewed,files_failed,session_id,engine_version,status,created_at
		FROM review_runs WHERE run_id=$1`, runID).
		Scan(&item.RunID, &item.Mode, &item.BaseRef, &item.HeadRef, &item.ResolvedBase, &item.ResolvedHead,
			&item.FilesSelected, &item.FilesReviewed, &item.FilesFailed, &item.SessionID, &item.EngineVersion,
			&item.Status, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewRun{}, ErrNotFound
	}
	return item, err
}

// DecideReviewFinding records what a person concluded about one finding.
func (s *Store) DecideReviewFinding(ctx context.Context, id, ownerID, decision string, admin bool) (ReviewFinding, error) {
	if !contains(ReviewFindingDecisions, decision) {
		return ReviewFinding{}, errors.New("알 수 없는 처리 상태입니다: " + decision)
	}
	query := `UPDATE review_findings SET status=$1, decided_by=$2, decided_at=now() WHERE id=$3`
	args := []any{decision, ownerID, id}
	if !admin {
		query += ` AND owner_id=$4`
		args = append(args, ownerID)
	}
	query += ` RETURNING ` + reviewFindingColumns
	item, err := scanReviewFinding(s.pool.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewFinding{}, ErrNotFound
	}
	return item, err
}

// ReviewFindingDecisions are what a person may conclude. 'open' is not among
// them: it is where a finding starts, and letting somebody set it back would
// lose who had already looked.
var ReviewFindingDecisions = []string{"accepted", "dismissed", "fixed"}

// ReviewSeverityAtLeast reports whether severity is at least as serious as
// floor. It exists so a quality gate is one comparison rather than a list of
// string equalities repeated wherever a gate is evaluated.
func ReviewSeverityAtLeast(severity, floor string) bool {
	rank := func(value string) int {
		for index, name := range ReviewSeverities {
			if strings.EqualFold(value, name) {
				return index
			}
		}
		return len(ReviewSeverities)
	}
	return rank(severity) <= rank(floor)
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
