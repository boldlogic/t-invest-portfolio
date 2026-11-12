/* Preserve full broker-report cash-operation descriptions. */
IF COL_LENGTH(N'tinvest.operation', N'name') < 1000
	ALTER TABLE tinvest.operation ALTER COLUMN name NVARCHAR(500) NOT NULL;
GO
