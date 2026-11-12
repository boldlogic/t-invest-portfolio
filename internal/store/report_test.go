package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"t-invest-portfolio/internal/brokerreport"
	"t-invest-portfolio/internal/portfolio"
)

func TestDecimalRatReadsSQLServerByteValues(t *testing.T) {
	got, ok := decimalRat([]byte("123.4500"))
	if !ok {
		t.Fatal("SQL Server decimal bytes were not parsed")
	}
	want, _ := new(big.Rat).SetString("123.45")
	if got.Cmp(want) != 0 {
		t.Fatalf("parsed amount=%s, want %s", got.RatString(), want.RatString())
	}
}

func TestReportImportPersistsRawThenRetriesAndDeduplicates(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "broker-report-2025-06-01-2025-12-31.xlsx")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	book, err := brokerreport.ReadWorkbook(data)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	records, err := brokerreport.Normalize(book, hash)
	if err != nil {
		t.Fatal(err)
	}
	db := openTemp(t)
	ctx := context.Background()

	_, err = db.ImportReport(ctx, filepath.Base(path), hash, "xlsx", book)
	if err == nil {
		t.Fatal("import without account mapping unexpectedly succeeded")
	}
	var rawCount int
	if err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM report_raw_row").Scan(&rawCount); err != nil {
		t.Fatal(err)
	}
	if rawCount == 0 {
		t.Fatal("raw report rows were not retained")
	}

	if _, err := db.sql.ExecContext(ctx, "INSERT INTO report_account_mapping(report_account,account_id) VALUES(?,?)", records.ReportAccount, "api-account"); err != nil {
		t.Fatal(err)
	}
	firstTrade := records.Trades[0]
	price, err := decimal.NewFromString(firstTrade.Price)
	if err != nil {
		t.Fatal(err)
	}
	quantity, err := decimal.NewFromString(firstTrade.Quantity)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertOperation(ctx, "api-account", "api-operation-before-report", portfolio.Operation{
		At: firstTrade.At, Type: "OPERATION_TYPE_BUY", Quantity: quantity.IntPart(),
		Payment: portfolio.Money{Amount: price.Mul(quantity), Currency: firstTrade.Currency},
		Fills:   []portfolio.Fill{{TradeNum: firstTrade.Number, AccountID: "api-account", At: firstTrade.At, Ticker: firstTrade.Ticker, Direction: firstTrade.Side, Price: portfolio.Money{Amount: price, Currency: firstTrade.Currency}, Quantity: quantity.IntPart()}},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := db.ImportReport(ctx, filepath.Base(path), hash, "xlsx", book)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.Trades == 0 || result.Operations == 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	var trades, operations, imports int
	if err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM trade_record").Scan(&trades); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM operation_record WHERE report_authoritative=1").Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM report_import").Scan(&imports); err != nil {
		t.Fatal(err)
	}
	if trades != result.Trades || operations == 0 || imports != 1 {
		t.Fatalf("trades=%d report operations=%d imports=%d result=%+v", trades, operations, imports, result)
	}
	var authoritative int
	if err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM trade_record WHERE account_id=? AND trade_number=? AND report_authoritative=1", "api-account", firstTrade.Number).Scan(&authoritative); err != nil {
		t.Fatal(err)
	}
	if authoritative != 1 {
		t.Fatalf("API fill and report trade were not merged: rows=%d", authoritative)
	}

	again, err := db.ImportReport(ctx, filepath.Base(path), hash, "xlsx", book)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Duplicate || again.ImportID != result.ImportID {
		t.Fatalf("repeat import not reported as duplicate: %+v", again)
	}
	var after int
	if err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM trade_record").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != trades {
		t.Fatalf("repeat import created trade rows: before=%d after=%d", trades, after)
	}

	var reportKey, reportType, reportName, atText, currency string
	var amountText string
	if err := db.sql.QueryRowContext(ctx, "SELECT operation_key,type,name,executed_at,payment_amount,payment_currency FROM operation_record WHERE report_authoritative=1 LIMIT 1").Scan(&reportKey, &reportType, &reportName, &atText, &amountText, &currency); err != nil {
		t.Fatal(err)
	}
	at, err := time.Parse(time.RFC3339Nano, atText)
	if err != nil {
		t.Fatal(err)
	}
	reportAmount, err := decimal.NewFromString(amountText)
	if err != nil {
		t.Fatal(err)
	}
	var operationsBefore int
	if err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM operation_record").Scan(&operationsBefore); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertOperation(ctx, "api-account", "api-operation-after-report", portfolio.Operation{
		At: at, Type: reportType, Name: "API enrichment", Payment: portfolio.Money{Amount: reportAmount, Currency: currency},
	}); err != nil {
		t.Fatal(err)
	}
	var operationsAfter int
	if err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM operation_record").Scan(&operationsAfter); err != nil {
		t.Fatal(err)
	}
	if operationsAfter != operationsBefore {
		t.Fatalf("API operation was not merged: before=%d after=%d", operationsBefore, operationsAfter)
	}
	var nameAfter string
	if err := db.sql.QueryRowContext(ctx, "SELECT name FROM operation_record WHERE operation_key=?", reportKey).Scan(&nameAfter); err != nil {
		t.Fatal(err)
	}
	if reportName != "" && nameAfter != reportName {
		t.Fatalf("API overwrote report name: before=%q after=%q", reportName, nameAfter)
	}
}

func TestAmbiguousCashOperationStaysSeparateAndIsReported(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	at := time.Date(2026, 6, 3, 12, 30, 0, 0, time.UTC)
	for _, key := range []string{"api-op-a", "api-op-b"} {
		err := db.UpsertOperation(ctx, "account-a", key, portfolio.Operation{
			At: at, Type: "OPERATION_TYPE_INPUT",
			Payment: portfolio.Money{Amount: decimal.NewFromInt(50), Currency: "RUB"},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	matched, ambiguous, err := db.upsertReportOperation(ctx, tx, "account-a", 1, at, "report-hash", brokerreport.Operation{
		Type: "input", RawType: "Пополнение счета", Currency: "RUB", Amount: "50", Key: "row-key", At: at, Sheet: "broker_rep", Row: 12,
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if matched || !ambiguous {
		t.Fatalf("matched=%t ambiguous=%t", matched, ambiguous)
	}
	var count int
	if err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM operation_record WHERE account_id='account-a'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("cash operation rows=%d, want 3 including separate report row", count)
	}
}

func TestReportTradeKeepsNewestPeriodAndSeparatesRepoLegs(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newer := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	older := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	trade := brokerreport.Trade{Number: "repo-42", RepoPart: "1", Ticker: "NEW", Name: "new", Side: "BUY", Currency: "RUB", Quantity: "1", Price: "10", Amount: "10", At: newer}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.upsertReportTrade(ctx, tx, "account-a", 101, newer, trade); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	trade.Ticker, trade.Name, trade.Price, trade.Amount = "OLD", "old", "9", "9"
	tx, err = db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.upsertReportTrade(ctx, tx, "account-a", 102, older, trade); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	trade.RepoPart = "2"
	if err := db.upsertReportTrade(ctx, tx, "account-a", 103, newer, trade); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM trade_record WHERE account_id='account-a' AND trade_number='repo-42'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("repo trade legs=%d, want 2", count)
	}
	var ticker string
	if err := db.sql.QueryRowContext(ctx, "SELECT ticker FROM trade_record WHERE account_id='account-a' AND trade_number='repo-42' AND repo_part=1").Scan(&ticker); err != nil {
		t.Fatal(err)
	}
	if ticker != "NEW" {
		t.Fatalf("older report replaced newest ticker: %s", ticker)
	}
}

func TestReportISINFallbackMergesExactAPITrade(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	at := time.Date(2025, 8, 4, 10, 15, 0, 0, time.UTC)
	fill := portfolio.Fill{TradeNum: "api-number", At: at, ISIN: "US0378331005", Ticker: "AAPL", Direction: "BUY", Price: portfolio.Money{Amount: decimal.NewFromInt(210), Currency: "USD"}, Quantity: 2}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.upsertAPITrade(ctx, tx, "account-a", portfolio.Operation{At: at}, fill); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tr := brokerreport.Trade{Number: "report-number", Ticker: "", ISIN: "US0378331005", Name: "Apple Inc.", Side: "BUY", Currency: "USD", Quantity: "2", Price: "210", Amount: "420", At: at}
	tx, err = db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	matched, ambiguous, err := db.upsertReportTradeWithMatch(ctx, tx, "account-a", 1, at, tr)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !matched || ambiguous {
		t.Fatalf("matched=%t ambiguous=%t", matched, ambiguous)
	}
	var count int
	var isin, ticker, number string
	var authoritative int
	if err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*),MAX(isin),MAX(ticker),MAX(trade_number),MAX(report_authoritative) FROM trade_record WHERE account_id=?", "account-a").Scan(&count, &isin, &ticker, &number, &authoritative); err != nil {
		t.Fatal(err)
	}
	if count != 1 || isin != "US0378331005" || ticker != "AAPL" || number != "report-number" || authoritative != 1 {
		t.Fatalf("count=%d isin=%q ticker=%q number=%q authoritative=%d", count, isin, ticker, number, authoritative)
	}
}

func TestAPIFillAfterReportMergesByISINWithoutOverwritingReport(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	at := time.Date(2025, 8, 4, 10, 15, 0, 0, time.UTC)
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tr := brokerreport.Trade{Number: "report-number", ISIN: "US0378331005", Ticker: "AAPL", Name: "Apple Inc.", Side: "BUY", Currency: "USD", Quantity: "2", Price: "210", Amount: "420", At: at}
	if err := db.upsertReportTrade(ctx, tx, "account-a", 1, at, tr); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	fill := portfolio.Fill{TradeNum: "api-number", At: at, ISIN: "US0378331005", Direction: "BUY", Price: portfolio.Money{Amount: decimal.NewFromInt(210), Currency: "USD"}, Quantity: 2}
	if err := db.upsertAPITrade(ctx, tx, "account-a", portfolio.Operation{At: at}, fill); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	var number, name string
	if err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*),MAX(trade_number),MAX(name) FROM trade_record WHERE account_id=?", "account-a").Scan(&count, &number, &name); err != nil {
		t.Fatal(err)
	}
	if count != 1 || number != "report-number" || name != "Apple Inc." {
		t.Fatalf("count=%d number=%q name=%q", count, number, name)
	}
}

func TestAPIISINReturnedInTickerIsStoredInFallbackColumn(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	at := time.Date(2025, 8, 4, 10, 15, 0, 0, time.UTC)
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	fill := portfolio.Fill{TradeNum: "api-number", At: at, Ticker: "us0378331005", Direction: "BUY", Price: portfolio.Money{Amount: decimal.NewFromInt(210), Currency: "USD"}, Quantity: 2}
	if err := db.upsertAPITrade(ctx, tx, "account-a", portfolio.Operation{At: at}, fill); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var ticker, isin string
	if err := db.sql.QueryRowContext(ctx, "SELECT ticker,isin FROM trade_record WHERE account_id=? AND trade_number=?", "account-a", "api-number").Scan(&ticker, &isin); err != nil {
		t.Fatal(err)
	}
	if ticker != "" || isin != "US0378331005" {
		t.Fatalf("ticker=%q isin=%q", ticker, isin)
	}
}

func TestReportISINFallbackLeavesAmbiguousAPIFillsSeparate(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	at := time.Date(2025, 8, 4, 10, 15, 0, 0, time.UTC)
	for _, number := range []string{"api-number-a", "api-number-b"} {
		tx, err := db.sql.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		fill := portfolio.Fill{TradeNum: number, At: at, ISIN: "US0378331005", Ticker: "AAPL", Direction: "BUY", Price: portfolio.Money{Amount: decimal.NewFromInt(210), Currency: "USD"}, Quantity: 2}
		if err := db.upsertAPITrade(ctx, tx, "account-a", portfolio.Operation{At: at}, fill); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tr := brokerreport.Trade{Number: "report-number", ISIN: "US0378331005", Side: "BUY", Currency: "USD", Quantity: "2", Price: "210", Amount: "420", At: at}
	matched, ambiguous, err := db.upsertReportTradeWithMatch(ctx, tx, "account-a", 1, at, tr)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM trade_record WHERE account_id=?", "account-a").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if matched || !ambiguous || count != 3 {
		t.Fatalf("matched=%t ambiguous=%t rows=%d", matched, ambiguous, count)
	}
}
