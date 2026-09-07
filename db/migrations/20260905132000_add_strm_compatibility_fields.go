package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upAddStrmCompatibilityFields, downAddStrmCompatibilityFields)
}

func migrationColumnExists(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&count)
	return count > 0, err
}

func addStrmColumnIfMissing(ctx context.Context, tx *sql.Tx, column, definition string) error {
	exists, err := migrationColumnExists(ctx, tx, "media_file", column)
	if err != nil {
		return fmt.Errorf("checking media_file.%s: %w", column, err)
	}
	if exists {
		return nil
	}
	if _, err = tx.ExecContext(ctx, "ALTER TABLE media_file ADD COLUMN "+column+" "+definition); err != nil {
		return fmt.Errorf("adding media_file.%s: %w", column, err)
	}
	return nil
}

func upAddStrmCompatibilityFields(ctx context.Context, tx *sql.Tx) error {
	// The old Redia image used the same columns in a private migration. Check
	// first so its databases upgrade without duplicate-column failures.
	if err := addStrmColumnIfMissing(ctx, tx, "is_strm", "BOOL NOT NULL DEFAULT FALSE"); err != nil {
		return err
	}
	if err := addStrmColumnIfMissing(ctx, tx, "strm_target", "VARCHAR(512) NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE media_file SET is_strm = TRUE WHERE lower(path) LIKE '%.strm'"); err != nil {
		return fmt.Errorf("marking existing STRM rows: %w", err)
	}
	return forceFullRescan(ctx, tx)
}

func downAddStrmCompatibilityFields(context.Context, *sql.Tx) error {
	// Deliberately retain the columns: they may predate this migration in a
	// legacy Redia database, and dropping user data during rollback is unsafe.
	return nil
}
