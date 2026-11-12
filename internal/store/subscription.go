package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"t-invest-portfolio/internal/portfolio"
)

// EnsureSubscription добавляет бумагу или дополняет уже лежащую строку.
// Повтор того же uid или пары тикер+класс новую строку не создаёт.
// Пустые uid и тикер одновременно не записываются.
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
	if item.InstrumentUID == "" && item.Ticker == "" {
		return nil
	}
	if item.InstrumentUID != "" {
		id, err := subscriptionID(ctx, tx, dialect, true, item.InstrumentUID, "")
		if err != nil {
			return err
		}
		if id != 0 {
			return updateSubscription(ctx, tx, dialect, id, item)
		}
	}
	if item.Ticker != "" {
		id, err := subscriptionID(ctx, tx, dialect, false, item.Ticker, item.ClassCode)
		if err != nil {
			return err
		}
		if id != 0 {
			if item.InstrumentUID != "" {
				if err := assignSubscriptionUID(ctx, tx, dialect, id, item.InstrumentUID); err != nil {
					return err
				}
			}
			return updateSubscription(ctx, tx, dialect, id, item)
		}
	}
	return insertSubscription(ctx, tx, dialect, item)
}

func subscriptionID(ctx context.Context, tx *sql.Tx, dialect string, byUID bool, first, second string) (int64, error) {
	query := `SELECT subscription_id FROM subscriptions WHERE instrument_uid=?`
	args := []any{first}
	if !byUID {
		query = `SELECT subscription_id FROM subscriptions WHERE instrument_uid='' AND ticker=? AND class_code=?`
		args = []any{first, second}
	}
	if dialect == "sqlserver" {
		query = `SELECT subscription_id FROM tinvest.subscriptions WITH (UPDLOCK, HOLDLOCK) WHERE instrument_uid=@p1`
		args = []any{first}
		if !byUID {
			query = `SELECT subscription_id FROM tinvest.subscriptions WITH (UPDLOCK, HOLDLOCK) WHERE instrument_uid=N'' AND ticker=@p1 AND class_code=@p2`
			args = []any{first, second}
		}
	}
	var id int64
	err := tx.QueryRowContext(ctx, query, args...).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

func updateSubscription(ctx context.Context, tx *sql.Tx, dialect string, id int64, item portfolio.Subscription) error {
	query := `UPDATE subscriptions SET
		ticker=CASE WHEN ?<>'' THEN ? ELSE ticker END,
		class_code=CASE WHEN ?<>'' THEN ? ELSE class_code END,
		figi=CASE WHEN ?<>'' THEN ? ELSE figi END,
		asset_uid=CASE WHEN ?<>'' THEN ? ELSE asset_uid END
		WHERE subscription_id=?`
	args := []any{item.Ticker, item.Ticker, item.ClassCode, item.ClassCode, item.Figi, item.Figi, item.AssetUID, item.AssetUID, id}
	if dialect == "sqlserver" {
		query = `UPDATE tinvest.subscriptions SET
			ticker=CASE WHEN @p1<>N'' THEN @p1 ELSE ticker END,
			class_code=CASE WHEN @p2<>N'' THEN @p2 ELSE class_code END,
			figi=CASE WHEN @p3<>N'' THEN @p3 ELSE figi END,
			asset_uid=CASE WHEN @p4<>N'' THEN @p4 ELSE asset_uid END
			WHERE subscription_id=@p5`
		args = []any{item.Ticker, item.ClassCode, item.Figi, item.AssetUID, id}
	}
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

func assignSubscriptionUID(ctx context.Context, tx *sql.Tx, dialect string, id int64, instrumentUID string) error {
	query := `UPDATE subscriptions SET instrument_uid=? WHERE subscription_id=? AND instrument_uid=''`
	args := []any{instrumentUID, id}
	if dialect == "sqlserver" {
		query = `UPDATE tinvest.subscriptions SET instrument_uid=@p1 WHERE subscription_id=@p2 AND instrument_uid=N''`
	}
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

func insertSubscription(ctx context.Context, tx *sql.Tx, dialect string, item portfolio.Subscription) error {
	query := `INSERT INTO subscriptions(instrument_uid,ticker,class_code,figi,asset_uid) VALUES(?,?,?,?,?)`
	args := []any{item.InstrumentUID, item.Ticker, item.ClassCode, item.Figi, item.AssetUID}
	if dialect == "sqlserver" {
		query = `INSERT INTO tinvest.subscriptions(instrument_uid,ticker,class_code,figi,asset_uid) VALUES(@p1,@p2,@p3,@p4,@p5)`
	}
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

// SetSubscriptionAsset дописывает актив в уже существующую строку с этим uid.
func (db *DB) SetSubscriptionAsset(ctx context.Context, instrumentUID, assetUID string) error {
	if instrumentUID == "" || assetUID == "" {
		return nil
	}
	query := `UPDATE subscriptions SET asset_uid=? WHERE instrument_uid=?`
	args := []any{assetUID, instrumentUID}
	if db.dialect == "sqlserver" {
		query = `UPDATE tinvest.subscriptions SET asset_uid=@p1 WHERE instrument_uid=@p2`
	}
	_, err := db.sql.ExecContext(ctx, query, args...)
	return err
}

// ListSubscriptions возвращает все бумаги подписки.
func (db *DB) ListSubscriptions(ctx context.Context) ([]portfolio.Subscription, error) {
	query := `SELECT instrument_uid, ticker, class_code, figi, asset_uid FROM subscriptions`
	if db.dialect == "sqlserver" {
		query = `SELECT instrument_uid, ticker, class_code, figi, asset_uid FROM tinvest.subscriptions`
	}
	rows, err := db.sql.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []portfolio.Subscription
	for rows.Next() {
		var item portfolio.Subscription
		if err := rows.Scan(&item.InstrumentUID, &item.Ticker, &item.ClassCode, &item.Figi, &item.AssetUID); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// BackfillSubscriptions добирает бумаги, которые уже лежат в позициях и сделках.
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
		if item.InstrumentUID == "" && item.Ticker == "" {
			continue
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
