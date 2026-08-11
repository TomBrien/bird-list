package app

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
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

	_, err := db.Exec(`INSERT INTO sightings (species, observed_at, location, region, notes) VALUES (?, ?, ?, ?, '')`, "Robin", time.Now().UTC(), "London", "uk")
	if err != nil {
		t.Fatalf("insert row: %v", err)
	}

	rec := httptest.NewRecorder()
	server.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("want status 200, got %d, body: %s", rec.Code, rec.Body.String())
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "1</strong> sightings") {
		t.Fatalf("expected lifetime count in response body")
	}
}

func TestSpeciesPageRequiresName(t *testing.T) {
	server, db := setupTestServer(t)
	defer db.Close()

	rec := httptest.NewRecorder()
	server.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/species", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("want status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Please provide a species name") {
		t.Fatalf("expected validation message")
	}
}
