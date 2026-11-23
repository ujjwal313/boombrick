package radio

import (
	"database/sql"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestToggleTwiceLeavesStationUnsaved(t *testing.T) {
	db := openDB(t)
	s, err := NewStations(db, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	st := Station{ID: "u1", Name: "Radio X", URL: "http://s/x"}
	if !s.Toggle(st) {
		t.Fatal("first Toggle should save")
	}
	if !s.Has("u1") {
		t.Fatal("station should be saved in memory")
	}
	if s.Toggle(st) {
		t.Fatal("second Toggle should remove")
	}
	if s.Has("u1") || len(s.List()) != 0 {
		t.Fatalf("station still in memory: %+v", s.List())
	}
	s.Close()
	got, err := Saved(db)
	if err != nil || len(got) != 0 {
		t.Fatalf("store has %+v, %v; want nothing", got, err)
	}
}

func TestToggleSignals(t *testing.T) {
	s, err := NewStations(openDB(t), quietLog())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	before := s.Version()
	s.Toggle(Station{ID: "u1", Name: "Radio X"})
	if s.Version() == before {
		t.Fatal("Toggle didn't change the version")
	}
	select {
	case <-s.Ready():
	case <-time.After(time.Second):
		t.Fatal("Toggle didn't signal Ready")
	}
}

func TestListIsNewestFirstAndACopy(t *testing.T) {
	db := openDB(t)
	s, err := NewStations(db, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	a := Station{ID: "a", Name: "A"}
	b := Station{ID: "b", Name: "B"}
	s.Toggle(a)
	s.Toggle(b)
	got := s.List()
	if !reflect.DeepEqual(got, []Station{b, a}) {
		t.Fatalf("list %+v, want b then a", got)
	}
	got[0].Name = "changed"
	if s.List()[0].Name != "B" {
		t.Fatal("List returned the stations' own slice")
	}
	s.Close()
	saved, err := Saved(db)
	if err != nil || len(saved) != 2 {
		t.Fatalf("store has %+v, %v", saved, err)
	}

	again, err := NewStations(db, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if !again.Has("a") || !again.Has("b") {
		t.Fatalf("reloaded list %+v", again.List())
	}
}

func TestToggleDoesNotWaitOnSlowStore(t *testing.T) {
	db := openDB(t)
	gate := make(chan struct{})
	s, err := newStations(db, quietLog(), func(db *sql.DB, w func(*sql.DB) error) error {
		<-gate
		return w(db)
	})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for i := 0; i < 40; i++ {
			s.Toggle(Station{ID: "u1", Name: "Radio X"})
		}
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("Toggle waited on the store")
	}
	close(gate)
	s.Close()
	// 40 toggles of one station leave it unsaved, with the writes in order.
	if got, err := Saved(db); err != nil || len(got) != 0 {
		t.Fatalf("store has %+v, %v; want nothing", got, err)
	}
}

func TestToggleAfterCloseKeepsListAndDoesNotPanic(t *testing.T) {
	db := openDB(t)
	s, err := NewStations(db, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s.Close()
	if !s.Toggle(Station{ID: "u1", Name: "Radio X"}) || !s.Has("u1") {
		t.Fatal("list should still change after Close")
	}
	if got, err := Saved(db); err != nil || len(got) != 0 {
		t.Fatalf("store has %+v, %v; the write should be dropped", got, err)
	}
}
