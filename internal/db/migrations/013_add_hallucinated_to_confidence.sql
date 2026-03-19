-- 013_add_hallucinated_to_confidence.sql
PRAGMA foreign_keys=OFF;

CREATE TABLE contacts_new (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  company_id   INTEGER NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
  name         TEXT,
  role         TEXT,
  email        TEXT,
  linkedin_url TEXT,
  source       TEXT CHECK(source IN ('linkedin','careers_page','manual','guessed')),
  confidence   TEXT CHECK(confidence IN ('verified','probable','guessed','hallucinated')),
  status       TEXT NOT NULL DEFAULT 'active'
               CHECK(status IN ('active','bounced','unsubscribed','do_not_contact')),
  notes        TEXT,
  created_at   TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at   TEXT NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO contacts_new (id, company_id, name, role, email, linkedin_url, source, confidence, status, notes, created_at, updated_at)
SELECT id, company_id, name, role, email, linkedin_url, source, confidence, status, notes, created_at, updated_at FROM contacts;

DROP TABLE contacts;
ALTER TABLE contacts_new RENAME TO contacts;

CREATE INDEX idx_contacts_company ON contacts(company_id);
CREATE INDEX idx_contacts_email   ON contacts(email);

PRAGMA foreign_keys=ON;
