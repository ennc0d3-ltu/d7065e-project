package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// MultiSink sends every reading down every path (MQTT for the many consumers,
// plus a direct path to the actuator that does not depend on the broker, D6).
// One working path is enough: it fails only if all of them do.
type MultiSink struct{ sinks []Sink }

// NewMultiSink combines sinks.
func NewMultiSink(sinks ...Sink) *MultiSink { return &MultiSink{sinks: sinks} }

// Publish implements Sink. Every path is tried even after one fails.
func (m *MultiSink) Publish(ctx context.Context, r Reading) error {
	var errs []error
	for _, s := range m.sinks {
		if err := s.Publish(ctx, r); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == len(m.sinks) && len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// HTTPSink posts each reading to a consumer's /readings endpoint, carrying
// the sim-time timestamp that BuildSim's sensor values do not.
type HTTPSink struct {
	base string
	http *http.Client
}

// NewHTTPSink returns a sink for the consumer at baseURL.
func NewHTTPSink(baseURL string) *HTTPSink {
	return &HTTPSink{base: baseURL, http: &http.Client{Timeout: time.Second}}
}

// Publish implements Sink.
func (h *HTTPSink) Publish(ctx context.Context, r Reading) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.base+"/readings", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("gateway: %s answered HTTP %d", h.base, resp.StatusCode)
	}
	return nil
}
