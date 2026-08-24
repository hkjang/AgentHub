package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hkjang/AgentHub/internal/quota"
)

// Departments and the per-person overrides that sit on top of them.
//
// The resolution is always the same three levels, in the same order: what the
// platform allows everybody, what this person's department allows one member,
// and what was set for this person. Field by field, so a department that sets a
// runtime count does not accidentally hand out unlimited memory.
//
// Separately from that, a department has a total — the capacity it was given —
// which is checked against everything its members are holding together. A person
// can be inside their own limit and still be refused because their department is
// full, and the message says which, because those need different answers.

// Department is one department and its two quotas.
type Department struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Quota       quota.Department `json:"quota"`
	// Members and what they are holding, filled in by the listing so an
	// administrator can see whether a limit is close before somebody hits it.
	Members   int        `json:"members"`
	Held      quota.Held `json:"held"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// UserQuota is one person's override.
type UserQuota struct {
	OwnerID   string       `json:"ownerId"`
	Username  string       `json:"username"`
	Quota     quota.Limits `json:"quota"`
	Note      string       `json:"note"`
	UpdatedAt time.Time    `json:"updatedAt"`
}

// EffectiveQuota is what applies to one person, and where each part came from.
// The console shows the source because "why can I only start two" is answered by
// the level rather than by the number.
type EffectiveQuota struct {
	OwnerID        string           `json:"ownerId"`
	DepartmentID   string           `json:"departmentId,omitempty"`
	Department     string           `json:"department,omitempty"`
	Platform       quota.Limits     `json:"platform"`
	Inherited      quota.Limits     `json:"inherited"`
	Personal       quota.Limits     `json:"personal"`
	Effective      quota.Limits     `json:"effective"`
	Held           quota.Held       `json:"held"`
	DepartmentQ    quota.Department `json:"departmentQuota"`
	DepartmentHeld quota.Held       `json:"departmentHeld"`
}

func (s *Store) Departments(ctx context.Context) ([]Department, error) {
	// What every department is holding, in two queries rather than two per
	// department. The listing is the screen an administrator opens to see whether
	// anybody is near a limit, and it fired 2N round trips to build one table.
	held, err := s.departmentsHeld(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.name, d.description, d.quota, d.created_at, d.updated_at,
		       (SELECT count(*) FROM users u WHERE u.department_id = d.id)
		FROM departments d ORDER BY d.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Department{}
	for rows.Next() {
		var item Department
		var raw []byte
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &raw, &item.CreatedAt, &item.UpdatedAt, &item.Members); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &item.Quota)
		item.Held = held[item.ID]
		items = append(items, item)
	}
	return items, rows.Err()
}

// departmentsHeld totals what each department's members hold right now. A
// department nobody has joined is absent from the map, which reads back as a
// zero Held — the same answer the per-department query gave.
func (s *Store) departmentsHeld(ctx context.Context) (map[string]quota.Held, error) {
	out := map[string]quota.Held{}
	rows, err := s.pool.Query(ctx, `
		SELECT u.department_id, count(*), COALESCE(sum(p.cpu_millis),0), COALESCE(sum(p.memory_mb),0), COALESCE(sum(p.gpu_count),0)
		FROM agent_runtimes r
		JOIN agent_definitions a ON a.id = r.agent_id
		JOIN users u ON u.id = r.owner_id
		LEFT JOIN runtime_profiles p ON p.id = a.runtime_profile_id
		WHERE r.desired_state = 'running' AND u.department_id IS NOT NULL
		GROUP BY u.department_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var held quota.Held
		if err := rows.Scan(&id, &held.Runtimes, &held.CPUMillis, &held.MemoryMB, &held.GPUs); err != nil {
			return nil, err
		}
		out[id] = held
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	storage, err := s.pool.Query(ctx, `
		SELECT u.department_id, COALESCE(sum(w.size_gb),0)
		FROM workspaces w JOIN users u ON u.id = w.owner_id
		WHERE u.department_id IS NOT NULL
		GROUP BY u.department_id`)
	if err != nil {
		return nil, err
	}
	defer storage.Close()
	for storage.Next() {
		var id string
		var size int
		if err := storage.Scan(&id, &size); err != nil {
			return nil, err
		}
		entry := out[id]
		entry.StorageGB = size
		out[id] = entry
	}
	return out, storage.Err()
}

func (s *Store) SaveDepartment(ctx context.Context, item Department) (Department, error) {
	item.Name = strings.TrimSpace(item.Name)
	if item.Name == "" || len(item.Name) > 80 {
		return Department{}, errors.New("부서 이름은 1~80자여야 합니다")
	}
	// A department being created has no id yet and gets one from its name. Two
	// names can slug to the same id — "플랫폼 팀" and "플랫폼-팀" — and an upsert
	// would then silently rewrite the existing department's limits while its
	// members went on pointing at it. So a create refuses the collision and an
	// update, which carries the id the caller is editing, still writes.
	creating := item.ID == ""
	if creating {
		item.ID = "dept-" + safeIdentifier(item.Name)
	}
	raw, err := json.Marshal(item.Quota)
	if err != nil {
		return Department{}, err
	}
	if creating {
		tag, insertErr := s.pool.Exec(ctx, `INSERT INTO departments(id,name,description,quota) VALUES($1,$2,$3,$4)
			ON CONFLICT(id) DO NOTHING`, item.ID, item.Name, item.Description, raw)
		if insertErr != nil {
			return Department{}, insertErr
		}
		if tag.RowsAffected() == 0 {
			return Department{}, Conflict{Message: "같은 이름의 부서가 이미 있습니다. 기존 부서를 수정하거나 다른 이름을 쓰세요"}
		}
		return item, nil
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO departments(id,name,description,quota) VALUES($1,$2,$3,$4)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description,quota=excluded.quota,updated_at=now()`,
		item.ID, item.Name, item.Description, raw)
	if err != nil {
		return Department{}, err
	}
	return item, nil
}

// DeleteDepartment removes it and leaves its members without one, which is the
// same state a fresh deployment is in: the platform's own limits apply.
func (s *Store) DeleteDepartment(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM departments WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetUserDepartment(ctx context.Context, userID, departmentID string) error {
	var value any
	if strings.TrimSpace(departmentID) != "" {
		value = departmentID
	}
	tag, err := s.pool.Exec(ctx, `UPDATE users SET department_id=$2, updated_at=now() WHERE id=$1`, userID, value)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SaveUserQuota(ctx context.Context, item UserQuota, actorID string) error {
	raw, err := json.Marshal(item.Quota)
	if err != nil {
		return err
	}
	// An override of nothing is not an override: removing every field removes the
	// row, so the console shows the person as inheriting rather than as having an
	// empty exception nobody can explain.
	if item.Quota.Empty() && strings.TrimSpace(item.Note) == "" {
		_, err = s.pool.Exec(ctx, `DELETE FROM user_quotas WHERE owner_id=$1`, item.OwnerID)
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO user_quotas(owner_id,quota,note,updated_by) VALUES($1,$2,$3,$4)
		ON CONFLICT(owner_id) DO UPDATE SET quota=excluded.quota,note=excluded.note,updated_by=excluded.updated_by,updated_at=now()`,
		item.OwnerID, raw, strings.TrimSpace(item.Note), nullText(actorID))
	return err
}

func (s *Store) UserQuotas(ctx context.Context) ([]UserQuota, error) {
	rows, err := s.pool.Query(ctx, `SELECT q.owner_id,u.username,q.quota,q.note,q.updated_at
		FROM user_quotas q JOIN users u ON u.id=q.owner_id ORDER BY u.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []UserQuota{}
	for rows.Next() {
		var item UserQuota
		var raw []byte
		if err := rows.Scan(&item.OwnerID, &item.Username, &raw, &item.Note, &item.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &item.Quota)
		items = append(items, item)
	}
	return items, rows.Err()
}

// ResolveQuota is the one place the three levels are combined, so the enqueue
// path, the runtime path and the console all answer the same question the same
// way.
func (s *Store) ResolveQuota(ctx context.Context, userID string) (EffectiveQuota, error) {
	return s.ResolveQuotaExcept(ctx, userID, "")
}

// ResolveQuotaExcept resolves the same three levels while leaving one runtime out
// of what is currently held — the one the caller is deciding about, whose record
// may already exist.
func (s *Store) ResolveQuotaExcept(ctx context.Context, userID, exceptRuntimeID string) (EffectiveQuota, error) {
	out := EffectiveQuota{OwnerID: userID}

	var governance governanceSettings
	if err := s.Setting(ctx, "governance", &governance); err != nil {
		return out, err
	}
	// The execution limits live in the same settings blob, read through a
	// different view of it — one place to configure, two shapes to read.
	var execution quota.Policy
	if err := s.Setting(ctx, "governance", &execution); err != nil && !errors.Is(err, ErrNotFound) {
		return out, err
	}
	out.Platform = quota.Limits{
		MaxRuntimes: governance.MaxRuntimesPerUser, MaxCPUMillis: governance.MaxCPUMillisPerUser,
		MaxMemoryMB: governance.MaxMemoryMBPerUser, MaxStorageGB: governance.MaxStorageGBPerUser,
		MaxGPUs:         governance.MaxGPUsPerUser,
		MaxRunningTasks: execution.MaxRunningTasksPerUser, TokenBudget: execution.TokenBudgetPerUser,
		CostBudget: execution.CostBudgetPerUser,
	}

	var departmentID *string
	var departmentName, departmentRaw, personalRaw []byte
	err := s.pool.QueryRow(ctx, `
		SELECT u.department_id, d.name, d.quota, q.quota
		FROM users u
		LEFT JOIN departments d ON d.id = u.department_id
		LEFT JOIN user_quotas q ON q.owner_id = u.id
		WHERE u.id = $1`, userID).Scan(&departmentID, &departmentName, &departmentRaw, &personalRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if departmentID != nil {
		out.DepartmentID = *departmentID
		out.Department = string(departmentName)
	}
	if len(departmentRaw) > 0 {
		_ = json.Unmarshal(departmentRaw, &out.DepartmentQ)
		out.Inherited = out.DepartmentQ.PerMember
	}
	if len(personalRaw) > 0 {
		_ = json.Unmarshal(personalRaw, &out.Personal)
	}
	out.Effective = quota.Resolve(out.Platform, out.Inherited, out.Personal)

	if out.Held, err = s.userHeldExcept(ctx, userID, exceptRuntimeID); err != nil {
		return out, err
	}
	if out.DepartmentID != "" {
		if out.DepartmentHeld, err = s.departmentHeldExcept(ctx, out.DepartmentID, exceptRuntimeID); err != nil {
			return out, err
		}
	}
	return out, nil
}

// UserHeld and DepartmentHeld are the same question asked of one person and of
// everybody in a department.
func (s *Store) UserHeld(ctx context.Context, userID string) (quota.Held, error) {
	// $2 is referenced even when nothing is excluded, so the two forms of this
	// query take the same arguments and cannot drift apart.
	return s.heldWhere(ctx, `r.owner_id = $1 AND r.id <> $2`, `w.owner_id = $1`, userID, "")
}

func (s *Store) userHeldExcept(ctx context.Context, userID, exceptRuntimeID string) (quota.Held, error) {
	return s.heldWhere(ctx, `r.owner_id = $1 AND r.id <> $2`, `w.owner_id = $1`, userID, exceptRuntimeID)
}

func (s *Store) DepartmentHeld(ctx context.Context, departmentID string) (quota.Held, error) {
	return s.departmentHeldExcept(ctx, departmentID, "")
}

func (s *Store) departmentHeldExcept(ctx context.Context, departmentID, exceptRuntimeID string) (quota.Held, error) {
	return s.heldWhere(ctx,
		`r.owner_id IN (SELECT id FROM users WHERE department_id = $1) AND r.id <> $2`,
		`w.owner_id IN (SELECT id FROM users WHERE department_id = $1)`,
		departmentID, exceptRuntimeID)
}

// heldWhere counts what is held. except is the runtime to leave out — the one the
// caller is deciding about, whose record may already exist — and is passed as a
// second parameter that no clause references when it is empty.
func (s *Store) heldWhere(ctx context.Context, runtimeWhere, workspaceWhere, arg, except string) (quota.Held, error) {
	var held quota.Held
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT count(*), COALESCE(sum(p.cpu_millis),0), COALESCE(sum(p.memory_mb),0), COALESCE(sum(p.gpu_count),0)
		FROM agent_runtimes r
		JOIN agent_definitions a ON a.id = r.agent_id
		LEFT JOIN runtime_profiles p ON p.id = a.runtime_profile_id
		WHERE %s AND r.desired_state = 'running'`, runtimeWhere), arg, except).
		Scan(&held.Runtimes, &held.CPUMillis, &held.MemoryMB, &held.GPUs)
	if err != nil {
		return held, err
	}
	err = s.pool.QueryRow(ctx, fmt.Sprintf(`SELECT COALESCE(sum(w.size_gb),0) FROM workspaces w WHERE %s`, workspaceWhere), arg).
		Scan(&held.StorageGB)
	return held, err
}

// safeIdentifier turns a name into something usable as an id.
func safeIdentifier(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '-', r == '_':
			b.WriteByte('-')
		default:
			// Korean and everything else: keep it, lowercased, so a department
			// named 플랫폼팀 gets an id somebody can recognise.
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-")
}

// DepartmentPressure is a department close enough to one of its limits that
// somebody should hear about it before a colleague is refused.
type DepartmentPressure struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Limit   string `json:"limit"`
	Used    int    `json:"used"`
	Allowed int    `json:"allowed"`
	Percent int    `json:"percent"`
}

// PressureThreshold is how full a limit has to be before it is worth saying.
// Below it the bars on the department screen are enough; at it, the next request
// is plausibly the one that gets refused.
const PressureThreshold = 80

// DepartmentsUnderPressure reports the department totals that are nearly spent.
//
// It reads the same numbers the department screen shows rather than counting
// again, so a warning and the screen it sends somebody to cannot disagree. Only
// held resources are considered: token and cost budgets are measured over a
// 30-day window, and a warning that a department is 80% through a month is a
// different, noisier kind of statement than one that it is nearly out of
// runtimes right now.
func (s *Store) DepartmentsUnderPressure(ctx context.Context) ([]DepartmentPressure, error) {
	departments, err := s.Departments(ctx)
	if err != nil {
		return nil, err
	}
	out := []DepartmentPressure{}
	for _, department := range departments {
		for _, dimension := range []struct {
			label   string
			used    int
			allowed int
		}{
			{"Runtime", department.Held.Runtimes, department.Quota.Total.MaxRuntimes},
			{"CPU", department.Held.CPUMillis, department.Quota.Total.MaxCPUMillis},
			{"Memory", department.Held.MemoryMB, department.Quota.Total.MaxMemoryMB},
			{"Storage", department.Held.StorageGB, department.Quota.Total.MaxStorageGB},
			{"GPU", department.Held.GPUs, department.Quota.Total.MaxGPUs},
		} {
			if dimension.allowed <= 0 {
				continue
			}
			percent := dimension.used * 100 / dimension.allowed
			if percent < PressureThreshold {
				continue
			}
			out = append(out, DepartmentPressure{
				ID: department.ID, Name: department.Name, Limit: dimension.label,
				Used: dimension.used, Allowed: dimension.allowed, Percent: percent,
			})
		}
	}
	return out, nil
}
