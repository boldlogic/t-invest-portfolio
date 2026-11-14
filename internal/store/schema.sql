CREATE TABLE IF NOT EXISTS current_state (
    entity TEXT NOT NULL,
    scope_key TEXT NOT NULL,
    entity_key TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    payload TEXT NOT NULL,
    PRIMARY KEY (entity, scope_key, entity_key)
);

CREATE TABLE IF NOT EXISTS history_record (
    entity TEXT NOT NULL,
    scope_key TEXT NOT NULL,
    record_key TEXT NOT NULL,
    event_at TEXT NOT NULL DEFAULT '',
    payload TEXT NOT NULL,
    PRIMARY KEY (entity, scope_key, record_key)
);

CREATE TABLE IF NOT EXISTS operation_record (
    operation_key TEXT NOT NULL PRIMARY KEY,
    account_id TEXT NOT NULL,
    executed_at TEXT NOT NULL,
    type TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT '',
    figi TEXT NOT NULL DEFAULT '',
    ticker TEXT NOT NULL DEFAULT '',
    instrument_uid TEXT NOT NULL DEFAULT '',
    payment_amount NUMERIC NOT NULL,
    payment_currency TEXT NOT NULL DEFAULT '',
    quantity INTEGER NOT NULL,
    normalized_type TEXT NOT NULL DEFAULT '',
    report_authoritative INTEGER NOT NULL DEFAULT 0,
    report_period_end TEXT NOT NULL DEFAULT '',
    report_import_id INTEGER
);

CREATE TABLE IF NOT EXISTS operation_fill (
    operation_key TEXT NOT NULL,
    fill_index INTEGER NOT NULL,
    trade_num TEXT NOT NULL DEFAULT '',
    executed_at TEXT NOT NULL DEFAULT '',
    instrument_uid TEXT NOT NULL DEFAULT '',
    figi TEXT NOT NULL DEFAULT '',
    ticker TEXT NOT NULL DEFAULT '',
    isin TEXT NOT NULL DEFAULT '',
    account_id TEXT NOT NULL DEFAULT '',
    direction TEXT NOT NULL DEFAULT '',
    price_amount NUMERIC NOT NULL,
    price_currency TEXT NOT NULL DEFAULT '',
    quantity INTEGER NOT NULL,
    PRIMARY KEY (operation_key, fill_index),
    FOREIGN KEY (operation_key) REFERENCES operation_record(operation_key) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS trade_record (
    account_id TEXT NOT NULL,
    trade_number TEXT NOT NULL,
    repo_part INTEGER NOT NULL DEFAULT 0,
    executed_at TEXT NOT NULL,
    instrument_uid TEXT NOT NULL DEFAULT '',
    figi TEXT NOT NULL DEFAULT '',
    ticker TEXT NOT NULL DEFAULT '',
    isin TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    direction TEXT NOT NULL DEFAULT '',
    price_amount NUMERIC,
    price_currency TEXT NOT NULL DEFAULT '',
    quantity NUMERIC,
    amount NUMERIC,
    report_authoritative INTEGER NOT NULL DEFAULT 0,
    report_period_end TEXT NOT NULL DEFAULT '',
    report_import_id INTEGER,
    PRIMARY KEY(account_id, trade_number, repo_part)
);
CREATE INDEX IF NOT EXISTS idx_trade_record_isin ON trade_record(isin) WHERE isin <> '';

CREATE TABLE IF NOT EXISTS report_import (
    import_id INTEGER PRIMARY KEY AUTOINCREMENT,
    content_sha256 TEXT NOT NULL UNIQUE,
    file_name TEXT NOT NULL,
    file_format TEXT NOT NULL,
    import_status TEXT NOT NULL,
    report_account TEXT NOT NULL DEFAULT '',
    account_id TEXT NOT NULL DEFAULT '',
    period_start TEXT NOT NULL DEFAULT '',
    period_end TEXT NOT NULL DEFAULT '',
    row_count INTEGER NOT NULL DEFAULT 0,
    trade_count INTEGER NOT NULL DEFAULT 0,
    operation_count INTEGER NOT NULL DEFAULT 0,
    warning_count INTEGER NOT NULL DEFAULT 0,
    error_text TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS report_raw_row (
    import_id INTEGER NOT NULL,
    sheet_name TEXT NOT NULL,
    row_number INTEGER NOT NULL,
    raw_values TEXT NOT NULL,
    PRIMARY KEY(import_id, sheet_name, row_number),
    FOREIGN KEY(import_id) REFERENCES report_import(import_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS report_account_mapping (
    report_account TEXT NOT NULL PRIMARY KEY,
    account_id TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS instrument_quote_subscriptions (
    instrument_id INTEGER PRIMARY KEY,
    instrument_uid TEXT NOT NULL DEFAULT '',
    ticker TEXT NOT NULL DEFAULT '',
    class_code TEXT NOT NULL DEFAULT '',
    figi TEXT NOT NULL DEFAULT '',
    asset_uid TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS instrument_subscription_requests (
    request_id INTEGER PRIMARY KEY AUTOINCREMENT,
    instrument_uid TEXT NOT NULL DEFAULT '',
    ticker TEXT NOT NULL DEFAULT '',
    class_code TEXT NOT NULL DEFAULT '',
    figi TEXT NOT NULL DEFAULT '',
    asset_uid TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_instrument_subscription_requests_uid
    ON instrument_subscription_requests(instrument_uid) WHERE instrument_uid <> '';

CREATE UNIQUE INDEX IF NOT EXISTS ux_instrument_subscription_requests_ticker
    ON instrument_subscription_requests(ticker, class_code) WHERE instrument_uid = '' AND ticker <> '';

CREATE TABLE IF NOT EXISTS insider_deal_record (
    record_key TEXT NOT NULL PRIMARY KEY,
    instrument_uid TEXT NOT NULL,
    trade_id INTEGER NOT NULL DEFAULT 0,
    direction TEXT NOT NULL DEFAULT '',
    currency TEXT NOT NULL DEFAULT '',
    executed_at TEXT NOT NULL DEFAULT '',
    quantity INTEGER NOT NULL,
    price_amount NUMERIC NOT NULL,
    ticker TEXT NOT NULL DEFAULT '',
    investor_name TEXT NOT NULL DEFAULT '',
    investor_position TEXT NOT NULL DEFAULT '',
    percentage NUMERIC,
    option_execution INTEGER NOT NULL DEFAULT 0,
    disclosure_at TEXT NOT NULL DEFAULT ''
);
