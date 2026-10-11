// Package buildsim is the shared HTTP client for BuildSim's REST API
// (docs: course repo buildingsim/docs/api). Every call has a timeout;
// retries and circuit breaking (NFR7) wrap this client, not live in it.
package buildsim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// ErrNotFound is returned for HTTP 404 (for example a sensor value written
// before its equipment is registered).
var ErrNotFound = errors.New("buildsim: not found")

// StatusError is returned for any other non-2xx response.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("buildsim: HTTP %d: %s", e.Code, e.Body)
}

// Sensor, Actuator and Equipment mirror BuildSim's JSON.
type Sensor struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	DataType string `json:"data_type"`
	Unit     string `json:"unit,omitempty"`
	Value    string `json:"value,omitempty"`
}

type Actuator struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	State string `json:"state,omitempty"`
}

type Equipment struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Type      string     `json:"type"`
	Category  string     `json:"category"`
	Level     string     `json:"level"`
	Room      string     `json:"room"`
	Status    string     `json:"status"`
	Sensors   []Sensor   `json:"sensors,omitempty"`
	Actuators []Actuator `json:"actuators,omitempty"`
}

// BulkResult is the response of POST /api/equipment/bulk.
type BulkResult struct {
	Created int `json:"created"`
	Skipped int `json:"skipped"`
	Total   int `json:"total"`
	Version int `json:"version"`
}

// Client talks to one BuildSim instance.
type Client struct {
	base string
	http *http.Client
}

// New returns a Client for baseURL (for example http://127.0.0.1:9090).
func New(baseURL string, timeout time.Duration) *Client {
	return &Client{base: baseURL, http: &http.Client{Timeout: timeout}}
}

// PutSensorValue writes one text reading to a sensor.
func (c *Client) PutSensorValue(ctx context.Context, id, value string) error {
	body := map[string]string{"data_type": "text", "value": value}
	return c.do(ctx, http.MethodPut, "/api/sensors/"+url.PathEscape(id)+"/value", body, nil)
}

// GetActuatorState reads the state an actuator has reached.
func (c *Client) GetActuatorState(ctx context.Context, id string) (string, error) {
	var a Actuator
	if err := c.do(ctx, http.MethodGet, "/api/actuators/"+url.PathEscape(id), nil, &a); err != nil {
		return "", err
	}
	return a.State, nil
}

// PutActuatorState writes the state an actuator process has reached.
func (c *Client) PutActuatorState(ctx context.Context, id, state string) error {
	body := map[string]string{"state": state}
	return c.do(ctx, http.MethodPut, "/api/actuators/"+url.PathEscape(id)+"/state", body, nil)
}

// BulkCreate registers equipment with its sensors and actuators; existing
// IDs are skipped, so it is safe to call on every restart (NFR4).
func (c *Client) BulkCreate(ctx context.Context, eq []Equipment) (BulkResult, error) {
	var res BulkResult
	err := c.do(ctx, http.MethodPost, "/api/equipment/bulk", eq, &res)
	return res, err
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var rdr io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &StatusError{Code: resp.StatusCode, Body: string(bytes.TrimSpace(b))}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
