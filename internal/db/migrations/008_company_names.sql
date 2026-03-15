ALTER TABLE companies ADD COLUMN legal_name      TEXT;
ALTER TABLE companies ADD COLUMN acronym         TEXT;
ALTER TABLE companies ADD COLUMN name_normalized TEXT;
CREATE INDEX IF NOT EXISTS idx_companies_name_norm ON companies(name_normalized);
