package database

import (
	"database/sql"
	"log"

	_ "github.com/lib/pq"
)

// ConnectPostgres establishes a connection to a PostgreSQL database
func ConnectPostgres(dsn string) *sql.DB {
	db, err := sql.Open("postgres", dsn)
	// Check if the database connection was successful
	// and if the database is reachable.

	if err != nil {
		log.Fatalf("failed to open db: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("failed to ping db: %v", err)
	}
	return db
}
