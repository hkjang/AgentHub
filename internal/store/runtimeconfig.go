package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// What a runtime reported about its own configuration.
//
// The report is written from inside the Pod after the initialiser has copied and
// merged the configuration, by reading back the file it just wrote. That is the
// only place that can honestly answer "is this setting in effect": the control
// plane knows what it sent, and until now nothing knew what arrived.

// RuntimeConfigReport is one runtime's last report.
type RuntimeConfigReport struct {
	RuntimeID   string    `json:"runtimeId"`
	AgentID     string    `json:"agentId"`
	RuntimeType string    `json:"runtimeType"`
	Fingerprint string    `json:"fingerprint"`
	Status      string    `json:"status"`
	Detail      string    `json:"detail,omitempty"`
	File        string    `json:"file,omitempty"`
	Keys        []string  `json:"keys"`
	ReportedAt  time.Time `json:"reportedAt"`
}

// SaveRuntimeConfigReport records what one Pod started with, replacing whatever it
// said last time: only the current state of a runtime is interesting, and a
// history of every restart would grow without bound for no reader.
func (s *Store) SaveRuntimeConfigReport(ctx context.Context, report RuntimeConfigReport) error {
	keys, err := json.Marshal(report.Keys)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO runtime_config_reports
		(runtime_id,agent_id,runtime_type,fingerprint,status,detail,file,keys,reported_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,now())
		ON CONFLICT (runtime_id) DO UPDATE SET agent_id=EXCLUDED.agent_id, runtime_type=EXCLUDED.runtime_type,
			fingerprint=EXCLUDED.fingerprint, status=EXCLUDED.status, detail=EXCLUDED.detail,
			file=EXCLUDED.file, keys=EXCLUDED.keys, reported_at=now()`,
		report.RuntimeID, report.AgentID, report.RuntimeType, report.Fingerprint,
		report.Status, report.Detail, report.File, keys)
	return err
}

// RuntimeConfigReportByRuntime reads one runtime's report.
func (s *Store) RuntimeConfigReportByRuntime(ctx context.Context, runtimeID string) (RuntimeConfigReport, error) {
	var report RuntimeConfigReport
	var keys []byte
	err := s.pool.QueryRow(ctx, `SELECT runtime_id,agent_id,runtime_type,fingerprint,status,detail,file,keys,reported_at
		FROM runtime_config_reports WHERE runtime_id=$1`, runtimeID).
		Scan(&report.RuntimeID, &report.AgentID, &report.RuntimeType, &report.Fingerprint,
			&report.Status, &report.Detail, &report.File, &keys, &report.ReportedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RuntimeConfigReport{}, ErrNotFound
	}
	if err != nil {
		return RuntimeConfigReport{}, err
	}
	_ = json.Unmarshal(keys, &report.Keys)
	return report, nil
}

// RuntimeConfigReports lists the reports for every runtime that has one, newest
// first, so an operator can see the fleet rather than one Pod.
func (s *Store) RuntimeConfigReports(ctx context.Context, limit int) ([]RuntimeConfigReport, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `SELECT runtime_id,agent_id,runtime_type,fingerprint,status,detail,file,keys,reported_at
		FROM runtime_config_reports ORDER BY reported_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []RuntimeConfigReport{}
	for rows.Next() {
		var report RuntimeConfigReport
		var keys []byte
		if err := rows.Scan(&report.RuntimeID, &report.AgentID, &report.RuntimeType, &report.Fingerprint,
			&report.Status, &report.Detail, &report.File, &keys, &report.ReportedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(keys, &report.Keys)
		items = append(items, report)
	}
	return items, rows.Err()
}
