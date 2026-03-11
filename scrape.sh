#!/bin/bash

# ============================================================
#  Job Scraper Orchestrator
#  Runs Gemini CLI through Stage 1 → Stage 2 → notifies you
# ============================================================

# --- CONFIG (edit these) ------------------------------------
YOUR_NAME=""
SKILLS="Go, Docker, Linux, CI/CD, Git, java, C, Grafana, Kubernetes"
INTERNSHIP_DURATION="1 year"
START_DATE="September 2025"
OUTPUT_FILE="$(pwd)/jobs.tsv"
GEMINI_CMD="gemini"   # or full path if needed: ~/.npm-global/bin/gemini
LOG_FILE="$(pwd)/scrape.log"

SITES=(
  "Welcome to the Jungle"
  "LinkedIn Jobs"
  "Indeed France"
  "Lesjeudis"
)

# --- NOTIFICATION FUNCTION ----------------------------------
notify() {
  local title="$1"
  local message="$2"

  # macOS
  if command -v osascript &>/dev/null; then
    osascript -e "display notification \"$message\" with title \"$title\" sound name \"Glass\""

  # Linux (GNOME / KDE)
  elif command -v notify-send &>/dev/null; then
    notify-send "$title" "$message"
  fi

  # Always print to terminal too
  echo ""
  echo "🔔 [$title] $message"
  echo ""
}

# --- STAGE 1 PROMPT BUILDER ---------------------------------
stage1_prompt() {
  local site="$1"
  cat <<PROMPT
You are helping me find internships in DevOps and backend development in France.
I am logged into $site in the browser.

## Your task
Search for job listings using these queries one by one:
1. "DevOps" stage
2. "Backend" stage
3. "SRE" stage
4. "DevOps" CDI
5. "Platform engineer" CDI
6. "Backend" CDI
7. "Python backend" CDI
8. "Golang backend" CDI

For each listing classify it:
- DIRECT      → explicitly an internship (stage) in DevOps, backend, or SRE
- COMPANY_LEAD → CDI/CDD with a technical DevOps or backend stack
- SKIP         → anything else

## Fields to extract (TSV row, tab-separated):
id | date_found | source_site | type | title | company | location | contract_type | tech_stack | description_summary | apply_url | careers_page_url | contact_name | contact_role | contact_email | contact_linkedin | notes | status

- id: auto-increment from last row in file
- date_found: today YYYY-MM-DD
- source_site: $site
- tech_stack: comma-separated keywords from the description
- description_summary: 2 sentences in English
- DIRECT rows  → status = TO_APPLY,   leave contact_* and careers_page_url empty
- COMPANY_LEAD → status = TO_ENRICH,  leave contact_* empty
- SKIP         → do not write the row

## Output rules
- Append to: $OUTPUT_FILE
- Write the header row ONLY if the file does not exist yet
- Append each row IMMEDIATELY after processing — do not batch
- After each query, print how many rows you added

## Interaction rules
- If you hit a CAPTCHA, login wall, or pagination issue → STOP and tell the user
- If a listing is ambiguous → STOP and ask the user to classify it
- Do not navigate away from a listing before the row is written
- When all queries for $site are done, print "STAGE1_DONE: $site"
PROMPT
}

# --- STAGE 2 PROMPT -----------------------------------------
stage2_prompt() {
  cat <<PROMPT
You are helping me find contact information for companies that may take an intern,
even though they only posted CDI/CDD roles.

Open this file: $OUTPUT_FILE
Find every row where type = COMPANY_LEAD AND status = TO_ENRICH.

## Enrichment steps per company (in order):
1. Go to the apply_url. Note the company name and size if visible.
2. Search "[company] careers" or "[company] recrutement".
   - If they have a spontaneous application form or email → save in careers_page_url
3. Go to their LinkedIn company page → People tab.
   - Small company (< 50 people): find CTO or tech lead
   - Mid-size: find HR, talent acquisition, or recruiter
   - Save: contact_name, contact_role, contact_linkedin
4. If an email is publicly visible → save in contact_email. NEVER guess or invent one.
5. Update the row:
   - Fill enriched fields
   - status = TO_CONTACT  (if contact found)
   - status = NO_CONTACT_FOUND  (if nothing found after all steps, note what you tried)

## Interaction rules
- Process one company at a time
- After each company: tell me what you found, then continue automatically unless you need my help
- If LinkedIn requires interaction (login, "see more") → stop and tell me
- If multiple contacts found → pick the most relevant one and note the others in the notes field
- When all COMPANY_LEAD rows are processed, print "STAGE2_DONE"
PROMPT
}

# --- MAIN ---------------------------------------------------
echo "=================================================" | tee -a "$LOG_FILE"
echo " Job Scraper started at $(date)" | tee -a "$LOG_FILE"
echo "=================================================" | tee -a "$LOG_FILE"

notify "Job Scraper" "Starting Stage 1 — scraping across ${#SITES[@]} sites"

# STAGE 1 — loop over all sites
for site in "${SITES[@]}"; do
  echo "" | tee -a "$LOG_FILE"
  echo "▶ Stage 1: $site" | tee -a "$LOG_FILE"
  echo "-------------------------------------------------" | tee -a "$LOG_FILE"

  prompt=$(stage1_prompt "$site")

  # Run Gemini CLI with the prompt
  # -p flag sends a prompt and runs non-interactively (adjust flag for your gemini version)
  echo "$prompt" | $GEMINI_CMD 2>&1 | tee -a "$LOG_FILE"

  notify "Stage 1 done" "Finished scraping $site"
  echo "" | tee -a "$LOG_FILE"

  # Small pause between sites to avoid hammering
  sleep 5
done

notify "Job Scraper" "Stage 1 complete — starting Stage 2 (enrichment)"

# STAGE 2 — enrich all COMPANY_LEAD rows
echo "" | tee -a "$LOG_FILE"
echo "▶ Stage 2: Enrichment" | tee -a "$LOG_FILE"
echo "-------------------------------------------------" | tee -a "$LOG_FILE"

stage2_prompt | $GEMINI_CMD 2>&1 | tee -a "$LOG_FILE"

# --- DONE ---------------------------------------------------
DIRECT_COUNT=$(grep -c "DIRECT" "$OUTPUT_FILE" 2>/dev/null || echo "0")
LEAD_COUNT=$(grep -c "COMPANY_LEAD" "$OUTPUT_FILE" 2>/dev/null || echo "0")

notify "✅ Scraping complete!" "${DIRECT_COUNT} direct internships · ${LEAD_COUNT} company leads → jobs.tsv"

echo "" | tee -a "$LOG_FILE"
echo "=================================================" | tee -a "$LOG_FILE"
echo " Done at $(date)" | tee -a "$LOG_FILE"
echo " Direct internships : $DIRECT_COUNT" | tee -a "$LOG_FILE"
echo " Company leads      : $LEAD_COUNT" | tee -a "$LOG_FILE"
echo " Output file        : $OUTPUT_FILE" | tee -a "$LOG_FILE"
echo "=================================================" | tee -a "$LOG_FILE"
