package db

import (
	"database/sql"
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type DB struct {
	*sql.DB
}

type Stats struct {
	TotalJobs          int
	NewJobsToday       int
	JobsByStatus       map[string]int
	JobsByType         map[string]int
	TotalProspects     int
	NewProspectsToday  int
	ProspectsByStatus  map[string]int
}

func (db *DB) GetStats() (Stats, error) {
	var s Stats
	s.JobsByStatus = make(map[string]int)
	s.JobsByType = make(map[string]int)
	s.ProspectsByStatus = make(map[string]int)

	err := db.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&s.TotalJobs)
	if err != nil {
		return s, err
	}

	err = db.QueryRow("SELECT COUNT(*) FROM jobs WHERE date_found=date('now')").Scan(&s.NewJobsToday)
	if err != nil {
		return s, err
	}

	rows, err := db.Query("SELECT status, COUNT(*) FROM jobs GROUP BY status")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var status string
			var count int
			if err := rows.Scan(&status, &count); err == nil {
				s.JobsByStatus[status] = count
			}
		}
	}

	rows, err = db.Query("SELECT type, COUNT(*) FROM jobs GROUP BY type")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var t string
			var count int
			if err := rows.Scan(&t, &count); err == nil {
				s.JobsByType[t] = count
			}
		}
	}

	err = db.QueryRow("SELECT COUNT(*) FROM companies").Scan(&s.TotalProspects)
	if err != nil {
		return s, err
	}

	err = db.QueryRow("SELECT COUNT(*) FROM companies WHERE date_found=date('now')").Scan(&s.NewProspectsToday)
	if err != nil {
		return s, err
	}

	rows, err = db.Query("SELECT status, COUNT(*) FROM companies GROUP BY status")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var status string
			var count int
			if err := rows.Scan(&status, &count); err == nil {
				s.ProspectsByStatus[status] = count
			}
		}
	}

	return s, nil
}

func NewDB(path string) (*DB, error) {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	// Connect with WAL mode and foreign keys
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	d := &DB{db}
	if err := d.initMigrations(); err != nil {
		return nil, err
	}

	return d, nil
}

func (db *DB) initMigrations() error {
	// Create schema_migrations table if it doesn't exist
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			applied_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
	`)
	if err != nil {
		return fmt.Errorf("failed to create schema_migrations table: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("failed to read migrations directory: %w", err)
	}

	var migrationFiles []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			migrationFiles = append(migrationFiles, entry.Name())
		}
	}
	sort.Strings(migrationFiles)

	applied := make(map[string]bool)
	rows, err := db.Query("SELECT name FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("failed to query applied migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		applied[name] = true
	}

	for _, name := range migrationFiles {
		if applied[name] {
			continue
		}

		log.Printf("Applying migration: %s", name)
		content, err := migrationsFS.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			return fmt.Errorf("failed to read migration %s: %w", name, err)
		}

		tx, err := db.Begin()
		if err != nil {
			return err
		}

		// Special handling for 011_company_email.sql to be idempotent
		if name == "011_company_email.sql" {
			_, err := tx.Exec(string(content))
			if err != nil {
				if !strings.Contains(err.Error(), "duplicate column name") {
					tx.Rollback()
					return fmt.Errorf("failed to apply migration %s: %w", name, err)
				}
			}
			// Already executed content above, record it and commit
			if _, err := tx.Exec("INSERT INTO schema_migrations (name) VALUES (?)", name); err != nil {
				tx.Rollback()
				return fmt.Errorf("failed to record migration %s: %w", name, err)
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			continue
		}

		if _, err := tx.Exec(string(content)); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to apply migration %s: %w", name, err)
		}

		// Special handling for 001_contacts.sql to be idempotent and safe
		if name == "001_contacts.sql" {
			alters := []string{
				"ALTER TABLE companies ADD COLUMN primary_contact_id INTEGER REFERENCES contacts(id)",
				"ALTER TABLE companies ADD COLUMN company_type TEXT DEFAULT 'UNKNOWN' CHECK(company_type IN ('TECH', 'TECH_ADJACENT', 'NON_TECH', 'UNKNOWN'))",
				"ALTER TABLE companies ADD COLUMN has_internal_tech_team INTEGER DEFAULT NULL",
				"ALTER TABLE companies ADD COLUMN tech_team_signals TEXT",
			}
			for _, sql := range alters {
				_, err := tx.Exec(sql)
				if err != nil {
					if !strings.Contains(err.Error(), "duplicate column name") {
						tx.Rollback()
						return fmt.Errorf("failed to alter table in migration %s: %w", name, err)
					}
				}
			}

			// Update primary_contact_id for existing ones
			_, err = tx.Exec(`
				UPDATE companies SET primary_contact_id = (
					SELECT id FROM contacts WHERE company_id = companies.id LIMIT 1
				) WHERE primary_contact_id IS NULL
			`)
			if err != nil {
				tx.Rollback()
				return fmt.Errorf("failed to update primary_contact_id in migration %s: %w", name, err)
			}
		}

		if _, err := tx.Exec("INSERT INTO schema_migrations (name) VALUES (?)", name); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to record migration %s: %w", name, err)
		}

		if err := tx.Commit(); err != nil {
			return err
		}
	}

	return nil
}

func ToNullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func ToNullInt64(i int64) sql.NullInt64 {
	return sql.NullInt64{Int64: i, Valid: i != 0}
}

func ToNullBool(b bool) sql.NullBool {
	return sql.NullBool{Bool: b, Valid: true}
}
