package mqttx

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/outbox"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

// livePublishTimeout bounds a live publish, so a dead broker costs at most
// this before the message goes to the outbox.
const livePublishTimeout = time.Second

// Queueing publishes live and, when the broker cannot take a message, applies
// outbox.Policy: history, dispatches and dead letters are queued on local disk
// and replayed later; control messages are not, and their failure is returned.
type Queueing struct {
	conn    *Conn
	ob      *outbox.Outbox
	process string
}

// NewQueueing wraps conn with the outbox ob for the named process.
func NewQueueing(conn *Conn, ob *outbox.Outbox, process string) *Queueing {
	return &Queueing{conn: conn, ob: ob, process: process}
}

// Outbox returns the queue, for health and stats.
func (q *Queueing) Outbox() *outbox.Outbox { return q.ob }

// Publish sends payload live. If that fails and the topic may be replayed, it
// is queued and Publish returns nil (it will be delivered later); otherwise
// the failure is returned, so the caller can retry it live (a command) or
// simply let the next one take its place (a heartbeat).
func (q *Queueing) Publish(ctx context.Context, topic string, qos byte, payload []byte) error {
	pctx, cancel := context.WithTimeout(ctx, livePublishTimeout)
	err := q.conn.Publish(pctx, topic, qos, payload)
	cancel()
	if err == nil {
		return nil
	}
	replay, droppable, queue := outbox.Policy(topic)
	if !queue {
		return err
	}
	if qerr := q.ob.Add(outbox.Entry{Topic: replay, Payload: payload, Droppable: droppable}); qerr != nil {
		return fmt.Errorf("mqttx: send failed (%v) and could not be queued: %w", err, qerr)
	}
	return nil
}

// DeadLetter sets aside a message this process received but could not use.
// It goes through Publish, so it is queued if the broker is down too.
func (q *Queueing) DeadLetter(ctx context.Context, topic string, payload []byte, cause error) {
	const maxPayload = 2048
	p := string(payload)
	if len(p) > maxPayload {
		p = p[:maxPayload] + "…"
	}
	body, err := json.Marshal(proto.DeadLetter{
		Process: q.process, Topic: topic, Payload: p, Error: cause.Error(),
		WallTS: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return
	}
	_ = q.Publish(ctx, proto.DeadLetterTopic(q.process), 1, body)
}

// RunDrain replays the outbox every `every` until ctx ends (run it in a
// goroutine). Entries go out in order; the first failure stops a round and the
// rest wait for the next one.
func RunDrain(ctx context.Context, q *Queueing, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if q.ob.Len() == 0 {
			continue
		}
		_, _ = q.ob.Drain(func(e outbox.Entry) error {
			pctx, cancel := context.WithTimeout(ctx, livePublishTimeout)
			defer cancel()
			return q.conn.Publish(pctx, e.Topic, 1, e.Payload)
		})
	}
}

// StartQueueing opens the process's outbox (OUTBOX_DIR, default "outbox",
// file <process>.jsonl), replays anything left from a previous run, and keeps
// draining it every second until ctx ends. The directory must be on a volume
// in a container, or a recreated container loses what was queued.
func StartQueueing(ctx context.Context, conn *Conn, process string) (*Queueing, error) {
	dir := os.Getenv("OUTBOX_DIR")
	if dir == "" {
		dir = "outbox"
	}
	ob, err := outbox.Open(filepath.Join(dir, process+".jsonl"), 50000)
	if err != nil {
		return nil, err
	}
	q := NewQueueing(conn, ob, process)
	go RunDrain(ctx, q, time.Second)
	return q, nil
}
