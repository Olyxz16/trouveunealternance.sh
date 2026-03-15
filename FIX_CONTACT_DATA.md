# Fix Plan — Company vs Contact Data Separation

> Concrete implementation plan for the agent currently working on the codebase.
> Addresses the data quality issues found in testing: company-level data appearing
> in individual contact fields, only one contact saved per company, and missing
> individual profile enrichment.

---

## Root Causes

### 1. `RawCompanyPage` conflates two levels of data
`internal/enricher/extract.go` has a single struct with both company-level fields
AND individual contact fields (`contact_name`, `contact_role`, `contact_linkedin`,
`contact_email`). The LLM is asked to do two jobs in one call and returns
whichever email/URL it finds first — which is usually the company's generic
`contact@` or the company LinkedIn URL, not an individual's.

### 2. Extraction prompt doesn't distinguish company vs individual
`ExtractionSystemPrompt` asks to "Find a primary technical contact" as part of
extracting company info. This is wrong — contact discovery is a separate step
with a separate fetch target (the People tab).

### 3. People tab prompt doesn't distinguish personal vs company URLs
`ContactSelectionPrompt` doesn't tell the LLM that `linkedin_url` must be a
personal `/in/` profile URL. The LLM returns the company `/company/` URL instead.

### 4. Only `Best` contact is saved, not all found contacts
In `enrich.go`, `contacts.Contacts` (the full list) is ignored. Only `contacts.Best`
is persisted. The contacts table should hold all found individuals.

### 5. No individual profile enrichment
The People tab gives name + role + `/in/` URL. The individual's own profile page
contains richer data (visible email, recent posts, precise background) that makes
outreach more personal. Currently this second fetch never happens.

### 6. Legacy `contact_*` columns on `companies` still written to / scanned
`000_base.sql` has `contact_name`, `contact_role`, `contact_email`,
`contact_linkedin` on `companies`. These are Python POC leftovers. They cause
scan mismatches and conceptual confusion. They should stop being used immediately.

---

## Target Data Model

### `companies` table — company-level only
Fields to ADD (via migration):
- `company_email TEXT` — generic company contact (careers@, info@, contact@)

Fields to STOP writing to (do not drop yet — SQLite constraint, leave null):
- `contact_name`, `contact_role`, `contact_email`, `contact_linkedin`

Fields already correct:
- `linkedin_url` — the company LinkedIn page (`/company/...`)
- `website` — company public website
- `careers_page_url` — direct URL to jobs/careers page

### `contacts` table — individuals only
Fields already exist and are correct:
- `name`, `role`, `email`, `linkedin_url`, `source`, `confidence`

`linkedin_url` in this table must ALWAYS be a personal profile (`/in/...`), never
a company page (`/company/...`).

The `primary_contact_id` FK on `companies` points to the best individual to
contact for this company.

---

## Changes Required

### Change 1 — Split `RawCompanyPage` into two structs
**File:** `internal/enricher/extract.go`

Replace `RawCompanyPage` with two structs:

```go
// CompanyPageData — extracted from company LinkedIn page or website.
// Company-level data ONLY. No individual people.
type CompanyPageData struct {
    Name                string   `json:"name"`
    Description         string   `json:"description"`
    City                string   `json:"city"`
    Headcount           string   `json:"headcount"`
    TechStack           []string `json:"tech_stack"`
    Website             string   `json:"website"`
    CareersPageURL      string   `json:"careers_page_url"`
    CompanyEmail        string   `json:"company_email"`    // generic: careers@, contact@, info@
    GithubOrg           string   `json:"github_org"`
    EngineeringBlogURL  string   `json:"engineering_blog_url"`
    OpenSourceMentioned bool     `json:"open_source_mentioned"`
    InfraKeywords       []string `json:"infrastructure_keywords"`
    CompanyType         string   `json:"company_type"`
    HasInternalTechTeam bool     `json:"has_internal_tech_team"`
    TechTeamSignals     []string `json:"tech_team_signals"`
}

// PeoplePageData — extracted from LinkedIn People tab.
// Individual people ONLY.
type PeoplePageData struct {
    Contacts []IndividualContact `json:"contacts"`
}

type IndividualContact struct {
    Name        string `json:"name"`
    Role        string `json:"role"`
    LinkedinURL string `json:"linkedin_url"` // MUST be /in/ personal profile, never /company/
    Email       string `json:"email"`         // personal work email if publicly visible; empty if not
}

// IndividualProfileData — extracted from a personal LinkedIn /in/ profile.
// Used to enrich a contact found on the People tab.
type IndividualProfileData struct {
    Name          string   `json:"name"`
    Role          string   `json:"role"`
    Email         string   `json:"email"`         // if visible on profile
    RecentPosts   []string `json:"recent_posts"`  // topics/themes of recent activity
    Background    string   `json:"background"`    // 1-2 sentence summary of their profile
    TechStack     []string `json:"tech_stack"`    // technologies mentioned on their profile
}
```

### Change 2 — Update `ExtractionSystemPrompt`
**File:** `internal/enricher/extract.go`

Replace with:

```go
const CompanyExtractionPrompt = `You are extracting COMPANY-LEVEL information from a company's LinkedIn page or website.

CRITICAL: Do NOT extract individual people's contact details. That is a separate step.

Rules:
- website: the company's public website URL (not LinkedIn)
- careers_page_url: direct URL to their jobs/careers page if visible
- company_email: a generic company contact address (careers@, jobs@, contact@, info@) if visible — NOT an individual's personal email
- Do NOT include any /in/ LinkedIn profile URLs — only company-level data belongs here
- company_type: TECH if core product is software/infra, TECH_ADJACENT if non-tech business with internal tech team, NON_TECH otherwise

Return ONLY a valid JSON object.`
```

### Change 3 — Update `ContactSelectionPrompt`
**File:** `internal/enricher/contacts.go`

Replace with:

```go
const PeopleExtractionPrompt = `You are extracting a list of individual employees from a LinkedIn People tab or employee listing.

Return ALL relevant contacts found (up to 5 people). Do not return just one.

STRICT RULES:
- linkedin_url MUST be a personal LinkedIn profile URL starting with /in/ — NEVER a /company/ URL
- email: only include if a personal work email is explicitly visible on the page — do NOT include generic company emails (contact@, info@, careers@, jobs@) — leave empty if unsure
- name and role are required — skip entries where you cannot determine both
- Focus on: CTO, VP Engineering, Engineering Manager, Tech Lead, DevOps Engineer, Infrastructure Manager, IT Director, Technical Recruiter

Return a JSON object with a single field "contacts" containing the list.`

const ContactRankingPrompt = `Given this list of contacts at a %s company, pick the single BEST person to cold-email for a DevOps/backend internship.

Priority order: CTO > Engineering Manager > Tech Lead > Infrastructure Manager > IT Director > Technical Recruiter > HR Manager

Return a JSON object with field "best" containing the chosen contact object (same fields as input), or null if none are suitable.`
```

Note the split into two prompts: one for extraction (get all people from the page),
one for ranking (pick the best one). This keeps the LLM tasks small and focused.

### Change 4 — Update `contacts.go` to use two-step approach
**File:** `internal/enricher/contacts.go`

Replace `DiscoverLinkedInContactsWithMD` with two functions:

```go
// ExtractPeopleFromPage extracts all individuals from a People tab markdown.
// Returns up to 5 candidates. Does NOT rank them.
func (c *Classifier) ExtractPeopleFromPage(ctx context.Context, markdown string, runID string) (PeoplePageData, error) {
    var result PeoplePageData
    req := llm.CompletionRequest{
        System: PeopleExtractionPrompt,
        User:   fmt.Sprintf("LinkedIn People tab content:\n\n%s", markdown),
    }
    err := c.llm.CompleteJSON(ctx, req, "extract_people", runID, &result)
    return result, err
}

// RankContacts picks the best contact from a list for a given company type.
func (c *Classifier) RankContacts(ctx context.Context, contacts []IndividualContact, companyType string, runID string) (*IndividualContact, error) {
    if len(contacts) == 0 {
        return nil, nil
    }
    if len(contacts) == 1 {
        return &contacts[0], nil
    }

    type rankResult struct {
        Best *IndividualContact `json:"best"`
    }
    var result rankResult
    
    contactsJSON, _ := json.Marshal(contacts)
    req := llm.CompletionRequest{
        System: fmt.Sprintf(ContactRankingPrompt, companyType),
        User:   fmt.Sprintf("Contacts:\n%s", string(contactsJSON)),
    }
    err := c.llm.CompleteJSON(ctx, req, "rank_contacts", runID, &result)
    return result.Best, err
}

// EnrichIndividualProfile fetches and extracts data from a personal /in/ profile.
func (c *Classifier) EnrichIndividualProfile(ctx context.Context, fetcher *scraper.CascadeFetcher, contact IndividualContact, runID string) (IndividualProfileData, error) {
    if contact.LinkedinURL == "" {
        return IndividualProfileData{Name: contact.Name, Role: contact.Role}, nil
    }

    res, err := fetcher.Fetch(ctx, contact.LinkedinURL)
    if err != nil {
        // Non-fatal: return what we already know
        return IndividualProfileData{Name: contact.Name, Role: contact.Role}, nil
    }

    var profile IndividualProfileData
    req := llm.CompletionRequest{
        System: IndividualProfilePrompt,
        User:   fmt.Sprintf("LinkedIn profile content:\n\n%s", res.ContentMD),
    }
    err = c.llm.CompleteJSON(ctx, req, "enrich_individual", runID, &profile)
    if err != nil {
        return IndividualProfileData{Name: contact.Name, Role: contact.Role}, nil
    }
    return profile, nil
}

const IndividualProfilePrompt = `You are extracting information from a personal LinkedIn profile page.

Return a JSON object with:
- name: full name
- role: current job title and company
- email: personal work email if explicitly visible on the profile — empty string if not visible
- recent_posts: list of up to 3 topics or themes from their recent activity (empty list if none)
- background: 1-2 sentence summary of their professional background relevant to tech
- tech_stack: list of technologies mentioned on their profile`
```

### Change 5 — Rewrite enrichment flow in `enrich.go`
**File:** `internal/enricher/enrich.go`

Replace the current "step 4" (contact discovery) section with:

```go
// 4. Extract company-level info from main page
info, err := e.classifier.ExtractCompanyInfo(ctx, res.ContentMD, runID)
if err != nil {
    return fmt.Errorf("extraction failed: %w", err)
}

updates := map[string]interface{}{
    "description":            info.Description,
    "tech_stack":             strings.Join(info.TechStack, ", "),
    "website":                firstNonEmpty(info.Website, website),
    "careers_page_url":       info.CareersPageURL,
    "company_email":          info.CompanyEmail,
    "has_internal_tech_team": info.HasInternalTechTeam,
    "tech_team_signals":      strings.Join(info.TechTeamSignals, ", "),
}
if comp.CompanyType == "UNKNOWN" {
    updates["company_type"] = info.CompanyType
}
_ = e.db.UpdateCompany(comp.ID, updates)

// 5. Discover individuals from LinkedIn People tab
if linkedin == "" {
    _ = e.db.UpdateCompany(comp.ID, map[string]interface{}{"status": "NO_CONTACT_FOUND"})
    return nil
}

peopleURL := strings.TrimSuffix(linkedin, "/") + "/people/"
peopleRes, err := e.fetcher.Fetch(ctx, peopleURL)
if err != nil {
    _ = e.db.UpdateCompany(comp.ID, map[string]interface{}{"status": "NO_CONTACT_FOUND"})
    return nil
}

people, err := e.classifier.ExtractPeopleFromPage(ctx, peopleRes.ContentMD, runID)
if err != nil || len(people.Contacts) == 0 {
    _ = e.db.UpdateCompany(comp.ID, map[string]interface{}{"status": "NO_CONTACT_FOUND"})
    return nil
}

// 6. Enrich top candidates from their individual /in/ profiles (max 3)
maxProfiles := 3
enriched := make([]IndividualContact, 0, len(people.Contacts))
for i, candidate := range people.Contacts {
    if i >= maxProfiles {
        // Save remaining candidates without profile enrichment
        enriched = append(enriched, candidate)
        continue
    }
    profile, _ := e.classifier.EnrichIndividualProfile(ctx, e.fetcher, candidate, runID)
    // Merge profile data back into candidate
    if profile.Email != "" && candidate.Email == "" {
        candidate.Email = profile.Email
    }
    enriched = append(enriched, candidate)
}

// 7. Rank to find best contact
best, _ := e.classifier.RankContacts(ctx, enriched, info.CompanyType, runID)

// 8. Save ALL contacts, mark best as primary
var primaryContactID int
for _, candidate := range enriched {
    isPrimary := best != nil && candidate.Name == best.Name
    contactID, err := e.db.AddContact(&db.Contact{
        CompanyID:   comp.ID,
        Name:        db.ToNullString(candidate.Name),
        Role:        db.ToNullString(candidate.Role),
        LinkedinURL: db.ToNullString(candidate.LinkedinURL),
        Email:       db.ToNullString(candidate.Email),
        Source:      db.ToNullString("linkedin"),
        Confidence:  db.ToNullString("probable"),
    }, isPrimary)
    if err == nil && isPrimary {
        primaryContactID = contactID
    }
}

status := "NO_CONTACT_FOUND"
if primaryContactID > 0 {
    status = "TO_CONTACT"
}
_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{"status": status})
return nil
```

Helper function to add at the bottom of `enrich.go`:
```go
func firstNonEmpty(vals ...string) string {
    for _, v := range vals {
        if v != "" {
            return v
        }
    }
    return ""
}
```

### Change 6 — Add migration 011
**File:** `internal/db/migrations/011_company_email.sql`

```sql
-- Add company-level email field.
-- The legacy contact_name/role/email/linkedin columns remain for now (SQLite
-- cannot drop columns without recreating the table) but should no longer be
-- written to. They will be cleaned up in a future migration.
ALTER TABLE companies ADD COLUMN company_email TEXT;
```

### Change 7 — Add `company_email` to `db.Company` struct and queries
**File:** `internal/db/companies.go`

1. Add `CompanyEmail sql.NullString` field to the `Company` struct.

2. Add `company_email` to the `INSERT` in `UpsertCompany`.

3. Update the `SELECT *` scans in `GetCompany` and `GetCompaniesForEnrichment`
   to scan `company_email` into the new field. This is the fragile part — `SELECT *`
   relies on column order. The safest fix is to switch both queries to explicit
   column lists rather than `SELECT *`. Do this as part of this change.

4. Add `company_email` to `UpdateCompany`'s valid fields (it already allows any
   field via the map, so this is just documentation — no code change needed there).

### Change 8 — Remove `RawCompanyPage` references throughout
**File:** `internal/enricher/extract.go`, `internal/enricher/enrich.go`

- Rename `ExtractCompanyInfo` to return `CompanyPageData` instead of `RawCompanyPage`
- Update `enrich.go` to use `CompanyPageData`
- Remove the old `RawCompanyPage` struct entirely

### Change 9 — Remove dead code from `contacts.go`
**File:** `internal/enricher/contacts.go`

Remove `DiscoverLinkedInContacts` (the one that calls `DiscoverURL` which returns
a stub error) and `DiscoverURL`. These are dead code — the new flow replaces them
entirely.

---

## Implementation Order

Do these in strict order — each change depends on the previous.

| Step | File | What |
|---|---|---|
| 1 | `internal/db/migrations/011_company_email.sql` | Add `company_email` column |
| 2 | `internal/db/companies.go` | Add `CompanyEmail` field, switch `SELECT *` to explicit columns |
| 3 | `internal/enricher/extract.go` | Replace `RawCompanyPage` with `CompanyPageData`, update `ExtractionSystemPrompt`, rename `ExtractCompanyInfo` |
| 4 | `internal/enricher/contacts.go` | Replace with `PeoplePageData`/`IndividualContact`/`IndividualProfileData` structs, new prompts, `ExtractPeopleFromPage`, `RankContacts`, `EnrichIndividualProfile` |
| 5 | `internal/enricher/enrich.go` | Rewrite contact discovery section (steps 4–8 above), add `firstNonEmpty` helper |
| 6 | Smoke test | Run `go build ./...` — should compile clean. Then `go run . enrich --batch 1` on one company and inspect the DB. |

---

## What to Verify After Implementation

Run enrichment on one company with a known LinkedIn page and check:

1. `companies.company_email` is set to a generic address like `careers@...` or empty — not a personal email
2. `companies.linkedin_url` is a `/company/` URL
3. `companies.website` is a plain domain URL
4. `companies.careers_page_url` points to their jobs page if one was found
5. `contacts` table has 2–5 rows for that company, not just 1
6. Each `contacts.linkedin_url` is a `/in/` personal profile URL — never `/company/`
7. `contacts.email` is either empty or a personal work email — never `contact@` or `info@`
8. `companies.primary_contact_id` points to the most relevant individual (CTO/tech lead rank)

---

## Notes on Individual Profile Enrichment

Fetching individual `/in/` profiles adds MCP calls per company. The cap of 3
profiles per company keeps this bounded. The `EnrichIndividualProfile` call is
non-fatal — if it fails (profile private, MCP timeout), the candidate is still
saved with just the name/role/URL from the People tab. The email from the
individual profile is the most valuable addition when present — it means you have
a verified personal work email rather than a guessed one.

The `IndividualProfileData.recent_posts` field is forward-looking — it feeds into
the draft generation in Step 17, allowing the LinkedIn hook to reference something
the person actually wrote or engaged with recently. Store it in `contacts.notes`
for now until a dedicated column is added.
