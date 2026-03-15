-- Proper name fields from StockUniteLegale join.
-- Eliminates "Company 32698..." fallbacks from the old single-file SIRENE scan.
ALTER TABLE companies ADD COLUMN legal_name       TEXT;  -- denominationUniteLegale raw
ALTER TABLE companies ADD COLUMN acronym          TEXT;  -- sigleUniteLegale
ALTER TABLE companies ADD COLUMN name_normalized  TEXT;  -- lowercased, accent-stripped, for fuzzy match
CREATE INDEX IF NOT EXISTS idx_companies_name_norm ON companies(name_normalized);
