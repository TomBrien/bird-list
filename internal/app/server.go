package app

import (
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/TomBrien/bird-list/internal/data"
)

const defaultRecentLimit = 5

type Server struct {
	repo      *data.Repository
	templates *template.Template
}

type LocationOption struct {
	Value string
	Label string
}

type HomePageData struct {
	TotalCount       int
	SelectedYear     string
	SelectedLocation string
	LocationOptions  []LocationOption
	RecentAdditions  []data.RecentAddition
	Error            string
}

type SpeciesPageData struct {
	Species   string
	Sightings []data.Sighting
	Error     string
}

func NewServer(db *sql.DB) (*Server, error) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("failed to determine template path")
	}
	templateDir := filepath.Join(filepath.Dir(currentFile), "..", "..", "templates")

	tmpl, err := template.ParseFiles(
		filepath.Join(templateDir, "home.html"),
		filepath.Join(templateDir, "species.html"),
	)
	if err != nil {
		return nil, err
	}

	return &Server{
		repo:      data.NewRepository(db),
		templates: tmpl,
	}, nil
}

func (s *Server) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleHome)
	mux.HandleFunc("GET /species", s.handleSpecies)
	return mux
}

func (s *Server) EnsureSchema(r *http.Request) error {
	return s.repo.EnsureSchema(r.Context())
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	if err := s.EnsureSchema(r); err != nil {
		http.Error(w, fmt.Sprintf("failed to initialize schema: %v", err), http.StatusInternalServerError)
		return
	}

	location := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("location")))
	if location == "" {
		location = data.LocationUK
	}

	var year int
	yearText := strings.TrimSpace(r.URL.Query().Get("year"))
	if yearText != "" {
		parsedYear, err := strconv.Atoi(yearText)
		if err != nil {
			renderHome(w, s.templates, HomePageData{
				SelectedLocation: location,
				SelectedYear:     yearText,
				LocationOptions:  locationOptions(),
				Error:            "Year must be numeric.",
			})
			return
		}
		year = parsedYear
	}

	filter := data.Filter{Year: year, Location: location}
	total, err := s.repo.CountSightings(r.Context(), filter)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to load total sightings: %v", err), http.StatusInternalServerError)
		return
	}

	recent, err := s.repo.RecentAdditions(r.Context(), filter, defaultRecentLimit)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to load recent additions: %v", err), http.StatusInternalServerError)
		return
	}

	renderHome(w, s.templates, HomePageData{
		TotalCount:       total,
		SelectedYear:     yearText,
		SelectedLocation: location,
		LocationOptions:  locationOptions(),
		RecentAdditions:  recent,
	})
}

func (s *Server) handleSpecies(w http.ResponseWriter, r *http.Request) {
	if err := s.EnsureSchema(r); err != nil {
		http.Error(w, fmt.Sprintf("failed to initialize schema: %v", err), http.StatusInternalServerError)
		return
	}

	species := strings.TrimSpace(r.URL.Query().Get("name"))
	if species == "" {
		renderSpecies(w, s.templates, SpeciesPageData{Error: "Please provide a species name."})
		return
	}

	sightings, err := s.repo.SpeciesSightings(r.Context(), species)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to search sightings: %v", err), http.StatusInternalServerError)
		return
	}

	renderSpecies(w, s.templates, SpeciesPageData{Species: species, Sightings: sightings})
}

func locationOptions() []LocationOption {
	return []LocationOption{
		{Value: data.LocationUK, Label: "UK"},
		{Value: data.LocationGlobal, Label: "Global"},
		{Value: data.LocationWesternPalearctic, Label: "Western Palearctic"},
	}
}

func renderHome(w http.ResponseWriter, tmpl *template.Template, data HomePageData) {
	if err := tmpl.ExecuteTemplate(w, "home", data); err != nil {
		http.Error(w, fmt.Sprintf("failed to render home page: %v", err), http.StatusInternalServerError)
	}
}

func renderSpecies(w http.ResponseWriter, tmpl *template.Template, data SpeciesPageData) {
	if err := tmpl.ExecuteTemplate(w, "species", data); err != nil {
		http.Error(w, fmt.Sprintf("failed to render species page: %v", err), http.StatusInternalServerError)
	}
}
