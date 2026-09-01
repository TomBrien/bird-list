package data

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xuri/excelize/v2"
)

const (
	LocationUK                = "uk"
	LocationGlobal            = "global"
	LocationWesternPalearctic = "western_palearctic"
	CountModeModern           = "modern"
	CountModeHistoric         = "historic"
)

type Filter struct {
	Year     int
	Location string
}

type RecentAddition struct {
	ID              int64
	Species         string
	FirstObservedAt time.Time
	Location        string
}

type SpeciesCountPoint struct {
	Date  time.Time
	Count int
}

type Sighting struct {
	ID                int64
	Species           string
	ObservedAt        time.Time
	Location          string
	Region            string
	Notes             string
	Count             string
	ScientificName    string
	IsHistoricSpecies bool
	Latitude          sql.NullFloat64
	Longitude         sql.NullFloat64
	GridReference     string
	BTOFields         []BTOField
}

type BTOField struct {
	Label string
	Value string
}

type VisitSpecies struct {
	Name  string
	Count string
}

type Visit struct {
	Date      time.Time
	Location  string
	StartTime time.Time
	EndTime   string
	Weather   string
	Latitude  sql.NullFloat64
	Longitude sql.NullFloat64
	Species   []VisitSpecies
}

type Species struct {
	Name                 string
	Count                int
	TaxonomicRank        int
	ParentScientificName string
	IsSubspecies         bool
	IsOffList            bool
	IsSynthetic          bool
	Children             []Species
	mostRecent           string
}

type Repository struct {
	db                *sql.DB
	taxonomy          map[string]taxon
	taxonomyOnce      sync.Once
	taxonomyUpdateErr error
}

type taxon struct {
	rank                 int
	parentScientificName string
	isSubspecies         bool
	isHistoricSpecies    bool
	englishName          string
}

//go:embed british_list.json
var britishListTaxonomy []byte

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db, taxonomy: loadTaxonomy()}
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
    count TEXT NOT NULL DEFAULT '1',
    scientific_name TEXT NOT NULL DEFAULT '',
    latitude REAL,
    longitude REAL,
    grid_reference TEXT NOT NULL DEFAULT '',
    bto_data TEXT NOT NULL DEFAULT '{}',
    bto_species_code INTEGER NOT NULL DEFAULT 0,
    taxonomy_rank INTEGER NOT NULL DEFAULT 0,
    parent_scientific_name TEXT NOT NULL DEFAULT '',
    is_subspecies INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_sightings_species ON sightings(species);
CREATE INDEX IF NOT EXISTS idx_sightings_observed_at ON sightings(observed_at);
CREATE INDEX IF NOT EXISTS idx_sightings_region ON sightings(region);
`
	if _, err := r.db.ExecContext(ctx, schema); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS preferences (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
)`); err != nil {
		return err
	}

	for column, definition := range map[string]string{
		"count":                  "TEXT NOT NULL DEFAULT '1'",
		"scientific_name":        "TEXT NOT NULL DEFAULT ''",
		"latitude":               "REAL",
		"longitude":              "REAL",
		"grid_reference":         "TEXT NOT NULL DEFAULT ''",
		"bto_data":               "TEXT NOT NULL DEFAULT '{}'",
		"bto_species_code":       "INTEGER NOT NULL DEFAULT 0",
		"taxonomy_rank":          "INTEGER NOT NULL DEFAULT 0",
		"parent_scientific_name": "TEXT NOT NULL DEFAULT ''",
		"is_subspecies":          "INTEGER NOT NULL DEFAULT 0",
		"is_historic_species":    "INTEGER NOT NULL DEFAULT 0",
	} {
		if err := r.ensureColumn(ctx, column, definition); err != nil {
			return err
		}
	}
	if _, err := r.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_sightings_import_identity ON sightings(species, observed_at, location)`); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, `
INSERT INTO preferences (key, value)
VALUES ('count_mode', ?)
ON CONFLICT(key) DO NOTHING`, CountModeModern); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, `
INSERT INTO preferences (key, value)
VALUES ('include_off_list', 'false')
ON CONFLICT(key) DO NOTHING`); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, `
INSERT INTO preferences (key, value)
VALUES ('species_order', 'alphabetical')
ON CONFLICT(key) DO NOTHING`); err != nil {
		return err
	}
	r.taxonomyOnce.Do(func() {
		r.taxonomyUpdateErr = r.backfillTaxonomyRanks(ctx)
	})
	return r.taxonomyUpdateErr
}

func (r *Repository) ensureColumn(ctx context.Context, column, definition string) error {
	var exists int
	if err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM pragma_table_info('sightings')
WHERE name = ?`, column).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		_, err := r.db.ExecContext(ctx, "ALTER TABLE sightings ADD COLUMN "+column+" "+definition)
		return err
	}
	return nil
}

func (r *Repository) CountSightings(ctx context.Context, filter Filter, countMode string, includeOffList bool) (int, error) {
	condition := `
(is_subspecies = 0 OR (
    is_subspecies = 1
    AND is_historic_species = 0
    AND parent_scientific_name <> ''
    AND LOWER(scientific_name) NOT LIKE '% f. domestica'
))`
	if countMode == CountModeHistoric {
		condition = `
(is_subspecies = 0 OR (
    is_subspecies = 1
    AND parent_scientific_name <> ''
    AND LOWER(scientific_name) NOT LIKE '% f. domestica'
))`
	}
	query := `
SELECT COUNT(DISTINCT CASE
    WHEN is_subspecies = 1
        AND is_historic_species = 0
        AND parent_scientific_name <> ''
        THEN LOWER(parent_scientific_name)
    WHEN scientific_name <> '' THEN LOWER(scientific_name)
    ELSE species
END)
FROM sightings
WHERE ` + condition
	if !includeOffList {
		query += " AND taxonomy_rank > 0"
	}
	args := make([]any, 0, 2)
	query, args = applyLocationFilter(query, args, filter.Location)
	query, args = applyYearFilter(query, args, filter.Year)

	var total int
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func (r *Repository) CumulativeSpeciesCounts(ctx context.Context, filter Filter, countMode string, includeOffList bool) ([]SpeciesCountPoint, error) {
	condition := `
(is_subspecies = 0 OR (
    is_subspecies = 1
    AND is_historic_species = 0
    AND parent_scientific_name <> ''
    AND LOWER(scientific_name) NOT LIKE '% f. domestica'
))`
	if countMode == CountModeHistoric {
		condition = `
(is_subspecies = 0 OR (
    is_subspecies = 1
    AND parent_scientific_name <> ''
    AND LOWER(scientific_name) NOT LIKE '% f. domestica'
))`
	}
	query := `
SELECT CASE
    WHEN is_subspecies = 1
        AND is_historic_species = 0
        AND parent_scientific_name <> ''
        THEN LOWER(parent_scientific_name)
    WHEN scientific_name <> '' THEN LOWER(scientific_name)
    ELSE species
END, observed_at
FROM sightings
WHERE ` + condition
	if !includeOffList {
		query += " AND taxonomy_rank > 0"
	}
	args := make([]any, 0, 2)
	query, args = applyLocationFilter(query, args, filter.Location)
	query, args = applyYearFilter(query, args, filter.Year)
	query += " ORDER BY observed_at ASC, id ASC"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	firstSeen := make(map[string]time.Time)
	for rows.Next() {
		var speciesKey, observedRaw string
		if err := rows.Scan(&speciesKey, &observedRaw); err != nil {
			return nil, err
		}
		if _, exists := firstSeen[speciesKey]; exists {
			continue
		}
		observedAt, err := parseObservedAt(observedRaw)
		if err != nil {
			return nil, err
		}
		firstSeen[speciesKey] = observedAt
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(firstSeen) == 0 {
		return []SpeciesCountPoint{}, nil
	}

	newSpeciesByDate := make(map[string]int)
	var firstDate, lastDate time.Time
	for _, observedAt := range firstSeen {
		date := time.Date(observedAt.Year(), observedAt.Month(), observedAt.Day(), 0, 0, 0, 0, observedAt.Location())
		dateKey := date.Format("2006-01-02")
		newSpeciesByDate[dateKey]++
		if firstDate.IsZero() || date.Before(firstDate) {
			firstDate = date
		}
		if lastDate.IsZero() || date.After(lastDate) {
			lastDate = date
		}
	}

	points := make([]SpeciesCountPoint, 0, int(lastDate.Sub(firstDate).Hours()/24)+1)
	total := 0
	for date := firstDate; !date.After(lastDate); date = date.AddDate(0, 0, 1) {
		total += newSpeciesByDate[date.Format("2006-01-02")]
		points = append(points, SpeciesCountPoint{Date: date, Count: total})
	}
	return points, nil
}

func (r *Repository) IncludeOffList(ctx context.Context) (bool, error) {
	var value string
	if err := r.db.QueryRowContext(ctx, `SELECT value FROM preferences WHERE key = 'include_off_list'`).Scan(&value); err != nil {
		return false, err
	}
	return value == "true", nil
}

func (r *Repository) SetIncludeOffList(ctx context.Context, includeOffList bool) error {
	value := "false"
	if includeOffList {
		value = "true"
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO preferences (key, value)
VALUES ('include_off_list', ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, value)
	return err
}

func (r *Repository) CountMode(ctx context.Context) (string, error) {
	var countMode string
	if err := r.db.QueryRowContext(ctx, `SELECT value FROM preferences WHERE key = 'count_mode'`).Scan(&countMode); err != nil {
		return "", err
	}
	if countMode != CountModeHistoric {
		return CountModeModern, nil
	}
	return countMode, nil
}

func (r *Repository) SetCountMode(ctx context.Context, countMode string) error {
	if countMode != CountModeModern && countMode != CountModeHistoric {
		return fmt.Errorf("unsupported count mode %q", countMode)
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO preferences (key, value)
VALUES ('count_mode', ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, countMode)
	return err
}

func (r *Repository) SpeciesOrder(ctx context.Context) (string, error) {
	var order string
	if err := r.db.QueryRowContext(ctx, `SELECT value FROM preferences WHERE key = 'species_order'`).Scan(&order); err != nil {
		return "", err
	}
	if !isSpeciesOrder(order) {
		return "alphabetical", nil
	}
	return order, nil
}

func (r *Repository) SetSpeciesOrder(ctx context.Context, order string) error {
	if !isSpeciesOrder(order) {
		return fmt.Errorf("unsupported species order %q", order)
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO preferences (key, value)
VALUES ('species_order', ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, order)
	return err
}

func isSpeciesOrder(order string) bool {
	return order == "alphabetical" || order == "taxonomic" || order == "recent"
}

func (r *Repository) RecentAdditions(ctx context.Context, filter Filter, limit int) ([]RecentAddition, error) {
	if limit <= 0 {
		return nil, errors.New("limit must be positive")
	}

	query := `
SELECT first_id, species, first_observed_at, first_location
FROM (
    SELECT s.species,
           (
             SELECT s2.id
             FROM sightings s2
             WHERE s2.species = s.species
             ORDER BY s2.observed_at ASC, s2.id ASC
             LIMIT 1
           ) AS first_id,
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
		if err := rows.Scan(&item.ID, &item.Species, &observedRaw, &item.Location); err != nil {
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
SELECT id, species, observed_at, location, region, notes, count, scientific_name, is_historic_species, latitude, longitude, grid_reference, bto_data
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
		var btoData string
		if err := rows.Scan(&s.ID, &s.Species, &s.ObservedAt, &s.Location, &s.Region, &s.Notes, &s.Count, &s.ScientificName, &s.IsHistoricSpecies, &s.Latitude, &s.Longitude, &s.GridReference, &btoData); err != nil {
			return nil, err
		}
		s.BTOFields = decodeBTOFields(btoData)
		result = append(result, s)
	}
	return result, rows.Err()
}

func (r *Repository) Visits(ctx context.Context) ([]Visit, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT species, count, observed_at, location, notes, latitude, longitude, bto_data
FROM sightings
ORDER BY observed_at DESC, location COLLATE NOCASE, species COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	visitsByKey := make(map[string]*Visit)
	visitKeys := make([]string, 0)
	speciesCounts := make(map[string]map[string][]string)
	for rows.Next() {
		var species, count, location, notes, btoData string
		var observedAt time.Time
		var latitude, longitude sql.NullFloat64
		if err := rows.Scan(&species, &count, &observedAt, &location, &notes, &latitude, &longitude, &btoData); err != nil {
			return nil, err
		}
		key := observedAt.Format("2006-01-02") + "\x00" + location
		visit := visitsByKey[key]
		if visit == nil {
			visit = &Visit{Date: observedAt, Location: location, StartTime: observedAt}
			visitsByKey[key] = visit
			visitKeys = append(visitKeys, key)
			speciesCounts[key] = make(map[string][]string)
		}
		if observedAt.Before(visit.StartTime) {
			visit.StartTime = observedAt
		}
		if !visit.Latitude.Valid && latitude.Valid && longitude.Valid {
			visit.Latitude, visit.Longitude = latitude, longitude
		}
		if endTime := visitEndTime(observedAt, notes); endTime != "" && endTime > visit.EndTime {
			visit.EndTime = endTime
		}
		for _, field := range decodeBTOFields(btoData) {
			if strings.Contains(strings.ToLower(field.Label), "weather") && field.Value != "" && !containsVisitValue(visit.Weather, field.Value) {
				if visit.Weather != "" {
					visit.Weather += "; "
				}
				visit.Weather += field.Value
			}
		}
		speciesCounts[key][species] = append(speciesCounts[key][species], count)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	visits := make([]Visit, 0, len(visitKeys))
	for _, key := range visitKeys {
		visit := visitsByKey[key]
		for species, counts := range speciesCounts[key] {
			visit.Species = append(visit.Species, VisitSpecies{Name: species, Count: strings.Join(counts, ", ")})
		}
		sort.Slice(visit.Species, func(i, j int) bool {
			return strings.ToLower(visit.Species[i].Name) < strings.ToLower(visit.Species[j].Name)
		})
		visits = append(visits, *visit)
	}
	return visits, nil
}

func visitEndTime(observedAt time.Time, notes string) string {
	const prefix = "End time: "
	if !strings.HasPrefix(notes, prefix) {
		return ""
	}
	endTime, err := time.Parse("15:04", strings.TrimPrefix(notes, prefix))
	if err != nil {
		return ""
	}
	return time.Date(observedAt.Year(), observedAt.Month(), observedAt.Day(), endTime.Hour(), endTime.Minute(), 0, 0, observedAt.Location()).Format("15:04")
}

func containsVisitValue(values, value string) bool {
	for _, existing := range strings.Split(values, "; ") {
		if existing == value {
			return true
		}
	}
	return false
}

func (r *Repository) AllSpecies(ctx context.Context, query, order string) ([]Species, error) {
	query = strings.TrimSpace(query)
	orderBy := "CASE WHEN taxonomic_rank = 0 THEN 1 ELSE 0 END, species COLLATE NOCASE"
	switch order {
	case "taxonomic":
		orderBy = "CASE WHEN taxonomic_rank = 0 THEN 1 ELSE 0 END, taxonomic_rank, species COLLATE NOCASE"
	case "recent":
		orderBy = "most_recent DESC, species COLLATE NOCASE"
	}
	rows, err := r.db.QueryContext(ctx, `
	SELECT species, scientific_name, parent_scientific_name, is_subspecies, COUNT(*) AS record_count, MIN(taxonomy_rank) AS taxonomic_rank, MAX(observed_at) AS most_recent
	FROM sightings
	WHERE ? = '' OR LOWER(species) LIKE '%' || LOWER(?) || '%'
	GROUP BY species, scientific_name, parent_scientific_name, is_subspecies
	ORDER BY `+orderBy, query, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	species := make([]Species, 0)
	byScientificName := make(map[string]int)
	for rows.Next() {
		var entry Species
		var scientificName string
		if err := rows.Scan(&entry.Name, &scientificName, &entry.ParentScientificName, &entry.IsSubspecies, &entry.Count, &entry.TaxonomicRank, &entry.mostRecent); err != nil {
			return nil, err
		}
		entry.IsOffList = entry.TaxonomicRank == 0
		entry.Children = []Species{}
		species = append(species, entry)
		byScientificName[normalizedTaxonName(scientificName)] = len(species) - 1
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	isChild := make([]bool, len(species))
	for index := 0; index < len(species); index++ {
		entry := species[index]
		if entry.IsSubspecies {
			if parentIndex, exists := byScientificName[normalizedTaxonName(entry.ParentScientificName)]; exists {
				species[parentIndex].Children = append(species[parentIndex].Children, entry)
				isChild[index] = true
				continue
			}
			if parent, exists := r.taxonomy[normalizedTaxonName(entry.ParentScientificName)]; exists {
				parentIndex := len(species)
				species = append(species, Species{
					Name:          parent.englishName,
					TaxonomicRank: parent.rank,
					IsSynthetic:   true,
					Children:      []Species{entry},
					mostRecent:    entry.mostRecent,
				})
				byScientificName[normalizedTaxonName(entry.ParentScientificName)] = parentIndex
				isChild = append(isChild, false)
				isChild[index] = true
			}
		}
	}
	topLevel := make([]Species, 0, len(species))
	for index, entry := range species {
		if !isChild[index] {
			topLevel = append(topLevel, entry)
		}
	}
	sortSpecies(topLevel, order)
	return topLevel, nil
}

func sortSpecies(species []Species, order string) {
	sort.SliceStable(species, func(i, j int) bool {
		left, right := species[i], species[j]
		switch order {
		case "recent":
			if left.mostRecent != right.mostRecent {
				return left.mostRecent > right.mostRecent
			}
		case "taxonomic":
			if left.IsOffList != right.IsOffList {
				return !left.IsOffList
			}
			if left.TaxonomicRank != right.TaxonomicRank {
				return left.TaxonomicRank < right.TaxonomicRank
			}
		default:
			if left.IsOffList != right.IsOffList {
				return !left.IsOffList
			}
		}
		return strings.ToLower(left.Name) < strings.ToLower(right.Name)
	})
}

func (r *Repository) SightingByID(ctx context.Context, id int64) (Sighting, error) {
	var sighting Sighting
	var btoData string
	err := r.db.QueryRowContext(ctx, `
	SELECT id, species, observed_at, location, region, notes, count, scientific_name, is_historic_species, latitude, longitude, grid_reference, bto_data
	FROM sightings WHERE id = ?`, id).Scan(
		&sighting.ID, &sighting.Species, &sighting.ObservedAt, &sighting.Location, &sighting.Region, &sighting.Notes,
		&sighting.Count, &sighting.ScientificName, &sighting.IsHistoricSpecies, &sighting.Latitude, &sighting.Longitude, &sighting.GridReference, &btoData,
	)
	if err != nil {
		return Sighting{}, err
	}
	sighting.BTOFields = decodeBTOFields(btoData)
	return sighting, nil
}

func (r *Repository) WipeSightings(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM sightings`)
	return err
}

type ImportResult struct {
	Imported int
}

type importedSighting struct {
	species              string
	observedAt           time.Time
	location             string
	region               string
	notes                string
	count                string
	scientificName       string
	latitude             sql.NullFloat64
	longitude            sql.NullFloat64
	gridReference        string
	btoData              string
	btoSpeciesCode       int
	taxonomyRank         int
	parentScientificName string
	isSubspecies         bool
	isHistoricSpecies    bool
}

var countPattern = regexp.MustCompile(`^(?:[1-9][0-9]*|[1-9][0-9]*\+|[cC][1-9][0-9]*)$`)

func (r *Repository) ImportBTORecords(ctx context.Context, input io.Reader) (ImportResult, error) {
	book, err := excelize.OpenReader(input)
	if err != nil {
		return ImportResult{}, fmt.Errorf("open workbook: %w", err)
	}
	defer func() { _ = book.Close() }()

	rows, err := book.GetRows("Records#1")
	if err != nil {
		return ImportResult{}, errors.New(`workbook must contain a "Records#1" worksheet`)
	}
	if len(rows) < 2 {
		return ImportResult{}, errors.New(`"Records#1" must contain a header row and at least one record`)
	}

	headers := make(map[string]int, len(rows[0]))
	for index, header := range rows[0] {
		headers[strings.TrimSpace(header)] = index
	}
	requiredHeaders := []string{"Species", "Place", "Date", "Start time", "End time", "Count"}
	for _, header := range requiredHeaders {
		if _, ok := headers[header]; !ok {
			return ImportResult{}, fmt.Errorf(`"Records#1" is missing required column %q`, header)
		}
	}

	records := make([]importedSighting, 0, len(rows)-1)
	for rowIndex, row := range rows[1:] {
		record, err := r.parseBTORecord(row, headers)
		if err != nil {
			return ImportResult{}, fmt.Errorf("row %d: %w", rowIndex+2, err)
		}
		records = append(records, record)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	statement, err := tx.PrepareContext(ctx, `
		INSERT INTO sightings (species, observed_at, location, region, notes, count, scientific_name, latitude, longitude, grid_reference, bto_data, bto_species_code, taxonomy_rank, parent_scientific_name, is_subspecies, is_historic_species)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(species, observed_at, location) DO NOTHING`)
	if err != nil {
		return ImportResult{}, err
	}
	defer statement.Close()

	recordsImported := 0
	for _, record := range records {
		// Count only rows newly persisted; repeated imports are intentionally ignored.
		result, err := statement.ExecContext(ctx, record.species, record.observedAt, record.location, record.region, record.notes, record.count, record.scientificName, record.latitude, record.longitude, record.gridReference, record.btoData, record.btoSpeciesCode, record.taxonomyRank, record.parentScientificName, record.isSubspecies, record.isHistoricSpecies)
		if err != nil {
			return ImportResult{}, err
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return ImportResult{}, err
		}
		if inserted == 1 {
			recordsImported++
		}
	}
	if err := tx.Commit(); err != nil {
		return ImportResult{}, err
	}
	return ImportResult{Imported: recordsImported}, nil
}

func (r *Repository) parseBTORecord(row []string, headers map[string]int) (importedSighting, error) {
	value := func(header string) string {
		index := headers[header]
		if index >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[index])
	}

	species, location, date := value("Species"), value("Place"), value("Date")
	if species == "" || location == "" || date == "" {
		return importedSighting{}, errors.New("Species, Place, and Date are required")
	}
	observedAt, err := parseBTODate(date)
	if err != nil {
		return importedSighting{}, fmt.Errorf("invalid Date %q", date)
	}
	if startTime := value("Start time"); startTime != "" {
		start, err := time.ParseInLocation("15:04", startTime, time.Local)
		if err != nil {
			return importedSighting{}, fmt.Errorf("invalid Start time %q", startTime)
		}
		observedAt = time.Date(observedAt.Year(), observedAt.Month(), observedAt.Day(), start.Hour(), start.Minute(), 0, 0, time.Local)
	}

	count := value("Count")
	if strings.EqualFold(count, "Present") {
		count = "1"
	}
	if !countPattern.MatchString(count) {
		return importedSighting{}, fmt.Errorf("invalid Count %q", value("Count"))
	}

	notes := ""
	if endTime := value("End time"); endTime != "" {
		if _, err := time.Parse("15:04", endTime); err != nil {
			return importedSighting{}, fmt.Errorf("invalid End time %q", endTime)
		}
		notes = "End time: " + endTime
	}
	btoData, err := json.Marshal(btoFields(row, headers))
	if err != nil {
		return importedSighting{}, fmt.Errorf("encode BTO data: %w", err)
	}
	scientificName := canonicalScientificName(value("Scientific name"))
	taxonomy := r.taxonomy[normalizedTaxonName(scientificName)]
	if parentScientificName := domesticParentScientificName(scientificName); parentScientificName != "" {
		parentTaxonomy := r.taxonomy[parentScientificName]
		taxonomy = taxon{
			rank:                 parentTaxonomy.rank,
			parentScientificName: parentScientificName,
			isSubspecies:         true,
		}
	}
	return importedSighting{
		species:              species,
		observedAt:           observedAt,
		location:             location,
		region:               LocationUK,
		notes:                notes,
		count:                count,
		scientificName:       scientificName,
		latitude:             parseCoordinate(value("Lat")),
		longitude:            parseCoordinate(value("Long")),
		gridReference:        value("Grid reference"),
		btoData:              string(btoData),
		btoSpeciesCode:       parseCode(value("BTO species code")),
		taxonomyRank:         taxonomy.rank,
		parentScientificName: taxonomy.parentScientificName,
		isSubspecies:         taxonomy.isSubspecies,
		isHistoricSpecies:    taxonomy.isHistoricSpecies,
	}, nil
}

func loadTaxonomy() map[string]taxon {
	var records []taxonomyRecord
	if err := json.Unmarshal(britishListTaxonomy, &records); err != nil {
		panic(fmt.Sprintf("decode embedded British List taxonomy: %v", err))
	}

	taxonomy := make(map[string]taxon)
	for _, record := range records {
		scientificName := normalizedTaxonName(record.ScientificName)
		if scientificName == "" {
			continue
		}
		isSubspecies := record.Rank == "Subspecies"
		parentScientificName := ""
		if isSubspecies {
			parentScientificName = parentSpeciesScientificName(scientificName)
		}
		taxonomy[scientificName] = taxon{
			rank:                 record.SortOrder,
			parentScientificName: parentScientificName,
			isSubspecies:         isSubspecies,
			englishName:          record.EnglishName,
		}
	}
	if len(taxonomy) == 0 {
		panic("embedded British List taxonomy worksheet has no species ranks")
	}
	addHistoricSpecies(taxonomy, "anas carolinensis", "anas crecca")
	addHistoricSpecies(taxonomy, "corvus cornix", "corvus corone")
	return taxonomy
}

type taxonomyRecord struct {
	SortOrder      int    `json:"sortOrder"`
	Rank           string `json:"rank"`
	ScientificName string `json:"scientificName"`
	EnglishName    string `json:"englishName"`
}

func addHistoricSpecies(taxonomy map[string]taxon, historicScientificName, parentScientificName string) {
	parent, exists := taxonomy[parentScientificName]
	if !exists {
		panic(fmt.Sprintf("embedded British List is missing historic parent %q", parentScientificName))
	}
	taxonomy[historicScientificName] = taxon{
		rank:                 parent.rank,
		parentScientificName: parentScientificName,
		isSubspecies:         true,
		isHistoricSpecies:    true,
	}
}

func (r *Repository) backfillTaxonomyRanks(ctx context.Context) error {
	for oldScientificName, newScientificName := range scientificNameCorrections {
		if _, err := r.db.ExecContext(ctx, `
UPDATE sightings SET scientific_name = ?
WHERE LOWER(TRIM(scientific_name)) = ?`, newScientificName, oldScientificName); err != nil {
			return err
		}
	}
	statement, err := r.db.PrepareContext(ctx, `
UPDATE sightings
SET taxonomy_rank = ?, parent_scientific_name = ?, is_subspecies = ?, is_historic_species = ?
WHERE LOWER(TRIM(scientific_name)) = ?`)
	if err != nil {
		return err
	}
	defer statement.Close()
	for scientificName, taxonomy := range r.taxonomy {
		if _, err := statement.ExecContext(ctx, taxonomy.rank, taxonomy.parentScientificName, taxonomy.isSubspecies, taxonomy.isHistoricSpecies, scientificName); err != nil {
			return err
		}
	}
	return r.backfillDomesticTaxa(ctx)
}

func (r *Repository) backfillDomesticTaxa(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, `
SELECT DISTINCT scientific_name
FROM sightings
WHERE LOWER(TRIM(scientific_name)) LIKE '% f. domestica'`)
	if err != nil {
		return err
	}
	domesticScientificNames := make([]string, 0)
	for rows.Next() {
		var scientificName string
		if err := rows.Scan(&scientificName); err != nil {
			_ = rows.Close()
			return err
		}
		domesticScientificNames = append(domesticScientificNames, scientificName)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	statement, err := r.db.PrepareContext(ctx, `
UPDATE sightings
SET taxonomy_rank = ?, parent_scientific_name = ?, is_subspecies = 1, is_historic_species = 0
WHERE LOWER(TRIM(scientific_name)) = ?`)
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, scientificName := range domesticScientificNames {
		parentScientificName := domesticParentScientificName(scientificName)
		if parentScientificName == "" {
			continue
		}
		if _, err := statement.ExecContext(ctx, r.taxonomy[parentScientificName].rank, parentScientificName, normalizedTaxonName(scientificName)); err != nil {
			return err
		}
	}
	return nil
}

var scientificNameCorrections = map[string]string{
	"charadrius dubius": "Thinornis dubius",
}

func canonicalScientificName(scientificName string) string {
	normalized := normalizedTaxonName(scientificName)
	if corrected, exists := scientificNameCorrections[normalized]; exists {
		return corrected
	}
	return strings.TrimSpace(scientificName)
}

func domesticParentScientificName(scientificName string) string {
	const domesticSuffix = " f. domestica"
	normalized := normalizedTaxonName(scientificName)
	if !strings.HasSuffix(normalized, domesticSuffix) {
		return ""
	}
	return strings.TrimSuffix(normalized, domesticSuffix)
}

func normalizedTaxonName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

func parentSpeciesScientificName(scientificName string) string {
	parts := strings.Fields(scientificName)
	if len(parts) < 3 {
		return ""
	}
	return strings.Join(parts[:2], " ")
}

func btoFields(row []string, headers map[string]int) []BTOField {
	fields := make([]BTOField, 0, len(headers))
	for index, value := range row {
		value = strings.TrimSpace(value)
		if value == "" || index >= len(headers) {
			continue
		}
		for header, headerIndex := range headers {
			if headerIndex == index {
				fields = append(fields, BTOField{Label: header, Value: value})
				break
			}
		}
	}
	return fields
}

func decodeBTOFields(raw string) []BTOField {
	var fields []BTOField
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil
	}
	return fields
}

func parseCoordinate(raw string) sql.NullFloat64 {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: value, Valid: true}
}

func parseCode(raw string) int {
	code, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return code
}

func parseBTODate(date string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02", "02/01/2006"} {
		if parsed, err := time.ParseInLocation(layout, date, time.Local); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported date format")
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
		"2006-01-02",
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
