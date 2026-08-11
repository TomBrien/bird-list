package data

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupTestRepo(t *testing.T) *Repository {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	repo := NewRepository(db)
	if err := repo.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	return repo
}

func addSighting(t *testing.T, repo *Repository, species string, observedAt time.Time, location, region string) {
	t.Helper()
	_, err := repo.db.Exec(`
INSERT INTO sightings (species, observed_at, location, region, notes)
VALUES (?, ?, ?, ?, '')
`, species, observedAt, location, region)
	if err != nil {
		t.Fatalf("insert sighting: %v", err)
	}
}

func TestCountSightingsWithFilters(t *testing.T) {
	repo := setupTestRepo(t)

	addSighting(t, repo, "Robin", time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC), "London", LocationUK)
	addSighting(t, repo, "Swan", time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), "Lake District", LocationUK)
	addSighting(t, repo, "Hoopoe", time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC), "Morocco", LocationWesternPalearctic)

	totalUK, err := repo.CountSightings(context.Background(), Filter{Location: LocationUK})
	if err != nil {
		t.Fatalf("count uk: %v", err)
	}
	if totalUK != 2 {
		t.Fatalf("want 2 UK sightings, got %d", totalUK)
	}

	total2025Global, err := repo.CountSightings(context.Background(), Filter{Year: 2025, Location: LocationGlobal})
	if err != nil {
		t.Fatalf("count global by year: %v", err)
	}
	if total2025Global != 2 {
		t.Fatalf("want 2 sightings in 2025, got %d", total2025Global)
	}
}

func TestSpeciesSightingsCaseInsensitive(t *testing.T) {
	repo := setupTestRepo(t)
	addSighting(t, repo, "Barn Owl", time.Now().UTC(), "Norfolk", LocationUK)

	results, err := repo.SpeciesSightings(context.Background(), "barn owl")
	if err != nil {
		t.Fatalf("species search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
}
