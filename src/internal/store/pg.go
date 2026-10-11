// Package store is the history database: TimescaleDB (decision D1). It holds
// what the system saw, decided and commanded, for the dashboard's history, the
// evaluation and the audit trail. It is never on the safety path: the detector
// and the sprinklers do not read it.
package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

// PG is a TimescaleDB-backed store.
type PG struct {
	pool *pgxpool.Pool
}

// Open connects and checks the connection. url is a libpq-style URL.
func Open(ctx context.Context, url string) (*PG, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &PG{pool: pool}, nil
}

// Close releases the connections.
func (p *PG) Close() { p.pool.Close() }

// schema is applied statement by statement and is safe to run repeatedly.
//
// Time axis: wall_ts (ingestion time) partitions the hypertables and drives
// retention. sim_time is stored beside it because requirements and evidence
// are in sim time (D5). Readings are kept 7 days: they are bulky and the
// evaluation reruns from the simulator. Decisions and commands are the audit
// trail (NFR5) and are kept. A QoS 1 redelivery can store a reading twice;
// analysis deduplicates on (sensor_id, seq, sim_time).
var schema = []string{
	`CREATE EXTENSION IF NOT EXISTS timescaledb`,

	`CREATE TABLE IF NOT EXISTS readings (
		wall_ts   timestamptz      NOT NULL DEFAULT now(),
		sim_time  double precision NOT NULL,
		sensor_id text             NOT NULL,
		room      text             NOT NULL,
		type      text             NOT NULL,
		unit      text             NOT NULL,
		value     double precision NOT NULL,
		seq       bigint           NOT NULL
	)`,
	`SELECT create_hypertable('readings', 'wall_ts', if_not_exists => TRUE)`,
	`CREATE INDEX IF NOT EXISTS readings_room_type_sim ON readings (room, type, sim_time DESC)`,
	`SELECT add_retention_policy('readings', INTERVAL '7 days', if_not_exists => TRUE)`,

	`CREATE TABLE IF NOT EXISTS decisions (
		wall_ts  timestamptz      NOT NULL DEFAULT now(),
		sim_time double precision NOT NULL,
		room     text             NOT NULL,
		tier     text             NOT NULL,
		state    text             NOT NULL,
		action   text             NOT NULL,
		reason   text             NOT NULL,
		stale    text[]           NOT NULL DEFAULT '{}'
	)`,
	`SELECT create_hypertable('decisions', 'wall_ts', if_not_exists => TRUE)`,
	`CREATE INDEX IF NOT EXISTS decisions_room_sim ON decisions (room, sim_time DESC)`,

	`CREATE TABLE IF NOT EXISTS commands (
		wall_ts  timestamptz      NOT NULL DEFAULT now(),
		sim_time double precision NOT NULL,
		room     text             NOT NULL,
		action   text             NOT NULL,
		reason   text             NOT NULL,
		seq      bigint           NOT NULL,
		ttl      double precision NOT NULL
	)`,
	`SELECT create_hypertable('commands', 'wall_ts', if_not_exists => TRUE)`,
	`CREATE INDEX IF NOT EXISTS commands_room_sim ON commands (room, sim_time DESC)`,

	// messages a process received but could not use (D13); kept for inspection
	`CREATE TABLE IF NOT EXISTS dead_letters (
		wall_ts   timestamptz NOT NULL DEFAULT now(),
		failed_at text        NOT NULL,
		process   text        NOT NULL,
		topic     text        NOT NULL,
		payload   text        NOT NULL,
		error     text        NOT NULL
	)`,
	`SELECT create_hypertable('dead_letters', 'wall_ts', if_not_exists => TRUE)`,

	// what the responders were told, and their acknowledgement (FR6)
	`CREATE TABLE IF NOT EXISTS dispatches (
		wall_ts          timestamptz      NOT NULL DEFAULT now(),
		sim_time         double precision NOT NULL,
		incident         text             NOT NULL,
		kind             text             NOT NULL,
		severity         text             NOT NULL,
		affected         text[]           NOT NULL DEFAULT '{}',
		in_danger        int              NOT NULL,
		at_risk          int              NOT NULL,
		safe             int              NOT NULL,
		in_danger_people text[]           NOT NULL DEFAULT '{}',
		routes           jsonb            NOT NULL DEFAULT '[]',
		no_route         text[]           NOT NULL DEFAULT '{}',
		occupancy_stale  boolean          NOT NULL DEFAULT false,
		occupancy_unknown boolean         NOT NULL DEFAULT false
	)`,
	`SELECT create_hypertable('dispatches', 'wall_ts', if_not_exists => TRUE)`,
	`ALTER TABLE dispatches ADD COLUMN IF NOT EXISTS en_route int NOT NULL DEFAULT 0`,
	`ALTER TABLE dispatches ADD COLUMN IF NOT EXISTS unaccounted int NOT NULL DEFAULT 0`,
	`CREATE TABLE IF NOT EXISTS dispatch_acks (
		wall_ts   timestamptz      NOT NULL DEFAULT now(),
		sim_time  double precision NOT NULL,
		incident  text             NOT NULL,
		responder text             NOT NULL
	)`,
	`SELECT create_hypertable('dispatch_acks', 'wall_ts', if_not_exists => TRUE)`,

	// one row per process: when it was last heard from (application health)
	`CREATE TABLE IF NOT EXISTS component_status (
		process   text PRIMARY KEY,
		last_seen timestamptz      NOT NULL DEFAULT now(),
		sim_time  double precision NOT NULL,
		seq       bigint           NOT NULL
	)`,
}

// Migrate creates the tables, hypertables, indexes and the retention policy.
func (p *PG) Migrate(ctx context.Context) error {
	for _, stmt := range schema {
		if _, err := p.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("store: migrate: %w", err)
		}
	}
	return nil
}

// InsertReadings writes a batch in one COPY.
func (p *PG) InsertReadings(ctx context.Context, rs []gateway.Reading) error {
	if len(rs) == 0 {
		return nil
	}
	rows := make([][]any, len(rs))
	for i, r := range rs {
		rows[i] = []any{r.SimTime, r.SensorID, r.Room, r.Type, r.Unit, r.Value, int64(r.Seq)}
	}
	_, err := p.pool.CopyFrom(ctx, pgx.Identifier{"readings"},
		[]string{"sim_time", "sensor_id", "room", "type", "unit", "value", "seq"}, pgx.CopyFromRows(rows))
	return err
}

// InsertDecisions writes decisions, keeping the reason and any stale signals.
func (p *PG) InsertDecisions(ctx context.Context, ds []proto.Decision) error {
	if len(ds) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, d := range ds {
		stale := d.Stale
		if stale == nil {
			stale = []string{} // a nil slice would be NULL, and the column is NOT NULL
		}
		b.Queue(`INSERT INTO decisions (sim_time, room, tier, state, action, reason, stale)
		         VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			d.SimTime, d.Room, d.Tier, d.State, d.Action, d.Reason, stale)
	}
	return p.sendBatch(ctx, b, len(ds))
}

// InsertCommands writes sprinkler commands.
func (p *PG) InsertCommands(ctx context.Context, cs []proto.Command) error {
	if len(cs) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, c := range cs {
		b.Queue(`INSERT INTO commands (sim_time, room, action, reason, seq, ttl)
		         VALUES ($1, $2, $3, $4, $5, $6)`,
			c.SimTime, c.Room, c.Action, c.Reason, int64(c.Seq), c.TTL)
	}
	return p.sendBatch(ctx, b, len(cs))
}

// InsertDeadLetters writes messages that a process could not use.
func (p *PG) InsertDeadLetters(ctx context.Context, ds []proto.DeadLetter) error {
	if len(ds) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, d := range ds {
		b.Queue(`INSERT INTO dead_letters (failed_at, process, topic, payload, error)
		         VALUES ($1, $2, $3, $4, $5)`, d.WallTS, d.Process, d.Topic, d.Payload, d.Error)
	}
	return p.sendBatch(ctx, b, len(ds))
}

// InsertDispatches writes what responders were told and their acknowledgements.
func (p *PG) InsertDispatches(ctx context.Context, ds []proto.Dispatch, acks []proto.DispatchAck) error {
	if len(ds) == 0 && len(acks) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, d := range ds {
		routes, err := json.Marshal(d.Routes)
		if err != nil {
			return err
		}
		people := make([]string, 0, len(d.InDanger))
		for _, pr := range d.InDanger {
			people = append(people, pr.Name+" ("+pr.Room+")")
		}
		affected, noRoute := d.Affected, d.NoRoute
		if affected == nil {
			affected = []string{}
		}
		if noRoute == nil {
			noRoute = []string{}
		}
		b.Queue(`INSERT INTO dispatches (sim_time, incident, kind, severity, affected, in_danger, at_risk, safe,
		                                 in_danger_people, routes, no_route, occupancy_stale, occupancy_unknown, en_route, unaccounted)
		         VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			d.SimTime, d.Incident, d.Kind, d.Severity, affected, len(d.InDanger), len(d.AtRisk), d.Safe,
			people, string(routes), noRoute, d.OccupancyStale, d.OccupancyUnknown, len(d.EnRoute), len(d.Unaccounted))
	}
	for _, a := range acks {
		b.Queue(`INSERT INTO dispatch_acks (sim_time, incident, responder) VALUES ($1,$2,$3)`, a.SimTime, a.Incident, a.Responder)
	}
	return p.sendBatch(ctx, b, len(ds)+len(acks))
}

// InsertHeartbeats upserts each process's latest heartbeat.
func (p *PG) InsertHeartbeats(ctx context.Context, hs []proto.Heartbeat) error {
	if len(hs) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, h := range hs {
		b.Queue(`INSERT INTO component_status (process, last_seen, sim_time, seq) VALUES ($1, now(), $2, $3)
		         ON CONFLICT (process) DO UPDATE SET last_seen = now(), sim_time = EXCLUDED.sim_time, seq = EXCLUDED.seq`,
			h.Process, h.SimTime, int64(h.Seq))
	}
	return p.sendBatch(ctx, b, len(hs))
}

func (p *PG) sendBatch(ctx context.Context, b *pgx.Batch, n int) error {
	res := p.pool.SendBatch(ctx, b)
	defer res.Close()
	for i := 0; i < n; i++ {
		if _, err := res.Exec(); err != nil {
			return err
		}
	}
	return nil
}
