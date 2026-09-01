package app

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupTestServer(t *testing.T) (*Server, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	server, err := NewServer(db)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := server.EnsureSchema(req); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	return server, db
}

func TestHomePageShowsLifetimeCount(t *testing.T) {
	server, db := setupTestServer(t)
	defer db.Close()

	_, err := db.Exec(`INSERT INTO sightings (species, observed_at, location, region, notes, taxonomy_rank) VALUES (?, ?, ?, ?, '', ?)`, "Robin", time.Now().UTC(), "London", "uk", 1)
	if err != nil {
		t.Fatalf("insert row: %v", err)
	}

	rec := httptest.NewRecorder()
	server.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("want status 200, got %d, body: %s", rec.Code, rec.Body.String())
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), `class="stat">1</div>`) {
		t.Fatalf("expected lifetime count in response body")
	}
}

func TestSpeciesPageShowsSpeciesListWithoutName(t *testing.T) {
	server, db := setupTestServer(t)
	defer db.Close()

	rec := httptest.NewRecorder()
	server.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/species", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("want status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "All species") {
		t.Fatalf("expected species list heading")
	}
}

func TestSpeciesPagePersistsSelectedOrder(t *testing.T) {
	server, db := setupTestServer(t)
	defer db.Close()

	rec := httptest.NewRecorder()
	server.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/species?order=taxonomic", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want status 200, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	server.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/species", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `name="order" value="taxonomic" checked`) {
		t.Fatalf("expected persisted taxonomic order")
	}
}

func TestVisitsPageRenders(t *testing.T) {
	server, db := setupTestServer(t)
	defer db.Close()

	rec := httptest.NewRecorder()
	server.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/visits", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<h1>Visits</h1>") {
		t.Fatalf("expected visits page heading")
	}
}

func TestStatsPageRendersCumulativeGraph(t *testing.T) {
	server, db := setupTestServer(t)
	defer db.Close()

	_, err := db.Exec(`
INSERT INTO sightings (species, observed_at, location, region, notes, taxonomy_rank)
VALUES
	('Robin', '2025-01-01', 'Cardiff', 'uk', '', 1),
	('Swan', '2025-01-03', 'Cardiff', 'uk', '', 1)`)
	if err != nil {
		t.Fatalf("insert sightings: %v", err)
	}

	rec := httptest.NewRecorder()
	server.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<h1>Stats</h1>") || !strings.Contains(body, `id="species-chart"`) || !strings.Contains(body, `id="chart-reset"`) || !strings.Contains(body, "pointerdown") || !strings.Contains(body, "2025-01-01:1,2025-01-02:1,2025-01-03:2,") || !strings.Contains(body, "<h2>Recent species additions</h2>") || !strings.Contains(body, "Swan</strong><span class=\"muted\"> - 2025-01-03, Cardiff") {
		t.Fatalf("expected cumulative species graph, got: %s", body)
	}
}

func TestVisitDetailAndSightingLink(t *testing.T) {
	server, db := setupTestServer(t)
	defer db.Close()

	result, err := db.Exec(`
INSERT INTO sightings (species, observed_at, location, region, notes, taxonomy_rank)
VALUES (?, ?, ?, ?, '', ?)`, "Robin", time.Date(2025, 4, 1, 9, 0, 0, 0, time.Local), "Bute Park", "uk", 1)
	if err != nil {
		t.Fatalf("insert sighting: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("get sighting ID: %v", err)
	}

	rec := httptest.NewRecorder()
	server.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/visits/2025-04-01?location=Bute+Park", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Robin") {
		t.Fatalf("expected visit detail, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sightings/"+strconv.FormatInt(id, 10), nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `/visits/2025-04-01?location=Bute`) {
		t.Fatalf("expected sighting visit link, got %d: %s", rec.Code, rec.Body.String())
	}
}
