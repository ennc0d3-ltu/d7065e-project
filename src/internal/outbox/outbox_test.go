package outbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func open(t *testing.T, path string, max int) *Outbox {
	t.Helper()
	o, err := Open(path, max)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func entry(topic string, droppable bool) Entry {
	return Entry{Topic: topic, Payload: []byte(`{"t":"` + topic + `"}`), Droppable: droppable}
}

func topics(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Topic)
	}
	return out
}

func collect(sent *[]Entry) func(Entry) error {
	return func(e Entry) error { *sent = append(*sent, e); return nil }
}

func TestDrainSendsEverythingInOrderAndEmptiesTheOutbox(t *testing.T) {
	o := open(t, filepath.Join(t.TempDir(), "ob.jsonl"), 100)
	for i := 0; i < 3; i++ {
		if err := o.Add(entry(fmt.Sprint(i), false)); err != nil {
			t.Fatal(err)
		}
	}
	var sent []Entry
	n, err := o.Drain(collect(&sent))
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got := topics(sent); fmt.Sprint(got) != "[0 1 2]" {
		t.Fatalf("order = %v", got)
	}
	if o.Len() != 0 {
		t.Fatalf("len = %d after a full drain", o.Len())
	}
}

func TestDrainStopsAtTheFirstFailureAndKeepsTheRest(t *testing.T) {
	o := open(t, filepath.Join(t.TempDir(), "ob.jsonl"), 100)
	for i := 0; i < 4; i++ {
		_ = o.Add(entry(fmt.Sprint(i), false))
	}
	calls := 0
	n, err := o.Drain(func(e Entry) error {
		calls++
		if calls == 3 {
			return errors.New("broker down again")
		}
		return nil
	})
	if err == nil || n != 2 {
		t.Fatalf("n=%d err=%v, want 2 sent and an error", n, err)
	}
	var sent []Entry
	_, _ = o.Drain(collect(&sent))
	if got := topics(sent); fmt.Sprint(got) != "[2 3]" {
		t.Fatalf("remaining = %v, want [2 3]: nothing lost, nothing sent twice", got)
	}
}

func TestEntriesSurviveAProcessCrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ob.jsonl")
	o := open(t, path, 100)
	_ = o.Add(entry("a", false))
	_ = o.Add(entry("b", true))
	_ = o.Close()

	reopened := open(t, path, 100)
	var sent []Entry
	_, _ = reopened.Drain(collect(&sent))
	if got := topics(sent); fmt.Sprint(got) != "[a b]" || !sent[1].Droppable {
		t.Fatalf("after reopen = %+v", sent)
	}
}

func TestDrainedEntriesAreNotResentAfterARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ob.jsonl")
	o := open(t, path, 100)
	_ = o.Add(entry("a", false))
	_ = o.Add(entry("b", false))
	_, _ = o.Drain(func(e Entry) error {
		if e.Topic == "b" {
			return errors.New("down")
		}
		return nil
	})
	_ = o.Close()
	var sent []Entry
	_, _ = open(t, path, 100).Drain(collect(&sent))
	if got := topics(sent); fmt.Sprint(got) != "[b]" {
		t.Fatalf("after restart = %v, want only [b]", got)
	}
}

func TestOverflowDropsTheOldestDroppableEntriesFirst(t *testing.T) {
	o := open(t, filepath.Join(t.TempDir(), "ob.jsonl"), 3)
	_ = o.Add(entry("keep1", false))
	_ = o.Add(entry("r1", true))
	_ = o.Add(entry("r2", true))
	_ = o.Add(entry("keep2", false)) // over the bound: r1 goes
	_ = o.Add(entry("r3", true))     // over again: r2 goes
	var sent []Entry
	_, _ = o.Drain(collect(&sent))
	if got := topics(sent); fmt.Sprint(got) != "[keep1 keep2 r3]" {
		t.Fatalf("kept = %v", got)
	}
	if o.Dropped() != 2 {
		t.Fatalf("dropped = %d, want 2", o.Dropped())
	}
}

func TestEntriesThatMustBeDeliveredAreNeverDropped(t *testing.T) {
	o := open(t, filepath.Join(t.TempDir(), "ob.jsonl"), 2)
	for i := 0; i < 5; i++ {
		_ = o.Add(entry(fmt.Sprint("dispatch", i), false))
	}
	if o.Len() != 5 || o.Dropped() != 0 {
		t.Fatalf("len=%d dropped=%d: a dispatch or decision must never be dropped", o.Len(), o.Dropped())
	}
}

func TestACorruptLineIsSkippedNotFatal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ob.jsonl")
	o := open(t, path, 100)
	_ = o.Add(entry("a", false))
	_ = o.Close()
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("{half a line\n")
	_ = f.Close()

	reopened := open(t, path, 100)
	if reopened.Len() != 1 || reopened.Corrupt() != 1 {
		t.Fatalf("len=%d corrupt=%d", reopened.Len(), reopened.Corrupt())
	}
}

func TestAddWhileDrainingIsSafeAndNothingIsLost(t *testing.T) {
	o := open(t, filepath.Join(t.TempDir(), "ob.jsonl"), 100000)
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = o.Add(entry(fmt.Sprint(i), false))
		}
	}()
	for i := 0; i < 50; i++ {
		_, _ = o.Drain(func(e Entry) error {
			mu.Lock()
			seen[e.Topic]++
			mu.Unlock()
			return nil
		})
	}
	wg.Wait()
	_, _ = o.Drain(func(e Entry) error { seen[e.Topic]++; return nil })
	if len(seen) != 500 {
		t.Fatalf("delivered %d distinct entries, want 500", len(seen))
	}
	for k, n := range seen {
		if n != 1 {
			t.Fatalf("entry %s delivered %d times", k, n)
		}
	}
}

func TestMemoryOnlyOutboxWorksWithoutAPath(t *testing.T) {
	o := open(t, "", 10)
	_ = o.Add(entry("a", false))
	var sent []Entry
	if n, err := o.Drain(collect(&sent)); n != 1 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
