// Package outbox is a small store-and-forward queue for messages a process
// could not send (the broker was down). It lives on local disk, so it survives
// a process crash or restart, and is drained in order when the broker is back.
//
// Rules (decision D13): an entry marked Droppable (a reading) may be shed when
// the outbox is full, oldest first; any other entry (a decision, a dispatch, a
// dead letter) is never dropped. What goes in here is history and messages for
// people, never control: sprinkler commands and heartbeats are not queued,
// because replaying them after an outage would act on a stale picture.
package outbox

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Entry is one queued message.
type Entry struct {
	Topic     string          `json:"topic"`
	Payload   json.RawMessage `json:"payload"`
	Droppable bool            `json:"droppable,omitempty"`
	ID        uint64          `json:"id"`
}

// Outbox is safe for concurrent use. An empty path keeps it in memory only.
type Outbox struct {
	path string
	max  int

	drainMu sync.Mutex // one drain at a time

	mu      sync.Mutex
	entries []Entry
	nextID  uint64
	file    *os.File
	dropped uint64
	corrupt int
}

// Open loads any entries left from a previous run of the process.
func Open(path string, limit int) (*Outbox, error) {
	o := &Outbox{path: path, max: limit, nextID: 1}
	if path == "" {
		return o, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			var e Entry
			if json.Unmarshal(sc.Bytes(), &e) != nil || e.Topic == "" {
				o.corrupt++ // a half-written last line after a crash
				continue
			}
			o.entries = append(o.entries, e)
			o.nextID = max(o.nextID, e.ID+1)
		}
		_ = f.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// rewrite cleanly (drops corrupt lines), then append from here on
	if err := o.rewriteLocked(); err != nil {
		return nil, err
	}
	return o, nil
}

// Add queues e. Entries that must be delivered are synced to disk before Add
// returns; readings are not, trading a few readings in a power cut for speed.
func (o *Outbox) Add(e Entry) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	e.ID = o.nextID
	o.nextID++
	o.entries = append(o.entries, e)
	if o.file != nil {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err := o.file.Write(append(line, '\n')); err != nil {
			return err
		}
		if !e.Droppable {
			if err := o.file.Sync(); err != nil {
				return err
			}
		}
	}
	if o.shedLocked() {
		return o.rewriteLocked()
	}
	return nil
}

// shedLocked drops the oldest droppable entries while over the bound.
func (o *Outbox) shedLocked() (changed bool) {
	for o.max > 0 && len(o.entries) > o.max {
		i := -1
		for j, e := range o.entries {
			if e.Droppable {
				i = j
				break
			}
		}
		if i < 0 {
			return changed // only must-deliver entries left: keep them all
		}
		o.entries = append(o.entries[:i], o.entries[i+1:]...)
		o.dropped++
		changed = true
	}
	return changed
}

// Drain sends queued entries in order until one fails, removes what was sent,
// and reports how many went out. A failed entry and everything after it stay.
func (o *Outbox) Drain(send func(Entry) error) (int, error) {
	o.drainMu.Lock()
	defer o.drainMu.Unlock()

	o.mu.Lock()
	batch := append([]Entry(nil), o.entries...)
	o.mu.Unlock()

	sent := map[uint64]bool{}
	var sendErr error
	for _, e := range batch {
		if err := send(e); err != nil {
			sendErr = err
			break
		}
		sent[e.ID] = true
	}
	if len(sent) == 0 {
		return 0, sendErr
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	keep := o.entries[:0]
	for _, e := range o.entries {
		if !sent[e.ID] {
			keep = append(keep, e)
		}
	}
	o.entries = keep
	if err := o.rewriteLocked(); err != nil && sendErr == nil {
		sendErr = err
	}
	return len(sent), sendErr
}

// rewriteLocked replaces the file with the current entries (atomic rename),
// then reopens it for appending.
func (o *Outbox) rewriteLocked() error {
	if o.path == "" {
		return nil
	}
	if o.file != nil {
		_ = o.file.Close()
		o.file = nil
	}
	tmp := o.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, e := range o.entries {
		line, err := json.Marshal(e)
		if err != nil {
			_ = f.Close()
			return err
		}
		_, _ = w.Write(append(line, '\n'))
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	_ = f.Close()
	if err := os.Rename(tmp, o.path); err != nil {
		return err
	}
	o.file, err = os.OpenFile(o.path, os.O_APPEND|os.O_WRONLY, 0o644)
	return err
}

// Len is the number of queued entries.
func (o *Outbox) Len() int { o.mu.Lock(); defer o.mu.Unlock(); return len(o.entries) }

// Dropped is how many readings were shed on overflow.
func (o *Outbox) Dropped() uint64 { o.mu.Lock(); defer o.mu.Unlock(); return o.dropped }

// Corrupt is how many unreadable lines were skipped when the file was loaded.
func (o *Outbox) Corrupt() int { o.mu.Lock(); defer o.mu.Unlock(); return o.corrupt }

// Close closes the file.
func (o *Outbox) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.file != nil {
		err := o.file.Close()
		o.file = nil
		return err
	}
	return nil
}
