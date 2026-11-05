package events

import (
	"encoding/json"
	"testing"
)

func TestPublishSubscribe(t *testing.T) {
	b := NewBus(10)
	ch, cancel := b.Subscribe(4, 0)
	defer cancel()
	b.Publish("x", map[string]int{"n": 1})
	ev := <-ch
	if ev.ID != 1 || ev.Type != "x" {
		t.Fatalf("got %+v", ev)
	}
	var v map[string]int
	if err := json.Unmarshal(ev.Data, &v); err != nil || v["n"] != 1 {
		t.Fatalf("data %s err %v", ev.Data, err)
	}
}

func TestReplaySinceID(t *testing.T) {
	b := NewBus(3)
	for i := 0; i < 5; i++ {
		b.Publish("x", i)
	}
	ch, cancel := b.Subscribe(1, 3)
	defer cancel()
	got := []uint64{(<-ch).ID, (<-ch).ID}
	if got[0] != 4 || got[1] != 5 {
		t.Fatalf("replay = %v", got)
	}
}

func TestSlowSubscriberIsDropped(t *testing.T) {
	b := NewBus(0)
	ch, cancel := b.Subscribe(1, 0)
	defer cancel()
	b.Publish("x", 1)
	b.Publish("x", 2) // buffer full: subscriber dropped
	if _, ok := <-ch; !ok {
		t.Fatal("first event should still be readable")
	}
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after overflow")
	}
	cancel() // idempotent after drop
}
