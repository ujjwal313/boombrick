package radio

import (
	"database/sql"
	"strings"
	"time"
)

// Saved lists the saved stations, the latest saved first.
func Saved(db *sql.DB) ([]Station, error) {
	rows, err := db.Query(`SELECT id, name, url, logo, country, country_code, codec, bitrate, tags
		FROM stations ORDER BY saved DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Station
	for rows.Next() {
		var s Station
		var tags string
		if err := rows.Scan(&s.ID, &s.Name, &s.URL, &s.Logo, &s.Country, &s.CountryCode, &s.Codec, &s.Bitrate, &tags); err != nil {
			return nil, err
		}
		if tags != "" {
			s.Tags = strings.Split(tags, ",")
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Save stores s as saved at at, replacing the station of the same ID.
func Save(db *sql.DB, s Station, at time.Time) error {
	_, err := db.Exec(`INSERT INTO stations (id, name, url, logo, country, country_code, codec, bitrate, tags, saved)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, url = excluded.url, logo = excluded.logo,
			country = excluded.country, country_code = excluded.country_code, codec = excluded.codec,
			bitrate = excluded.bitrate, tags = excluded.tags, saved = excluded.saved`,
		s.ID, s.Name, s.URL, s.Logo, s.Country, s.CountryCode, s.Codec, s.Bitrate, strings.Join(s.Tags, ","), at.UnixMilli())
	return err
}

// Remove deletes the saved station with the given ID.
func Remove(db *sql.DB, id string) error {
	_, err := db.Exec(`DELETE FROM stations WHERE id = ?`, id)
	return err
}
