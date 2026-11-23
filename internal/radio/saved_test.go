package radio

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/store"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "boombrick.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st.DB
}

func TestSavedSameMillisecondOrdersByName(t *testing.T) {
	db := openDB(t)
	at := time.Unix(1700000000, 0)
	for _, s := range []Station{{ID: "u1", Name: "Zeta"}, {ID: "u2", Name: "Alpha"}, {ID: "u3", Name: "Mid"}} {
		if err := Save(db, s, at); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Saved(db)
	if err != nil || len(got) != 3 || got[0].Name != "Alpha" || got[1].Name != "Mid" || got[2].Name != "Zeta" {
		t.Fatalf("saved %+v, %v; want Alpha, Mid, Zeta", got, err)
	}
}

func TestResaveUpdatesFields(t *testing.T) {
	db := openDB(t)
	at := time.Unix(1700000000, 0)
	if err := Save(db, Station{ID: "u1", Name: "Old", URL: "http://old", Codec: "MP3", Bitrate: 64, Tags: []string{"a"}}, at); err != nil {
		t.Fatal(err)
	}
	want := Station{ID: "u1", Name: "New", URL: "http://new", Logo: "https://l", Country: "India", CountryCode: "IN", Codec: "AAC", Bitrate: 128, Tags: []string{"b", "c"}}
	if err := Save(db, want, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err := Saved(db)
	if err != nil || !reflect.DeepEqual(got, []Station{want}) {
		t.Fatalf("saved %+v, %v; want %+v", got, err, want)
	}
}

func TestSavedEmpty(t *testing.T) {
	got, err := Saved(openDB(t))
	if err != nil || len(got) != 0 {
		t.Fatalf("saved %+v, %v; want an empty list", got, err)
	}
}

func TestSavedStations(t *testing.T) {
	db := openDB(t)
	a := Station{ID: "u1", Name: "Radio X", URL: "http://s/x", Logo: "https://x/logo.png", Country: "India", CountryCode: "IN",
		Codec: "MP3", Bitrate: 128, Tags: []string{"news", "hindi"}}
	b := Station{ID: "u2", Name: "Radio Y", URL: "http://s/y", Country: "Norway", CountryCode: "NO", Codec: "AAC+", Bitrate: 64}
	at := time.Unix(1700000000, 0)
	for _, s := range []struct {
		st Station
		at time.Time
	}{{a, at}, {b, at.Add(time.Second)}} {
		if err := Save(db, s.st, s.at); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Saved(db)
	if err != nil || !reflect.DeepEqual(got, []Station{b, a}) {
		t.Fatalf("saved %+v, %v; want %+v then %+v", got, err, b, a)
	}
	if err := Save(db, a, at.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, err = Saved(db); err != nil || len(got) != 2 || got[0].ID != "u1" {
		t.Fatalf("saved again %+v, %v; want u1 first", got, err)
	}
	if err := Remove(db, "u2"); err != nil {
		t.Fatal(err)
	}
	if got, err = Saved(db); err != nil || !reflect.DeepEqual(got, []Station{a}) {
		t.Fatalf("after remove %+v, %v", got, err)
	}
}
