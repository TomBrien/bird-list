package main

import (
	"database/sql"
	"log"
	"net/http"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/TomBrien/bird-list/internal/app"
)

func main() {
	dsn := os.Getenv("BIRD_LIST_DB")
	if dsn == "" {
		dsn = filepath.Join("data", "birds.db")
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	server, err := app.NewServer(db)
	if err != nil {
		log.Fatalf("initialize server: %v", err)
	}

	if err := db.Ping(); err != nil {
		log.Fatalf("connect db: %v", err)
	}

	addr := ":8080"
	log.Printf("starting bird-list on %s", addr)
	if err := http.ListenAndServe(addr, server.Router()); err != nil {
		log.Fatalf("server exited: %v", err)
	}
}
