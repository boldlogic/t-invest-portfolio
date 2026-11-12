/*
	Apply once to the SQL Server database used by T-Invest and QUIK.
	Raw report rows are retained when normalization fails; normalized records
	are written only after the report account has an explicit API account map.
*/
IF OBJECT_ID(N'tinvest.report_import', N'U') IS NULL
BEGIN
	CREATE TABLE tinvest.report_import (
		import_id BIGINT IDENTITY(1,1) NOT NULL CONSTRAINT PK_tinvest_report_import PRIMARY KEY,
		content_sha256 CHAR(64) NOT NULL CONSTRAINT UQ_tinvest_report_import_sha256 UNIQUE,
		file_name NVARCHAR(260) NOT NULL,
		file_format VARCHAR(8) NOT NULL,
		import_status VARCHAR(16) NOT NULL,
		report_account NVARCHAR(100) NULL,
		account_id NVARCHAR(100) NULL,
		period_start DATE NULL,
		period_end DATE NULL,
		row_count INT NOT NULL CONSTRAINT DF_tinvest_report_import_rows DEFAULT (0),
		trade_count INT NOT NULL CONSTRAINT DF_tinvest_report_import_trades DEFAULT (0),
		operation_count INT NOT NULL CONSTRAINT DF_tinvest_report_import_operations DEFAULT (0),
		warning_count INT NOT NULL CONSTRAINT DF_tinvest_report_import_warnings DEFAULT (0),
		error_text NVARCHAR(2000) NULL,
		created_at DATETIMEOFFSET(7) NOT NULL CONSTRAINT DF_tinvest_report_import_created DEFAULT (SYSDATETIMEOFFSET()),
		finished_at DATETIMEOFFSET(7) NULL
	);
END;
GO

IF OBJECT_ID(N'tinvest.report_raw_row', N'U') IS NULL
BEGIN
	CREATE TABLE tinvest.report_raw_row (
		import_id BIGINT NOT NULL,
		sheet_name NVARCHAR(128) NOT NULL,
		row_number INT NOT NULL,
		raw_values NVARCHAR(MAX) NOT NULL,
		CONSTRAINT PK_tinvest_report_raw_row PRIMARY KEY (import_id, sheet_name, row_number),
		CONSTRAINT FK_tinvest_report_raw_row_import FOREIGN KEY (import_id)
			REFERENCES tinvest.report_import(import_id) ON DELETE CASCADE
	);
END;
GO

IF OBJECT_ID(N'tinvest.report_account_mapping', N'U') IS NULL
BEGIN
	CREATE TABLE tinvest.report_account_mapping (
		report_account NVARCHAR(100) NOT NULL CONSTRAINT PK_tinvest_report_account_mapping PRIMARY KEY,
		account_id NVARCHAR(100) NOT NULL,
		updated_at DATETIMEOFFSET(7) NOT NULL CONSTRAINT DF_tinvest_report_account_mapping_updated DEFAULT (SYSDATETIMEOFFSET())
	);
END;
GO

IF OBJECT_ID(N'tinvest.trade_record', N'U') IS NULL
BEGIN
	CREATE TABLE tinvest.trade_record (
		event_key CHAR(64) NOT NULL CONSTRAINT PK_tinvest_trade_record PRIMARY KEY,
		account_id NVARCHAR(100) NOT NULL,
		trade_number NVARCHAR(128) NOT NULL,
		repo_part TINYINT NOT NULL CONSTRAINT DF_tinvest_trade_repo_part DEFAULT (0),
		executed_at DATETIMEOFFSET(7) NOT NULL,
		instrument_uid NVARCHAR(100) NOT NULL CONSTRAINT DF_tinvest_trade_uid DEFAULT (N''),
		figi NVARCHAR(100) NOT NULL CONSTRAINT DF_tinvest_trade_figi DEFAULT (N''),
		ticker NVARCHAR(100) NOT NULL CONSTRAINT DF_tinvest_trade_ticker DEFAULT (N''),
		instrument_name NVARCHAR(300) NOT NULL CONSTRAINT DF_tinvest_trade_name DEFAULT (N''),
		direction NVARCHAR(32) NOT NULL CONSTRAINT DF_tinvest_trade_direction DEFAULT (N''),
		price_amount DECIMAL(28,8) NULL,
		price_currency NVARCHAR(16) NOT NULL CONSTRAINT DF_tinvest_trade_currency DEFAULT (N''),
		quantity DECIMAL(28,8) NULL,
		amount DECIMAL(28,8) NULL,
		report_authoritative BIT NOT NULL CONSTRAINT DF_tinvest_trade_authoritative DEFAULT (0),
		report_period_end DATE NULL,
		report_import_id BIGINT NULL,
		CONSTRAINT FK_tinvest_trade_report_import FOREIGN KEY (report_import_id)
			REFERENCES tinvest.report_import(import_id)
	);
END;
GO

;WITH api_trades AS (
	SELECT
		COALESCE(NULLIF(f.account_id, N''), o.account_id) AS account_id,
		f.trade_num,
		COALESCE(f.executed_at, o.executed_at) AS executed_at,
		f.instrument_uid, f.figi, f.ticker, f.direction,
		f.price_amount, f.price_currency, f.quantity,
		ROW_NUMBER() OVER (
			PARTITION BY COALESCE(NULLIF(f.account_id, N''), o.account_id), f.trade_num
			ORDER BY COALESCE(f.executed_at, o.executed_at) DESC
		) AS row_num
	FROM tinvest.operation_fill f
	JOIN tinvest.operation o ON o.operation_id = f.operation_id
	WHERE NULLIF(f.trade_num, N'') IS NOT NULL
)
INSERT INTO tinvest.trade_record (
	event_key, account_id, trade_number, repo_part, executed_at,
	instrument_uid, figi, ticker, direction, price_amount, price_currency,
	quantity, amount, report_authoritative
)
SELECT
	LOWER(CONVERT(CHAR(64), HASHBYTES('SHA2_256',
		CONVERT(VARCHAR(512), CONCAT(account_id, '|', trade_num, '|0'))), 2)),
	account_id, trade_num, 0, executed_at, instrument_uid, figi, ticker,
	direction, price_amount, price_currency, quantity,
	CASE WHEN price_amount IS NULL OR quantity IS NULL THEN NULL ELSE price_amount * quantity END,
	0
FROM api_trades source
WHERE row_num = 1 AND NOT EXISTS (
	SELECT 1 FROM tinvest.trade_record target
	WHERE target.event_key = LOWER(CONVERT(CHAR(64), HASHBYTES('SHA2_256',
		CONVERT(VARCHAR(512), CONCAT(source.account_id, '|', source.trade_num, '|0'))), 2))
);
GO

IF COL_LENGTH(N'tinvest.operation', N'report_authoritative') IS NULL
	ALTER TABLE tinvest.operation ADD report_authoritative BIT NOT NULL
		CONSTRAINT DF_tinvest_operation_report_authoritative DEFAULT (0);
GO
IF COL_LENGTH(N'tinvest.operation', N'normalized_type') IS NULL
	ALTER TABLE tinvest.operation ADD normalized_type NVARCHAR(100) NOT NULL
		CONSTRAINT DF_tinvest_operation_normalized_type DEFAULT (N'');
GO
IF COL_LENGTH(N'tinvest.operation', N'report_period_end') IS NULL
	ALTER TABLE tinvest.operation ADD report_period_end DATE NULL;
GO
IF COL_LENGTH(N'tinvest.operation', N'report_import_id') IS NULL
	ALTER TABLE tinvest.operation ADD report_import_id BIGINT NULL;
GO
IF NOT EXISTS (SELECT 1 FROM sys.foreign_keys WHERE name = N'FK_tinvest_operation_report_import')
	ALTER TABLE tinvest.operation ADD CONSTRAINT FK_tinvest_operation_report_import
		FOREIGN KEY (report_import_id) REFERENCES tinvest.report_import(import_id);
GO
