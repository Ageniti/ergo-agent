// Package migrations embeds and applies the ECS example database schema.
package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"strings"
)

//go:embed *.sql
var files embed.FS

func Up(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS agent_schema_migrations (name VARCHAR(255) PRIMARY KEY, checksum CHAR(64) NOT NULL, applied_at DATETIME(6) NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return err
	}
	for _, name := range names {
		source, readErr := files.ReadFile(name)
		if readErr != nil {
			return readErr
		}
		digest := sha256.Sum256(source)
		checksum := hex.EncodeToString(digest[:])
		var applied string
		err = db.QueryRowContext(ctx, `SELECT checksum FROM agent_schema_migrations WHERE name=?`, name).Scan(&applied)
		if err == nil {
			if applied != checksum {
				return fmt.Errorf("migration %s changed after being applied", name)
			}
			continue
		}
		if err != sql.ErrNoRows {
			return err
		}
		for _, statement := range strings.Split(string(source), ";") {
			statement = strings.TrimSpace(statement)
			if statement == "" || strings.HasPrefix(statement, "--") && !strings.Contains(statement, "\n") {
				continue
			}
			if _, err = db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply %s: %w", name, err)
			}
		}
		if _, err = db.ExecContext(ctx, `INSERT INTO agent_schema_migrations (name, checksum, applied_at) VALUES (?, ?, UTC_TIMESTAMP(6))`, name, checksum); err != nil {
			return err
		}
	}
	return nil
}
