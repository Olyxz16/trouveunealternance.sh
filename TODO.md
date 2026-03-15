# JobHunter Refactoring Progress

- [x] Fix 2: Update `scrape_cache` CHECK constraint in `internal/db/migrations/009_fix_scrape_cache_method.sql`
- [x] Fix 1: Add `company_email` to `companies_new` in `internal/db/migrations/010_company_status.sql`
- [x] Fix 9: Resolve `max()` builtin conflict in `internal/tui/pipeline_view.go`
- [x] Fix 3: Implement `shouldCache` in `internal/scraper/cascade.go`
- [x] Fix 4: Normalize LinkedIn URLs in `internal/enricher/contacts.go`
- [x] Fix 5: Handle NULL `primary_contact_id` in `cmd/generate.go`
- [x] Fix 6: Verify `allCompanyCols` scan order in `internal/db/companies.go` (Verified)
- [x] Fix 7: Implement `GetJobs` and fix `handleGetJobs`
- [x] Fix 8: Update health panel queries and dashboard
- [x] Fix 10: Update `PLAN.md`
- [x] Fix 11: Remove dead `JinaError` in `internal/errors/errors.go`
- [x] Fix 12: Update stale comment in `internal/scraper/fetcher.go`
- [x] Final Validation: Wipe DB and re-run migrations
