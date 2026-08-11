package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	LocationUK                = "uk"
	LocationGlobal            = "global"
	LocationWesternPalearctic = "western_palearctic"
)

type Filter struct {
	Year     int
	Location string
}

type RecentAddition struct {
	Species         string
	FirstObservedAt time.Time
	Location        string
}

type Sighting struct {
	ID         int64
	Species    string
	ObservedAt time.Time
	Location   string
	Region     string
	Notes      string
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) EnsureSchema(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS sightings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    species TEXT NOT NULL,
    observed_at DATETIME NOT NULL,
    location TEXT NOT NULL,
    region TEXT NOT NULL,
    notes TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_sightings_species ON sightings(species);
CREATE INDEX IF NOT EXISTS idx_sightings_observed_at ON sightings(observed_at);
CREATE INDEX IF NOT EXISTS idx_sightings_region ON sightings(region);
`
	_, err := r.db.ExecContext(ctx, schema)
	return err
}

func (r *Repository) CountSightings(ctx context.Context, filter Filter) (int, error) {
	query := "SELECT COUNT(*) FROM sightings WHERE 1=1"
	args := make([]any, 0, 2)
	query, args = applyLocationFilter(query, args, filter.Location)
	query, args = applyYearFilter(query, args, filter.Year)

	var total int
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func (r *Repository) RecentAdditions(ctx context.Context, filter Filter, limit int) ([]RecentAddition, error) {
	if limit <= 0 {
		return nil, errors.New("limit must be positive")
	}

	query := `
SELECT species, first_observed_at, first_location
FROM (
    SELECT species,
           MIN(observed_at) AS first_observed_at,
           (
             SELECT s2.location
             FROM sightings s2
             WHERE s2.species = s.species
             ORDER BY s2.observed_at ASC, s2.id ASC
             LIMIT 1
           ) AS first_location
    FROM sightings s
    WHERE 1=1`
	args := make([]any, 0, 4)
	query, args = applyLocationFilter(query, args, filter.Location)
	query, args = applyYearFilter(query, args, filter.Year)
	query += " GROUP BY species) ORDER BY first_observed_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]RecentAddition, 0, limit)
	for rows.Next() {
		var item RecentAddition
		var observedRaw string
		if err := rows.Scan(&item.Species, &observedRaw, &item.Location); err != nil {
			return nil, err
		}
		observedAt, err := parseObservedAt(observedRaw)
		if err != nil {
			return nil, err
		}
		item.FirstObservedAt = observedAt
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *Repository) SpeciesSightings(ctx context.Context, species string) ([]Sighting, error) {
	species = strings.TrimSpace(species)
	if species == "" {
		return []Sighting{}, nil
	}

	rows, err := r.db.QueryContext(ctx, `
SELECT id, species, observed_at, location, region, notes
FROM sightings
WHERE LOWER(species) = LOWER(?)
ORDER BY observed_at DESC, id DESC`, species)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]Sighting, 0)
	for rows.Next() {
		var s Sighting
		if err := rows.Scan(&s.ID, &s.Species, &s.ObservedAt, &s.Location, &s.Region, &s.Notes); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

func applyLocationFilter(query string, args []any, location string) (string, []any) {
	switch strings.ToLower(strings.TrimSpace(location)) {
	case "", LocationUK:
		query += " AND region = ?"
		args = append(args, LocationUK)
	case LocationGlobal:
		// No region filter.
	case LocationWesternPalearctic:
		query += " AND region = ?"
		args = append(args, LocationWesternPalearctic)
	default:
		query += " AND region = ?"
		args = append(args, LocationUK)
	}
	return query, args
}

func applyYearFilter(query string, args []any, year int) (string, []any) {
	if year <= 0 {
		return query, args
	}
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)
	query += " AND observed_at >= ? AND observed_at < ?"
	args = append(args, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
	return query, args
}

func parseObservedAt(raw string) (time.Time, error) {
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unable to parse observed_at %q", raw)
}
