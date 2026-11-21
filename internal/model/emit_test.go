package model

import (
	"testing"
	"time"
)

func TestEmitKeepsHeadlinesAndDropsOnlyProgress(t *testing.T) {
	events := make(chan Event, 1)
	EmitProgress(events, Event{Phase: "download", Completed: 1})
	EmitProgress(events, Event{Phase: "download", Completed: 2}) // renderer behind: dropped
	sent := make(chan struct{})
	go func() {
		Emit(events, Event{Phase: "installed"})
		close(sent)
	}()
	select {
	case <-sent:
		t.Fatal("Emit returned while the buffer was full")
	case <-time.After(20 * time.Millisecond):
	}
	if e := <-events; e.Completed != 1 || e.At.IsZero() {
		t.Fatalf("first event = %+v", e)
	}
	<-sent
	if e := <-events; e.Phase != "installed" {
		t.Fatalf("headline = %+v", e)
	}
}
