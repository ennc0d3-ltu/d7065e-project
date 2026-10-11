package physics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// PeopleSource returns the head-count per room ("<level>/<room>"); in the
// running system it reads BuildSim's occupancy, which the course occupancysim
// writes (D12).
type PeopleSource func(ctx context.Context) (map[string]int, error)

// PollPeople sets every room's head-count from src, so body heat follows the
// real occupancy. A room the source does not list is empty. A failed read
// keeps the last counts and returns 1: an unreachable source must not empty
// the building.
func (e *Engine) PollPeople(ctx context.Context, src PeopleSource) (errs int) {
	counts, err := src(ctx)
	if err != nil {
		return 1
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, rs := range e.cfg.Rooms {
		e.people[e.index[rs.Name]] = counts[rs.Name]
	}
	return 0
}

// AlignOccupancyClock sets the occupancy simulator at baseURL to run at
// `factor` simulated seconds per real second from `start` (HH:MM), so people
// and physics advance together. Both clocks then run side by side; they are
// not locked, so drift is measured, not assumed away (D5).
func AlignOccupancyClock(ctx context.Context, baseURL string, factor float64, start string) error {
	body, err := json.Marshal(map[string]any{"factor": factor, "time": start, "running": true})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/clock", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("occupancy clock refused: HTTP %d %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	return nil
}
