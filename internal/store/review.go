package store

import (
	"context"
	"errors"
	"strconv"
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
	ID           string `json:"id"`
	RunID        string `json:"runId"`
	TaskID       string `json:"taskId,omitempty"`
	AgentID      string `json:"agentId"`
	OwnerID      string `json:"ownerId"`
	FilePath     string `json:"filePath"`
	StartLine    int    `json:"startLine"`
	EndLine      int    `json:"endLine"`
	Severity     string `json:"severity"`
	Category     string `json:"category"`
	Message      string `json:"message"`
	ExistingCode string `json:"existingCode,omitempty"`
	Suggestion   string `json:"suggestion,omitempty"`
	Status       string `json:"status"`
	// FixTaskID names the task somebody asked to fix this with. It is not a claim
	// that the finding is fixed — nothing has checked that — only that a fix was
	// asked for, which is what the console needs to stop somebody asking twice.
	FixTaskID string     `json:"fixTaskId,omitempty"`
	DecidedBy *string    `json:"decidedBy,omitempty"`
	DecidedAt *time.Time `json:"decidedAt,omitempty"`
	Source    string     `json:"source"`
	CreatedAt time.Time  `json:"createdAt"`
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

const reviewFindingColumns = `id,run_id,COALESCE(task_id,''),agent_id,owner_id,file_path,start_line,end_line,severity,category,message,existing_code,suggestion,status,decided_by,decided_at,source,created_at,COALESCE(fix_task_id,'')`

func scanReviewFinding(row pgx.Row) (ReviewFinding, error) {
	var item ReviewFinding
	err := row.Scan(&item.ID, &item.RunID, &item.TaskID, &item.AgentID, &item.OwnerID, &item.FilePath,
		&item.StartLine, &item.EndLine, &item.Severity, &item.Category, &item.Message,
		&item.ExistingCode, &item.Suggestion, &item.Status, &item.DecidedBy, &item.DecidedAt,
		&item.Source, &item.CreatedAt, &item.FixTaskID)
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

// ReviewFindingFilter narrows the list across every review somebody has run.
type ReviewFindingFilter struct {
	OwnerID  string
	AgentID  string
	Severity string
	Category string
	// Status defaults to open: the list exists to show what is still to be dealt
	// with, and a page that opens on a year of dismissed findings is a page
	// nobody reads twice.
	Status string
	Limit  int
	Offset int
}

// ReviewFindingPage is one page of findings with the size of the whole result,
// so the console can say how much is behind it.
type ReviewFindingPage struct {
	Items  []ReviewFinding `json:"items"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
	// OpenBySeverity counts what is still open across the whole filter, not the
	// page. A count of the page is the mistake the notification bell was fixed
	// for and the review queue after it.
	OpenBySeverity map[string]int `json:"openBySeverity"`
}

// ReviewFindingsFor lists findings across every review, worst first.
//
// Without this a finding can only be reached by knowing which run produced it.
// Somebody who ran three reviews yesterday had no way to ask what is still open,
// which is the only question the list is for.
func (s *Store) ReviewFindingsFor(ctx context.Context, filter ReviewFindingFilter, admin bool) (ReviewFindingPage, error) {
	filter.Limit = clampLimit(filter.Limit, 50, 200)
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	where := []string{"1=1"}
	args := []any{}
	add := func(clause string, value any) {
		args = append(args, value)
		where = append(where, strings.Replace(clause, "?", "$"+strconv.Itoa(len(args)), 1))
	}
	if !admin {
		add("owner_id = ?", filter.OwnerID)
	}
	if value := strings.TrimSpace(filter.AgentID); value != "" {
		add("agent_id = ?", value)
	}
	if value := strings.TrimSpace(filter.Severity); value != "" {
		add("severity = ?", value)
	}
	if value := strings.TrimSpace(filter.Category); value != "" {
		add("category = ?", value)
	}
	switch strings.TrimSpace(filter.Status) {
	case "", "open":
		where = append(where, "status = 'open'")
	case "all":
	default:
		add("status = ?", filter.Status)
	}
	predicate := strings.Join(where, " AND ")

	page := ReviewFindingPage{Items: []ReviewFinding{}, Limit: filter.Limit, Offset: filter.Offset, OpenBySeverity: map[string]int{}}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM review_findings WHERE `+predicate, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	// The counts are of the table this filter selects, not of the page.
	counts, err := s.pool.Query(ctx, `SELECT severity, count(*) FROM review_findings WHERE `+predicate+` GROUP BY severity`, args...)
	if err != nil {
		return page, err
	}
	for counts.Next() {
		var severity string
		var count int
		if err := counts.Scan(&severity, &count); err != nil {
			counts.Close()
			return page, err
		}
		page.OpenBySeverity[severity] = count
	}
	counts.Close()
	if err := counts.Err(); err != nil {
		return page, err
	}

	rows, err := s.pool.Query(ctx, `SELECT `+reviewFindingColumns+` FROM review_findings WHERE `+predicate+`
		ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 ELSE 3 END,
		created_at DESC, file_path
		LIMIT $`+strconv.Itoa(len(args)+1)+` OFFSET $`+strconv.Itoa(len(args)+2),
		append(args, filter.Limit, filter.Offset)...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanReviewFinding(rows)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	return page, rows.Err()
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

// LinkReviewFix records the task somebody asked to fix a finding with.
//
// It does not touch the finding's status. Asking for a fix is not the same as
// having one, and a platform that marked the finding fixed here would be
// reporting its own hope.
func (s *Store) LinkReviewFix(ctx context.Context, findingID, taskID, ownerID string, admin bool) (ReviewFinding, error) {
	query := `UPDATE review_findings SET fix_task_id=$1 WHERE id=$2`
	args := []any{taskID, findingID}
	if !admin {
		query += ` AND owner_id=$3`
		args = append(args, ownerID)
	}
	query += ` RETURNING ` + reviewFindingColumns
	item, err := scanReviewFinding(s.pool.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewFinding{}, ErrNotFound
	}
	return item, err
}

// ReviewFindingByID reads one finding.
func (s *Store) ReviewFindingByID(ctx context.Context, id, ownerID string, admin bool) (ReviewFinding, error) {
	query := `SELECT ` + reviewFindingColumns + ` FROM review_findings WHERE id=$1`
	args := []any{id}
	if !admin {
		query += ` AND owner_id=$2`
		args = append(args, ownerID)
	}
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
