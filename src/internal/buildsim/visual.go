package buildsim

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// Person is one occupant in BuildSim's occupancy map.
type Person struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Icon     string    `json:"icon,omitempty"`
	Position []float64 `json:"position,omitempty"`
}

// RoomOccupancy is who is in one room.
type RoomOccupancy struct {
	Persons []Person `json:"persons"`
	Aliens  []Person `json:"aliens"`
}

// GetOccupancy reads the global occupancy, keyed "<level>/<room>". Read-only:
// the course occupancysim is the single writer (D12).
func (c *Client) GetOccupancy(ctx context.Context) (map[string]RoomOccupancy, error) {
	var occ map[string]RoomOccupancy
	err := c.do(ctx, http.MethodGet, "/api/occupancy", nil, &occ)
	return occ, err
}

// Layer is a room heatmap in the viewer (visualization.md).
type Layer struct {
	ID      string             `json:"id"`
	Label   string             `json:"label"`
	Unit    string             `json:"unit,omitempty"`
	Source  string             `json:"source,omitempty"`
	Minimum float64            `json:"minimum"`
	Maximum float64            `json:"maximum"`
	Opacity float64            `json:"opacity,omitempty"`
	Palette []string           `json:"palette"`
	Values  map[string]float64 `json:"values"`
}

// Effect is a rendering instruction (fire, smoke, sprinkler, water, warning).
type Effect struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Label     string    `json:"label,omitempty"`
	Level     string    `json:"level,omitempty"`
	Room      string    `json:"room,omitempty"`
	Position  []float64 `json:"position,omitempty"`
	Radius    float64   `json:"radius,omitempty"`
	Height    float64   `json:"height,omitempty"`
	Intensity float64   `json:"intensity,omitempty"`
}

// Alert is a current decision card in the viewer. BuildSim caps them at 100.
type Alert struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // info, warning or critical
	Title    string `json:"title"`
	Message  string `json:"message,omitempty"`
	Level    string `json:"level,omitempty"`
	Room     string `json:"room,omitempty"`
}

// Each PUT replaces the whole collection, so exactly one process (the
// dashboard) must own each of these endpoints. An empty slice is sent as [],
// never null, so it clears the collection.

// PutRoomLayers replaces the room heatmaps.
func (c *Client) PutRoomLayers(ctx context.Context, layers []Layer) error {
	if layers == nil {
		layers = []Layer{}
	}
	return c.do(ctx, http.MethodPut, "/api/room-layers", layers, nil)
}

// PutEffects replaces the visual effects.
func (c *Client) PutEffects(ctx context.Context, effects []Effect) error {
	if effects == nil {
		effects = []Effect{}
	}
	return c.do(ctx, http.MethodPut, "/api/effects", effects, nil)
}

// PutAlerts replaces the current alerts.
func (c *Client) PutAlerts(ctx context.Context, alerts []Alert) error {
	if alerts == nil {
		alerts = []Alert{}
	}
	return c.do(ctx, http.MethodPut, "/api/alerts", alerts, nil)
}

// RoutePoint is one waypoint of a computed route.
type RoutePoint struct {
	RoomID int     `json:"room_id"`
	Name   string  `json:"name"`
	Level  string  `json:"level"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
}

// Route is a shortest walkable path.
type Route struct {
	Path     []RoutePoint `json:"path"`
	Distance float64      `json:"distance"`
}

// Route computes the walkable route between two named rooms, qualified by
// level because room names repeat between floors. A missing room or no
// connection is ErrNotFound. Locked, jammed or blocked doors are already
// excluded by BuildSim (D7).
func (c *Client) Route(ctx context.Context, fromLevel, fromName, toLevel, toName string) (Route, error) {
	q := url.Values{
		"from_name": {fromName}, "from_level": {fromLevel},
		"to_name": {toName}, "to_level": {toLevel},
		"type": {"walkable"},
	}
	var r Route
	err := c.do(ctx, http.MethodGet, "/api/graph/route?"+q.Encode(), nil, &r)
	return r, err
}

// KeepRegistered registers eq now and then every `every` until ctx ends, so a
// BuildSim restart (all its state is in memory) is repaired within one
// interval (NFR4). Registration skips ids that exist, so repeating it is safe.
// onResult may be nil; it sees each attempt's outcome.
func KeepRegistered(ctx context.Context, c *Client, eq []Equipment, every time.Duration, onResult func(BulkResult, error)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		res, err := c.BulkCreate(ctx, eq)
		if onResult != nil {
			onResult(res, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Entity is a person (or robot) BuildSim draws. Only the course occupancysim
// writes them; we read them to see who is walking where.
type Entity struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Level    string    `json:"level"`
	Room     string    `json:"room,omitempty"` // empty while walking
	Status   string    `json:"status"`
	Position []float64 `json:"position,omitempty"`
}

// GetEntities reads every entity. Read-only.
func (c *Client) GetEntities(ctx context.Context) ([]Entity, error) {
	var out []Entity
	err := c.do(ctx, http.MethodGet, "/api/entities", nil, &out)
	return out, err
}

// Door is a BuildSim door: a runtime object that adds identity, state and lock
// state to an architectural opening. A locked or jammed door (or one marked
// blocked) removes its entry node from walkable routes; a closed but unlocked
// door stays routable. PUT /api/doors replaces the whole collection, so one
// process must own it (the door actuator).
type Door struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Kind        string `json:"kind"` // door, entrance or fire_exit
	Level       string `json:"level"`
	Room        string `json:"room"`
	EntryNodeID int    `json:"entry_node_id,omitempty"`
	State       string `json:"state"`      // open, closed or blocked
	LockState   string `json:"lock_state"` // unlocked, locked or jammed
}

// PutDoors replaces every door. An empty slice is sent as [] (not null).
func (c *Client) PutDoors(ctx context.Context, doors []Door) error {
	if doors == nil {
		doors = []Door{}
	}
	return c.do(ctx, http.MethodPut, "/api/doors", doors, nil)
}

// GetDoors reads every door; BuildSim answers null when there are none.
func (c *Client) GetDoors(ctx context.Context) ([]Door, error) {
	var out []Door
	err := c.do(ctx, http.MethodGet, "/api/doors", nil, &out)
	return out, err
}
