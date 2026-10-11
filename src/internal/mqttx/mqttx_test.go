package mqttx

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

// startBroker runs an embedded MQTT broker on a free port and returns its URL.
func startBroker(t *testing.T) (url string, stop func()) {
	t.Helper()
	s := mqtt.New(&mqtt.Options{InlineClient: true})
	if err := s.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	l := listeners.NewTCP(listeners.Config{ID: "test", Address: "127.0.0.1:0"})
	if err := s.AddListener(l); err != nil {
		t.Fatal(err)
	}
	if err := s.Serve(); err != nil {
		t.Fatal(err)
	}
	return "tcp://" + l.Address(), func() { _ = s.Close() }
}

type msg struct {
	topic   string
	payload []byte
}

func collect() (chan msg, func(string, []byte)) {
	ch := make(chan msg, 16)
	return ch, func(topic string, payload []byte) { ch <- msg{topic, payload} }
}

func waitMsg(t *testing.T, ch chan msg) msg {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("no message received")
		return msg{}
	}
}

func connect(t *testing.T, url, id string) *Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := Connect(ctx, url, id)
	if err != nil {
		t.Fatalf("connect %s: %v", id, err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

func TestPublishedReadingReachesASubscriber(t *testing.T) {
	url, stop := startBroker(t)
	defer stop()
	sub := connect(t, url, "detector")
	pub := connect(t, url, "gateway")

	ch, h := collect()
	if err := sub.Subscribe(context.Background(), "sensors/#", 1, h); err != nil {
		t.Fatal(err)
	}

	r := gateway.Reading{SensorID: "A109-temp", Room: "level0/A109", Type: "temperature",
		Unit: "°C", Value: 41.5, Seq: 7, SimTime: 120}
	sink := NewReadingSink(pub, 1)
	if err := sink.Publish(context.Background(), r); err != nil {
		t.Fatal(err)
	}

	m := waitMsg(t, ch)
	if m.topic != "sensors/level0/A109/temperature" {
		t.Fatalf("topic = %q", m.topic)
	}
	var got gateway.Reading
	if err := json.Unmarshal(m.payload, &got); err != nil {
		t.Fatal(err)
	}
	if got != r {
		t.Fatalf("payload = %+v, want %+v", got, r)
	}
}

func TestSubscriberOnlyGetsMatchingTopics(t *testing.T) {
	url, stop := startBroker(t)
	defer stop()
	sub := connect(t, url, "smoke-only")
	pub := connect(t, url, "gateway")

	ch, h := collect()
	if err := sub.Subscribe(context.Background(), "sensors/+/+/smoke", 1, h); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_ = pub.Publish(ctx, "sensors/level0/A109/temperature", 1, []byte(`{}`))
	_ = pub.Publish(ctx, "sensors/level0/A109/smoke", 1, []byte(`{"x":1}`))

	m := waitMsg(t, ch)
	if m.topic != "sensors/level0/A109/smoke" {
		t.Fatalf("got %q, want the smoke topic first", m.topic)
	}
	select {
	case extra := <-ch:
		t.Fatalf("unexpected extra message on %q", extra.topic)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestPublishFailsFastWhenTheBrokerIsUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	// nothing listens on this port
	if _, err := Connect(ctx, "tcp://127.0.0.1:1", "orphan"); err == nil {
		t.Fatal("expected a connect error")
	}
}

func TestSubscriptionSurvivesAReconnect(t *testing.T) {
	url, stop := startBroker(t)
	defer stop()
	sub := connect(t, url, "detector")
	pub := connect(t, url, "gateway")

	ch, h := collect()
	if err := sub.Subscribe(context.Background(), "sensors/#", 1, h); err != nil {
		t.Fatal(err)
	}
	sub.dropForTest()

	// keep publishing until the re-established subscription delivers one
	deadline := time.After(5 * time.Second)
	for {
		_ = pub.Publish(context.Background(), "sensors/level0/A109/co", 1, []byte(`{}`))
		select {
		case <-ch:
			return
		case <-time.After(200 * time.Millisecond):
		case <-deadline:
			t.Fatal("subscription was not restored after reconnect")
		}
	}
}

func TestHeartbeatsKeepArrivingWithRisingSequenceUntilCancelled(t *testing.T) {
	url, stop := startBroker(t)
	defer stop()
	sub := connect(t, url, "watcher")
	pub := connect(t, url, "bridge")

	ch, h := collect()
	if err := sub.Subscribe(context.Background(), "heartbeat/#", 1, h); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		PublishHeartbeats(ctx, pub, "bridge", 20*time.Millisecond, func() float64 { return 7 })
		close(done)
	}()

	var last uint64
	for i := 0; i < 3; i++ {
		m := waitMsg(t, ch)
		if m.topic != "heartbeat/bridge" {
			t.Fatalf("topic = %q", m.topic)
		}
		var hb proto.Heartbeat
		if err := json.Unmarshal(m.payload, &hb); err != nil {
			t.Fatal(err)
		}
		if hb.Process != "bridge" || hb.SimTime != 7 || hb.Seq <= last {
			t.Fatalf("heartbeat = %+v after seq %d", hb, last)
		}
		last = hb.Seq
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("PublishHeartbeats did not stop after the context was cancelled")
	}
}

func TestHeartbeatSurvivesAnUnreachableBroker(t *testing.T) {
	url, stop := startBroker(t)
	pub := connect(t, url, "bridge")
	stop() // the broker dies; publishing must not panic or block the caller forever
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	PublishHeartbeats(ctx, pub, "bridge", 20*time.Millisecond, nil) // nil sim clock: reports 0
}

func TestPersistentSessionReceivesWhatWasPublishedWhileOffline(t *testing.T) {
	url, stop := startBroker(t)
	defer stop()
	pub := connect(t, url, "gateway")
	ch, h := collect()
	opts := Options{Persistent: true, Subscriptions: []Subscription{{Filter: "sensors/#", QoS: 1, Handler: h}}}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	db, err := ConnectWith(ctx, url, "db-writer", opts)
	if err != nil {
		t.Fatal(err)
	}
	waitSubscribed(t, pub, ch, "sensors/warmup")
	db.Close(context.Background()) // the db-writer goes down...

	for i := 0; i < 3; i++ { // ...and misses these
		if err := pub.Publish(context.Background(), "sensors/level0/A109/co", 1, []byte(`{"n":`+string(rune('0'+i))+`}`)); err != nil {
			t.Fatal(err)
		}
	}
	db2, err := ConnectWith(ctx, url, "db-writer", opts) // back, same identity
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close(context.Background())
	for i := 0; i < 3; i++ {
		if m := waitMsg(t, ch); m.topic != "sensors/level0/A109/co" {
			t.Fatalf("got %q", m.topic)
		}
	}
}

func TestCleanSessionDoesNotReceiveWhatItMissed(t *testing.T) {
	// the detector and actuator must act on the present, never on a backlog
	url, stop := startBroker(t)
	defer stop()
	pub := connect(t, url, "gateway")
	ch, h := collect()
	opts := Options{Subscriptions: []Subscription{{Filter: "sensors/#", QoS: 1, Handler: h}}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	det, err := ConnectWith(ctx, url, "detector", opts)
	if err != nil {
		t.Fatal(err)
	}
	waitSubscribed(t, pub, ch, "sensors/warmup")
	det.Close(context.Background())
	_ = pub.Publish(context.Background(), "sensors/level0/A109/co", 1, []byte(`{}`))
	det2, err := ConnectWith(ctx, url, "detector", opts)
	if err != nil {
		t.Fatal(err)
	}
	defer det2.Close(context.Background())
	select {
	case m := <-ch:
		t.Fatalf("a clean session received a message from while it was offline: %q", m.topic)
	case <-time.After(400 * time.Millisecond):
	}
}

// waitSubscribed publishes on topic until the subscription delivers it.
func waitSubscribed(t *testing.T, pub *Conn, ch chan msg, topic string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		_ = pub.Publish(context.Background(), topic, 1, []byte(`{}`))
		select {
		case <-ch:
			return
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatal("subscription never became active")
		}
	}
}

func TestTopicMatching(t *testing.T) {
	cases := []struct {
		filter, topic string
		want          bool
	}{
		{"sensors/#", "sensors/level0/A109/temperature", true},
		{"sensors/+/+/smoke", "sensors/level0/A109/smoke", true},
		{"sensors/+/+/smoke", "sensors/level0/A109/co", false},
		{"sensors/level0/#", "sensors/level1/A109/co", false},
		{"decisions/+", "decisions/level0", true},
		{"decisions/+", "decisions/level0/A109", false},
		{"#", "anything/at/all", true},
		{"a/b", "a/b", true},
		{"a/b", "a/b/c", false},
	}
	for _, c := range cases {
		if got := topicMatches(c.filter, c.topic); got != c.want {
			t.Errorf("topicMatches(%q,%q) = %v, want %v", c.filter, c.topic, got, c.want)
		}
	}
}
