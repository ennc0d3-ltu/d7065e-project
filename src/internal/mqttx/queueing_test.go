package mqttx

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/outbox"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

// brokerOn starts a broker on a fixed address, so a test can kill it and
// bring a new one up on the same port.
func brokerOn(t *testing.T, addr string) func() {
	t.Helper()
	s := mqtt.New(&mqtt.Options{InlineClient: true})
	_ = s.AddHook(new(auth.AllowHook), nil)
	if err := s.AddListener(listeners.NewTCP(listeners.Config{ID: "t", Address: addr})); err != nil {
		t.Fatal(err)
	}
	if err := s.Serve(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	return func() {
		if !stopped {
			_ = s.Close()
			stopped = true
		}
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return "127.0.0.1:" + strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func newQueueing(t *testing.T, url string) (*Queueing, *outbox.Outbox) {
	t.Helper()
	ob, err := outbox.Open(filepath.Join(t.TempDir(), "ob.jsonl"), 1000)
	if err != nil {
		t.Fatal(err)
	}
	conn := connect(t, url, "producer")
	return NewQueueing(conn, ob, "gateway"), ob
}

func TestQueueingPublishesLiveWhileTheBrokerIsUp(t *testing.T) {
	url, stop := startBroker(t)
	defer stop()
	q, ob := newQueueing(t, url)
	sub := connect(t, url, "watcher")
	ch, h := collect()
	_ = sub.Subscribe(context.Background(), "sensors/#", 1, h)
	waitSubscribed(t, q.conn, ch, "sensors/warmup")

	if err := q.Publish(context.Background(), "sensors/level0/A109/co", 1, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if m := waitMsg(t, ch); m.topic != "sensors/level0/A109/co" || ob.Len() != 0 {
		t.Fatalf("topic=%q queued=%d", m.topic, ob.Len())
	}
}

func TestAReadingThatCannotBeSentIsReplayedOnlyOnTheHistoryTopic(t *testing.T) {
	addr := freeAddr(t)
	stop := brokerOn(t, addr)
	url := "tcp://" + addr
	q, ob := newQueueing(t, url)

	stop() // the broker dies
	if err := q.Publish(context.Background(), "sensors/level0/A109/co", 1, []byte(`{"v":1}`)); err != nil {
		t.Fatalf("a queued reading must not be reported as a failure: %v", err)
	}
	if ob.Len() != 1 {
		t.Fatalf("queued = %d, want 1", ob.Len())
	}

	stop2 := brokerOn(t, addr) // it comes back
	defer stop2()
	watcher := connect(t, url, "watcher")
	ch, h := collect()
	_ = watcher.Subscribe(context.Background(), "#", 1, h)
	waitFor := time.After(5 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunDrain(ctx, q, 50*time.Millisecond)
	for {
		select {
		case m := <-ch:
			if m.topic == "sensors/level0/A109/co" {
				t.Fatal("the stale reading was replayed on the live topic")
			}
			if m.topic == "replay/sensors/level0/A109/co" {
				if ob.Len() != 0 {
					t.Fatalf("still queued after the drain: %d", ob.Len())
				}
				return
			}
		case <-waitFor:
			t.Fatal("the queued reading was never replayed")
		}
	}
}

func TestACommandThatCannotBeSentIsNeverQueued(t *testing.T) {
	addr := freeAddr(t)
	stop := brokerOn(t, addr)
	q, ob := newQueueing(t, "tcp://"+addr)
	stop()
	err := q.Publish(context.Background(), "commands/sprinkler/level0/A109", 1, []byte(`{}`))
	if err == nil {
		t.Fatal("a failed command was reported as sent")
	}
	if ob.Len() != 0 {
		t.Fatal("a sprinkler command was queued for replay")
	}
}

func TestDeadLetterCarriesTheBadMessageAndTheError(t *testing.T) {
	url, stop := startBroker(t)
	defer stop()
	q, _ := newQueueing(t, url)
	sub := connect(t, url, "watcher")
	ch, h := collect()
	_ = sub.Subscribe(context.Background(), "dlq/#", 1, h)
	waitSubscribed(t, q.conn, ch, "dlq/warmup")

	q.DeadLetter(context.Background(), "sensors/x", []byte(`nope`), errors.New("bad payload"))
	m := waitMsg(t, ch)
	var dl proto.DeadLetter
	if err := json.Unmarshal(m.payload, &dl); err != nil {
		t.Fatal(err)
	}
	if m.topic != "dlq/gateway" || dl.Process != "gateway" || dl.Topic != "sensors/x" || dl.Payload != "nope" || dl.Error != "bad payload" || dl.WallTS == "" {
		t.Fatalf("topic=%q dead letter=%+v", m.topic, dl)
	}
}
