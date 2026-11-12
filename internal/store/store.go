package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/microsoft/go-mssqldb"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

const Path = "data/portfolio.sqlite"

// DB stores keyed current state and business history.
type DB struct {
	sql     *sql.DB
	dialect string
}

func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("каталог базы: %w", err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	raw.SetMaxOpenConns(1)
	db := &DB{sql: raw, dialect: "sqlite"}
	if _, err := raw.Exec(schema); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("схема: %w", err)
	}
	if err := migrateSQLiteReportSchema(context.Background(), raw); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("миграция схемы импорта отчётов SQLite: %w", err)
	}
	if err := migrateSQLiteHistory(context.Background(), raw); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("перенос нормализованной истории SQLite: %w", err)
	}
	return db, nil
}

func OpenSQLServer(dsn string) (*DB, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("задайте TINVEST_SQL_DSN")
	}
	raw, err := sql.Open("sqlserver", dsn)
	if err != nil {
		return nil, fmt.Errorf("открытие SQL Server: %w", err)
	}
	raw.SetMaxOpenConns(8)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := raw.PingContext(ctx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("подключение к SQL Server: %w", err)
	}
	var ready bool
	err = raw.QueryRowContext(ctx, `
SELECT CASE WHEN
	OBJECT_ID(N'portfolio.positions', N'U') IS NOT NULL AND
	OBJECT_ID(N'portfolio.source_accounts', N'U') IS NOT NULL AND
	OBJECT_ID(N'ref.external_systems', N'U') IS NOT NULL AND
	OBJECT_ID(N'ref.external_codes', N'U') IS NOT NULL AND
	OBJECT_ID(N'ref.external_code_types', N'U') IS NOT NULL AND
	OBJECT_ID(N'ref.instruments', N'U') IS NOT NULL AND
	OBJECT_ID(N'ref.boards', N'U') IS NOT NULL AND
	OBJECT_ID(N'ref.instrument_type_boards', N'U') IS NOT NULL AND
	OBJECT_ID(N'tinvest.current_state', N'U') IS NOT NULL AND
	OBJECT_ID(N'tinvest.history_record', N'U') IS NOT NULL AND
	OBJECT_ID(N'tinvest.instrument', N'U') IS NOT NULL AND
	OBJECT_ID(N'tinvest.report_import', N'U') IS NOT NULL AND
	OBJECT_ID(N'tinvest.report_raw_row', N'U') IS NOT NULL AND
	OBJECT_ID(N'tinvest.report_account_mapping', N'U') IS NOT NULL AND
	OBJECT_ID(N'tinvest.trade_record', N'U') IS NOT NULL AND
	COL_LENGTH(N'tinvest.trade_record', N'isin') IS NOT NULL AND
	OBJECT_ID(N'tinvest.subscriptions', N'U') IS NOT NULL AND
	COL_LENGTH(N'tinvest.operation', N'report_authoritative') IS NOT NULL AND
	COL_LENGTH(N'tinvest.operation', N'normalized_type') IS NOT NULL
THEN 1 ELSE 0 END`).Scan(&ready)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("проверка схемы SQL Server: %w", err)
	}
	if !ready {
		_ = raw.Close()
		return nil, fmt.Errorf("в SQL Server не применена миграция состояния T-Invest/общего портфеля, migrations/100_tinvest_broker_reports.sql или quik-portfolio/migrations/088_tinvest_subscriptions.sql")
	}
	return &DB{sql: raw, dialect: "sqlserver"}, nil
}

func (db *DB) Close() error {
	if db == nil || db.sql == nil {
		return nil
	}
	return db.sql.Close()
}
