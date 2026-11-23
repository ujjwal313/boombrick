package radio

import (
	"database/sql"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Stations is the saved stations, kept in memory for the screens and
// written to the store in the order they change, on a goroutine of its own.
type Stations struct {
	log     *slog.Logger
	mu      sync.Mutex
	list    []Station // newest first
	pending []func(*sql.DB) error
	closed  bool
	version atomic.Uint64
	ready   chan struct{}
	wake    chan struct{}
	done    chan struct{}
}

// NewStations loads the saved stations from db and starts writing changes
// back to it. Close it when done.
func NewStations(db *sql.DB, log *slog.Logger) (*Stations, error) {
	return newStations(db, log, func(db *sql.DB, w func(*sql.DB) error) error { return w(db) })
}

// newStations is NewStations with run making each write, so a test can hold
// the writes up.
func newStations(db *sql.DB, log *slog.Logger, run func(*sql.DB, func(*sql.DB) error) error) (*Stations, error) {
	list, err := Saved(db)
	if err != nil {
		return nil, err
	}
	s := &Stations{log: log, list: list, ready: make(chan struct{}, 1),
		wake: make(chan struct{}, 1), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		for range s.wake {
			s.mu.Lock()
			batch, closed := s.pending, s.closed
			s.pending = nil
			s.mu.Unlock()
			for _, w := range batch {
				if err := run(db, w); err != nil {
					s.log.Warn("saving stations", "err", err)
				}
			}
			if closed {
				return
			}
		}
	}()
	return s, nil
}

// List returns the saved stations, the latest saved first. The slice is a
// copy, but the stations' tags are shared, so callers mustn't change them.
func (s *Stations) List() []Station {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.list)
}

// Has reports whether the station with the given ID is saved.
func (s *Stations) Has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.index(id) >= 0
}

// Toggle saves st at the top of the list if it isn't saved, and removes it
// if it is. It reports whether st is saved now. The store is written later,
// in the order of the changes, so Toggle never waits on it. After Close the
// list still changes but the write is dropped.
func (s *Stations) Toggle(st Station) (saved bool) {
	s.mu.Lock()
	var write func(*sql.DB) error
	if i := s.index(st.ID); i >= 0 {
		s.list = slices.Delete(s.list, i, i+1)
		write = func(db *sql.DB) error { return Remove(db, st.ID) }
	} else {
		s.list = slices.Insert(s.list, 0, st)
		saved = true
		at := time.Now()
		write = func(db *sql.DB) error { return Save(db, st, at) }
	}
	closed := s.closed
	if !closed {
		s.pending = append(s.pending, write)
	}
	s.mu.Unlock()
	if closed {
		s.log.Warn("station change after close, not saved", "id", st.ID)
	} else {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	s.signal()
	return saved
}

// Version changes whenever a station is saved or removed, so a screen asks
// for the list again only when it has changed.
func (s *Stations) Version() uint64 { return s.version.Load() }

// Ready receives a value when the list has changed.
func (s *Stations) Ready() <-chan struct{} { return s.ready }

// Close waits for the writes still to be made. It is safe to call twice.
func (s *Stations) Close() {
	s.mu.Lock()
	first := !s.closed
	s.closed = true
	s.mu.Unlock()
	if first {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	<-s.done
}

func (s *Stations) index(id string) int {
	return slices.IndexFunc(s.list, func(st Station) bool { return st.ID == id })
}

func (s *Stations) signal() {
	s.version.Add(1)
	select {
	case s.ready <- struct{}{}:
	default:
	}
}
