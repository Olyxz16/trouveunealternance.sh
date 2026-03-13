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
-- SQLite doesn't support IF NOT EXISTS in ALTER TABLE ADD COLUMN directly in all versions, 
-- but we can try to add them. The migration runner will need to handle errors or we can use a trick.
-- For simplicity in Go, we will try to execute these and ignore "duplicate column" errors.

-- These will be handled in Go code to be safe, but kept here for reference
-- ALTER TABLE companies ADD COLUMN primary_contact_id INTEGER REFERENCES contacts(id);
-- ALTER TABLE companies ADD COLUMN company_type TEXT DEFAULT 'UNKNOWN' CHECK(company_type IN ('TECH', 'TECH_ADJACENT', 'NON_TECH', 'UNKNOWN'));
-- ALTER TABLE companies ADD COLUMN has_internal_tech_team INTEGER DEFAULT NULL;
-- ALTER TABLE companies ADD COLUMN tech_team_signals TEXT;

-- Migrate existing data from companies to contacts
INSERT INTO contacts (company_id, name, role, email, linkedin_url, source, confidence)
SELECT id, contact_name, contact_role, contact_email, contact_linkedin,
       'linkedin', 'probable'
FROM companies
WHERE (contact_name IS NOT NULL OR contact_email IS NOT NULL)
AND NOT EXISTS (SELECT 1 FROM contacts WHERE contacts.company_id = companies.id);
