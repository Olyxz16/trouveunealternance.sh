-- Add company-level email field.
-- The legacy contact_name/role/email/linkedin columns remain for now (SQLite
-- cannot drop columns without recreating the table) but should no longer be
-- written to. They will be cleaned up in a future migration.
ALTER TABLE companies ADD COLUMN company_email TEXT;
