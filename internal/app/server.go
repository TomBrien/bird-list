package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
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
	ImportMessage    string
	Error            string
}

type SpeciesPageData struct {
	Query             string
	Order             string
	Species           string
	ScientificName    string
	IsHistoricSpecies bool
	SpeciesList       []data.Species
	OffListSpecies    []data.Species
	Sightings         []data.Sighting
	Error             string
}

type SettingsPageData struct {
	ImportMessage  string
	Message        string
	Error          string
	CountMode      string
	IncludeOffList bool
}

type SightingPageData struct {
	Sighting data.Sighting
	MapURL   string
	Error    string
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
		filepath.Join(templateDir, "settings.html"),
		filepath.Join(templateDir, "sighting.html"),
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
	mux.HandleFunc("POST /import/bto", s.handleBTOImport)
	mux.HandleFunc("GET /species", s.handleSpecies)
	mux.HandleFunc("GET /api/species", s.handleSpeciesSuggestions)
	mux.HandleFunc("GET /settings", s.handleSettings)
	mux.HandleFunc("POST /settings/count-mode", s.handleCountMode)
	mux.HandleFunc("POST /settings/off-list", s.handleIncludeOffList)
	mux.HandleFunc("POST /settings/wipe", s.handleWipeSightings)
	mux.HandleFunc("GET /sightings/{id}", s.handleSighting)
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
	countMode, err := s.repo.CountMode(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to load count mode: %v", err), http.StatusInternalServerError)
		return
	}
	includeOffList, err := s.repo.IncludeOffList(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to load off-list setting: %v", err), http.StatusInternalServerError)
		return
	}
	total, err := s.repo.CountSightings(r.Context(), filter, countMode, includeOffList)
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
		ImportMessage:    importMessage(r.URL.Query().Get("imported")),
	})
}

func (s *Server) handleBTOImport(w http.ResponseWriter, r *http.Request) {
	if err := s.EnsureSchema(r); err != nil {
		http.Error(w, fmt.Sprintf("failed to initialize schema: %v", err), http.StatusInternalServerError)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		renderSettings(w, s.templates, SettingsPageData{Error: "Upload a valid .xlsx file no larger than 32 MB."})
		return
	}
	file, header, err := r.FormFile("records")
	if err != nil {
		renderSettings(w, s.templates, SettingsPageData{Error: "Choose a BTO .xlsx export to import."})
		return
	}
	defer file.Close()
	if !strings.HasSuffix(strings.ToLower(header.Filename), ".xlsx") {
		renderSettings(w, s.templates, SettingsPageData{Error: "Only .xlsx files are supported."})
		return
	}
	result, err := s.repo.ImportBTORecords(r.Context(), file)
	if err != nil {
		renderSettings(w, s.templates, SettingsPageData{Error: fmt.Sprintf("Import failed: %v", err)})
		return
	}
	http.Redirect(w, r, "/settings?imported="+strconv.Itoa(result.Imported), http.StatusSeeOther)
}

func (s *Server) handleSpecies(w http.ResponseWriter, r *http.Request) {
	if err := s.EnsureSchema(r); err != nil {
		http.Error(w, fmt.Sprintf("failed to initialize schema: %v", err), http.StatusInternalServerError)
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	species := strings.TrimSpace(r.URL.Query().Get("name"))
	order := strings.TrimSpace(r.URL.Query().Get("order"))
	if order != "taxonomic" && order != "recent" {
		order = "alphabetical"
	}
	speciesList, err := s.repo.AllSpecies(r.Context(), query, order)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to load species: %v", err), http.StatusInternalServerError)
		return
	}
	if species == "" {
		speciesList, offListSpecies := splitOffListSpecies(speciesList)
		renderSpecies(w, s.templates, SpeciesPageData{Query: query, Order: order, SpeciesList: speciesList, OffListSpecies: offListSpecies})
		return
	}

	sightings, err := s.repo.SpeciesSightings(r.Context(), species)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to search sightings: %v", err), http.StatusInternalServerError)
		return
	}

	scientificName := ""
	if len(sightings) > 0 {
		scientificName = sightings[0].ScientificName
	}
	renderSpecies(w, s.templates, SpeciesPageData{Query: query, Order: order, Species: species, ScientificName: scientificName, IsHistoricSpecies: len(sightings) > 0 && sightings[0].IsHistoricSpecies, SpeciesList: speciesList, Sightings: sightings})
}

func (s *Server) handleSpeciesSuggestions(w http.ResponseWriter, r *http.Request) {
	if err := s.EnsureSchema(r); err != nil {
		http.Error(w, fmt.Sprintf("failed to initialize schema: %v", err), http.StatusInternalServerError)
		return
	}
	species, err := s.repo.AllSpecies(r.Context(), r.URL.Query().Get("q"), "alphabetical")
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to load species suggestions: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(species); err != nil {
		http.Error(w, fmt.Sprintf("failed to encode species suggestions: %v", err), http.StatusInternalServerError)
	}
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if err := s.EnsureSchema(r); err != nil {
		http.Error(w, fmt.Sprintf("failed to initialize schema: %v", err), http.StatusInternalServerError)
		return
	}
	countMode, err := s.repo.CountMode(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to load count mode: %v", err), http.StatusInternalServerError)
		return
	}
	includeOffList, err := s.repo.IncludeOffList(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to load off-list setting: %v", err), http.StatusInternalServerError)
		return
	}
	renderSettings(w, s.templates, SettingsPageData{ImportMessage: importMessage(r.URL.Query().Get("imported")), Message: r.URL.Query().Get("message"), CountMode: countMode, IncludeOffList: includeOffList})
}

func (s *Server) handleCountMode(w http.ResponseWriter, r *http.Request) {
	if err := s.EnsureSchema(r); err != nil {
		http.Error(w, fmt.Sprintf("failed to initialize schema: %v", err), http.StatusInternalServerError)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "failed to read count mode", http.StatusBadRequest)
		return
	}
	if err := s.repo.SetCountMode(r.Context(), r.FormValue("count_mode")); err != nil {
		countMode, modeErr := s.repo.CountMode(r.Context())
		if modeErr != nil {
			http.Error(w, fmt.Sprintf("failed to load count mode: %v", modeErr), http.StatusInternalServerError)
			return
		}
		renderSettings(w, s.templates, SettingsPageData{CountMode: countMode, Error: "Choose either modern or historic species count."})
		return
	}
	http.Redirect(w, r, "/settings?message=Species+count+mode+updated.", http.StatusSeeOther)
}

func (s *Server) handleIncludeOffList(w http.ResponseWriter, r *http.Request) {
	if err := s.EnsureSchema(r); err != nil {
		http.Error(w, fmt.Sprintf("failed to initialize schema: %v", err), http.StatusInternalServerError)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "failed to read off-list setting", http.StatusBadRequest)
		return
	}
	if err := s.repo.SetIncludeOffList(r.Context(), r.FormValue("include_off_list") == "true"); err != nil {
		http.Error(w, fmt.Sprintf("failed to save off-list setting: %v", err), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings?message=Off-list+count+setting+updated.", http.StatusSeeOther)
}

func splitOffListSpecies(species []data.Species) ([]data.Species, []data.Species) {
	onList := make([]data.Species, 0, len(species))
	offList := make([]data.Species, 0)
	for _, entry := range species {
		if entry.IsOffList {
			offList = append(offList, entry)
		} else {
			onList = append(onList, entry)
		}
	}
	return onList, offList
}

func (s *Server) handleWipeSightings(w http.ResponseWriter, r *http.Request) {
	if err := s.EnsureSchema(r); err != nil {
		http.Error(w, fmt.Sprintf("failed to initialize schema: %v", err), http.StatusInternalServerError)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "failed to read wipe request", http.StatusBadRequest)
		return
	}
	if r.FormValue("confirmation") != "DELETE" {
		renderSettings(w, s.templates, SettingsPageData{Error: `Type DELETE to confirm that all sightings should be removed.`})
		return
	}
	if err := s.repo.WipeSightings(r.Context()); err != nil {
		http.Error(w, fmt.Sprintf("failed to wipe sightings: %v", err), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings?message=All+sightings+were+deleted.", http.StatusSeeOther)
}

func (s *Server) handleSighting(w http.ResponseWriter, r *http.Request) {
	if err := s.EnsureSchema(r); err != nil {
		http.Error(w, fmt.Sprintf("failed to initialize schema: %v", err), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	sighting, err := s.repo.SightingByID(r.Context(), id)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to load sighting: %v", err), http.StatusInternalServerError)
		return
	}
	mapURL := ""
	if sighting.Latitude.Valid && sighting.Longitude.Valid {
		latitude := sighting.Latitude.Float64
		longitude := sighting.Longitude.Float64
		latitudeDelta := 0.0018
		longitudeDelta := 0.5 / (111.32 * math.Cos(latitude*math.Pi/180))
		mapURL = fmt.Sprintf("https://www.openstreetmap.org/export/embed.html?bbox=%.6f%%2C%.6f%%2C%.6f%%2C%.6f&layer=mapnik&marker=%.6f%%2C%.6f", longitude-longitudeDelta, latitude-latitudeDelta, longitude+longitudeDelta, latitude+latitudeDelta, latitude, longitude)
	}
	renderSighting(w, s.templates, SightingPageData{Sighting: sighting, MapURL: mapURL})
}

func importMessage(imported string) string {
	count, err := strconv.Atoi(imported)
	if err != nil || count <= 0 {
		return ""
	}
	return fmt.Sprintf("Imported %d BTO record(s).", count)
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

func renderSettings(w http.ResponseWriter, tmpl *template.Template, data SettingsPageData) {
	if err := tmpl.ExecuteTemplate(w, "settings", data); err != nil {
		http.Error(w, fmt.Sprintf("failed to render settings page: %v", err), http.StatusInternalServerError)
	}
}

func renderSighting(w http.ResponseWriter, tmpl *template.Template, data SightingPageData) {
	if err := tmpl.ExecuteTemplate(w, "sighting", data); err != nil {
		http.Error(w, fmt.Sprintf("failed to render sighting page: %v", err), http.StatusInternalServerError)
	}
}
