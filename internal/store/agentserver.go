package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The agent servers this deployment may send work to.
//
// These are not runtimes the platform starts. They are capacity it is given —
// a development box, a machine inside the secure network, one with a GPU — so
// they are registered by an administrator rather than spawned, and what matters
// about each is where it sits as much as how to reach it.

// AgentServer is one registered server.
type AgentServer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
	Kind    string `json:"kind"`
	// NetworkZone is how this deployment names the network the server sits in.
	// Free text: the platform does not get to decide what a site calls its zones.
	NetworkZone string `json:"networkZone"`
	// Capacity is how many conversations it may hold at once. Zero means unknown,
	// which placement treats as unbounded rather than as full — refusing to send
	// work to a server because nobody typed a number would be the platform
	// inventing a limit.
	Capacity     int        `json:"capacity"`
	Enabled      bool       `json:"enabled"`
	Health       string     `json:"health"`
	HealthDetail string     `json:"healthDetail,omitempty"`
	CheckedAt    *time.Time `json:"checkedAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// AgentServerKinds are the kinds this platform knows how to speak to.
var AgentServerKinds = []string{"openhands"}

const agentServerColumns = `id,name,base_url,kind,network_zone,capacity,enabled,health,health_detail,checked_at,created_at,updated_at`

func scanAgentServer(row pgx.Row) (AgentServer, error) {
	var item AgentServer
	err := row.Scan(&item.ID, &item.Name, &item.BaseURL, &item.Kind, &item.NetworkZone,
		&item.Capacity, &item.Enabled, &item.Health, &item.HealthDetail, &item.CheckedAt,
		&item.CreatedAt, &item.UpdatedAt)
	return item, err
}

// SaveAgentServer registers a server or updates one.
func (s *Store) SaveAgentServer(ctx context.Context, item AgentServer, actorID string) (AgentServer, error) {
	if strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.BaseURL) == "" {
		return AgentServer{}, errors.New("이름과 주소가 필요합니다")
	}
	if item.Kind == "" {
		item.Kind = "openhands"
	}
	if !contains(AgentServerKinds, item.Kind) {
		return AgentServer{}, errors.New("알 수 없는 서버 종류입니다: " + item.Kind)
	}
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	// A registered server keeps whatever the last check found. Resetting it here
	// would make every edit — renaming it, changing its capacity — look like a
	// server nobody has checked.
	row := s.pool.QueryRow(ctx, `INSERT INTO agent_servers
		(id,name,base_url,kind,network_zone,capacity,enabled,created_by)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (id) DO UPDATE SET name=excluded.name, base_url=excluded.base_url,
			kind=excluded.kind, network_zone=excluded.network_zone, capacity=excluded.capacity,
			enabled=excluded.enabled, updated_at=now()
		RETURNING `+agentServerColumns,
		item.ID, strings.TrimSpace(item.Name), strings.TrimSpace(item.BaseURL), item.Kind,
		strings.TrimSpace(item.NetworkZone), item.Capacity, item.Enabled, nullText(actorID))
	return scanAgentServer(row)
}

// AgentServers lists what is registered.
func (s *Store) AgentServers(ctx context.Context) ([]AgentServer, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+agentServerColumns+` FROM agent_servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AgentServer{}
	for rows.Next() {
		item, err := scanAgentServer(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// AgentServerByID reads one.
func (s *Store) AgentServerByID(ctx context.Context, id string) (AgentServer, error) {
	item, err := scanAgentServer(s.pool.QueryRow(ctx, `SELECT `+agentServerColumns+` FROM agent_servers WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentServer{}, ErrNotFound
	}
	return item, err
}

// DeleteAgentServer removes one.
func (s *Store) DeleteAgentServer(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM agent_servers WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordAgentServerHealth keeps what a check found.
//
// Kept rather than asked on every read: a console listing ten servers must not
// make ten outbound calls, and an operator needs to see the last answer even when
// the server has since stopped answering at all.
func (s *Store) RecordAgentServerHealth(ctx context.Context, id, health, detail string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_servers SET health=$2, health_detail=$3, checked_at=now() WHERE id=$1`,
		id, health, detail)
	return err
}
