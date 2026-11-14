package store

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"t-invest-portfolio/internal/portfolio"
)

type sqlExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
}

type mssqlTx struct {
	tx  *sql.Tx
	ctx context.Context
}

const (
	externalCodeTypeInstrument = 3
	externalCodeTypeFIGI       = 4
	externalCodeTypeUID        = 6
)

var (
	conflictInsert = regexp.MustCompile(`(?is)^\s*INSERT\s+INTO\s+([a-z_][a-z0-9_]*)\s*\(([^)]*)\)\s*VALUES\s*\(([^)]*)\)\s*ON\s+CONFLICT\s*\(([^)]*)\)(?:\s+WHERE\s+(.*?))?\s+DO\s+(?:UPDATE\s+SET\s+(.*)|NOTHING)\s*;?\s*$`)
	ignoreInsert   = regexp.MustCompile(`(?is)^\s*INSERT\s+OR\s+IGNORE\s+INTO\s+([a-z_][a-z0-9_]*)\s*\(([^)]*)\)\s*VALUES\s*\(([^)]*)\)\s*;?\s*$`)
	plainInsert    = regexp.MustCompile(`(?is)^\s*INSERT\s+INTO\s+([a-z_][a-z0-9_]*)\s*\(([^)]*)\)\s*VALUES\s*\(([^)]*)\)`)
	tableRef       = regexp.MustCompile(`(?i)\b(FROM|JOIN|INTO)\s+([a-z_][a-z0-9_]*)\b`)
	question       = regexp.MustCompile(`\?`)
)

func wrapTx(ctx context.Context, tx *sql.Tx, dialect string) sqlExecutor {
	if dialect == "sqlserver" {
		return &mssqlTx{tx: tx, ctx: ctx}
	}
	return tx
}

func (tx *mssqlTx) Exec(query string, args ...any) (sql.Result, error) {
	query, args = rewriteSQLServer(query, args)
	return tx.tx.ExecContext(tx.ctx, query, args...)
}

func (tx *mssqlTx) upsertSourceAccount(account portfolio.Account) (int64, error) {
	var id int64
	err := tx.tx.QueryRowContext(tx.ctx, `
DECLARE @system_id TINYINT = (SELECT ext_system_id FROM ref.external_systems WHERE ext_system = N'TINVEST');
IF @system_id IS NULL THROW 51010, N'Источник TINVEST не зарегистрирован в ref.external_systems', 1;
MERGE portfolio.source_accounts WITH (HOLDLOCK) AS target
USING (SELECT @p1 AS external_account_id, @p2 AS name, @p3 AS account_type, @system_id AS ext_system_id) AS source
ON target.ext_system_id = source.ext_system_id AND target.external_account_id = source.external_account_id
WHEN MATCHED THEN UPDATE SET name = source.name, account_type = source.account_type
WHEN NOT MATCHED THEN INSERT (ext_system_id, external_account_id, name, account_type)
VALUES (source.ext_system_id, source.external_account_id, source.name, source.account_type)
OUTPUT INSERTED.source_account_id;`, account.ID, account.Name, account.Type).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("счёт T-Invest %s: %w", account.ID, err)
	}
	return id, nil
}

func (tx *mssqlTx) linkInstrument(uid, figi, ticker string) error {
	if uid == figi {
		uid = ""
	}
	var systemID int
	if err := tx.tx.QueryRowContext(tx.ctx, `SELECT ext_system_id FROM ref.external_systems WHERE ext_system = N'TINVEST'`).Scan(&systemID); err != nil {
		return fmt.Errorf("источник TINVEST в справочнике: %w", err)
	}
	var candidates []int64
	for _, code := range []struct {
		value string
		kind  int
		any   bool
	}{{uid, externalCodeTypeUID, false}, {figi, externalCodeTypeFIGI, true}, {ticker, externalCodeTypeInstrument, false}} {
		if strings.TrimSpace(code.value) == "" {
			continue
		}
		var targets []int64
		var err error
		if code.any {
			targets, err = tx.externalCodeTargetsAnySystem(code.kind, code.value)
		} else {
			targets, err = tx.externalCodeTargets(systemID, code.kind, code.value)
		}
		if err != nil {
			return fmt.Errorf("сопоставление кода T-Invest: %w", err)
		}
		if len(targets) > 1 {
			return nil
		}
		if len(targets) == 1 {
			candidates = append(candidates, targets[0])
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	refID := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate != refID {
			return nil
		}
	}
	localKey := localInstrumentKey(uid, figi)
	if _, err := tx.tx.ExecContext(tx.ctx, `UPDATE tinvest.instrument SET ref_instrument_id = @p1 WHERE uid = @p2`, refID, localKey); err != nil {
		return fmt.Errorf("связь T-Invest с общим инструментом: %w", err)
	}
	return nil
}

func (tx *mssqlTx) externalCodeTargets(systemID, codeType int, code string) ([]int64, error) {
	return tx.readExternalCodeTargets(`SELECT DISTINCT internal_id FROM ref.external_codes WHERE ext_system_id = @p1 AND ext_code_type_id = @p2 AND ext_code = @p3`, systemID, codeType, code)
}

func (tx *mssqlTx) externalCodeTargetsAnySystem(codeType int, code string) ([]int64, error) {
	return tx.readExternalCodeTargets(`SELECT DISTINCT internal_id FROM ref.external_codes WHERE ext_code_type_id = @p1 AND ext_code = @p2`, codeType, code)
}

func (tx *mssqlTx) readExternalCodeTargets(query string, args ...any) ([]int64, error) {
	rows, err := tx.tx.QueryContext(tx.ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0, 2)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (tx *mssqlTx) localLoadDate() (time.Time, error) {
	var loadDate time.Time
	if err := tx.tx.QueryRowContext(tx.ctx, `SELECT CAST(SYSDATETIMEOFFSET() AS date)`).Scan(&loadDate); err != nil {
		return time.Time{}, fmt.Errorf("дата SQL Server: %w", err)
	}
	return loadDate, nil
}

func (tx *mssqlTx) replacePortfolioPositions(accountID string, loadDate time.Time, positions []portfolio.Position) error {
	var sourceAccountID int64
	if err := tx.tx.QueryRowContext(tx.ctx, `SELECT source_account_id FROM portfolio.source_accounts sa
JOIN ref.external_systems es ON es.ext_system_id = sa.ext_system_id
WHERE es.ext_system = N'TINVEST' AND sa.external_account_id = @p1`, accountID).Scan(&sourceAccountID); err != nil {
		return fmt.Errorf("поиск счёта T-Invest %s: %w", accountID, err)
	}
	if _, err := tx.tx.ExecContext(tx.ctx, `DELETE FROM portfolio.positions WHERE load_date = @p1 AND source_account_id = @p2`, loadDate, sourceAccountID); err != nil {
		return fmt.Errorf("очистка дневных позиций T-Invest: %w", err)
	}
	for _, position := range positions {
		uid := localInstrumentKey(position.InstrumentUID, position.Figi)
		if uid == "" {
			continue
		}
		var refID sql.NullInt64
		if err := tx.tx.QueryRowContext(tx.ctx, `SELECT ref_instrument_id FROM tinvest.instrument WHERE uid = @p1`, uid).Scan(&refID); err != nil {
			return err
		}
		if !refID.Valid {
			continue
		}
		_, err := tx.tx.ExecContext(tx.ctx, `
MERGE portfolio.positions WITH (HOLDLOCK) AS target
USING (SELECT @p1 AS load_date, @p2 AS source_account_id, @p3 AS instrument_id, @p4 AS balance) AS source
ON target.load_date = source.load_date
	AND target.source_account_id = source.source_account_id
	AND target.instrument_id = source.instrument_id
WHEN MATCHED THEN UPDATE SET balance = source.balance
WHEN NOT MATCHED THEN INSERT (load_date, source_account_id, instrument_id, balance)
VALUES (source.load_date, source.source_account_id, source.instrument_id, source.balance);`, loadDate, sourceAccountID, refID.Int64, position.Quantity.String())
		if err != nil {
			return fmt.Errorf("запись общей позиции %s: %w", position.Ticker, err)
		}
	}
	return nil
}

func nullableInt64(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func localInstrumentKey(uid, figi string) string {
	if uid != "" {
		return uid
	}
	return figi
}

func rewriteSQLServer(query string, args []any) (string, []any) {
	if match := ignoreInsert.FindStringSubmatch(query); match != nil && strings.EqualFold(match[1], "operation") {
		query = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (account_id, executed_at, type, instrument_uid, payment_amount, payment_currency, quantity) DO NOTHING", match[1], match[2], match[3])
	}
	if match := conflictInsert.FindStringSubmatch(query); match != nil {
		return rewriteMerge(match, args)
	}
	argColumns := insertArgumentColumns(plainInsert.FindStringSubmatch(query))
	query = tableRef.ReplaceAllStringFunc(query, func(value string) string {
		parts := tableRef.FindStringSubmatch(value)
		return parts[1] + " tinvest." + parts[2]
	})
	argIndex := 0
	query = question.ReplaceAllStringFunc(query, func(string) string {
		argIndex++
		return fmt.Sprintf("@p%d", argIndex)
	})
	return query, normalizeArgs(args, argColumns)
}

func rewriteMerge(match []string, args []any) (string, []any) {
	table := match[1]
	columns := splitSQLList(match[2])
	values := splitSQLList(match[3])
	keys := splitSQLList(match[4])
	filter := strings.TrimSpace(match[5])
	updates := strings.TrimSpace(match[6])
	if len(columns) != len(values) {
		return match[0], args
	}
	var query strings.Builder
	fmt.Fprintf(&query, "MERGE tinvest.%s WITH (HOLDLOCK) AS target USING (VALUES (", table)
	for i, value := range values {
		if i > 0 {
			query.WriteString(", ")
		}
		query.WriteString(value)
	}
	query.WriteString(")) AS source (")
	query.WriteString(strings.Join(columns, ", "))
	query.WriteString(") ON ")
	for i, key := range keys {
		if i > 0 {
			query.WriteString(" AND ")
		}
		fmt.Fprintf(&query, "target.%s = source.%s", key, key)
	}
	if filter != "" {
		query.WriteString(" AND ")
		query.WriteString(qualifyTargetFilter(filter))
	}
	if updates != "" {
		query.WriteString(" WHEN MATCHED THEN UPDATE SET ")
		assignments := strings.Split(updates, ",")
		for i, item := range assignments {
			parts := strings.SplitN(item, "=", 2)
			if len(parts) != 2 {
				return match[0], args
			}
			if i > 0 {
				query.WriteString(", ")
			}
			rhs := strings.ReplaceAll(strings.TrimSpace(parts[1]), "excluded.", "source.")
			rhs = strings.ReplaceAll(rhs, table+".", "target.")
			fmt.Fprintf(&query, "target.%s = %s", strings.TrimSpace(parts[0]), rhs)
		}
	}
	query.WriteString(" WHEN NOT MATCHED THEN INSERT (")
	query.WriteString(strings.Join(columns, ", "))
	query.WriteString(") VALUES (")
	for i, column := range columns {
		if i > 0 {
			query.WriteString(", ")
		}
		fmt.Fprintf(&query, "source.%s", column)
	}
	query.WriteString(");")
	argColumns := make([]string, 0, len(args))
	for i, value := range values {
		if value == "?" {
			argColumns = append(argColumns, columns[i])
		}
	}
	return bindQuestionMarks(query.String()), normalizeArgs(args, argColumns)
}

func insertArgumentColumns(match []string) []string {
	if match == nil {
		return nil
	}
	columns := splitSQLList(match[2])
	values := splitSQLList(match[3])
	out := make([]string, 0, len(values))
	for i, value := range values {
		if value == "?" && i < len(columns) {
			out = append(out, columns[i])
		}
	}
	return out
}

func splitSQLList(value string) []string {
	parts := strings.Split(value, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func qualifyTargetFilter(filter string) string {
	for _, column := range []string{"trade_id", "trade_num", "uid", "news_id", "alfa_two", "alfa_three"} {
		filter = regexp.MustCompile(`(?i)\b`+column+`\b`).ReplaceAllString(filter, "target."+column)
	}
	return filter
}

func bindQuestionMarks(query string) string {
	index := 0
	return question.ReplaceAllStringFunc(query, func(string) string {
		index++
		return fmt.Sprintf("@p%d", index)
	})
}

func normalizeArgs(args []any, columns []string) []any {
	for i, value := range args {
		text, ok := value.(string)
		if !ok {
			continue
		}
		if len(columns) > i && isDecimalColumn(columns[i]) && text == "" {
			args[i] = nil
			continue
		}
		if len(columns) > i && isDateTimeColumn(columns[i]) && text == "" {
			args[i] = nil
			continue
		}
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			args[i] = parsed
			continue
		}
		if parsed, err := time.Parse("2006-01-02", text); err == nil {
			args[i] = parsed
		}
	}
	return args
}

func isDecimalColumn(column string) bool {
	switch strings.ToLower(column) {
	case "amount", "quantity", "current_amount", "nkd_amount", "market_amount", "last_price", "close_price", "expected_yield", "price_amount", "nominal_amount", "pay_amount", "value", "percent", "percentage", "commission", "broker_commission", "exchange_commission":
		return true
	default:
		return false
	}
}

func isDateTimeColumn(column string) bool {
	switch strings.ToLower(column) {
	case "checked_at", "executed_at", "received_at", "event_at", "ordered_at", "accrued_at", "published_at", "disclosure_at", "first_1min", "first_1day", "start_at", "end_at", "pay_date", "record_date", "payment_date", "event_date", "day", "maturity", "expire_date":
		return true
	default:
		return false
	}
}
