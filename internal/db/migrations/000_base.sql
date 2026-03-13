CREATE TABLE IF NOT EXISTS schema_migrations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    applied_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS companies (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    -- Identity
    name             TEXT NOT NULL,
    siren            TEXT UNIQUE,          -- 9-digit French company ID
    siret            TEXT,                 -- 14-digit establishment ID
    naf_code         TEXT,                 -- e.g. 62.01Z
    naf_label        TEXT,                 -- e.g. "Programmation informatique"
    -- Location
    city             TEXT,
    department       TEXT,
    address          TEXT,
    -- Size & profile
    headcount_range  TEXT,                 -- e.g. "10-19"
    headcount_exact  INTEGER,              -- from LinkedIn if available
    creation_year    INTEGER,
    legal_form       TEXT,
    -- Web presence
    website          TEXT,
    linkedin_url     TEXT,
    twitter_url      TEXT,
    github_url       TEXT,
    -- Tech profile (enriched)
    tech_stack       TEXT,                 -- comma-separated, from LinkedIn/jobs
    description      TEXT,                 -- company summary
    -- Contact (Legacy, replaced by contacts table in 001_contacts.sql)
    contact_name     TEXT,
    contact_role     TEXT,
    contact_email    TEXT,
    contact_linkedin TEXT,
    careers_page_url TEXT,
    -- Pipeline
    source           TEXT,                 -- pappers, frenchtech, maps, manual
    status           TEXT NOT NULL DEFAULT 'NEW'
        CHECK(status IN (
            'NEW','ENRICHING','TO_CONTACT','CONTACTED','REPLIED',
            'NOT_TECH','PASS'
        )),
    relevance_score  INTEGER DEFAULT 0,    -- 0-10
    email_draft      TEXT,                 -- JSON {subject,body,linkedin_msg}
    notes            TEXT,
    -- Timestamps
    date_found       TEXT NOT NULL DEFAULT (date('now')),
    updated_at       TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_companies_status ON companies(status);
CREATE INDEX IF NOT EXISTS idx_companies_city   ON companies(city);
CREATE INDEX IF NOT EXISTS idx_companies_score  ON companies(relevance_score DESC);

CREATE TABLE IF NOT EXISTS jobs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    date_found      TEXT    NOT NULL DEFAULT (date('now')),
    source_site     TEXT    NOT NULL,
    type            TEXT    NOT NULL CHECK(type IN ('DIRECT','COMPANY_LEAD')),
    title           TEXT    NOT NULL,
    company         TEXT    NOT NULL,
    location        TEXT,
    contract_type   TEXT,
    tech_stack      TEXT,   -- comma-separated tags
    description_summary TEXT,
    apply_url       TEXT,
    careers_page_url TEXT,
    contact_name    TEXT,
    contact_role    TEXT,
    contact_email   TEXT,
    contact_linkedin TEXT,
    relevance_score INTEGER DEFAULT 0,  -- 0-10, LLM-assigned
    notes           TEXT,
    status          TEXT    NOT NULL DEFAULT 'TO_APPLY'
        CHECK(status IN (
            'TO_APPLY','TO_ENRICH','TO_CONTACT',
            'NO_CONTACT_FOUND','CONTACTED','REPLIED','PASS'
        )),
    email_draft     TEXT,   -- JSON: {subject, body, linkedin_msg}
    created_at      TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT    NOT NULL DEFAULT (datetime('now')),
    UNIQUE(company, title)  -- prevent duplicates
);

CREATE TABLE IF NOT EXISTS activity_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id      INTEGER REFERENCES jobs(id) ON DELETE CASCADE,
    action      TEXT    NOT NULL,
    detail      TEXT,
    ts          TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_status ON jobs(status);
CREATE INDEX IF NOT EXISTS idx_type   ON jobs(type);
CREATE INDEX IF NOT EXISTS idx_score  ON jobs(relevance_score DESC);
