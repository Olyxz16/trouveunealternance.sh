#!/bin/bash

# ============================================================
#  Cold Email Drafter
#  Reads all TO_CONTACT rows from jobs.tsv and drafts emails
# ============================================================

# --- CONFIG (edit these) ------------------------------------
YOUR_NAME="Your Name"
YOUR_SCHOOL="Your School / University, Year"
SKILLS="Python, Docker, Linux, CI/CD, Git"
INTERNSHIP_DURATION="6"
START_DATE="September 2025"
INTERESTS="infrastructure, distributed systems, backend architecture"
TSV_FILE="$(pwd)/jobs.tsv"
OUTPUT_DIR="$(pwd)/emails"
GEMINI_CMD="gemini"
# ------------------------------------------------------------

mkdir -p "$OUTPUT_DIR"

notify() {
  local title="$1"; local message="$2"
  if command -v osascript &>/dev/null; then
    osascript -e "display notification \"$message\" with title \"$title\" sound name \"Glass\""
  elif command -v notify-send &>/dev/null; then
    notify-send "$title" "$message"
  fi
  echo "🔔 [$title] $message"
}

echo "================================================="
echo " Cold Email Drafter"
echo "================================================="

# Extract TO_CONTACT rows (skip header)
HEADER=$(head -1 "$TSV_FILE")
ROWS=$(awk -F'\t' 'NR > 1 && $18 == "TO_CONTACT"' "$TSV_FILE")

if [ -z "$ROWS" ]; then
  echo "No TO_CONTACT rows found in $TSV_FILE. Run scrape.sh first."
  exit 1
fi

COUNT=$(echo "$ROWS" | wc -l | tr -d ' ')
echo "Found $COUNT companies to contact."
echo ""

notify "Email Drafter" "Drafting $COUNT cold emails…"

# Process each row
ROW_NUM=0
while IFS=$'\t' read -r id date_found source_site type title company location contract_type tech_stack description_summary apply_url careers_page_url contact_name contact_role contact_email contact_linkedin notes status; do
  ROW_NUM=$((ROW_NUM + 1))
  SAFE_COMPANY=$(echo "$company" | tr ' /' '__' | tr -cd '[:alnum:]_-')
  OUTPUT_FILE="$OUTPUT_DIR/${ROW_NUM}_${SAFE_COMPANY}.md"

  echo "[$ROW_NUM/$COUNT] Drafting for: $company ($contact_name — $contact_role)"

  PROMPT="You are helping me write a cold email to a company I want to apply to as an intern,
even though they didn't post an internship offer.

## Company details (from their job posting):
- Company: $company
- Job they posted: $title ($contract_type)
- Tech stack from listing: $tech_stack
- Role summary: $description_summary
- Contact name: $contact_name
- Contact role: $contact_role
- Contact email: $contact_email
- Contact LinkedIn: $contact_linkedin
- Their careers page: $careers_page_url

## About me:
- Name: $YOUR_NAME
- School: $YOUR_SCHOOL
- Skills: $SKILLS
- Internship duration: ${INTERNSHIP_DURATION} months starting $START_DATE
- Genuine interests: $INTERESTS

## Write:
1. A cold email in French, max 150 words, that:
   - Opens with a specific reference to the $contract_type role they posted (shows I did my homework)
   - Mentions one specific thing about their tech stack or product that genuinely interests me
   - Asks if they'd consider an intern alongside their current hiring
   - Ends with a clear CTA (20-min call or send my CV)
   - Tone: warm and direct, not a formal cover letter

2. A subject line (one line, punchy)

3. A LinkedIn message version (max 300 characters) in case email isn't available

Format your response in Markdown with these sections:
# Subject
# Email
# LinkedIn Message
"

  echo "$PROMPT" | $GEMINI_CMD > "$OUTPUT_FILE" 2>&1

  echo "   ✓ Saved to $OUTPUT_FILE"
done <<< "$ROWS"

echo ""
echo "================================================="
echo " Done! $ROW_NUM emails drafted in: $OUTPUT_DIR"
echo "================================================="

notify "✅ Emails ready" "$ROW_NUM cold emails drafted in /emails/"
