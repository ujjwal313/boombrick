package library

import (
	"context"
	"testing"
	"time"
)

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"Track 2", "Track 10", -1},
		{"Track 10", "Track 2", 1},
		{"abc", "ABD", -1},
		{"Abc", "abc", 0},
		{"a", "ab", -1},
		{"007", "7", 0},
		{"Side B", "side a", 1},
		{"", "a", -1},
	} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestIndexOrders(t *testing.T) {
	albums := []Album{ // as Albums returns them: newest first
		{Artist: "odalys", Title: "Copper Season", Year: 2019, Tracks: []Track{{Title: "rust"}, {Title: "Ember"}}},
		{Artist: "Kenji Arata", Title: "Afterhours", Year: 2021, Tracks: []Track{{Title: "Neon 10"}}},
		{Artist: "odalys", Title: "Album 2", Year: 2015, Tracks: []Track{{Title: "Neon 2"}}},
	}
	ix := NewIndex(albums)
	titles := func(as []*Album) (s []string) {
		for _, a := range as {
			s = append(s, a.Title)
		}
		return s
	}
	if got := titles(ix.Albums); !equal(got, "Afterhours", "Album 2", "Copper Season") {
		t.Errorf("Albums = %v", got)
	}
	if got := titles(ix.Recent); !equal(got, "Copper Season", "Afterhours", "Album 2") {
		t.Errorf("Recent = %v", got)
	}
	if len(ix.Artists) != 2 || ix.Artists[0].Name != "Kenji Arata" || ix.Artists[1].Name != "odalys" {
		t.Fatalf("Artists = %+v", ix.Artists)
	}
	if a := ix.Artists[1]; a.Songs != 3 || !equal(titles(a.Albums), "Album 2", "Copper Season") {
		t.Errorf("odalys = %d songs, albums %v; want 3 and oldest first", a.Songs, titles(a.Albums))
	}
	var songs []string
	for _, s := range ix.Songs {
		songs = append(songs, s.Title)
	}
	if !equal(songs, "Ember", "Neon 2", "Neon 10", "rust") {
		t.Errorf("Songs = %v", songs)
	}
	if ix.Album("Kenji Arata", "Afterhours") != ix.Recent[1] || ix.Album("Nobody", "Nothing") != nil {
		t.Error("Album lookup is wrong")
	}
}

// Genres are sorted by name, one whatever its case, as first spelled; each
// holds its songs by artist, the artist's albums oldest first, then track.
func TestIndexGenres(t *testing.T) {
	song := func(artist, title string, genres ...string) Track {
		return Track{Artist: artist, Title: title, Duration: time.Minute, Genres: genres}
	}
	albums := []Album{ // as Albums returns them: newest first
		{Artist: "Odalys", Title: "Copper Season", Year: 2019, Tracks: []Track{song("Odalys", "Rust", "rock", "Pop"),
			song("Odalys", "Ember"), song("Odalys & Mira", "Tin Roof", "Rock")}},
		{Artist: "Kenji Arata", Title: "Afterhours", Year: 2021, Tracks: []Track{song("Kenji Arata", "Neon", "Jazz", "ROCK")}},
		{Artist: "Odalys", Title: "Northbound", Year: 2016, Tracks: []Track{song("Odalys", "Salt", "Rock")}},
	}
	ix := NewIndex(albums)
	var names []string
	for _, g := range ix.Genres {
		names = append(names, g.Name)
	}
	if !equal(names, "Jazz", "Pop", "ROCK") {
		t.Fatalf("Genres = %v", names)
	}
	rock := ix.Genres[2]
	var songs []string
	for _, s := range rock.Songs {
		songs = append(songs, s.Title)
	}
	// Artists counts who each song is by, as the genre's page lists them, not
	// the album artists.
	if !equal(songs, "Neon", "Salt", "Rust", "Tin Roof") || rock.Duration != 4*time.Minute || rock.Artists != 3 {
		t.Fatalf("Rock's songs %v, %v long, by %d artists", songs, rock.Duration, rock.Artists)
	}
	if rock.Songs[0] != &ix.Recent[1].Tracks[0] {
		t.Error("a genre's songs should point into the albums")
	}
	if len(NewIndex(albums[1:2:2]).Genres) != 2 || len(NewIndex(nil).Genres) != 0 {
		t.Error("genres of one album, or of none")
	}
}

func TestLoad(t *testing.T) {
	db := openDB(t)
	root := t.TempDir()
	touch(t, root, "A/X/01 One.mp3", "1")
	if _, err := Scan(context.Background(), db, root, formats); err != nil {
		t.Fatal(err)
	}
	ix, err := Load(db)
	if err != nil || len(ix.Albums) != 1 || len(ix.Songs) != 1 || ix.Songs[0] != &ix.Albums[0].Tracks[0] {
		t.Fatalf("Load = %+v, %v", ix, err)
	}
}

// The index finds the files imports downloaded by their Spotify IDs.
func TestIndexBySpotifyID(t *testing.T) {
	db := openDB(t)
	changes := []change{
		{"/m/Boombrick/Ed Sheeran/Sapphire/01 Sapphire.opus", 1, 1, Track{Title: "Sapphire", SpotifyID: "4Q0qVhFQa7j6jRKzo3HDmP"}},
		{"/m/Odalys/Copper Season/01 Rust.flac", 1, 1, Track{Title: "Rust"}},
	}
	if err := writeChanges(db, changes, nil, 1); err != nil {
		t.Fatal(err)
	}
	ix, err := Load(db)
	if err != nil {
		t.Fatal(err)
	}
	if tr := ix.Spotify("4Q0qVhFQa7j6jRKzo3HDmP"); tr == nil || tr.Title != "Sapphire" {
		t.Fatalf("Spotify = %+v", tr)
	}
	if ix.Spotify("") != nil || len(ix.spotify) != 1 {
		t.Fatalf("%d songs by Spotify ID", len(ix.spotify))
	}
}

func equal(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestIndexKeepsBooksApart(t *testing.T) {
	ix := NewIndex([]Album{
		{Artist: "Georges Simenon", Title: "La casa del giudice", Tracks: []Track{{Path: "/m/Audiolibri/La casa.m4b", Title: "La casa del giudice"}}},
		{Artist: "Abba", Title: "Gold", Tracks: []Track{{Path: "/m/Abba/01.m4a", Title: "Waterloo"}}},
		{Artist: "Mix", Title: "Mix", Tracks: []Track{{Path: "/m/mix/1.mp3"}, {Path: "/m/mix/2.M4B"}}},
	})
	if len(ix.Books) != 1 || ix.Books[0].Title != "La casa del giudice" {
		t.Fatalf("books: %v", ix.Books)
	}
	if len(ix.Albums) != 2 || len(ix.Songs) != 3 {
		t.Errorf("a book leaked into albums or songs: %d albums, %d songs", len(ix.Albums), len(ix.Songs))
	}
	if ix.Album("Georges Simenon", "La casa del giudice") == nil {
		t.Error("Continue listening can't find the book")
	}
	for _, a := range ix.Artists {
		if a.Name == "Georges Simenon" {
			t.Error("a book's author is listed as an artist")
		}
	}
}
