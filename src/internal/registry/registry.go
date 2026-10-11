// Package registry stores versioned detector bundles (thresholds, optional
// model, evaluation report) and says which one is active. Publishing never
// activates: promotion names a human approver (decision D15). The detector
// never depends on the registry to run; it polls, validates, caches and keeps
// its last known good bundle (see detectsvc.Loader).
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/detector"
)

// Status is where a bundle is in its life.
type Status string

const (
	Candidate Status = "candidate"
	Active    Status = "active"
	Retired   Status = "retired"
)

// Bundle is one deployable version of the detector's tunable parts.
type Bundle struct {
	Version    int
	Status     Status
	Thresholds detector.Thresholds
	Model      json.RawMessage `json:",omitempty"` // detector.Model JSON; empty = rules only
	Report     json.RawMessage `json:",omitempty"` // evaluation report that justified it
	Note       string
	Author     string
	Created    time.Time
	PromotedBy string `json:",omitempty"`
	Promoted   time.Time
}

// Validate is the gate every bundle passes on publish and again in the
// detector before it is applied.
func (b Bundle) Validate() error {
	if err := detector.ValidateThresholds(b.Thresholds); err != nil {
		return err
	}
	if len(b.Model) > 0 {
		var m detector.Model
		if err := json.Unmarshal(b.Model, &m); err != nil {
			return fmt.Errorf("model does not load: %w", err)
		}
	}
	return nil
}

type state struct {
	Bundles []Bundle
	History []int // versions in the order they were activated
}

// Store is a file-backed registry.
type Store struct {
	mu   sync.Mutex
	path string
	st   state
}

// OpenStore loads the registry from path, or starts empty.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.st); err != nil {
		return nil, fmt.Errorf("registry: %s is corrupt: %w", path, err)
	}
	return s, nil
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.st, "", " ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ErrNotFound is returned for an unknown version.
var ErrNotFound = errors.New("registry: no such version")

// Publish validates and stores a bundle as a candidate.
func (s *Store) Publish(b Bundle) (int, error) {
	if err := b.Validate(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b.Version = len(s.st.Bundles) + 1
	b.Status, b.Created, b.PromotedBy, b.Promoted = Candidate, time.Now().UTC(), "", time.Time{}
	s.st.Bundles = append(s.st.Bundles, b)
	if err := s.save(); err != nil {
		s.st.Bundles = s.st.Bundles[:len(s.st.Bundles)-1]
		return 0, err
	}
	return b.Version, nil
}

// Get returns one version.
func (s *Store) Get(v int) (Bundle, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v < 1 || v > len(s.st.Bundles) {
		return Bundle{}, false
	}
	return s.st.Bundles[v-1], true
}

// List returns every version, oldest first.
func (s *Store) List() []Bundle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Bundle{}, s.st.Bundles...)
}

// Active returns the active bundle, if any.
func (s *Store) Active() (Bundle, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active()
}

func (s *Store) active() (Bundle, bool) {
	for _, b := range s.st.Bundles {
		if b.Status == Active {
			return b, true
		}
	}
	return Bundle{}, false
}

func (s *Store) activate(v int, by string) error {
	for i := range s.st.Bundles {
		if s.st.Bundles[i].Status == Active {
			s.st.Bundles[i].Status = Retired
		}
	}
	b := &s.st.Bundles[v-1]
	b.Status, b.PromotedBy, b.Promoted = Active, by, time.Now().UTC()
	s.st.History = append(s.st.History, v)
	return s.save()
}

// Promote makes a version active. A named approver is required.
func (s *Store) Promote(v int, by string) error {
	if by == "" {
		return errors.New("registry: promotion needs an approver")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v < 1 || v > len(s.st.Bundles) {
		return ErrNotFound
	}
	if err := s.st.Bundles[v-1].Validate(); err != nil {
		return err
	}
	return s.activate(v, by)
}

// Rollback re-activates the bundle that was active before the current one.
func (s *Store) Rollback(by string) error {
	if by == "" {
		return errors.New("registry: rollback needs an approver")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.st.History
	if len(h) < 2 {
		return errors.New("registry: nothing to roll back to")
	}
	prev := h[len(h)-2]
	if err := s.activate(prev, by); err != nil {
		return err
	}
	return nil
}
