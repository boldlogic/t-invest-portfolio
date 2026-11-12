package store

import (
	"strings"
	"testing"
)

func TestRewriteSQLServerUpsert(t *testing.T) {
	query := `INSERT INTO instrument(uid, figi, ticker) VALUES(?, ?, ?)
ON CONFLICT(uid) DO UPDATE SET
	figi=CASE WHEN excluded.figi<>'' THEN excluded.figi ELSE instrument.figi END,
	ticker=CASE WHEN excluded.ticker<>'' THEN excluded.ticker ELSE instrument.ticker END`
	got, args := rewriteSQLServer(query, []any{"uid-1", "figi-1", "TICK"})
	for _, want := range []string{
		"MERGE tinvest.instrument WITH (HOLDLOCK)",
		"target.figi = CASE WHEN source.figi<>'' THEN source.figi ELSE target.figi END",
		"target.ticker = CASE WHEN source.ticker<>'' THEN source.ticker ELSE target.ticker END",
		"@p1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("запрос не содержит %q: %s", want, got)
		}
	}
	if len(args) != 3 {
		t.Fatalf("параметров = %d", len(args))
	}
}

func TestRewriteSQLServerInsertIgnore(t *testing.T) {
	query := `INSERT OR IGNORE INTO operation(account_id, executed_at, type, instrument_uid, payment_amount, payment_currency, quantity, name)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`
	got, args := rewriteSQLServer(query, []any{"account", "2026-11-12T10:00:00Z", "buy", "uid", "1.25", "RUB", "2", "name"})
	for _, want := range []string{
		"MERGE tinvest.operation WITH (HOLDLOCK)",
		"target.account_id = source.account_id",
		"target.executed_at = source.executed_at",
		"WHEN NOT MATCHED THEN INSERT",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("query does not contain %q: %s", want, got)
		}
	}
	if strings.Contains(got, "WHEN MATCHED") {
		t.Fatalf("ignore insert must not update existing operation: %s", got)
	}
	if len(args) != 8 {
		t.Fatalf("args = %d, want 8", len(args))
	}
}

func TestRewriteSQLServerQualifiesPartialConflictFilter(t *testing.T) {
	query := `INSERT INTO fill(trade_num, account_id) VALUES(?, ?)
ON CONFLICT(trade_num) WHERE trade_num <> '' DO UPDATE SET account_id=excluded.account_id`
	got, _ := rewriteSQLServer(query, []any{"trade-1", "account"})
	if !strings.Contains(got, "AND target.trade_num <> ''") {
		t.Fatalf("partial conflict filter is not qualified: %s", got)
	}
}

func TestRewriteSQLServerScopesTablesAndNullsOptionalNumbers(t *testing.T) {
	query := `INSERT INTO position(instrument_uid, current_amount, trading_status) VALUES(?, ?, ?)`
	got, args := rewriteSQLServer(query, []any{"uid-1", "", ""})
	if !strings.Contains(got, "INSERT INTO tinvest.position") {
		t.Fatalf("таблица не квалифицирована: %s", got)
	}
	if args[1] != nil {
		t.Fatalf("пустая сумма должна стать NULL, получено %#v", args[1])
	}
	if args[2] != "" {
		t.Fatalf("пустой статус должен остаться строкой, получено %#v", args[2])
	}
}
