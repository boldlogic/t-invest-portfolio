package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"t-invest-portfolio/internal/portfolio"
)

// Record is one keyed value returned by an independent polling task.
type Record struct {
	Key     string
	At      time.Time
	Payload any
}

func timeText(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func (db *DB) GetCurrent(ctx context.Context, entity, scope, key string) (json.RawMessage, error) {
	query := `SELECT payload FROM current_state WHERE entity=? AND scope_key=? AND entity_key=?`
	if db.dialect == "sqlserver" {
		query = `SELECT payload FROM tinvest.current_state WHERE entity=@p1 AND scope_key=@p2 AND entity_key=@p3`
	}
	var payload []byte
	err := db.sql.QueryRowContext(ctx, query, entity, scope, key).Scan(&payload)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

// ReplaceCurrent atomically replaces the current rows owned by one task scope.
// A failed API call must never call this method, so stale data is preserved on errors.
func (db *DB) ReplaceCurrent(ctx context.Context, entity, scope string, rows []Record) error {
	if entity == "" || scope == "" {
		return fmt.Errorf("entity and scope are required")
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if db.dialect == "sqlserver" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM tinvest.current_state WHERE entity=@p1 AND scope_key=@p2`, entity, scope); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `DELETE FROM current_state WHERE entity=? AND scope_key=?`, entity, scope); err != nil {
		return err
	}
	for _, row := range rows {
		if row.Key == "" {
			continue
		}
		payload, err := json.Marshal(row.Payload)
		if err != nil {
			return fmt.Errorf("encode %s/%s: %w", entity, row.Key, err)
		}
		observed := timeText(row.At)
		if db.dialect == "sqlserver" {
			at := row.At
			if at.IsZero() {
				at = time.Now().UTC()
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO tinvest.current_state(entity,scope_key,entity_key,observed_at,payload) VALUES(@p1,@p2,@p3,@p4,@p5)`, entity, scope, row.Key, at.UTC(), string(payload))
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO current_state(entity,scope_key,entity_key,observed_at,payload) VALUES(?,?,?,?,?)`, entity, scope, row.Key, observed, string(payload))
		}
		if err != nil {
			return fmt.Errorf("store %s/%s: %w", entity, row.Key, err)
		}
	}
	return tx.Commit()
}

// UpsertHistory inserts or corrects one business event identified by its key.
func (db *DB) UpsertHistory(ctx context.Context, entity, scope, key string, at time.Time, value any) error {
	if entity == "" || scope == "" || key == "" {
		return fmt.Errorf("entity, scope and key are required")
	}
	if entity == "operation" {
		operation, ok := value.(portfolio.Operation)
		if !ok {
			return fmt.Errorf("operation history requires portfolio.Operation, got %T", value)
		}
		return db.UpsertOperation(ctx, scope, key, operation)
	}
	if entity == "insider_deal" {
		deal, ok := value.(portfolio.InsiderDeal)
		if !ok {
			return fmt.Errorf("insider deal history requires portfolio.InsiderDeal, got %T", value)
		}
		return db.UpsertInsiderDeal(ctx, scope, key, deal)
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if db.dialect == "sqlserver" {
		var atValue any
		if !at.IsZero() {
			atValue = at.UTC()
		}
		_, err = db.sql.ExecContext(ctx, `MERGE tinvest.history_record WITH (HOLDLOCK) AS target
USING (SELECT @p1 AS entity,@p2 AS scope_key,@p3 AS record_key) AS source
ON target.entity=source.entity AND target.scope_key=source.scope_key AND target.record_key=source.record_key
WHEN MATCHED THEN UPDATE SET event_at=@p4,payload=@p5
WHEN NOT MATCHED THEN INSERT(entity,scope_key,record_key,event_at,payload) VALUES(@p1,@p2,@p3,@p4,@p5);`, entity, scope, key, atValue, string(payload))
	} else {
		_, err = db.sql.ExecContext(ctx, `INSERT INTO history_record(entity,scope_key,record_key,event_at,payload) VALUES(?,?,?,?,?)
ON CONFLICT(entity,scope_key,record_key) DO UPDATE SET event_at=excluded.event_at,payload=excluded.payload`, entity, scope, key, timeText(at), string(payload))
	}
	return err
}

func migrateSQLiteHistory(ctx context.Context, raw *sql.DB) error {
	rows, err := raw.QueryContext(ctx, `SELECT entity,scope_key,record_key,payload FROM history_record WHERE entity IN ('operation','insider_deal') ORDER BY entity,scope_key,record_key`)
	if err != nil {
		return err
	}
	type legacyRecord struct {
		entity, scope, key string
		payload            []byte
	}
	var records []legacyRecord
	for rows.Next() {
		var record legacyRecord
		if err := rows.Scan(&record.entity, &record.scope, &record.key, &record.payload); err != nil {
			_ = rows.Close()
			return err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	db := &DB{sql: raw, dialect: "sqlite"}
	for _, record := range records {
		switch record.entity {
		case "operation":
			var operation portfolio.Operation
			if err := json.Unmarshal(record.payload, &operation); err != nil {
				return fmt.Errorf("decode legacy operation %s: %w", record.key, err)
			}
			if err := db.UpsertOperation(ctx, record.scope, record.key, operation); err != nil {
				return fmt.Errorf("migrate legacy operation %s: %w", record.key, err)
			}
		case "insider_deal":
			var deal portfolio.InsiderDeal
			if err := json.Unmarshal(record.payload, &deal); err != nil {
				return fmt.Errorf("decode legacy insider deal %s: %w", record.key, err)
			}
			if err := db.UpsertInsiderDeal(ctx, record.scope, record.key, deal); err != nil {
				return fmt.Errorf("migrate legacy insider deal %s: %w", record.key, err)
			}
		}
		if _, err := raw.ExecContext(ctx, `DELETE FROM history_record WHERE entity=? AND scope_key=? AND record_key=?`, record.entity, record.scope, record.key); err != nil {
			return err
		}
	}
	return nil
}

// UpsertOperation stores an operation in its relational table and replaces its fill rows.
func (db *DB) UpsertOperation(ctx context.Context, accountID, key string, operation portfolio.Operation) error {
	if accountID == "" || key == "" || operation.At.IsZero() {
		return fmt.Errorf("account, operation key and execution time are required")
	}
	if db.dialect != "sqlserver" {
		tx, err := db.sql.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		operationKey, _, matched, err := db.mergeAPIIntoReportOperation(ctx, tx, accountID, key, operation)
		if err != nil {
			return err
		}
		if !matched {
			operationKey = key
			_, err = tx.ExecContext(ctx, `INSERT INTO operation_record(operation_key,account_id,executed_at,type,normalized_type,name,state,figi,ticker,instrument_uid,payment_amount,payment_currency,quantity)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(operation_key) DO UPDATE SET
account_id=excluded.account_id,
executed_at=CASE WHEN operation_record.report_authoritative=0 THEN excluded.executed_at ELSE operation_record.executed_at END,
type=CASE WHEN operation_record.report_authoritative=0 THEN excluded.type ELSE operation_record.type END,
normalized_type=CASE WHEN operation_record.report_authoritative=0 THEN excluded.normalized_type ELSE operation_record.normalized_type END,
name=CASE WHEN operation_record.report_authoritative=1 AND operation_record.name<>'' THEN operation_record.name ELSE excluded.name END,
state=CASE WHEN operation_record.report_authoritative=1 AND operation_record.state<>'' THEN operation_record.state ELSE excluded.state END,
figi=CASE WHEN operation_record.report_authoritative=1 AND operation_record.figi<>'' THEN operation_record.figi ELSE excluded.figi END,
ticker=CASE WHEN operation_record.report_authoritative=1 AND operation_record.ticker<>'' THEN operation_record.ticker ELSE excluded.ticker END,
instrument_uid=CASE WHEN operation_record.report_authoritative=1 AND operation_record.instrument_uid<>'' THEN operation_record.instrument_uid ELSE excluded.instrument_uid END,
payment_amount=CASE WHEN operation_record.report_authoritative=0 THEN excluded.payment_amount ELSE operation_record.payment_amount END,
payment_currency=CASE WHEN operation_record.report_authoritative=1 AND operation_record.payment_currency<>'' THEN operation_record.payment_currency ELSE excluded.payment_currency END,
quantity=CASE WHEN operation_record.report_authoritative=1 AND operation_record.quantity<>0 THEN operation_record.quantity ELSE excluded.quantity END`,
				key, accountID, timeText(operation.At), operation.Type, canonicalOperationType(operation.Type, operation.Name), operation.Name, operation.State, operation.Figi, operation.Ticker, operation.InstrumentUID, operation.Payment.Amount.String(), operation.Payment.Currency, operation.Quantity)
			if err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM operation_fill WHERE operation_key=?`, operationKey); err != nil {
			return err
		}
		for index, fill := range operation.Fills {
			if _, err := tx.ExecContext(ctx, `INSERT INTO operation_fill(operation_key,fill_index,trade_num,executed_at,instrument_uid,figi,ticker,account_id,direction,price_amount,price_currency,quantity) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
				operationKey, index, fill.TradeNum, timeText(fill.At), fill.InstrumentUID, fill.Figi, fill.Ticker, fill.AccountID, fill.Direction, fill.Price.Amount.String(), fill.Price.Currency, fill.Quantity); err != nil {
				return err
			}
			if err := db.upsertAPITrade(ctx, tx, accountID, operation, fill); err != nil {
				return fmt.Errorf("сохранить нормализованную сделку %s: %w", fill.TradeNum, err)
			}
			if err := ensureSubscriptionTx(ctx, tx, db.dialect, portfolio.Subscription{InstrumentUID: fill.InstrumentUID, Ticker: fill.Ticker, Figi: fill.Figi}); err != nil {
				return err
			}
		}
		return tx.Commit()
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, operationID, matched, err := db.mergeAPIIntoReportOperation(ctx, tx, accountID, key, operation)
	if err != nil {
		return err
	}
	if !matched {
		err = tx.QueryRowContext(ctx, `MERGE tinvest.operation WITH (HOLDLOCK) AS target
USING (SELECT @p1 AS operation_key,@p2 AS account_id,@p3 AS executed_at,@p4 AS type,@p5 AS instrument_uid,@p6 AS payment_amount,@p7 AS payment_currency,@p8 AS quantity,@p9 AS name,@p10 AS state,@p11 AS figi,@p12 AS ticker,@p13 AS normalized_type) AS source
ON target.operation_key=source.operation_key OR (target.account_id=source.account_id AND target.executed_at=source.executed_at AND target.type=source.type AND target.instrument_uid=source.instrument_uid AND target.payment_amount=source.payment_amount AND target.payment_currency=source.payment_currency AND target.quantity=source.quantity)
WHEN MATCHED THEN UPDATE SET operation_key=source.operation_key,name=CASE WHEN target.report_authoritative=1 AND target.name<>N'' THEN target.name ELSE source.name END,state=CASE WHEN target.report_authoritative=1 AND target.state<>N'' THEN target.state ELSE source.state END,figi=CASE WHEN target.report_authoritative=1 AND target.figi<>N'' THEN target.figi ELSE source.figi END,ticker=CASE WHEN target.report_authoritative=1 AND target.ticker<>N'' THEN target.ticker ELSE source.ticker END,normalized_type=CASE WHEN target.report_authoritative=1 THEN target.normalized_type ELSE source.normalized_type END,payment_currency=CASE WHEN target.report_authoritative=1 AND target.payment_currency<>N'' THEN target.payment_currency ELSE source.payment_currency END
WHEN NOT MATCHED THEN INSERT(operation_key,account_id,executed_at,type,normalized_type,instrument_uid,payment_amount,payment_currency,quantity,name,state,figi,ticker)
VALUES(source.operation_key,source.account_id,source.executed_at,source.type,source.normalized_type,source.instrument_uid,source.payment_amount,source.payment_currency,source.quantity,source.name,source.state,source.figi,source.ticker)
OUTPUT INSERTED.operation_id;`, key, accountID, operation.At.UTC(), operation.Type, operation.InstrumentUID, operation.Payment.Amount.String(), operation.Payment.Currency, operation.Quantity, operation.Name, operation.State, operation.Figi, operation.Ticker, canonicalOperationType(operation.Type, operation.Name)).Scan(&operationID)
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tinvest.operation_fill WHERE operation_id=@p1`, operationID); err != nil {
		return err
	}
	for index, fill := range operation.Fills {
		var fillAt any
		if !fill.At.IsZero() {
			fillAt = fill.At.UTC()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tinvest.operation_fill(operation_id,fill_index,trade_num,executed_at,instrument_uid,figi,ticker,account_id,direction,price_amount,price_currency,quantity) VALUES(@p1,@p2,@p3,@p4,@p5,@p6,@p7,@p8,@p9,@p10,@p11,@p12)`,
			operationID, index, fill.TradeNum, fillAt, fill.InstrumentUID, fill.Figi, fill.Ticker, fill.AccountID, fill.Direction, fill.Price.Amount.String(), fill.Price.Currency, fill.Quantity); err != nil {
			return err
		}
		if err := db.upsertAPITrade(ctx, tx, accountID, operation, fill); err != nil {
			return fmt.Errorf("сохранить нормализованную сделку %s: %w", fill.TradeNum, err)
		}
		if err := ensureSubscriptionTx(ctx, tx, db.dialect, portfolio.Subscription{InstrumentUID: fill.InstrumentUID, Ticker: fill.Ticker, Figi: fill.Figi}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpsertInsiderDeal stores a typed insider deal by its stable event key.
func (db *DB) UpsertInsiderDeal(ctx context.Context, instrumentUID, key string, deal portfolio.InsiderDeal) error {
	if instrumentUID == "" || key == "" {
		return fmt.Errorf("instrument and insider deal key are required")
	}
	if db.dialect != "sqlserver" {
		_, err := db.sql.ExecContext(ctx, `INSERT INTO insider_deal_record(record_key,instrument_uid,trade_id,direction,currency,executed_at,quantity,price_amount,ticker,investor_name,investor_position,percentage,option_execution,disclosure_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(record_key) DO UPDATE SET instrument_uid=excluded.instrument_uid,trade_id=excluded.trade_id,direction=excluded.direction,currency=excluded.currency,executed_at=excluded.executed_at,quantity=excluded.quantity,price_amount=excluded.price_amount,ticker=excluded.ticker,investor_name=excluded.investor_name,investor_position=excluded.investor_position,percentage=excluded.percentage,option_execution=excluded.option_execution,disclosure_at=excluded.disclosure_at`,
			key, instrumentUID, deal.TradeID, deal.Direction, deal.Currency, timeText(deal.At), deal.Quantity, deal.Price.Amount.String(), deal.Ticker, deal.InvestorName, deal.InvestorPosition, deal.Percentage, deal.OptionExecution, timeText(deal.Disclosure))
		return err
	}
	var executedAt, disclosureAt any
	if !deal.At.IsZero() {
		executedAt = deal.At.UTC()
	}
	if !deal.Disclosure.IsZero() {
		disclosureAt = deal.Disclosure.UTC()
	}
	_, err := db.sql.ExecContext(ctx, `MERGE tinvest.insider_deal WITH (HOLDLOCK) AS target
USING (SELECT @p1 AS record_key,@p2 AS trade_id,@p3 AS instrument_uid,@p4 AS direction,@p5 AS currency,@p6 AS executed_at,@p7 AS quantity,@p8 AS price_amount,@p9 AS ticker,@p10 AS investor_name,@p11 AS investor_position,@p12 AS percentage,@p13 AS option_execution,@p14 AS disclosure_at) AS source
ON target.record_key=source.record_key OR (source.trade_id<>0 AND target.trade_id=source.trade_id) OR (source.trade_id=0 AND target.trade_id=0 AND target.instrument_uid=source.instrument_uid AND target.executed_at=source.executed_at AND target.investor_name=source.investor_name AND target.direction=source.direction AND target.quantity=source.quantity AND target.price_amount=source.price_amount)
WHEN MATCHED THEN UPDATE SET record_key=source.record_key,instrument_uid=source.instrument_uid,direction=source.direction,currency=source.currency,executed_at=source.executed_at,quantity=source.quantity,price_amount=source.price_amount,ticker=source.ticker,investor_name=source.investor_name,investor_position=source.investor_position,percentage=source.percentage,option_execution=source.option_execution,disclosure_at=source.disclosure_at
WHEN NOT MATCHED THEN INSERT(record_key,trade_id,instrument_uid,direction,currency,executed_at,quantity,price_amount,ticker,investor_name,investor_position,percentage,option_execution,disclosure_at)
VALUES(source.record_key,source.trade_id,source.instrument_uid,source.direction,source.currency,source.executed_at,source.quantity,source.price_amount,source.ticker,source.investor_name,source.investor_position,source.percentage,source.option_execution,source.disclosure_at);`,
		key, deal.TradeID, instrumentUID, deal.Direction, deal.Currency, executedAt, deal.Quantity, deal.Price.Amount.String(), deal.Ticker, deal.InvestorName, deal.InvestorPosition, nullableText(deal.Percentage), deal.OptionExecution, disclosureAt)
	return err
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// SavePortfolioPositions keeps the shared portfolio table in step with the broker's daily position state.
func (db *DB) SavePortfolioPositions(ctx context.Context, accountID string, positions []portfolio.Position) error {
	if db.dialect != "sqlserver" || accountID == "" {
		return nil
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	mssql := &mssqlTx{tx: tx, ctx: ctx}
	if _, err := mssql.upsertSourceAccount(portfolio.Account{ID: accountID}); err != nil {
		return err
	}
	for _, position := range positions {
		uid := localInstrumentKey(position.InstrumentUID, position.Figi)
		if uid == "" {
			continue
		}
		if _, err := mssql.Exec(`INSERT INTO instrument(uid, figi, ticker, name, class_code, kind) VALUES(?, ?, ?, ?, ?, ?)
			ON CONFLICT(uid) DO UPDATE SET
				figi=CASE WHEN excluded.figi<>'' THEN excluded.figi ELSE instrument.figi END,
				ticker=CASE WHEN excluded.ticker<>'' THEN excluded.ticker ELSE instrument.ticker END,
				name=CASE WHEN excluded.name<>'' THEN excluded.name ELSE instrument.name END,
				class_code=CASE WHEN excluded.class_code<>'' THEN excluded.class_code ELSE instrument.class_code END,
				kind=CASE WHEN excluded.kind<>'' THEN excluded.kind ELSE instrument.kind END`,
			uid, position.Figi, position.Ticker, position.Name, position.ClassCode, position.InstrumentType); err != nil {
			return err
		}
		if err := mssql.linkInstrument(uid, position.Figi, position.Ticker, position.Name, position.ClassCode); err != nil {
			return err
		}
	}
	date, err := mssql.localLoadDate()
	if err != nil {
		return err
	}
	if err := mssql.replacePortfolioPositions(accountID, date, positions); err != nil {
		return err
	}
	return tx.Commit()
}
