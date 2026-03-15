-- Recreate without CHECK to allow the full status set
-- NEW, ENRICHING, ENRICHED, TO_CONTACT, NO_CONTACT_FOUND, CONTACTED, REPLIED, NOT_TECH, NEEDS_REVIEW, FAILED, PASS
CREATE TABLE companies_new (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    name             TEXT NOT NULL,
    siren            TEXT UNIQUE,
    siret            TEXT,
    naf_code         TEXT,
    naf_label        TEXT,
    city             TEXT,
    department       TEXT,
    address          TEXT,
    headcount_range  TEXT,
    headcount_exact  INTEGER,
    creation_year    INTEGER,
    legal_form       TEXT,
    website          TEXT,
    linkedin_url     TEXT,
    twitter_url      TEXT,
    github_url       TEXT,
    tech_stack       TEXT,
    description      TEXT,
    contact_name     TEXT,
    contact_role     TEXT,
    contact_email    TEXT,
    contact_linkedin TEXT,
    careers_page_url TEXT,
    source           TEXT,
    status           TEXT NOT NULL DEFAULT 'NEW',
    relevance_score  INTEGER DEFAULT 0,
    email_draft      TEXT,
    notes            TEXT,
    date_found       TEXT NOT NULL DEFAULT (date('now')),
    updated_at       TEXT NOT NULL DEFAULT (datetime('now')),
    primary_contact_id INTEGER,
    company_type     TEXT DEFAULT 'UNKNOWN',
    has_internal_tech_team INTEGER,
    tech_team_signals TEXT,
    legal_name       TEXT,
    acronym          TEXT,
    name_normalized  TEXT,
    company_email    TEXT
);

INSERT INTO companies_new (
    id, name, siren, siret, naf_code, naf_label, city, department, address,
    headcount_range, headcount_exact, creation_year, legal_form, website,
    linkedin_url, twitter_url, github_url, tech_stack, description,
    contact_name, contact_role, contact_email, contact_linkedin,
    careers_page_url, source, status, relevance_score, email_draft,
    notes, date_found, updated_at, primary_contact_id, company_type,
    has_internal_tech_team, tech_team_signals, legal_name, acronym,
    name_normalized
)
SELECT 
    id, name, siren, siret, naf_code, naf_label, city, department, address,
    headcount_range, headcount_exact, creation_year, legal_form, website,
    linkedin_url, twitter_url, github_url, tech_stack, description,
    contact_name, contact_role, contact_email, contact_linkedin,
    careers_page_url, source, status, relevance_score, email_draft,
    notes, date_found, updated_at, primary_contact_id, company_type,
    has_internal_tech_team, tech_team_signals, legal_name, acronym,
    name_normalized
FROM companies;
DROP TABLE companies;
ALTER TABLE companies_new RENAME TO companies;

CREATE INDEX IF NOT EXISTS idx_companies_status ON companies(status);
CREATE INDEX IF NOT EXISTS idx_companies_city   ON companies(city);
CREATE INDEX IF NOT EXISTS idx_companies_score  ON companies(relevance_score DESC);
CREATE INDEX IF NOT EXISTS idx_companies_name_norm ON companies(name_normalized);
