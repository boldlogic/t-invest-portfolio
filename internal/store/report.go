package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"t-invest-portfolio/internal/brokerreport"
	"t-invest-portfolio/internal/portfolio"
)

var isinCodePattern = regexp.MustCompile(`(?i)^[A-Z]{2}[A-Z0-9]{9}[0-9]$`)

type ReportImportResult struct {
	ImportID   int64  `json:"importId"`
	Status     string `json:"status"`
	Rows       int    `json:"rows"`
	Trades     int    `json:"trades"`
	Operations int    `json:"operations"`
	Warnings   int    `json:"warnings"`
	Duplicate  bool   `json:"duplicate"`
	Error      string `json:"error,omitempty"`
}

var ErrReportAccountMapping = errors.New("не настроено соответствие счёта отчёта")

func (db *DB) failReportFailure(ctx context.Context, result ReportImportResult, cause error) (ReportImportResult, error) {
	result.Status = "failed"
	result.Error = cause.Error()
	return result, db.failReportImport(ctx, result.ImportID, cause)
}

func (db *DB) ImportReport(ctx context.Context, fileName, hash, format string, book brokerreport.Workbook) (ReportImportResult, error) {
	result, process, err := db.beginReportImport(ctx, fileName, hash, format, book)
	if err != nil || !process {
		return result, err
	}
	records, err := brokerreport.Normalize(book, hash)
	if err != nil {
		return db.failReportFailure(ctx, result, fmt.Errorf("%w: %v", brokerreport.ErrInvalidReport, err))
	}
	accountID, err := db.reportAccountID(ctx, records.ReportAccount)
	if err != nil {
		return db.failReportFailure(ctx, result, err)
	}
	if accountID == "" {
		return db.failReportFailure(ctx, result, fmt.Errorf("%w: для номера счёта отчёта %q нет соответствия account_id в tinvest.report_account_mapping", ErrReportAccountMapping, records.ReportAccount))
	}
	if err := db.updateReportHeader(ctx, result.ImportID, accountID, records); err != nil {
		return db.failReportFailure(ctx, result, err)
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	warnings := 0
	for _, tr := range records.Trades {
		_, ambiguous, err := db.upsertReportTradeWithMatch(ctx, tx, accountID, result.ImportID, records.PeriodEnd, tr)
		if ambiguous {
			warnings++
		}
		if err != nil {
			_ = tx.Rollback()
			return db.failReportFailure(ctx, result, err)
		}
	}
	for _, op := range records.Operations {
		_, ambiguous, err := db.upsertReportOperation(ctx, tx, accountID, result.ImportID, records.PeriodEnd, hash, op)
		if err != nil {
			_ = tx.Rollback()
			return db.failReportFailure(ctx, result, err)
		}
		if ambiguous {
			warnings++
		}
	}
	if err := db.finishReportImport(ctx, tx, result.ImportID, len(records.Trades), len(records.Operations), warnings); err != nil {
		_ = tx.Rollback()
		return db.failReportFailure(ctx, result, err)
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return db.failReportFailure(ctx, result, err)
	}
	result.Status, result.Trades, result.Operations, result.Warnings = "completed", len(records.Trades), len(records.Operations), warnings
	return result, nil
}

func (db *DB) RecordReportFailure(ctx context.Context, fileName, hash, format string, cause error) (ReportImportResult, error) {
	result, process, err := db.beginReportImport(ctx, fileName, hash, format, brokerreport.Workbook{})
	if err != nil || !process {
		return result, err
	}
	if err := db.failReportImport(ctx, result.ImportID, cause); err != nil {
		return result, err
	}
	result.Status = "failed"
	result.Error = cause.Error()
	return result, nil
}

func (db *DB) beginReportImport(ctx context.Context, fileName, hash, format string, book brokerreport.Workbook) (ReportImportResult, bool, error) {
	result := ReportImportResult{}
	rowCount := 0
	for _, s := range book.Sheets {
		rowCount += len(s.Rows)
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return result, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	q := "SELECT import_id,import_status,row_count,trade_count,operation_count,warning_count FROM report_import WHERE content_sha256=?"
	if db.dialect == "sqlserver" {
		q = "SELECT import_id,import_status,row_count,trade_count,operation_count,warning_count FROM tinvest.report_import WITH (UPDLOCK,HOLDLOCK) WHERE content_sha256=@p1"
	}
	err = tx.QueryRowContext(ctx, q, hash).Scan(&result.ImportID, &status, &result.Rows, &result.Trades, &result.Operations, &result.Warnings)
	process := false
	if err == nil {
		result.Status, result.Duplicate = status, true
		if status == "completed" {
			return result, false, nil
		}
		process = true
		if db.dialect == "sqlserver" {
			_, err = tx.ExecContext(ctx, "DELETE FROM tinvest.report_raw_row WHERE import_id=@p1; UPDATE tinvest.report_import SET import_status='processing',file_name=@p2,file_format=@p3,row_count=@p4,trade_count=0,operation_count=0,warning_count=0,error_text=NULL,finished_at=NULL WHERE import_id=@p1", result.ImportID, fileName, format, rowCount)
		} else {
			if _, err = tx.ExecContext(ctx, "DELETE FROM report_raw_row WHERE import_id=?", result.ImportID); err == nil {
				_, err = tx.ExecContext(ctx, "UPDATE report_import SET import_status='processing',file_name=?,file_format=?,row_count=?,trade_count=0,operation_count=0,warning_count=0,error_text='',finished_at='' WHERE import_id=?", fileName, format, rowCount, result.ImportID)
			}
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		process = true
		if db.dialect == "sqlserver" {
			err = tx.QueryRowContext(ctx, "INSERT INTO tinvest.report_import(content_sha256,file_name,file_format,import_status,row_count) OUTPUT INSERTED.import_id VALUES(@p1,@p2,@p3,'processing',@p4)", hash, fileName, format, rowCount).Scan(&result.ImportID)
		} else {
			err = tx.QueryRowContext(ctx, "INSERT INTO report_import(content_sha256,file_name,file_format,import_status,row_count) VALUES(?,?,?,'processing',?) RETURNING import_id", hash, fileName, format, rowCount).Scan(&result.ImportID)
		}
	} else {
		return result, false, err
	}
	if err != nil {
		return result, false, fmt.Errorf("создать импорт отчёта: %w", err)
	}
	for _, sheet := range book.Sheets {
		for i, row := range sheet.Rows {
			raw, e := json.Marshal(row)
			if e != nil {
				return result, false, e
			}
			if db.dialect == "sqlserver" {
				_, err = tx.ExecContext(ctx, "INSERT INTO tinvest.report_raw_row(import_id,sheet_name,row_number,raw_values) VALUES(@p1,@p2,@p3,@p4)", result.ImportID, sheet.Name, i+1, string(raw))
			} else {
				_, err = tx.ExecContext(ctx, "INSERT INTO report_raw_row(import_id,sheet_name,row_number,raw_values) VALUES(?,?,?,?)", result.ImportID, sheet.Name, i+1, string(raw))
			}
			if err != nil {
				return result, false, fmt.Errorf("сохранить raw строку %s:%d: %w", sheet.Name, i+1, err)
			}
		}
	}
	result.Status, result.Rows = "processing", rowCount
	if err = tx.Commit(); err != nil {
		return result, false, err
	}
	return result, process, nil
}

func (db *DB) reportAccountID(ctx context.Context, reportAccount string) (string, error) {
	q := "SELECT account_id FROM report_account_mapping WHERE report_account=?"
	if db.dialect == "sqlserver" {
		q = "SELECT account_id FROM tinvest.report_account_mapping WHERE report_account=@p1"
	}
	var id string
	err := db.sql.QueryRowContext(ctx, q, reportAccount).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}
func (db *DB) updateReportHeader(ctx context.Context, id int64, account string, r brokerreport.Records) error {
	q := "UPDATE report_import SET report_account=?,account_id=?,period_start=?,period_end=? WHERE import_id=?"
	args := []any{r.ReportAccount, account, r.PeriodStart.Format("2006-01-02"), r.PeriodEnd.Format("2006-01-02"), id}
	if db.dialect == "sqlserver" {
		q = "UPDATE tinvest.report_import SET report_account=@p1,account_id=@p2,period_start=@p3,period_end=@p4 WHERE import_id=@p5"
		args = []any{r.ReportAccount, account, r.PeriodStart, r.PeriodEnd, id}
	}
	_, err := db.sql.ExecContext(ctx, q, args...)
	return err
}
func (db *DB) failReportImport(ctx context.Context, id int64, cause error) error {
	msg := cause.Error()
	if len([]rune(msg)) > 2000 {
		msg = string([]rune(msg)[:2000])
	}
	q := "UPDATE report_import SET import_status='failed',error_text=?,finished_at=CURRENT_TIMESTAMP WHERE import_id=?"
	args := []any{msg, id}
	if db.dialect == "sqlserver" {
		q = "UPDATE tinvest.report_import SET import_status='failed',error_text=@p1,finished_at=SYSDATETIMEOFFSET() WHERE import_id=@p2"
	}
	_, err := db.sql.ExecContext(ctx, q, args...)
	if err != nil {
		return errors.Join(cause, err)
	}
	return cause
}
func (db *DB) finishReportImport(ctx context.Context, tx *sql.Tx, id int64, trades, ops, warnings int) error {
	q := "UPDATE report_import SET import_status='completed',trade_count=?,operation_count=?,warning_count=?,error_text='',finished_at=CURRENT_TIMESTAMP WHERE import_id=?"
	args := []any{trades, ops, warnings, id}
	if db.dialect == "sqlserver" {
		q = "UPDATE tinvest.report_import SET import_status='completed',trade_count=@p1,operation_count=@p2,warning_count=@p3,error_text=NULL,finished_at=SYSDATETIMEOFFSET() WHERE import_id=@p4"
	}
	_, err := tx.ExecContext(ctx, q, args...)
	return err
}

func tradeEventKey(account, number string, part int) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{account, strings.TrimSpace(number), fmt.Sprint(part)}, "|")))
	return hex.EncodeToString(sum[:])
}

func (db *DB) upsertReportTrade(ctx context.Context, tx *sql.Tx, account string, importID int64, period time.Time, tr brokerreport.Trade) error {
	_, _, err := db.upsertReportTradeWithMatch(ctx, tx, account, importID, period, tr)
	return err
}

func (db *DB) upsertReportTradeWithMatch(ctx context.Context, tx *sql.Tx, account string, importID int64, period time.Time, tr brokerreport.Trade) (bool, bool, error) {
	part := 0
	if tr.RepoPart == "1" {
		part = 1
	} else if tr.RepoPart == "2" {
		part = 2
	}
	event := tradeEventKey(account, tr.Number, part)
	instrument := portfolio.Subscription{Ticker: tr.Ticker}
	if db.dialect == "sqlserver" && tr.ISIN != "" {
		resolved, err := db.resolveInstrumentByISIN(ctx, tx, tr.ISIN)
		if err != nil {
			return false, false, err
		}
		instrument = resolved
	}
	if db.dialect == "sqlserver" {
		candidate, count, err := db.findISINTradeCandidate(ctx, tx, account, period, tr, part)
		if err != nil {
			return false, false, err
		}
		if count == 1 {
			return true, false, db.mergeReportTradeByISIN(ctx, tx, candidate, account, importID, period, tr, instrument)
		}
		ambiguous := count > 1
		q := `MERGE tinvest.trade_record WITH (HOLDLOCK) AS t
USING (SELECT @p1 AS event_key,@p2 AS account_id,@p3 AS trade_number,@p4 AS repo_part,@p5 AS executed_at,@p6 AS instrument_uid,@p7 AS figi,@p8 AS ticker,@p9 AS isin,@p10 AS instrument_name,@p11 AS direction,@p12 AS price_amount,@p13 AS price_currency,@p14 AS quantity,@p15 AS amount,@p16 AS report_period_end,@p17 AS report_import_id) AS s
ON t.event_key=s.event_key
WHEN MATCHED AND (t.report_period_end IS NULL OR s.report_period_end>=t.report_period_end) THEN UPDATE SET instrument_uid=COALESCE(NULLIF(s.instrument_uid,N''),t.instrument_uid),figi=COALESCE(NULLIF(s.figi,N''),t.figi),ticker=COALESCE(NULLIF(s.ticker,N''),t.ticker),isin=COALESCE(NULLIF(s.isin,N''),t.isin),instrument_name=COALESCE(NULLIF(s.instrument_name,N''),t.instrument_name),direction=COALESCE(NULLIF(s.direction,N''),t.direction),executed_at=s.executed_at,price_amount=COALESCE(s.price_amount,t.price_amount),price_currency=COALESCE(NULLIF(s.price_currency,N''),t.price_currency),quantity=COALESCE(s.quantity,t.quantity),amount=COALESCE(s.amount,t.amount),report_authoritative=1,report_period_end=s.report_period_end,report_import_id=s.report_import_id
WHEN NOT MATCHED THEN INSERT(event_key,account_id,trade_number,repo_part,executed_at,instrument_uid,figi,ticker,isin,instrument_name,direction,price_amount,price_currency,quantity,amount,report_authoritative,report_period_end,report_import_id) VALUES(s.event_key,s.account_id,s.trade_number,s.repo_part,s.executed_at,s.instrument_uid,s.figi,s.ticker,s.isin,s.instrument_name,s.direction,s.price_amount,s.price_currency,s.quantity,s.amount,1,s.report_period_end,s.report_import_id);`
		_, err = tx.ExecContext(ctx, q, event, account, tr.Number, part, tr.At.UTC(), instrument.InstrumentUID, instrument.Figi, instrument.Ticker, tr.ISIN, tr.Name, tr.Side, tr.Price, tr.Currency, tr.Quantity, tr.Amount, period, importID)
		if err != nil {
			return false, ambiguous, err
		}
		return false, ambiguous, ensureSubscriptionTx(ctx, tx, db.dialect, instrument)
	}
	candidate, count, err := db.findISINTradeCandidate(ctx, tx, account, period, tr, part)
	if err != nil {
		return false, false, err
	}
	if count == 1 {
		return true, false, db.mergeReportTradeByISIN(ctx, tx, candidate, account, importID, period, tr, instrument)
	}
	ambiguous := count > 1
	q := "INSERT INTO trade_record(account_id,trade_number,repo_part,executed_at,instrument_uid,figi,ticker,isin,name,direction,price_amount,price_currency,quantity,amount,report_authoritative,report_period_end,report_import_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(account_id,trade_number,repo_part) DO UPDATE SET instrument_uid=COALESCE(NULLIF(excluded.instrument_uid,''),trade_record.instrument_uid),figi=COALESCE(NULLIF(excluded.figi,''),trade_record.figi),ticker=COALESCE(NULLIF(excluded.ticker,''),trade_record.ticker),isin=COALESCE(NULLIF(excluded.isin,''),trade_record.isin),name=COALESCE(NULLIF(excluded.name,''),trade_record.name),direction=COALESCE(NULLIF(excluded.direction,''),trade_record.direction),executed_at=excluded.executed_at,price_amount=COALESCE(excluded.price_amount,trade_record.price_amount),price_currency=COALESCE(NULLIF(excluded.price_currency,''),trade_record.price_currency),quantity=COALESCE(excluded.quantity,trade_record.quantity),amount=COALESCE(excluded.amount,trade_record.amount),report_authoritative=1,report_period_end=excluded.report_period_end,report_import_id=excluded.report_import_id WHERE trade_record.report_period_end='' OR excluded.report_period_end>=trade_record.report_period_end"
	_, err = tx.ExecContext(ctx, q, account, tr.Number, part, tr.At.UTC().Format(time.RFC3339Nano), instrument.InstrumentUID, instrument.Figi, instrument.Ticker, tr.ISIN, tr.Name, tr.Side, tr.Price, tr.Currency, tr.Quantity, tr.Amount, 1, period.Format("2006-01-02"), importID)
	if err != nil {
		return false, ambiguous, err
	}
	return false, ambiguous, ensureSubscriptionTx(ctx, tx, db.dialect, instrument)
}

// A report ISIN identifies the instrument, not an individual execution. Require
// an exact account/time/side/quantity/price/currency match and exactly one row.
func (db *DB) findISINTradeCandidate(ctx context.Context, tx *sql.Tx, account string, period time.Time, tr brokerreport.Trade, part int) (string, int, error) {
	if tr.ISIN == "" || part != 0 {
		return "", 0, nil
	}
	q := `SELECT event_key,executed_at,direction,price_amount,price_currency,quantity FROM tinvest.trade_record WHERE account_id=@p1 AND isin=@p2 AND repo_part=0 AND report_authoritative=0`
	args := []any{account, tr.ISIN}
	if db.dialect != "sqlserver" {
		q = `SELECT trade_number,executed_at,direction,price_amount,price_currency,quantity FROM trade_record WHERE account_id=? AND isin=? AND repo_part=0 AND report_authoritative=0`
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	wantPrice, ok := new(big.Rat).SetString(tr.Price)
	if !ok {
		return "", 0, nil
	}
	wantQty, ok := new(big.Rat).SetString(tr.Quantity)
	if !ok {
		return "", 0, nil
	}
	wantAt := tr.At.UTC()
	found, count := "", 0
	for rows.Next() {
		var key, direction, currency string
		var rawAt, rawPrice, rawQty any
		if err := rows.Scan(&key, &rawAt, &direction, &rawPrice, &currency, &rawQty); err != nil {
			return "", 0, err
		}
		at, validAt := reportTradeTime(rawAt)
		price, validPrice := decimalRat(rawPrice)
		qty, validQty := decimalRat(rawQty)
		if validAt && at.Equal(wantAt) && canonicalTradeSide(direction) == canonicalTradeSide(tr.Side) && strings.EqualFold(strings.TrimSpace(currency), strings.TrimSpace(tr.Currency)) && validPrice && price.Cmp(wantPrice) == 0 && validQty && qty.Cmp(wantQty) == 0 {
			found, count = key, count+1
		}
	}
	if err := rows.Err(); err != nil {
		return "", 0, err
	}
	return found, count, nil
}

func reportTradeTime(value any) (time.Time, bool) {
	switch v := value.(type) {
	case time.Time:
		return v.UTC(), true
	case string:
		t, err := time.Parse(time.RFC3339Nano, v)
		return t.UTC(), err == nil
	case []byte:
		t, err := time.Parse(time.RFC3339Nano, string(v))
		return t.UTC(), err == nil
	default:
		return time.Time{}, false
	}
}

func canonicalTradeSide(v string) string {
	n := strings.ToLower(strings.TrimSpace(v))
	if strings.Contains(n, "buy") || strings.Contains(n, "покуп") {
		return "buy"
	}
	if strings.Contains(n, "sell") || strings.Contains(n, "прод") {
		return "sell"
	}
	return n
}

func (db *DB) mergeReportTradeByISIN(ctx context.Context, tx *sql.Tx, eventKey, account string, importID int64, period time.Time, tr brokerreport.Trade, instrument portfolio.Subscription) error {
	q := `UPDATE tinvest.trade_record SET trade_number=@p1,ticker=COALESCE(NULLIF(@p2,N''),ticker),isin=@p3,instrument_uid=COALESCE(NULLIF(@p4,N''),instrument_uid),figi=COALESCE(NULLIF(@p5,N''),figi),instrument_name=COALESCE(NULLIF(@p6,N''),instrument_name),direction=COALESCE(NULLIF(@p7,N''),direction),price_amount=COALESCE(@p8,price_amount),price_currency=COALESCE(NULLIF(@p9,N''),price_currency),quantity=COALESCE(@p10,quantity),amount=COALESCE(@p11,amount),report_authoritative=1,report_period_end=@p12,report_import_id=@p13 WHERE event_key=@p14 AND (report_period_end IS NULL OR report_period_end<=@p12)`
	args := []any{tr.Number, instrument.Ticker, tr.ISIN, instrument.InstrumentUID, instrument.Figi, tr.Name, tr.Side, tr.Price, tr.Currency, tr.Quantity, tr.Amount, period, importID, eventKey}
	if db.dialect != "sqlserver" {
		q = `UPDATE trade_record SET trade_number=?,ticker=COALESCE(NULLIF(?,''),ticker),isin=?,instrument_uid=COALESCE(NULLIF(?,''),instrument_uid),figi=COALESCE(NULLIF(?,''),figi),name=COALESCE(NULLIF(?,''),name),direction=COALESCE(NULLIF(?,''),direction),price_amount=COALESCE(?,price_amount),price_currency=COALESCE(NULLIF(?,''),price_currency),quantity=COALESCE(?,quantity),amount=COALESCE(?,amount),report_authoritative=1,report_period_end=?,report_import_id=? WHERE account_id=? AND trade_number=? AND repo_part=0 AND (report_period_end='' OR report_period_end<=?)`
		args = []any{tr.Number, instrument.Ticker, tr.ISIN, instrument.InstrumentUID, instrument.Figi, tr.Name, tr.Side, tr.Price, tr.Currency, tr.Quantity, tr.Amount, period.Format("2006-01-02"), importID, account, eventKey, period.Format("2006-01-02")}
	}
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return err
	}
	return ensureSubscriptionTx(ctx, tx, db.dialect, instrument)
}

func (db *DB) resolveInstrumentByISIN(ctx context.Context, tx *sql.Tx, isin string) (portfolio.Subscription, error) {
	result := portfolio.Subscription{}
	q := `SELECT DISTINCT i.uid,i.figi,i.ticker FROM tinvest.instrument i JOIN ref.instruments r ON UPPER(r.isin)=@p1 WHERE r.instrument_id=i.ref_instrument_id OR EXISTS (SELECT 1 FROM ref.external_codes ec JOIN ref.external_systems es ON es.ext_system_id=ec.ext_system_id WHERE ec.internal_id=r.instrument_id AND es.ext_system=N'TINVEST' AND ec.ext_code IN (i.uid,i.figi,i.ticker))`
	rows, err := tx.QueryContext(ctx, q, strings.ToUpper(isin))
	if err != nil {
		return result, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var uid, figi, ticker string
		if err := rows.Scan(&uid, &figi, &ticker); err != nil {
			return result, err
		}
		result = portfolio.Subscription{InstrumentUID: uid, Figi: figi, Ticker: ticker}
		count++
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if count != 1 {
		return portfolio.Subscription{}, nil
	}
	return result, nil
}

func (db *DB) upsertReportOperation(ctx context.Context, tx *sql.Tx, account string, importID int64, period time.Time, hash string, op brokerreport.Operation) (bool, bool, error) {
	value, ok := new(big.Rat).SetString(op.Amount)
	if !ok {
		return false, false, fmt.Errorf("неверная сумма операции %s:%d", op.Sheet, op.Row)
	}
	query := "SELECT operation_key,type,name,payment_amount,payment_currency FROM operation_record WHERE account_id=? AND executed_at=?"
	if db.dialect == "sqlserver" {
		query = "SELECT operation_key,type,name,payment_amount,payment_currency FROM tinvest.operation WHERE account_id=@p1 AND executed_at=@p2"
	}
	at := op.At.UTC().Format(time.RFC3339Nano)
	if db.dialect == "sqlserver" {
		at = op.At.UTC().Format(time.RFC3339Nano)
	}
	rows, err := tx.QueryContext(ctx, query, account, at)
	if err != nil {
		return false, false, err
	}
	var key string
	count := 0
	for rows.Next() {
		var candidate, typ, name, currency string
		var amount any
		if err := rows.Scan(&candidate, &typ, &name, &amount, &currency); err != nil {
			rows.Close()
			return false, false, err
		}
		parsed, ok := decimalRat(amount)
		if ok && parsed.Cmp(value) == 0 && canonicalOperationType(typ, name) == op.Type {
			count++
			key = candidate
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, false, err
	}
	rows.Close()
	ambiguous := count > 1
	if count == 1 {
		q := "UPDATE operation_record SET normalized_type=?,report_authoritative=1,report_period_end=?,report_import_id=?,name=CASE WHEN name='' THEN ? ELSE name END WHERE operation_key=? AND (report_period_end='' OR report_period_end<=?)"
		args := []any{op.Type, period.Format("2006-01-02"), importID, op.Note, key}
		if db.dialect == "sqlserver" {
			q = "UPDATE tinvest.operation SET normalized_type=@p1,report_authoritative=1,report_period_end=@p2,report_import_id=@p3,name=CASE WHEN name IS NULL OR name=N'' THEN @p4 ELSE name END WHERE operation_key=@p5 AND (report_period_end IS NULL OR report_period_end<=@p2)"
			args = []any{op.Type, period, importID, op.Note, key}
		} else {
			args = append(args, period.Format("2006-01-02"))
		}
		_, err := tx.ExecContext(ctx, q, args...)
		return true, false, err
	}
	keyDigest := sha256.Sum256([]byte(hash + ":" + op.Key))
	key = "report:" + hex.EncodeToString(keyDigest[:])[:57]
	q := "INSERT INTO operation_record(operation_key,account_id,executed_at,type,normalized_type,name,state,figi,ticker,instrument_uid,payment_amount,payment_currency,quantity,report_authoritative,report_period_end,report_import_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"
	args := []any{key, account, op.At.UTC().Format(time.RFC3339Nano), op.RawType, op.Type, op.Note, "", "", "", "", op.Amount, op.Currency, 0, 1, period.Format("2006-01-02"), importID}
	if db.dialect == "sqlserver" {
		q = "INSERT INTO tinvest.operation(operation_key,account_id,executed_at,type,normalized_type,name,state,figi,ticker,instrument_uid,payment_amount,payment_currency,quantity,report_authoritative,report_period_end,report_import_id) VALUES(@p1,@p2,@p3,@p4,@p5,@p6,N'',N'',N'',N'',CONVERT(decimal(28,8),@p7),@p8,0,1,@p9,@p10)"
		args = []any{key, account, op.At.UTC(), op.RawType, op.Type, op.Note, op.Amount, op.Currency, period, importID}
	}
	_, err = tx.ExecContext(ctx, q, args...)
	return false, ambiguous, err
}

func canonicalOperationType(typ, name string) string {
	n := strings.ToLower(strings.TrimSpace(typ + " " + name))
	switch {
	case strings.Contains(n, "input") || strings.Contains(n, "deposit") || strings.Contains(n, "пополнение"):
		return "input"
	case strings.Contains(n, "output") || strings.Contains(n, "withdraw"):
		return "output"
	case strings.Contains(n, "tax") || strings.Contains(n, "налог"):
		return "tax"
	case strings.Contains(n, "fee") || strings.Contains(n, "commission") || strings.Contains(n, "комисси"):
		return "commission"
	case strings.Contains(n, "buy") || strings.Contains(n, "sell") || strings.Contains(n, "покупка/продажа"):
		return "trade_settlement"
	case strings.Contains(n, "dividend") || strings.Contains(n, "coupon") || strings.Contains(n, "выплат") || strings.Contains(n, "купон"):
		return "income"
	case strings.Contains(n, "transfer") || strings.Contains(n, "перевод"):
		return "transfer"
	default:
		return strings.ToLower(strings.Join(strings.Fields(typ), " "))
	}
}

// mergeAPIIntoReportOperation uses the user's exact type/time/amount key.
// It only returns a match when precisely one authoritative report row qualifies.
func (db *DB) mergeAPIIntoReportOperation(ctx context.Context, tx *sql.Tx, accountID string, apiKey string, operation portfolio.Operation) (string, int64, bool, error) {
	query := "SELECT operation_key,type,name,payment_amount,normalized_type,report_authoritative FROM operation_record WHERE account_id=? AND executed_at=?"
	at := operation.At.UTC().Format(time.RFC3339Nano)
	if db.dialect == "sqlserver" {
		query = "SELECT operation_id,operation_key,type,name,payment_amount,normalized_type,report_authoritative FROM tinvest.operation WHERE account_id=@p1 AND executed_at=@p2"
	}
	rows, err := tx.QueryContext(ctx, query, accountID, at)
	if err != nil {
		return "", 0, false, err
	}
	var foundKey string
	var foundID int64
	matches := 0
	apiAmount, ok := new(big.Rat).SetString(operation.Payment.Amount.String())
	if !ok {
		rows.Close()
		return "", 0, false, fmt.Errorf("неверная сумма API операции")
	}
	for rows.Next() {
		var id int64
		var key, typ, name, normalized string
		var amount any
		var authoritative bool
		if db.dialect == "sqlserver" {
			if err := rows.Scan(&id, &key, &typ, &name, &amount, &normalized, &authoritative); err != nil {
				rows.Close()
				return "", 0, false, err
			}
		} else {
			if err := rows.Scan(&key, &typ, &name, &amount, &normalized, &authoritative); err != nil {
				rows.Close()
				return "", 0, false, err
			}
		}
		stored, valid := decimalRat(amount)
		if authoritative && valid && stored.Cmp(apiAmount) == 0 && canonicalOperationType(typ, name) == canonicalOperationType(operation.Type, operation.Name) {
			matches++
			foundKey = key
			foundID = id
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", 0, false, err
	}
	rows.Close()
	if matches != 1 {
		return "", 0, false, nil
	}
	q := "UPDATE operation_record SET name=CASE WHEN name='' THEN ? ELSE name END,state=CASE WHEN state='' THEN ? ELSE state END,figi=CASE WHEN figi='' THEN ? ELSE figi END,ticker=CASE WHEN ticker='' THEN ? ELSE ticker END,instrument_uid=CASE WHEN instrument_uid='' THEN ? ELSE instrument_uid END,payment_currency=CASE WHEN payment_currency='' THEN ? ELSE payment_currency END,quantity=CASE WHEN quantity=0 THEN ? ELSE quantity END WHERE operation_key=?"
	args := []any{operation.Name, operation.State, operation.Figi, operation.Ticker, operation.InstrumentUID, operation.Payment.Currency, operation.Quantity, foundKey}
	if db.dialect == "sqlserver" {
		q = "UPDATE tinvest.operation SET name=CASE WHEN name IS NULL OR name=N'' THEN @p1 ELSE name END,state=CASE WHEN state IS NULL OR state=N'' THEN @p2 ELSE state END,figi=CASE WHEN figi IS NULL OR figi=N'' THEN @p3 ELSE figi END,ticker=CASE WHEN ticker IS NULL OR ticker=N'' THEN @p4 ELSE ticker END,instrument_uid=CASE WHEN instrument_uid IS NULL OR instrument_uid=N'' THEN @p5 ELSE instrument_uid END,payment_currency=CASE WHEN payment_currency IS NULL OR payment_currency=N'' THEN @p6 ELSE payment_currency END,quantity=CASE WHEN quantity=0 THEN @p7 ELSE quantity END WHERE operation_key=@p8"
		args = []any{operation.Name, operation.State, operation.Figi, operation.Ticker, operation.InstrumentUID, operation.Payment.Currency, operation.Quantity, foundKey}
	}
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return "", 0, false, err
	}
	_ = apiKey
	return foundKey, foundID, true, nil
}

func decimalRat(value any) (*big.Rat, bool) {
	var text string
	switch v := value.(type) {
	case []byte:
		text = string(v)
	case string:
		text = v
	case nil:
		return nil, false
	default:
		text = fmt.Sprint(v)
	}
	return new(big.Rat).SetString(strings.TrimSpace(text))
}

func migrateSQLiteReportSchema(ctx context.Context, db *sql.DB) error {
	columns := map[string]bool{}
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(operation_record)")
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var def any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for name, definition := range map[string]string{"normalized_type": "TEXT NOT NULL DEFAULT ''", "report_authoritative": "INTEGER NOT NULL DEFAULT 0", "report_period_end": "TEXT NOT NULL DEFAULT ''", "report_import_id": "INTEGER"} {
		if columns[name] {
			continue
		}
		if _, err := db.ExecContext(ctx, "ALTER TABLE operation_record ADD COLUMN "+name+" "+definition); err != nil {
			return fmt.Errorf("добавить operation_record.%s: %w", name, err)
		}
	}
	tradeColumns := map[string]bool{}
	tradeRows, err := db.QueryContext(ctx, "PRAGMA table_info(trade_record)")
	if err != nil {
		return err
	}
	for tradeRows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err := tradeRows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			tradeRows.Close()
			return err
		}
		tradeColumns[name] = true
	}
	if err := tradeRows.Err(); err != nil {
		tradeRows.Close()
		return err
	}
	tradeRows.Close()
	if !tradeColumns["isin"] {
		if _, err := db.ExecContext(ctx, "ALTER TABLE trade_record ADD COLUMN isin TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("добавить trade_record.isin: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_trade_record_isin ON trade_record(isin) WHERE isin <> ''"); err != nil {
		return fmt.Errorf("создать индекс trade_record.isin: %w", err)
	}
	return nil
}

func (db *DB) upsertAPITrade(ctx context.Context, tx *sql.Tx, accountID string, operation portfolio.Operation, fill portfolio.Fill) error {
	if strings.TrimSpace(fill.TradeNum) == "" {
		return nil
	}
	account := fill.AccountID
	if account == "" {
		account = accountID
	}
	at := fill.At
	if at.IsZero() {
		at = operation.At
	}
	if at.IsZero() {
		return nil
	}
	price := fill.Price.Amount.String()
	qty := fmt.Sprint(fill.Quantity)
	amount := fill.Price.Amount.Mul(bigDecimal(fill.Quantity)).String()
	event := tradeEventKey(account, fill.TradeNum, 0)
	isin := strings.ToUpper(strings.TrimSpace(fill.ISIN))
	ticker := strings.TrimSpace(fill.Ticker)
	if isin == "" && isinCodePattern.MatchString(ticker) {
		isin, ticker = strings.ToUpper(ticker), ""
	}
	fill.Ticker, fill.ISIN = ticker, isin
	if db.dialect == "sqlserver" {
		if isin == "" {
			var err error
			isin, err = db.resolveISINByUID(ctx, tx, fill.InstrumentUID)
			if err != nil {
				return err
			}
		}
		matched, err := db.mergeAPITradeIntoReport(ctx, tx, account, isin, at, fill)
		if err != nil || matched {
			return err
		}
		q := "MERGE tinvest.trade_record WITH (HOLDLOCK) t USING (SELECT @p1 event_key,@p2 account_id,@p3 trade_number,@p4 executed_at,@p5 instrument_uid,@p6 figi,@p7 ticker,@p8 isin,@p9 direction,@p10 price_amount,@p11 price_currency,@p12 quantity,@p13 amount) s ON t.event_key=s.event_key WHEN MATCHED AND t.report_authoritative=0 THEN UPDATE SET executed_at=s.executed_at,instrument_uid=s.instrument_uid,figi=s.figi,ticker=s.ticker,isin=COALESCE(NULLIF(s.isin,N''),t.isin),direction=s.direction,price_amount=s.price_amount,price_currency=s.price_currency,quantity=s.quantity,amount=s.amount WHEN NOT MATCHED THEN INSERT(event_key,account_id,trade_number,executed_at,instrument_uid,figi,ticker,isin,direction,price_amount,price_currency,quantity,amount) VALUES(s.event_key,s.account_id,s.trade_number,s.executed_at,s.instrument_uid,s.figi,s.ticker,s.isin,s.direction,s.price_amount,s.price_currency,s.quantity,s.amount);"
		_, err = tx.ExecContext(ctx, q, event, account, fill.TradeNum, at.UTC(), fill.InstrumentUID, fill.Figi, ticker, isin, fill.Direction, price, fill.Price.Currency, qty, amount)
		return err
	}
	matched, err := db.mergeAPITradeIntoReport(ctx, tx, account, isin, at, fill)
	if err != nil || matched {
		return err
	}
	q := "INSERT INTO trade_record(account_id,trade_number,repo_part,executed_at,instrument_uid,figi,ticker,isin,direction,price_amount,price_currency,quantity,amount) VALUES(?,?,0,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(account_id,trade_number,repo_part) DO UPDATE SET executed_at=CASE WHEN trade_record.report_authoritative=0 THEN excluded.executed_at ELSE trade_record.executed_at END,instrument_uid=CASE WHEN trade_record.report_authoritative=0 THEN excluded.instrument_uid ELSE COALESCE(NULLIF(trade_record.instrument_uid,''),excluded.instrument_uid) END,figi=CASE WHEN trade_record.report_authoritative=0 THEN excluded.figi ELSE COALESCE(NULLIF(trade_record.figi,''),excluded.figi) END,ticker=CASE WHEN trade_record.report_authoritative=0 THEN excluded.ticker ELSE COALESCE(NULLIF(trade_record.ticker,''),excluded.ticker) END,isin=COALESCE(NULLIF(excluded.isin,''),trade_record.isin),direction=CASE WHEN trade_record.report_authoritative=0 THEN excluded.direction ELSE COALESCE(NULLIF(trade_record.direction,''),excluded.direction) END,price_amount=CASE WHEN trade_record.report_authoritative=0 THEN excluded.price_amount ELSE COALESCE(trade_record.price_amount,excluded.price_amount) END,price_currency=CASE WHEN trade_record.report_authoritative=0 THEN excluded.price_currency ELSE COALESCE(NULLIF(trade_record.price_currency,''),excluded.price_currency) END,quantity=CASE WHEN trade_record.report_authoritative=0 THEN excluded.quantity ELSE COALESCE(trade_record.quantity,excluded.quantity) END,amount=CASE WHEN trade_record.report_authoritative=0 THEN excluded.amount ELSE COALESCE(trade_record.amount,excluded.amount) END"
	_, err = tx.ExecContext(ctx, q, account, fill.TradeNum, at.UTC().Format(time.RFC3339Nano), fill.InstrumentUID, fill.Figi, ticker, isin, fill.Direction, price, fill.Price.Currency, qty, amount)
	return err
}

func (db *DB) resolveISINByUID(ctx context.Context, tx *sql.Tx, uid string) (string, error) {
	if uid == "" {
		return "", nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT UPPER(LTRIM(RTRIM(r.isin))) FROM tinvest.instrument i JOIN ref.instruments r ON (r.instrument_id=i.ref_instrument_id OR EXISTS (SELECT 1 FROM ref.external_codes ec JOIN ref.external_systems es ON es.ext_system_id=ec.ext_system_id WHERE ec.internal_id=r.instrument_id AND es.ext_system=N'TINVEST' AND ec.ext_code_type_id=6 AND ec.ext_code=i.uid)) WHERE i.uid=@p1 AND NULLIF(r.isin,N'') IS NOT NULL`, uid)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	isin, count := "", 0
	for rows.Next() {
		if err := rows.Scan(&isin); err != nil {
			return "", err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if count != 1 {
		return "", nil
	}
	return isin, nil
}

func (db *DB) mergeAPITradeIntoReport(ctx context.Context, tx *sql.Tx, account, isin string, at time.Time, fill portfolio.Fill) (bool, error) {
	if isin == "" {
		return false, nil
	}
	q := `SELECT event_key,executed_at,direction,price_amount,price_currency,quantity FROM tinvest.trade_record WHERE account_id=@p1 AND isin=@p2 AND repo_part=0 AND report_authoritative=1`
	args := []any{account, isin}
	if db.dialect != "sqlserver" {
		q = `SELECT trade_number,executed_at,direction,price_amount,price_currency,quantity FROM trade_record WHERE account_id=? AND isin=? AND repo_part=0 AND report_authoritative=1`
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	wantPrice := new(big.Rat).SetInt(fill.Price.Amount.BigInt())
	wantQty := new(big.Rat).SetInt64(fill.Quantity)
	found := ""
	count := 0
	for rows.Next() {
		var key, direction, currency string
		var rawAt, rawPrice, rawQty any
		if err := rows.Scan(&key, &rawAt, &direction, &rawPrice, &currency, &rawQty); err != nil {
			return false, err
		}
		a, okA := reportTradeTime(rawAt)
		p, okP := decimalRat(rawPrice)
		q, okQ := decimalRat(rawQty)
		if okA && a.Equal(at.UTC()) && canonicalTradeSide(direction) == canonicalTradeSide(fill.Direction) && strings.EqualFold(currency, fill.Price.Currency) && okP && p.Cmp(wantPrice) == 0 && okQ && q.Cmp(wantQty) == 0 {
			found = key
			count++
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if count != 1 {
		return false, nil
	}
	update := `UPDATE tinvest.trade_record SET instrument_uid=COALESCE(NULLIF(instrument_uid,N''),@p1),figi=COALESCE(NULLIF(figi,N''),@p2),ticker=COALESCE(NULLIF(ticker,N''),@p3) WHERE event_key=@p4`
	uargs := []any{fill.InstrumentUID, fill.Figi, fill.Ticker, found}
	if db.dialect != "sqlserver" {
		update = `UPDATE trade_record SET instrument_uid=COALESCE(NULLIF(instrument_uid,''),?),figi=COALESCE(NULLIF(figi,''),?),ticker=COALESCE(NULLIF(ticker,''),?) WHERE account_id=? AND trade_number=? AND repo_part=0`
		uargs = []any{fill.InstrumentUID, fill.Figi, fill.Ticker, account, found}
	}
	_, err = tx.ExecContext(ctx, update, uargs...)
	return err == nil, err
}

func bigDecimal(value int64) decimal.Decimal { return decimal.NewFromInt(value) }
