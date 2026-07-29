package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

type migration func(context.Context, *sql.Tx) error

// migrations is keyed by the version being upgraded. Every migration advances
// exactly one version so a database can be upgraded through the full chain.
var migrations = map[int]migration{
	8: func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"ALTER TABLE ingest_jobs ADD COLUMN category TEXT NOT NULL DEFAULT ''",
		); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `CREATE TABLE download_categories (
    name TEXT PRIMARY KEY,
    save_path TEXT NOT NULL DEFAULT ''
)`)
		return err
	},
	3: func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"ALTER TABLE ingest_jobs ADD COLUMN category TEXT NOT NULL DEFAULT ''",
		); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
CREATE TABLE download_categories (
    name                    TEXT PRIMARY KEY,
    save_path               TEXT NOT NULL DEFAULT ''
)`)
		return err
	},
}

func runMigration(
	ctx context.Context,
	connection *sql.Conn,
	fromVersion int,
	migrate migration,
) (returnError error) {
	if _, err := connection.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return fmt.Errorf("迁移数据库 v%d→v%d 前关闭外键检查: %w",
			fromVersion, fromVersion+1, err)
	}
	defer func() {
		if _, err := connection.ExecContext(ctx, "PRAGMA foreign_keys = ON"); returnError == nil && err != nil {
			returnError = fmt.Errorf("迁移数据库后恢复外键检查: %w", err)
		}
	}()

	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始迁移数据库 v%d→v%d: %w", fromVersion, fromVersion+1, err)
	}
	defer transaction.Rollback()

	if err := migrate(ctx, transaction); err != nil {
		return fmt.Errorf("迁移数据库 v%d→v%d: %w", fromVersion, fromVersion+1, err)
	}
	if err := checkForeignKeys(ctx, transaction); err != nil {
		return fmt.Errorf("迁移数据库 v%d→v%d 后检查外键: %w",
			fromVersion, fromVersion+1, err)
	}
	if _, err := transaction.ExecContext(
		ctx, fmt.Sprintf("PRAGMA user_version = %d", fromVersion+1),
	); err != nil {
		return fmt.Errorf("记录数据库版本 %d: %w", fromVersion+1, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("提交数据库迁移 v%d→v%d: %w", fromVersion, fromVersion+1, err)
	}
	return nil
}

func checkForeignKeys(ctx context.Context, transaction *sql.Tx) error {
	rows, err := transaction.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var foreignKeyID int
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			return err
		}
		return fmt.Errorf("表 %s 的记录 %v 引用了不存在的 %s 记录（外键 %d）",
			table, rowID, parent, foreignKeyID)
	}
	return rows.Err()
}
