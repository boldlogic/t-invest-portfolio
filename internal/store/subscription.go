package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"t-invest-portfolio/internal/portfolio"
)

// EnsureSubscription records source identifiers for resolution by core-reference.
func (db *DB) EnsureSubscription(ctx context.Context, item portfolio.Subscription) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSubscriptionTx(ctx, tx, db.dialect, item); err != nil {
		return err
	}
	return tx.Commit()
}

func ensureSubscriptionTx(ctx context.Context, tx *sql.Tx, dialect string, item portfolio.Subscription) error {
	board := strings.TrimSpace(item.ClassCode)
	if dialect != "sqlserver" {
		return ensureSQLiteSubscriptionRequest(ctx, tx, item, board)
	}
	for _, key := range []struct {
		codeType string
		value    string
	}{{"instrument_uid", item.InstrumentUID}, {"figi", item.Figi}, {"instrument_code", item.Ticker}} {
		code := strings.TrimSpace(key.value)
		if code == "" {
			continue
		}
		_, err := tx.ExecContext(ctx, `
DECLARE @system_id TINYINT = (SELECT ext_system_id FROM ref.external_systems WHERE ext_system=N'TINVEST');
DECLARE @code_type_id TINYINT = (SELECT ext_code_type_id FROM ref.external_code_types WHERE code=@p1);
IF @system_id IS NULL OR @code_type_id IS NULL
    THROW 51041, N'В ref отсутствует система TINVEST или тип внешнего кода инструмента', 1;
IF NOT EXISTS (
    SELECT 1 FROM market.instrument_subscription_requests WITH (UPDLOCK,HOLDLOCK)
    WHERE ext_system_id=@system_id AND ext_code_type_id=@code_type_id
      AND external_code=@p2 AND board_code=@p3
)
    INSERT INTO market.instrument_subscription_requests
        (ext_system_id,ext_code_type_id,external_code,board_code,asset_uid)
    VALUES (@system_id,@code_type_id,@p2,@p3,@p4);
ELSE IF @p4<>N''
    UPDATE market.instrument_subscription_requests SET asset_uid=@p4
    WHERE ext_system_id=@system_id AND ext_code_type_id=@code_type_id
      AND external_code=@p2 AND board_code=@p3 AND asset_uid=N'';`,
			key.codeType, code, board, strings.TrimSpace(item.AssetUID))
		if err != nil {
			return fmt.Errorf("записать заявку T-Invest %s: %w", key.codeType, err)
		}
	}
	return nil
}

func ensureSQLiteSubscriptionRequest(ctx context.Context, tx *sql.Tx, item portfolio.Subscription, board string) error {
	if strings.TrimSpace(item.InstrumentUID) == "" && strings.TrimSpace(item.Ticker) == "" && strings.TrimSpace(item.Figi) == "" {
		return nil
	}
	var id int64
	err := sql.ErrNoRows
	if item.InstrumentUID != "" {
		err = tx.QueryRowContext(ctx, `SELECT request_id FROM instrument_subscription_requests WHERE instrument_uid=?`, item.InstrumentUID).Scan(&id)
	}
	if err == sql.ErrNoRows && item.Ticker != "" {
		err = tx.QueryRowContext(ctx, `SELECT request_id FROM instrument_subscription_requests WHERE instrument_uid='' AND ticker=? AND class_code=?`, item.Ticker, board).Scan(&id)
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if id != 0 {
		_, err = tx.ExecContext(ctx, `UPDATE instrument_subscription_requests SET
			instrument_uid=CASE WHEN ?<>'' THEN ? ELSE instrument_uid END,
			ticker=CASE WHEN ?<>'' THEN ? ELSE ticker END,
			class_code=CASE WHEN ?<>'' THEN ? ELSE class_code END,
			figi=CASE WHEN ?<>'' THEN ? ELSE figi END,
			asset_uid=CASE WHEN ?<>'' THEN ? ELSE asset_uid END
			WHERE request_id=?`, item.InstrumentUID, item.InstrumentUID, item.Ticker, item.Ticker, board, board,
			item.Figi, item.Figi, item.AssetUID, item.AssetUID, id)
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO instrument_subscription_requests(instrument_uid,ticker,class_code,figi,asset_uid) VALUES(?,?,?,?,?)`, item.InstrumentUID, item.Ticker, board, item.Figi, item.AssetUID)
	return err
}

// SetSubscriptionAsset stores the T-Invest asset identity beside its source instrument.
func (db *DB) SetSubscriptionAsset(ctx context.Context, instrumentUID, assetUID string) error {
	instrumentUID = strings.TrimSpace(instrumentUID)
	assetUID = strings.TrimSpace(assetUID)
	if instrumentUID == "" || assetUID == "" {
		return nil
	}
	if db.dialect != "sqlserver" {
		_, err := db.sql.ExecContext(ctx, `UPDATE instrument_subscription_requests SET asset_uid=? WHERE instrument_uid=?`, assetUID, instrumentUID)
		return err
	}
	_, err := db.sql.ExecContext(ctx, `
DECLARE @system_id TINYINT = (SELECT ext_system_id FROM ref.external_systems WHERE ext_system=N'TINVEST');
DECLARE @uid_type_id TINYINT = (SELECT ext_code_type_id FROM ref.external_code_types WHERE code='instrument_uid');
UPDATE market.instrument_subscription_requests
SET asset_uid=@p1
WHERE ext_system_id=@system_id AND ext_code_type_id=@uid_type_id
  AND external_code=@p2 AND asset_uid=N'';
IF EXISTS (SELECT 1 FROM tinvest.instrument WHERE uid=@p2)
BEGIN
    IF NOT EXISTS (SELECT 1 FROM tinvest.instrument_asset WITH (UPDLOCK,HOLDLOCK) WHERE instrument_uid=@p2)
        INSERT INTO tinvest.instrument_asset(instrument_uid,asset_uid) VALUES(@p2,@p1);
    ELSE
        UPDATE tinvest.instrument_asset SET asset_uid=@p1 WHERE instrument_uid=@p2 AND asset_uid=N'';
END`, assetUID, instrumentUID)
	return err
}

// ListSubscriptions returns enabled shared subscriptions and unresolved T-Invest requests.
func (db *DB) ListSubscriptions(ctx context.Context) ([]portfolio.Subscription, error) {
	query := `SELECT instrument_uid,ticker,class_code,figi,asset_uid,0 FROM instrument_subscription_requests`
	if db.dialect == "sqlserver" {
		query = `
WITH active AS (
    SELECT q.instrument_id,
        MAX(CASE WHEN ect.code='instrument_uid' THEN ec.ext_code END) AS instrument_uid,
        MAX(CASE WHEN ect.code='instrument_code' THEN ec.ext_code END) AS ticker,
        MAX(CASE WHEN ect.code='figi' THEN ec.ext_code END) AS figi,
        MAX(CASE WHEN ib.is_primary=1 THEN b.code END) AS class_code
    FROM market.instrument_quote_subscriptions q
    JOIN ref.external_codes ec ON ec.internal_id=q.instrument_id
    JOIN ref.external_systems es ON es.ext_system_id=ec.ext_system_id AND es.ext_system=N'TINVEST'
    JOIN ref.external_code_types ect ON ect.ext_code_type_id=ec.ext_code_type_id
    LEFT JOIN ref.instrument_boards ib ON ib.instrument_id=q.instrument_id
    LEFT JOIN ref.boards b ON b.board_id=ib.board_id
    WHERE q.enabled=1
    GROUP BY q.instrument_id
)
SELECT COALESCE(a.instrument_uid,N''), COALESCE(a.ticker,N''), COALESCE(i.class_code,a.class_code,N''),
       COALESCE(a.figi,N''), COALESCE(ia.asset_uid,(
           SELECT MAX(r.asset_uid)
           FROM market.instrument_subscription_requests r
           JOIN ref.external_systems request_system ON request_system.ext_system_id=r.ext_system_id AND request_system.ext_system=N'TINVEST'
           JOIN ref.external_code_types request_type ON request_type.ext_code_type_id=r.ext_code_type_id AND request_type.code='instrument_uid'
           WHERE r.resolved_instrument_id=a.instrument_id
       ),N''), CAST(1 AS bit)
FROM active a
LEFT JOIN tinvest.instrument i ON i.uid=a.instrument_uid
LEFT JOIN tinvest.instrument_asset ia ON ia.instrument_uid=a.instrument_uid
UNION ALL
SELECT CASE WHEN ect.code='instrument_uid' THEN r.external_code ELSE N'' END,
       CASE WHEN ect.code='instrument_code' THEN r.external_code ELSE N'' END,
       r.board_code,
       CASE WHEN ect.code='figi' THEN r.external_code ELSE N'' END,
       r.asset_uid,
       CAST(0 AS bit)
FROM market.instrument_subscription_requests r
JOIN ref.external_systems es ON es.ext_system_id=r.ext_system_id AND es.ext_system=N'TINVEST'
JOIN ref.external_code_types ect ON ect.ext_code_type_id=r.ext_code_type_id
WHERE r.resolved_at IS NULL
ORDER BY 1,2,3`
	}
	rows, err := db.sql.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []portfolio.Subscription
	for rows.Next() {
		var item portfolio.Subscription
		if err := rows.Scan(&item.InstrumentUID, &item.Ticker, &item.ClassCode, &item.Figi, &item.AssetUID, &item.Resolved); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// BackfillSubscriptions queues instruments observed in positions, trades and fills for resolution.
func (db *DB) BackfillSubscriptions(ctx context.Context) error {
	items, err := db.subscriptionCandidates(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := db.EnsureSubscription(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) subscriptionCandidates(ctx context.Context) ([]portfolio.Subscription, error) {
	tradeQuery := `SELECT instrument_uid, ticker, figi FROM trade_record`
	fillQuery := `SELECT instrument_uid, ticker, figi FROM operation_fill`
	positionQuery := `SELECT payload FROM current_state WHERE entity='position'`
	if db.dialect == "sqlserver" {
		tradeQuery = `SELECT instrument_uid, ticker, figi FROM tinvest.trade_record`
		fillQuery = `SELECT instrument_uid, ticker, figi FROM tinvest.operation_fill`
		positionQuery = `SELECT payload FROM tinvest.current_state WHERE entity=N'position'`
	}
	var items []portfolio.Subscription
	for _, query := range []string{tradeQuery, fillQuery} {
		found, err := scanTradeSubscriptions(ctx, db.sql, query)
		if err != nil {
			return nil, err
		}
		items = append(items, found...)
	}
	rows, err := db.sql.QueryContext(ctx, positionQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var position portfolio.Position
		if err := json.Unmarshal(payload, &position); err != nil {
			return nil, fmt.Errorf("позиция подписки: %w", err)
		}
		if item, ok := portfolio.SubscriptionFromPosition(position); ok {
			items = append(items, item)
		}
	}
	return items, rows.Err()
}

func scanTradeSubscriptions(ctx context.Context, db *sql.DB, query string) ([]portfolio.Subscription, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []portfolio.Subscription
	for rows.Next() {
		var item portfolio.Subscription
		if err := rows.Scan(&item.InstrumentUID, &item.Ticker, &item.Figi); err != nil {
			return nil, err
		}
		if item.InstrumentUID == "" && item.Ticker == "" && item.Figi == "" {
			continue
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
