package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"t-invest-portfolio/internal/portfolio"

	"github.com/shopspring/decimal"
)

func TestCurrentStateReplacesByKeyAndScope(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	when := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	if err := db.ReplaceCurrent(ctx, "position", "account-a", []Record{
		{Key: "uid-a", At: when, Payload: map[string]int{"quantity": 2}},
		{Key: "uid-b", At: when, Payload: map[string]int{"quantity": 4}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceCurrent(ctx, "position", "account-b", []Record{{Key: "uid-a", At: when, Payload: map[string]int{"quantity": 9}}}); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceCurrent(ctx, "position", "account-a", []Record{{Key: "uid-a", At: when.Add(time.Minute), Payload: map[string]int{"quantity": 5}}}); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM current_state WHERE entity='position'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("rows = %d, want 2", rows)
	}
	var payload string
	if err := db.sql.QueryRow(`SELECT payload FROM current_state WHERE entity='position' AND scope_key='account-a' AND entity_key='uid-a'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var value map[string]int
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		t.Fatal(err)
	}
	if value["quantity"] != 5 {
		t.Fatalf("payload = %s", payload)
	}
}

func TestHistoryUsesEventKeyAndDate(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	when := time.Date(2026, 11, 11, 18, 15, 0, 0, time.UTC)
	for _, amount := range []int{11, 12} {
		if err := db.UpsertHistory(ctx, "coupon", "instrument-a", "event-1", when, map[string]int{"amount": amount}); err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM history_record`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d, want 1", rows)
	}
	var eventAt string
	var payload string
	if err := db.sql.QueryRow(`SELECT event_at,payload FROM history_record WHERE record_key='event-1'`).Scan(&eventAt, &payload); err != nil {
		t.Fatal(err)
	}
	if eventAt != when.Format(time.RFC3339Nano) || payload != `{"amount":12}` {
		t.Fatalf("event = %s %s", eventAt, payload)
	}
}

func TestOperationUpsertUsesRelationalRowsAndReplacesFills(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	when := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	op := portfolio.Operation{
		At: when, Type: "BUY", InstrumentUID: "uid-1", Quantity: 2,
		Payment: portfolio.Money{Amount: decimal.NewFromInt(100), Currency: "RUB"},
		Fills:   []portfolio.Fill{{TradeNum: "fill-1", AccountID: "account-a", At: when, Direction: "BUY", Price: portfolio.Money{Amount: decimal.NewFromInt(50), Currency: "RUB"}, Quantity: 1}},
	}
	if err := db.UpsertHistory(ctx, "operation", "account-a", "operation-key", when, op); err != nil {
		t.Fatal(err)
	}
	op.Name = "corrected"
	op.Fills = nil
	if err := db.UpsertHistory(ctx, "operation", "account-a", "operation-key", when, op); err != nil {
		t.Fatal(err)
	}
	var historyRows, operationRows, fillRows int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM history_record WHERE entity='operation'`).Scan(&historyRows); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM operation_record WHERE operation_key='operation-key' AND name='corrected'`).Scan(&operationRows); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM operation_fill WHERE operation_key='operation-key'`).Scan(&fillRows); err != nil {
		t.Fatal(err)
	}
	if historyRows != 0 || operationRows != 1 || fillRows != 0 {
		t.Fatalf("history=%d operation=%d fills=%d, want 0, 1, 0", historyRows, operationRows, fillRows)
	}
}

func TestInsiderDealUpsertUsesRelationalTable(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	deal := portfolio.InsiderDeal{
		TradeID: 77, InstrumentUID: "uid-2", Direction: "BUY", Currency: "RUB",
		At: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC), Quantity: 3,
		Price: portfolio.Money{Amount: decimal.NewFromInt(50), Currency: "RUB"}, Ticker: "ABC",
		InvestorName: "Investor", InvestorPosition: "Officer", Percentage: "0.25",
		OptionExecution: true, Disclosure: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC),
	}
	if err := db.UpsertHistory(ctx, "insider_deal", "uid-2", "insider-key", deal.At, deal); err != nil {
		t.Fatal(err)
	}
	var historyRows, dealRows int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM history_record WHERE entity='insider_deal'`).Scan(&historyRows); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM insider_deal_record WHERE record_key='insider-key' AND trade_id=77 AND percentage=0.25 AND option_execution=1`).Scan(&dealRows); err != nil {
		t.Fatal(err)
	}
	if historyRows != 0 || dealRows != 1 {
		t.Fatalf("history=%d insider_deal=%d, want 0, 1", historyRows, dealRows)
	}
}

func TestSQLiteOpenMigratesLegacyHistoryOutOfJSONTable(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	operation := portfolio.Operation{
		At: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC), Type: "BUY", Quantity: 1,
		Payment: portfolio.Money{Amount: decimal.NewFromInt(25), Currency: "RUB"},
	}
	encoded, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO history_record(entity,scope_key,record_key,event_at,payload) VALUES('operation','account-a','legacy-op',?,?)`, operation.At.Format(time.RFC3339Nano), string(encoded)); err != nil {
		t.Fatal(err)
	}
	if err := migrateSQLiteHistory(ctx, db.sql); err != nil {
		t.Fatal(err)
	}
	var legacyRows, normalizedRows int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM history_record WHERE entity='operation'`).Scan(&legacyRows); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM operation_record WHERE operation_key='legacy-op'`).Scan(&normalizedRows); err != nil {
		t.Fatal(err)
	}
	if legacyRows != 0 || normalizedRows != 1 {
		t.Fatalf("legacy=%d normalized=%d, want 0, 1", legacyRows, normalizedRows)
	}
}

func TestSchemaHasNoSnapshotTables(t *testing.T) {
	db := openTemp(t)
	var count int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND lower(name) LIKE '%snapshot%'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("snapshot tables = %d", count)
	}
}

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "portfolio.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
