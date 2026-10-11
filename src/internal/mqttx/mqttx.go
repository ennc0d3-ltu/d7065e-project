// Package mqttx is the shared MQTT client (decision D1): auto-reconnect,
// subscriptions restored after every reconnect, QoS 1 publish that fails fast
// instead of queueing silently (a lost message must show as a sequence gap, H7).
package mqttx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

// Handler receives one message.
type Handler func(topic string, payload []byte)

type subscription struct {
	filter string
	qos    byte
	h      Handler
}

// Conn is one MQTT client connection.
type Conn struct {
	cm     *autopaho.ConnectionManager
	cancel context.CancelFunc

	mu   sync.Mutex
	subs []subscription
}

// Subscription is a handler registered when the connection is made.
type Subscription struct {
	Filter  string
	QoS     byte
	Handler Handler
}

// Options configure a connection.
type Options struct {
	// Persistent keeps the broker-side session (subscriptions and queued QoS 1
	// messages) for SessionExpiry while the client is away, so a consumer that
	// restarts receives what it missed. Only for consumers that need history
	// (db-writer, responder); the detector and the actuator use clean
	// sessions because they must act on the present, never on a backlog.
	Persistent    bool
	SessionExpiry time.Duration // default 1 h when Persistent
	// Subscriptions are registered before the first connection, so messages
	// the broker delivers the moment a persistent session resumes are not lost.
	Subscriptions []Subscription
}

// Connect dials brokerURL (for example tcp://127.0.0.1:1883) with a clean
// session and waits until the connection is up or ctx ends. The connection
// itself outlives ctx and reconnects on its own until Close.
func Connect(ctx context.Context, brokerURL, clientID string) (*Conn, error) {
	return ConnectWith(ctx, brokerURL, clientID, Options{})
}

// ConnectWith is Connect with options.
func ConnectWith(ctx context.Context, brokerURL, clientID string, opts Options) (*Conn, error) {
	u, err := url.Parse(brokerURL)
	if err != nil {
		return nil, err
	}
	c := &Conn{}
	for _, sub := range opts.Subscriptions {
		c.subs = append(c.subs, subscription{sub.Filter, sub.QoS, sub.Handler})
	}
	connCtx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	var expiry uint32
	if opts.Persistent {
		expiry = 3600
		if opts.SessionExpiry > 0 {
			expiry = uint32(opts.SessionExpiry / time.Second)
		}
	}
	cfg := autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{u},
		KeepAlive:                     10,
		CleanStartOnInitialConnection: !opts.Persistent,
		SessionExpiryInterval:         expiry,
		ConnectTimeout:                2 * time.Second,
		ReconnectBackoff:              func(int) time.Duration { return 250 * time.Millisecond },
		OnConnectionUp: func(*autopaho.ConnectionManager, *paho.Connack) {
			go c.resubscribe(connCtx)
		},
		ClientConfig: paho.ClientConfig{
			ClientID: clientID,
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(pr paho.PublishReceived) (bool, error) {
					c.dispatch(pr.Packet.Topic, pr.Packet.Payload)
					return true, nil
				},
			},
		},
	}
	cm, err := autopaho.NewConnection(connCtx, cfg)
	if err != nil {
		cancel()
		return nil, err
	}
	c.cm = cm
	if err := cm.AwaitConnection(ctx); err != nil {
		cancel()
		return nil, fmt.Errorf("mqtt connect %s: %w", brokerURL, err)
	}
	return c, nil
}

// Publish sends one message at the given QoS. It returns an error if the
// message could not be handed to the broker.
func (c *Conn) Publish(ctx context.Context, topic string, qos byte, payload []byte) error {
	_, err := c.cm.Publish(ctx, &paho.Publish{Topic: topic, QoS: qos, Payload: payload})
	return err
}

// Subscribe registers h for topics matching filter. The subscription is
// restored automatically after a reconnect.
func (c *Conn) Subscribe(ctx context.Context, filter string, qos byte, h Handler) error {
	c.mu.Lock()
	c.subs = append(c.subs, subscription{filter, qos, h})
	c.mu.Unlock()
	return c.subscribe(ctx, filter, qos)
}

func (c *Conn) subscribe(ctx context.Context, filter string, qos byte) error {
	ack, err := c.cm.Subscribe(ctx, &paho.Subscribe{
		Subscriptions: []paho.SubscribeOptions{{Topic: filter, QoS: qos}},
	})
	if err != nil {
		return err
	}
	for _, rc := range ack.Reasons {
		if rc >= 0x80 {
			return fmt.Errorf("mqtt subscribe %q refused (reason 0x%02x)", filter, rc)
		}
	}
	return nil
}

func (c *Conn) resubscribe(ctx context.Context) {
	c.mu.Lock()
	subs := append([]subscription(nil), c.subs...)
	c.mu.Unlock()
	for _, s := range subs {
		sctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_ = c.subscribe(sctx, s.filter, s.qos)
		cancel()
	}
}

func (c *Conn) dispatch(topic string, payload []byte) {
	c.mu.Lock()
	var hs []Handler
	for _, s := range c.subs {
		if topicMatches(s.filter, topic) {
			hs = append(hs, s.h)
		}
	}
	c.mu.Unlock()
	for _, h := range hs {
		h(topic, payload)
	}
}

// Close disconnects and stops reconnecting.
func (c *Conn) Close(ctx context.Context) {
	if c.cm != nil {
		_ = c.cm.Disconnect(ctx)
	}
	c.cancel()
}

// dropForTest severs the network connection so tests can check reconnect.
func (c *Conn) dropForTest() { c.cm.TerminateConnectionForTest() }

// topicMatches implements MQTT filter matching for "+" and "#".
func topicMatches(filter, topic string) bool {
	f, t := strings.Split(filter, "/"), strings.Split(topic, "/")
	for i, seg := range f {
		if seg == "#" {
			return true
		}
		if i >= len(t) {
			return false
		}
		if seg != "+" && seg != t[i] {
			return false
		}
	}
	return len(f) == len(t)
}

// ReadingSink publishes gateway readings to sensors/<level>/<room>/<type>
// (the gateway.Sink used in production).
type ReadingSink struct {
	conn publisher
	qos  byte
}

type publisher interface {
	Publish(ctx context.Context, topic string, qos byte, payload []byte) error
}

// NewReadingSink returns a sink publishing at the given QoS through conn (a
// *Conn, or a *Queueing so readings that cannot be sent go to the history replay).
func NewReadingSink(conn publisher, qos byte) *ReadingSink { return &ReadingSink{conn: conn, qos: qos} }

// Publish implements gateway.Sink.
func (s *ReadingSink) Publish(ctx context.Context, r gateway.Reading) error {
	payload, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.conn.Publish(ctx, "sensors/"+r.Room+"/"+r.Type, s.qos, payload)
}

var _ gateway.Sink = (*ReadingSink)(nil)

// PublishHeartbeats publishes a liveness message for process every `every`
// until ctx ends (call it in a goroutine). simTime reports the process's view
// of sim time and may be nil. A failed publish is ignored: the next beat is
// the retry, and a missing heartbeat is exactly the signal the dashboard and
// the actuator watch for.
func PublishHeartbeats(ctx context.Context, c *Conn, process string, every time.Duration, simTime func() float64) {
	t := time.NewTicker(every)
	defer t.Stop()
	var seq uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		seq++
		hb := proto.Heartbeat{Process: process, Seq: seq}
		if simTime != nil {
			hb.SimTime = simTime()
		}
		payload, err := json.Marshal(hb)
		if err != nil {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_ = c.Publish(pctx, proto.HeartbeatTopic(process), 0, payload)
		cancel()
	}
}
