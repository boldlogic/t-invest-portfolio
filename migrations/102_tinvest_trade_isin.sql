/* Keep ISIN distinct from ticker and index it for exact instrument fallback. */
IF COL_LENGTH(N'tinvest.trade_record', N'isin') IS NULL
	ALTER TABLE tinvest.trade_record ADD isin NVARCHAR(12) NOT NULL
		CONSTRAINT DF_tinvest_trade_record_isin DEFAULT (N'');
GO

IF NOT EXISTS (SELECT 1 FROM sys.indexes WHERE object_id=OBJECT_ID(N'tinvest.trade_record') AND name=N'IX_tinvest_trade_record_isin')
	CREATE NONCLUSTERED INDEX IX_tinvest_trade_record_isin ON tinvest.trade_record(isin) WHERE isin<>N'';
GO

/* Earlier report imports stored an ISIN in ticker; move it to the new field. */
UPDATE tinvest.trade_record
SET isin=UPPER(LTRIM(RTRIM(ticker))), ticker=N''
WHERE report_authoritative=1
	AND LEN(LTRIM(RTRIM(ticker)))=12
	AND LTRIM(RTRIM(ticker)) COLLATE Latin1_General_100_BIN2 LIKE
		'[A-Z][A-Z][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][0-9]';
GO

/* The same API field can contain an ISIN instead of a ticker. Normalize API facts too. */
UPDATE tinvest.trade_record
SET isin=UPPER(LTRIM(RTRIM(ticker))), ticker=N''
WHERE report_authoritative=0 AND LEN(LTRIM(RTRIM(ticker)))=12
	AND LTRIM(RTRIM(ticker)) COLLATE Latin1_General_100_BIN2 LIKE
		'[A-Z][A-Z][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][0-9]';
GO

/* Restore an API ticker where the same trade number was already imported. */
;WITH api_trade AS (
	SELECT COALESCE(NULLIF(f.account_id,N''),o.account_id) AS account_id,
		f.trade_num,f.ticker,f.figi,f.instrument_uid,
		ROW_NUMBER() OVER (PARTITION BY COALESCE(NULLIF(f.account_id,N''),o.account_id),f.trade_num
			ORDER BY COALESCE(f.executed_at,o.executed_at) DESC) AS row_num
	FROM tinvest.operation_fill f
	JOIN tinvest.operation o ON o.operation_id=f.operation_id
	WHERE NULLIF(f.trade_num,N'') IS NOT NULL
)
UPDATE tr SET
	tr.ticker=CASE WHEN LEN(LTRIM(RTRIM(api.ticker)))=12 AND LTRIM(RTRIM(api.ticker)) COLLATE Latin1_General_100_BIN2 LIKE
		'[A-Z][A-Z][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][0-9]'
		THEN tr.ticker ELSE COALESCE(NULLIF(api.ticker,N''),tr.ticker) END,
	tr.figi=COALESCE(NULLIF(api.figi,N''),tr.figi),
	tr.instrument_uid=COALESCE(NULLIF(api.instrument_uid,N''),tr.instrument_uid)
FROM tinvest.trade_record tr
JOIN api_trade api ON api.account_id=tr.account_id AND api.trade_num=tr.trade_number AND api.row_num=1
WHERE tr.report_authoritative=1 AND tr.repo_part=0 AND tr.isin<>N'';
GO

/* Add ISINs to existing API facts whose instrument has an unambiguous reference link. */
UPDATE tr SET tr.isin=UPPER(LTRIM(RTRIM(ref.isin)))
FROM tinvest.trade_record tr
JOIN tinvest.instrument i ON i.uid=tr.instrument_uid
JOIN ref.instruments ref ON ref.instrument_id=i.ref_instrument_id
WHERE tr.isin=N'' AND NULLIF(ref.isin,N'') IS NOT NULL;
GO

/* Some API instruments are linked to the reference catalog only by their T-Invest UID. */
UPDATE tr SET tr.isin=UPPER(LTRIM(RTRIM(ref.isin)))
FROM tinvest.trade_record tr
JOIN tinvest.instrument i ON i.uid=tr.instrument_uid
JOIN ref.external_systems es ON es.ext_system=N'TINVEST'
JOIN ref.external_codes ec ON ec.ext_system_id=es.ext_system_id AND ec.ext_code_type_id=6 AND ec.ext_code=i.uid
JOIN ref.instruments ref ON ref.instrument_id=ec.internal_id
WHERE tr.isin=N'' AND NULLIF(ref.isin,N'') IS NOT NULL;
GO
