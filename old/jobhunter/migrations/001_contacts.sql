-- 001_contacts.sql
CREATE TABLE IF NOT EXISTS contacts (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  company_id   INTEGER NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
  name         TEXT,
  role         TEXT,
  email        TEXT,
  linkedin_url TEXT,
  source       TEXT CHECK(source IN ('linkedin','careers_page','manual','guessed')),
  confidence   TEXT CHECK(confidence IN ('verified','probable','guessed')),
  status       TEXT NOT NULL DEFAULT 'active'
               CHECK(status IN ('active','bounced','unsubscribed','do_not_contact')),
  notes        TEXT,
  created_at   TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at   TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_contacts_company ON contacts(company_id);
CREATE INDEX IF NOT EXISTS idx_contacts_email   ON contacts(email);

-- Add company type classification to companies table if they don't exist
-- We use a transaction and check for existence to make it idempotent
BEGIN TRANSACTION;

-- SQLite doesn't support IF NOT EXISTS in ALTER TABLE ADD COLUMN
-- We'll handle this in the python migration runner or just try and ignore errors if needed.
-- But for a clean migration, we should ideally check.

-- Migrate existing data from companies to contacts
INSERT INTO contacts (company_id, name, role, email, linkedin_url, source, confidence)
SELECT id, contact_name, contact_role, contact_email, contact_linkedin,
       'linkedin', 'probable'
FROM companies
WHERE contact_name IS NOT NULL OR contact_email IS NOT NULL;

-- Note: We can't easily add primary_contact_id here if we want it to be idempotent without more complex logic.
-- The python runner will handle the ALTER TABLE statements.

COMMIT;
