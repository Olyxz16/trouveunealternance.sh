"""
db.py — SQLite database layer for JobHunter
"""
import sqlite3
import json
import os
from datetime import datetime
from pathlib import Path
from typing import Optional

DB_PATH = Path(__file__).parent / "data" / "jobs.db"


def get_conn() -> sqlite3.Connection:
    DB_PATH.parent.mkdir(exist_ok=True)
    conn = sqlite3.connect(DB_PATH)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute("PRAGMA foreign_keys=ON")
    return conn


def init_db():
    """Initialize base schema and run migrations."""
    with get_conn() as conn:
        # Base tables
        conn.executescript("""
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
        """)
        
    run_migrations()
    print(f"✓ Database ready at {DB_PATH}")


def run_migrations():
    migrations_dir = Path(__file__).parent / "migrations"
    if not migrations_dir.exists():
        return

    migration_files = sorted(migrations_dir.glob("*.sql"))
    
    with get_conn() as conn:
        applied = {row["name"] for row in conn.execute("SELECT name FROM schema_migrations").fetchall()}
        
        for m_file in migration_files:
            if m_file.name not in applied:
                print(f"  Applying migration: {m_file.name}")
                sql = m_file.read_text()
                try:
                    conn.executescript(sql)
                    
                    # Handle specific ALTER TABLE statements for 001_contacts.sql
                    if m_file.name == "001_contacts.sql":
                        try:
                            conn.execute("ALTER TABLE companies ADD COLUMN primary_contact_id INTEGER REFERENCES contacts(id)")
                        except sqlite3.OperationalError: pass # Already exists
                        try:
                            conn.execute("ALTER TABLE companies ADD COLUMN company_type TEXT DEFAULT 'UNKNOWN' CHECK(company_type IN ('TECH', 'TECH_ADJACENT', 'NON_TECH', 'UNKNOWN'))")
                        except sqlite3.OperationalError: pass
                        try:
                            conn.execute("ALTER TABLE companies ADD COLUMN has_internal_tech_team INTEGER DEFAULT NULL")
                        except sqlite3.OperationalError: pass
                        try:
                            conn.execute("ALTER TABLE companies ADD COLUMN tech_team_signals TEXT")
                        except sqlite3.OperationalError: pass
                        
                        # Update primary_contact_id for existing ones
                        conn.execute("""
                            UPDATE companies SET primary_contact_id = (
                                SELECT id FROM contacts WHERE company_id = companies.id LIMIT 1
                            )
                        """)

                    conn.execute("INSERT INTO schema_migrations (name) VALUES (?)", (m_file.name,))
                    conn.commit()
                except Exception as e:
                    conn.rollback()
                    print(f"  ❌ Error applying migration {m_file.name}: {e}")
                    raise


def upsert_job(data: dict) -> tuple[int, bool]:
    """Insert or ignore duplicate. Returns (id, is_new)."""
    cols = [
        "source_site","type","title","company","location","contract_type",
        "tech_stack","description_summary","apply_url","relevance_score",
        "notes","status","date_found"
    ]
    row = {k: data.get(k) for k in cols}
    row["date_found"] = row.get("date_found") or datetime.today().strftime("%Y-%m-%d")

    with get_conn() as conn:
        cur = conn.execute(
            f"""INSERT OR IGNORE INTO jobs ({",".join(row.keys())})
                VALUES ({",".join("?" for _ in row)})""",
            list(row.values())
        )
        if cur.lastrowid and cur.rowcount:
            log_activity(cur.lastrowid, "SCRAPED", f"{data.get('type')} from {data.get('source_site')}", conn)
            return cur.lastrowid, True
        # Already exists — fetch its id
        existing = conn.execute(
            "SELECT id FROM jobs WHERE company=? AND title=?",
            (data["company"], data["title"])
        ).fetchone()
        return existing["id"], False


def update_job(job_id: int, fields: dict):
    fields["updated_at"] = datetime.now().isoformat()
    set_clause = ", ".join(f"{k}=?" for k in fields)
    with get_conn() as conn:
        conn.execute(
            f"UPDATE jobs SET {set_clause} WHERE id=?",
            [*fields.values(), job_id]
        )


def get_job(job_id: int) -> Optional[dict]:
    with get_conn() as conn:
        row = conn.execute("SELECT * FROM jobs WHERE id=?", (job_id,)).fetchone()
        return dict(row) if row else None


def get_jobs(status: str = None, type_: str = None, search: str = None) -> list[dict]:
    query = "SELECT * FROM jobs WHERE 1=1"
    params = []
    if status:
        query += " AND status=?"
        params.append(status)
    if type_:
        query += " AND type=?"
        params.append(type_)
    if search:
        query += " AND (company LIKE ? OR title LIKE ? OR tech_stack LIKE ?)"
        params += [f"%{search}%"] * 3
    query += " ORDER BY relevance_score DESC, date_found DESC"
    with get_conn() as conn:
        rows = conn.execute(query, params).fetchall()
        return [dict(r) for r in rows]


def log_activity(job_id: int, action: str, detail: str = None, conn=None):
    def _insert(c):
        c.execute(
            "INSERT INTO activity_log (job_id, action, detail) VALUES (?,?,?)",
            (job_id, action, detail)
        )
    if conn:
        _insert(conn)
    else:
        with get_conn() as c:
            _insert(c)


def get_recent_activity(limit: int = 30) -> list[dict]:
    with get_conn() as conn:
        rows = conn.execute("""
            SELECT a.*, j.company, j.title
            FROM activity_log a
            LEFT JOIN jobs j ON a.job_id = j.id
            ORDER BY a.ts DESC LIMIT ?
        """, (limit,)).fetchall()
        return [dict(r) for r in rows]


def get_stats() -> dict:
    with get_conn() as conn:
        total = conn.execute("SELECT COUNT(*) FROM jobs").fetchone()[0]
        by_status = dict(conn.execute(
            "SELECT status, COUNT(*) FROM jobs GROUP BY status"
        ).fetchall())
        by_type = dict(conn.execute(
            "SELECT type, COUNT(*) FROM jobs GROUP BY type"
        ).fetchall())
        new_today = conn.execute(
            "SELECT COUNT(*) FROM jobs WHERE date_found=date('now')"
        ).fetchone()[0]
        # Prospect stats
        total_prospects = conn.execute("SELECT COUNT(*) FROM companies").fetchone()[0]
        prospects_by_status = dict(conn.execute(
            "SELECT status, COUNT(*) FROM companies GROUP BY status"
        ).fetchall())
        new_prospects_today = conn.execute(
            "SELECT COUNT(*) FROM companies WHERE date_found=date('now')"
        ).fetchone()[0]
    return {
        "total": total,
        "new_today": new_today,
        "by_status": by_status,
        "by_type": by_type,
        "total_prospects": total_prospects,
        "new_prospects_today": new_prospects_today,
        "prospects_by_status": prospects_by_status,
    }


# ── COMPANIES ─────────────────────────────────────────────────────────────────

COMPANY_COLS = [
    "name", "siren", "siret", "naf_code", "naf_label",
    "city", "department", "address",
    "headcount_range", "headcount_exact", "creation_year", "legal_form",
    "website", "linkedin_url", "twitter_url", "github_url",
    "tech_stack", "description",
    "contact_name", "contact_role", "contact_email", "contact_linkedin",
    "careers_page_url", "source", "status", "relevance_score", "notes", "date_found",
    "company_type", "has_internal_tech_team", "tech_team_signals", "primary_contact_id"
]


def upsert_company(data: dict) -> tuple[int, bool]:
    """Insert or ignore by SIREN (or name if no SIREN). Returns (id, is_new)."""
    row = {k: data.get(k) for k in COMPANY_COLS if k in data}
    row["date_found"] = row.get("date_found") or datetime.today().strftime("%Y-%m-%d")
    row["status"] = row.get("status") or "NEW"

    with get_conn() as conn:
        # Dedup by SIREN if available, else by name+city
        if row.get("siren"):
            existing = conn.execute(
                "SELECT id FROM companies WHERE siren=?", (row["siren"],)
            ).fetchone()
        else:
            existing = conn.execute(
                "SELECT id FROM companies WHERE name=? AND city=?",
                (row["name"], row.get("city"))
            ).fetchone()

        if existing:
            return existing["id"], False

        cur = conn.execute(
            f"""INSERT INTO companies ({",".join(row.keys())})
                VALUES ({",".join("?" for _ in row)})""",
            list(row.values())
        )
        return cur.lastrowid, True


def update_company(company_id: int, fields: dict):
    fields["updated_at"] = datetime.now().isoformat()
    # Filter only columns that exist
    valid_fields = {k: v for k, v in fields.items() if k in COMPANY_COLS or k == "updated_at"}
    set_clause = ", ".join(f"{k}=?" for k in valid_fields)
    with get_conn() as conn:
        conn.execute(
            f"UPDATE companies SET {set_clause} WHERE id=?",
            [*valid_fields.values(), company_id]
        )


def get_company(company_id: int) -> Optional[dict]:
    with get_conn() as conn:
        row = conn.execute("SELECT * FROM companies WHERE id=?", (company_id,)).fetchone()
        return dict(row) if row else None


def get_companies(
    status: str = None,
    city: str = None,
    search: str = None,
    min_score: int = 0,
) -> list[dict]:
    query = "SELECT * FROM companies WHERE (relevance_score >= ? OR relevance_score IS NULL)"
    params = [min_score]
    if status:
        query += " AND status=?"
        params.append(status)
    if city:
        query += " AND (city LIKE ? OR department LIKE ?)"
        params += [f"%{city}%", f"%{city}%"]
    if search:
        query += " AND (name LIKE ? OR tech_stack LIKE ? OR description LIKE ? OR naf_label LIKE ?)"
        params += [f"%{search}%"] * 4
    query += " ORDER BY relevance_score DESC, date_found DESC"
    with get_conn() as conn:
        rows = conn.execute(query, params).fetchall()
        return [dict(r) for r in rows]


def get_prospect_cities() -> list[dict]:
    \"\"\"Return distinct cities with company counts, for the sidebar.\"\"\"
    with get_conn() as conn:
        rows = conn.execute(\"\"\"
            SELECT city, COUNT(*) as count
            FROM companies
            WHERE city IS NOT NULL AND city != ''
            GROUP BY city ORDER BY count DESC LIMIT 20
        \"\"\").fetchall()
        return [dict(r) for r in rows]

# ── RUNS & USAGE (V1) ────────────────────────────────────────────────────────

def get_runs(limit: int = 50) -> list[dict]:
    with get_conn() as conn:
        rows = conn.execute(\"\"\"
            SELECT run_id, 
                   MIN(ts) as start_time, 
                   MAX(ts) as last_activity,
                   COUNT(CASE WHEN status='ok' THEN 1 END) as ok_count,
                   COUNT(CASE WHEN status='error' THEN 1 END) as error_count,
                   COUNT(CASE WHEN status='needs_review' THEN 1 END) as review_count,
                   COUNT(DISTINCT company_id) as companies_processed
            FROM run_log
            GROUP BY run_id
            ORDER BY start_time DESC
            LIMIT ?
        \"\"\", (limit,)).fetchall()
        return [dict(r) for r in rows]

def get_run_detail(run_id: str) -> list[dict]:
    with get_conn() as conn:
        rows = conn.execute(\"\"\"
            SELECT l.*, c.name as company_name, j.title as job_title
            FROM run_log l
            LEFT JOIN companies c ON l.company_id = c.id
            LEFT JOIN jobs j ON l.job_id = j.id
            WHERE l.run_id = ?
            ORDER BY l.ts ASC
        \"\"\", (run_id,)).fetchall()
        return [dict(r) for r in rows]

def get_usage_today() -> dict:
    with get_conn() as conn:
        row = conn.execute(\"\"\"
            SELECT COUNT(*) as requests,
                   SUM(prompt_tokens) as prompt_tokens,
                   SUM(completion_tokens) as completion_tokens,
                   SUM(cost_usd) as total_cost
            FROM llm_usage
            WHERE date(ts) = date('now')
        \"\"\").fetchone()
        return dict(row) if row else {\"requests\": 0, \"prompt_tokens\": 0, \"completion_tokens\": 0, \"total_cost\": 0}

def get_usage_history(days: int = 30) -> list[dict]:
    with get_conn() as conn:
        rows = conn.execute(\"\"\"
            SELECT date(ts) as day,
                   COUNT(*) as requests,
                   SUM(prompt_tokens) as prompt_tokens,
                   SUM(completion_tokens) as completion_tokens,
                   SUM(cost_usd) as cost
            FROM llm_usage
            GROUP BY day
            ORDER BY day DESC
            LIMIT ?
        \"\"\", (days,)).fetchall()
        return [dict(r) for r in rows]

def get_contacts(company_id: int) -> list[dict]:
    with get_conn() as conn:
        rows = conn.execute(\"\"\"
            SELECT * FROM contacts WHERE company_id = ? ORDER BY created_at DESC
        \"\"\", (company_id,)).fetchall()
        return [dict(r) for r in rows]

def get_scraping_health() -> dict:
    with get_conn() as conn:
        jina_stats = conn.execute(\"\"\"
            SELECT COUNT(*) as total,
                   COUNT(CASE WHEN quality > 0.5 THEN 1 END) as healthy
            FROM scrape_cache
            WHERE method = 'jina' AND fetched_at > datetime('now', '-24 hours')
        \"\"\").fetchone()
        
        mcp_count = conn.execute(\"\"\"
            SELECT COUNT(*) FROM scrape_cache 
            WHERE method = 'mcp' AND fetched_at > datetime('now', '-24 hours')
        \"\"\").fetchone()[0]
        
        needs_review = conn.execute(\"\"\"
            SELECT COUNT(*) FROM run_log WHERE status = 'needs_review'
        \"\"\").fetchone()[0]
        
        return {
            \"jina_total\": jina_stats[\"total\"],
            \"jina_healthy\": jina_stats[\"healthy\"],
            \"mcp_24h\": mcp_count,
            \"needs_review_count\": needs_review
        }


if __name__ == "__main__":
    init_db()
