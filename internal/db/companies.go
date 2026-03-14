package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type Company struct {
	ID                  int            `json:"id"`
	Name                string         `json:"name"`
	Siren               sql.NullString `json:"siren"`
	Siret               sql.NullString `json:"siret"`
	NAFCode             sql.NullString `json:"naf_code"`
	NAFLabel            sql.NullString `json:"naf_label"`
	City                sql.NullString `json:"city"`
	Department          sql.NullString `json:"department"`
	Address             sql.NullString `json:"address"`
	HeadcountRange      sql.NullString `json:"headcount_range"`
	HeadcountExact      sql.NullInt64  `json:"headcount_exact"`
	CreationYear        sql.NullInt64  `json:"creation_year"`
	LegalForm           sql.NullString `json:"legal_form"`
	Website             sql.NullString `json:"website"`
	LinkedinURL         sql.NullString `json:"linkedin_url"`
	TwitterURL          sql.NullString `json:"twitter_url"`
	GithubURL           sql.NullString `json:"github_url"`
	TechStack           sql.NullString `json:"tech_stack"`
	Description         sql.NullString `json:"description"`
	CareersPageURL      sql.NullString `json:"careers_page_url"`
	Source              sql.NullString `json:"source"`
	Status              string         `json:"status"`
	RelevanceScore      int            `json:"relevance_score"`
	EmailDraft          sql.NullString `json:"email_draft"`
	Notes               sql.NullString `json:"notes"`
	DateFound           string         `json:"date_found"`
	UpdatedAt           string         `json:"updated_at"`
	CompanyType         string         `json:"company_type"`
	HasInternalTechTeam sql.NullBool   `json:"has_internal_tech_team"`
	TechTeamSignals     sql.NullString `json:"tech_team_signals"`
	PrimaryContactID    sql.NullInt64  `json:"primary_contact_id"`
}

func (db *DB) UpsertCompany(c *Company) (int, bool, error) {
	if c.DateFound == "" {
		c.DateFound = time.Now().Format("2006-01-02")
	}
	if c.Status == "" {
		c.Status = "NEW"
	}

	var id int
	var err error
	if c.Siren.Valid && c.Siren.String != "" {
		err = db.QueryRow("SELECT id FROM companies WHERE siren=?", c.Siren.String).Scan(&id)
	} else {
		err = db.QueryRow("SELECT id FROM companies WHERE name=? AND city=?", c.Name, c.City.String).Scan(&id)
	}

	if err == nil {
		return id, false, nil
	}
	if err != sql.ErrNoRows {
		return 0, false, err
	}

	res, err := db.Exec(`
		INSERT INTO companies (
			name, siren, siret, naf_code, naf_label, city, department, address,
			headcount_range, headcount_exact, creation_year, legal_form,
			website, linkedin_url, twitter_url, github_url, tech_stack, description,
			careers_page_url, source, status, relevance_score, notes, date_found,
			company_type, has_internal_tech_team, tech_team_signals
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		c.Name, c.Siren, c.Siret, c.NAFCode, c.NAFLabel, c.City, c.Department, c.Address,
		c.HeadcountRange, c.HeadcountExact, c.CreationYear, c.LegalForm,
		c.Website, c.LinkedinURL, c.TwitterURL, c.GithubURL, c.TechStack, c.Description,
		c.CareersPageURL, c.Source, c.Status, c.RelevanceScore, c.Notes, c.DateFound,
		c.CompanyType, c.HasInternalTechTeam, c.TechTeamSignals,
	)
	if err != nil {
		return 0, false, err
	}

	lastID, err := res.LastInsertId()
	if err != nil {
		return 0, false, err
	}

	return int(lastID), true, nil
}

func (db *DB) UpdateCompany(id int, fields map[string]interface{}) error {
	if len(fields) == 0 {
		return nil
	}

	fields["updated_at"] = time.Now().Format(time.RFC3339)

	var setClauses []string
	var args []interface{}
	for k, v := range fields {
		setClauses = append(setClauses, fmt.Sprintf("%s=?", k))
		args = append(args, v)
	}
	args = append(args, id)

	query := fmt.Sprintf("UPDATE companies SET %s WHERE id=?", strings.Join(setClauses, ", "))
	_, err := db.Exec(query, args...)
	return err
}

func (db *DB) GetCompany(id int) (*Company, error) {
	var c Company
	var contactName, contactRole, contactEmail, contactLinkedin sql.NullString
	err := db.QueryRow("SELECT * FROM companies WHERE id=?", id).Scan(
		&c.ID, &c.Name, &c.Siren, &c.Siret, &c.NAFCode, &c.NAFLabel,
		&c.City, &c.Department, &c.Address,
		&c.HeadcountRange, &c.HeadcountExact, &c.CreationYear, &c.LegalForm,
		&c.Website, &c.LinkedinURL, &c.TwitterURL, &c.GithubURL,
		&c.TechStack, &c.Description,
		&contactName, &contactRole, &contactEmail, &contactLinkedin,
		&c.CareersPageURL, &c.Source, &c.Status, &c.RelevanceScore, &c.EmailDraft, &c.Notes,
		&c.DateFound, &c.UpdatedAt, &c.PrimaryContactID, &c.CompanyType, &c.HasInternalTechTeam, &c.TechTeamSignals,
	)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (db *DB) GetCompaniesForEnrichment() ([]Company, error) {
	rows, err := db.Query("SELECT * FROM companies WHERE status = 'NEW' AND (primary_contact_id IS NULL OR company_type = 'UNKNOWN')")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var companies []Company
	for rows.Next() {
		var c Company
		var contactName, contactRole, contactEmail, contactLinkedin sql.NullString
		err := rows.Scan(
			&c.ID, &c.Name, &c.Siren, &c.Siret, &c.NAFCode, &c.NAFLabel,
			&c.City, &c.Department, &c.Address,
			&c.HeadcountRange, &c.HeadcountExact, &c.CreationYear, &c.LegalForm,
			&c.Website, &c.LinkedinURL, &c.TwitterURL, &c.GithubURL,
			&c.TechStack, &c.Description,
			&contactName, &contactRole, &contactEmail, &contactLinkedin,
			&c.CareersPageURL, &c.Source, &c.Status, &c.RelevanceScore, &c.EmailDraft, &c.Notes,
			&c.DateFound, &c.UpdatedAt, &c.PrimaryContactID, &c.CompanyType, &c.HasInternalTechTeam, &c.TechTeamSignals,
		)
		if err != nil {
			return nil, err
		}
		companies = append(companies, c)
	}
	return companies, nil
}
