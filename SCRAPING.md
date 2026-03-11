You are helping me find internships in DevOps and backend development in France.
I am logged into [SITE NAME] in the browser. 

## Your task
Search for job listings using these queries one by one:
1. "DevOps" + stage
2. "Backend" + stage
3. "SRE" + stage
4. "DevOps" CDI
5. "Platform engineer" CDI
6. "Backend" CDI
7. "Python backend" CDI
8. "Golang backend" CDI

For each listing you open, classify it:
- DIRECT → it is explicitly an internship (stage) in DevOps, backend, or SRE
- COMPANY_LEAD → it is a CDI/CDD with a clearly technical DevOps or backend stack, meaning the company likely has a team that could take an intern
- SKIP → anything else, do not save it

## For each DIRECT listing, extract:
- id: generate an incremental integer
- date_found: today's date (YYYY-MM-DD)
- source_site: the site name
- type: DIRECT
- title: exact job title
- company: company name
- location: city
- contract_type: stage
- tech_stack: comma-separated keywords extracted from the description (e.g. Docker, Kubernetes, Python, CI/CD...)
- description_summary: write a 2-sentence summary of the role in English
- apply_url: the direct URL to the listing
- careers_page_url: leave empty
- contact_name: leave empty
- contact_role: leave empty
- contact_email: leave empty
- contact_linkedin: leave empty
- notes: anything unusual or worth flagging
- status: TO_APPLY

## For each COMPANY_LEAD listing, extract:
- Same fields as above, but:
- type: COMPANY_LEAD
- contract_type: CDI or CDD
- status: TO_ENRICH

## Output format
Append each row to a file called jobs.tsv using tab separation.
Write the header row only if the file does not exist yet.
Append one row per listing immediately after you process it — do not wait until the end.

## Interaction rules
- After finishing each search query, tell me how many rows you added and ask if I want you to continue to the next query.
- If a listing is ambiguous (e.g. a 6-month CDD, or a vague title), stop and ask me whether to classify it as COMPANY_LEAD or SKIP.
- If you hit a CAPTCHA, a login wall, or a pagination issue, stop and tell me so I can handle it.
- Do not navigate away from a listing until the row is written.
