package data

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
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

	totalUK, err := repo.CountSightings(context.Background(), Filter{Location: LocationUK}, CountModeModern, true)
	if err != nil {
		t.Fatalf("count uk: %v", err)
	}
	if totalUK != 2 {
		t.Fatalf("want 2 UK sightings, got %d", totalUK)
	}

	total2025Global, err := repo.CountSightings(context.Background(), Filter{Year: 2025, Location: LocationGlobal}, CountModeModern, true)
	if err != nil {
		t.Fatalf("count global by year: %v", err)
	}
	if total2025Global != 2 {
		t.Fatalf("want 2 sightings in 2025, got %d", total2025Global)
	}
}

func TestCumulativeSpeciesCountsUsesTotalCountRules(t *testing.T) {
	repo := setupTestRepo(t)
	_, err := repo.db.Exec(`
INSERT INTO sightings (species, scientific_name, observed_at, location, region, parent_scientific_name, is_subspecies, is_historic_species, taxonomy_rank)
VALUES
	('Teal', 'Anas crecca', '2025-01-01', 'Cardiff', 'uk', '', 0, 0, 10),
	('Pied Wagtail', 'Motacilla alba yarrellii', '2025-01-03', 'Cardiff', 'uk', 'motacilla alba', 1, 0, 11),
	('White Wagtail', 'Motacilla alba', '2025-01-05', 'Cardiff', 'uk', '', 0, 0, 10),
	('Unknown Bird', 'Unmatched bird', '2025-01-06', 'Cardiff', 'uk', '', 0, 0, 0)`)
	if err != nil {
		t.Fatalf("insert sightings: %v", err)
	}

	points, err := repo.CumulativeSpeciesCounts(context.Background(), Filter{Location: LocationUK}, CountModeModern, false)
	if err != nil {
		t.Fatalf("load cumulative counts: %v", err)
	}
	if len(points) != 3 || points[0].Count != 1 || points[2].Count != 2 {
		t.Fatalf("want daily counts 1, 1, 2; got %#v", points)
	}
	total, err := repo.CountSightings(context.Background(), Filter{Location: LocationUK}, CountModeModern, false)
	if err != nil {
		t.Fatalf("count sightings: %v", err)
	}
	if points[len(points)-1].Count != total {
		t.Fatalf("want final series count %d to match total count, got %d", total, points[len(points)-1].Count)
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

func TestLoadsBritishListSpeciesRanks(t *testing.T) {
	taxonomy := loadTaxonomy()
	if taxonomy["turdus merula"].rank == 0 {
		t.Fatal("Blackbird should have a rank from the embedded British List")
	}
	piedWagtail := taxonomy["motacilla alba yarrellii"]
	if !piedWagtail.isSubspecies || piedWagtail.parentScientificName != "motacilla alba" {
		t.Fatalf("want Pied Wagtail classified under Motacilla alba, got %#v", piedWagtail)
	}
	for historicName, parentName := range map[string]string{
		"anas carolinensis": "anas crecca",
		"corvus cornix":     "corvus corone",
	} {
		historic := taxonomy[historicName]
		if !historic.isHistoricSpecies || historic.parentScientificName != parentName {
			t.Fatalf("want %s classified as historic under %s, got %#v", historicName, parentName, historic)
		}
	}
}

func TestGroupsSubspeciesAndExcludesThemFromTotal(t *testing.T) {
	repo := setupTestRepo(t)
	_, err := repo.db.Exec(`
INSERT INTO sightings (species, scientific_name, observed_at, location, region, parent_scientific_name, is_subspecies, taxonomy_rank)
VALUES
	('White Wagtail', 'Motacilla alba', '2025-01-01', 'Cardiff', 'uk', '', 0, 10),
	('Pied Wagtail', 'Motacilla alba yarrellii', '2025-01-02', 'Cardiff', 'uk', 'motacilla alba', 1, 11)`)
	if err != nil {
		t.Fatalf("insert taxonomy sightings: %v", err)
	}

	total, err := repo.CountSightings(context.Background(), Filter{Location: LocationUK}, CountModeModern, true)
	if err != nil {
		t.Fatalf("count species: %v", err)
	}
	if total != 1 {
		t.Fatalf("want only the parent species counted, got %d", total)
	}

	species, err := repo.AllSpecies(context.Background(), "", "taxonomic")
	if err != nil {
		t.Fatalf("load species: %v", err)
	}
	if len(species) != 1 || species[0].Name != "White Wagtail" || len(species[0].Children) != 1 || species[0].Children[0].Name != "Pied Wagtail" {
		t.Fatalf("want a White Wagtail parent with Pied Wagtail child, got %#v", species)
	}
}

func TestCountSightingsIncludesOrphanedRegularSubspeciesAsParent(t *testing.T) {
	repo := setupTestRepo(t)
	_, err := repo.db.Exec(`
INSERT INTO sightings (species, scientific_name, observed_at, location, region, parent_scientific_name, is_subspecies, taxonomy_rank)
VALUES
	('White-spotted Bluethroat', 'Luscinia svecica cyanecula', '2025-01-01', 'Cardiff', 'uk', 'luscinia svecica', 1, 10)`)
	if err != nil {
		t.Fatalf("insert orphaned subspecies: %v", err)
	}

	total, err := repo.CountSightings(context.Background(), Filter{Location: LocationUK}, CountModeModern, false)
	if err != nil {
		t.Fatalf("count species: %v", err)
	}
	if total != 1 {
		t.Fatalf("want the parent species counted once, got %d", total)
	}

	species, err := repo.AllSpecies(context.Background(), "", "taxonomic")
	if err != nil {
		t.Fatalf("load species: %v", err)
	}
	if len(species) != 1 || species[0].Name != "Bluethroat" || !species[0].IsSynthetic || len(species[0].Children) != 1 {
		t.Fatalf("want synthetic Bluethroat parent with one child, got %#v", species)
	}
}

func TestVisitsGroupsSightingsByDateAndLocation(t *testing.T) {
	repo := setupTestRepo(t)
	btoData, err := json.Marshal([]BTOField{{Label: "Weather", Value: "Sunny intervals"}})
	if err != nil {
		t.Fatalf("encode BTO fields: %v", err)
	}
	_, err = repo.db.Exec(`
INSERT INTO sightings (species, count, observed_at, location, region, notes, latitude, longitude, bto_data)
VALUES
	('Robin', '2', '2025-04-01 09:00:00', 'Bute Park', 'uk', 'End time: 10:30', 51.49, -3.19, ?),
	('Grey Wagtail', '1', '2025-04-01 09:15:00', 'Bute Park', 'uk', '', 51.49, -3.19, ?),
	('Coot', '1', '2025-04-02 09:00:00', 'Roath Park', 'uk', '', NULL, NULL, '[]')`,
		string(btoData), string(btoData))
	if err != nil {
		t.Fatalf("insert visit sightings: %v", err)
	}

	visits, err := repo.Visits(context.Background())
	if err != nil {
		t.Fatalf("load visits: %v", err)
	}
	if len(visits) != 2 {
		t.Fatalf("want 2 visits, got %d", len(visits))
	}
	visit := visits[1]
	if visit.Location != "Bute Park" || visit.StartTime.Format("15:04") != "09:00" || visit.EndTime != "10:30" || visit.Weather != "Sunny intervals" || len(visit.Species) != 2 {
		t.Fatalf("want grouped Bute Park visit, got %#v", visit)
	}
}

func TestHistoricCountIncludesHistoricSpeciesOnlyInHistoricMode(t *testing.T) {
	repo := setupTestRepo(t)
	_, err := repo.db.Exec(`
INSERT INTO sightings (species, scientific_name, observed_at, location, region, parent_scientific_name, is_subspecies, is_historic_species)
VALUES
	('Teal', 'Anas crecca', '2025-01-01', 'Cardiff', 'uk', '', 0, 0),
	('Green-winged Teal', 'Anas carolinensis', '2025-01-02', 'Cardiff', 'uk', 'anas crecca', 1, 1),
	('Pied Wagtail', 'Motacilla alba yarrellii', '2025-01-03', 'Cardiff', 'uk', 'motacilla alba', 1, 0)`)
	if err != nil {
		t.Fatalf("insert historic sighting: %v", err)
	}

	modernTotal, err := repo.CountSightings(context.Background(), Filter{Location: LocationUK}, CountModeModern, true)
	if err != nil {
		t.Fatalf("count modern species: %v", err)
	}
	historicTotal, err := repo.CountSightings(context.Background(), Filter{Location: LocationUK}, CountModeHistoric, true)
	if err != nil {
		t.Fatalf("count historic species: %v", err)
	}
	if modernTotal != 2 || historicTotal != 3 {
		t.Fatalf("want modern total 2 and historic total 3, got %d and %d", modernTotal, historicTotal)
	}
}

func TestOffListSpeciesSortLastAndRequireOptInForTotals(t *testing.T) {
	repo := setupTestRepo(t)
	_, err := repo.db.Exec(`
INSERT INTO sightings (species, scientific_name, observed_at, location, region, taxonomy_rank)
VALUES
	('Robin', 'Erithacus rubecula', '2025-01-01', 'Cardiff', 'uk', 10),
	('Unknown Bird', 'Unmatched bird', '2025-01-02', 'Cardiff', 'uk', 0)`)
	if err != nil {
		t.Fatalf("insert off-list species: %v", err)
	}

	species, err := repo.AllSpecies(context.Background(), "", "alphabetical")
	if err != nil {
		t.Fatalf("load alphabetical species: %v", err)
	}
	if len(species) != 2 || species[0].Name != "Robin" || species[1].Name != "Unknown Bird" || !species[1].IsOffList {
		t.Fatalf("want on-list species followed by off-list species, got %#v", species)
	}

	withoutOffList, err := repo.CountSightings(context.Background(), Filter{Location: LocationUK}, CountModeModern, false)
	if err != nil {
		t.Fatalf("count without off-list species: %v", err)
	}
	withOffList, err := repo.CountSightings(context.Background(), Filter{Location: LocationUK}, CountModeModern, true)
	if err != nil {
		t.Fatalf("count with off-list species: %v", err)
	}
	if withoutOffList != 1 || withOffList != 2 {
		t.Fatalf("want totals 1 then 2, got %d then %d", withoutOffList, withOffList)
	}
}

func TestBackfillsScientificNameCorrectionAndDomesticTaxa(t *testing.T) {
	repo := setupTestRepo(t)
	_, err := repo.db.Exec(`
INSERT INTO sightings (species, scientific_name, observed_at, location, region)
VALUES
	('Little Ringed Plover', 'Charadrius dubius', '2025-01-01', 'Cardiff', 'uk'),
	('Greylag Goose', 'Anser anser', '2025-01-02', 'Cardiff', 'uk'),
	('Domestic Greylag Goose', 'Anser anser f. domestica', '2025-01-03', 'Cardiff', 'uk')`)
	if err != nil {
		t.Fatalf("insert legacy taxa: %v", err)
	}
	if err := repo.backfillTaxonomyRanks(context.Background()); err != nil {
		t.Fatalf("backfill taxa: %v", err)
	}

	var scientificName, parentScientificName string
	var isSubspecies bool
	if err := repo.db.QueryRow(`
SELECT scientific_name, parent_scientific_name, is_subspecies
FROM sightings WHERE species = 'Little Ringed Plover'`).Scan(&scientificName, &parentScientificName, &isSubspecies); err != nil {
		t.Fatalf("load plover: %v", err)
	}
	if scientificName != "Thinornis dubius" || parentScientificName != "" || isSubspecies {
		t.Fatalf("want corrected species taxon, got %q, %q, %t", scientificName, parentScientificName, isSubspecies)
	}
	if err := repo.db.QueryRow(`
SELECT parent_scientific_name, is_subspecies
FROM sightings WHERE species = 'Domestic Greylag Goose'`).Scan(&parentScientificName, &isSubspecies); err != nil {
		t.Fatalf("load domestic goose: %v", err)
	}
	if parentScientificName != "anser anser" || !isSubspecies {
		t.Fatalf("want domestic goose nested under Anser anser, got %q and %t", parentScientificName, isSubspecies)
	}
}

func TestImportBTORecordsNormalizesCountsAndEndTimes(t *testing.T) {
	repo := setupTestRepo(t)
	workbook := newBTOWorkbook(t, [][]string{
		{"Robin", "Taff Trail", "2024-05-01", "09:30", "11:15", "Present"},
		{"Carrion Crow", "Bute Park", "02/05/2024", "10:00", "", "20+"},
		{"Coot", "Roath Park", "2024-05-03", "12:00", "12:05", "c20"},
	})

	result, err := repo.ImportBTORecords(context.Background(), workbook)
	if err != nil {
		t.Fatalf("import records: %v", err)
	}
	if result.Imported != 3 {
		t.Fatalf("want 3 imported records, got %d", result.Imported)
	}

	var count, notes string
	var observedAt time.Time
	if err := repo.db.QueryRow(`SELECT count, notes, observed_at FROM sightings WHERE species = 'Robin'`).Scan(&count, &notes, &observedAt); err != nil {
		t.Fatalf("load imported robin: %v", err)
	}
	if count != "1" || notes != "End time: 11:15" {
		t.Fatalf("want normalized count and end-time note, got count %q and notes %q", count, notes)
	}
	if observedAt.Hour() != 9 || observedAt.Minute() != 30 {
		t.Fatalf("want start time 09:30, got %s", observedAt.Format(time.RFC3339))
	}

	var crowCount, cootCount string
	if err := repo.db.QueryRow(`SELECT count FROM sightings WHERE species = 'Carrion Crow'`).Scan(&crowCount); err != nil {
		t.Fatalf("load imported crow: %v", err)
	}
	if err := repo.db.QueryRow(`SELECT count FROM sightings WHERE species = 'Coot'`).Scan(&cootCount); err != nil {
		t.Fatalf("load imported coot: %v", err)
	}
	if crowCount != "20+" || cootCount != "c20" {
		t.Fatalf("want preserved count forms, got %q and %q", crowCount, cootCount)
	}
}

func TestImportBTORecordsRejectsInvalidRowsWithoutInserting(t *testing.T) {
	repo := setupTestRepo(t)
	workbook := newBTOWorkbook(t, [][]string{
		{"Robin", "Taff Trail", "2024-05-01", "09:30", "11:15", "1"},
		{"Coot", "Roath Park", "2024-05-03", "12:00", "12:05", "many"},
	})

	if _, err := repo.ImportBTORecords(context.Background(), workbook); err == nil {
		t.Fatal("want invalid count error")
	}
	var records int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM sightings`).Scan(&records); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if records != 0 {
		t.Fatalf("want no rows after failed import, got %d", records)
	}
}

func TestImportBTORecordsSkipsDuplicateSightings(t *testing.T) {
	repo := setupTestRepo(t)
	records := [][]string{{"Robin", "Taff Trail", "2024-05-01", "09:30", "11:15", "1"}}

	first, err := repo.ImportBTORecords(context.Background(), newBTOWorkbook(t, records))
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	second, err := repo.ImportBTORecords(context.Background(), newBTOWorkbook(t, records))
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if first.Imported != 1 || second.Imported != 0 {
		t.Fatalf("want import counts 1 then 0, got %d then %d", first.Imported, second.Imported)
	}
}

func newBTOWorkbook(t *testing.T, records [][]string) *bytes.Reader {
	t.Helper()
	book := excelize.NewFile()
	defaultSheet := book.GetSheetName(0)
	book.SetSheetName(defaultSheet, "Records#1")
	headers := []string{"Species", "Place", "Date", "Start time", "End time", "Count"}
	if err := book.SetSheetRow("Records#1", "A1", &headers); err != nil {
		t.Fatalf("set headers: %v", err)
	}
	for index, record := range records {
		cell, err := excelize.CoordinatesToCellName(1, index+2)
		if err != nil {
			t.Fatalf("create cell name: %v", err)
		}
		if err := book.SetSheetRow("Records#1", cell, &record); err != nil {
			t.Fatalf("set record: %v", err)
		}
	}
	buffer, err := book.WriteToBuffer()
	if err != nil {
		t.Fatalf("write workbook: %v", err)
	}
	if err := book.Close(); err != nil {
		t.Fatalf("close workbook: %v", err)
	}
	return bytes.NewReader(buffer.Bytes())
}
