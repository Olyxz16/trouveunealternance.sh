package db

import (
	"database/sql"
	"encoding/json"
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
	LegalName           sql.NullString `json:"legal_name"`
	Acronym             sql.NullString `json:"acronym"`
	NameNormalized      sql.NullString `json:"name_normalized"`
	CompanyEmail        sql.NullString `json:"company_email"`
}

func (c Company) MarshalJSON() ([]byte, error) {
	type Alias Company
	return json.Marshal(&struct {
		Alias
		Siren               string `json:"siren"`
		Siret               string `json:"siret"`
		NAFCode             string `json:"naf_code"`
		NAFLabel            string `json:"naf_label"`
		City                string `json:"city"`
		Department          string `json:"department"`
		Address             string `json:"address"`
		HeadcountRange      string `json:"headcount_range"`
		HeadcountExact      int64  `json:"headcount_exact"`
		CreationYear        int64  `json:"creation_year"`
		LegalForm           string `json:"legal_form"`
		Website             string `json:"website"`
		LinkedinURL         string `json:"linkedin_url"`
		TwitterURL          string `json:"twitter_url"`
		GithubURL           string `json:"github_url"`
		TechStack           string `json:"tech_stack"`
		Description         string `json:"description"`
		CareersPageURL      string `json:"careers_page_url"`
		Source              string `json:"source"`
		EmailDraft          string `json:"email_draft"`
		Notes               string `json:"notes"`
		HasInternalTechTeam *bool  `json:"has_internal_tech_team"`
		TechTeamSignals     string `json:"tech_team_signals"`
		PrimaryContactID    int64  `json:"primary_contact_id"`
		LegalName           string `json:"legal_name"`
		Acronym             string `json:"acronym"`
		NameNormalized      string `json:"name_normalized"`
		CompanyEmail        string `json:"company_email"`
	}{
		Alias:               Alias(c),
		Siren:               c.Siren.String,
		Siret:               c.Siret.String,
		NAFCode:             c.NAFCode.String,
		NAFLabel:            c.NAFLabel.String,
		City:                c.City.String,
		Department:          c.Department.String,
		Address:             c.Address.String,
		HeadcountRange:      c.HeadcountRange.String,
		HeadcountExact:      c.HeadcountExact.Int64,
		CreationYear:        c.CreationYear.Int64,
		LegalForm:           c.LegalForm.String,
		Website:             c.Website.String,
		LinkedinURL:         c.LinkedinURL.String,
		TwitterURL:          c.TwitterURL.String,
		GithubURL:           c.GithubURL.String,
		TechStack:           c.TechStack.String,
		Description:         c.Description.String,
		CareersPageURL:      c.CareersPageURL.String,
		Source:              c.Source.String,
		EmailDraft:          c.EmailDraft.String,
		Notes:               c.Notes.String,
		HasInternalTechTeam: func() *bool {
			if c.HasInternalTechTeam.Valid {
				return &c.HasInternalTechTeam.Bool
			}
			return nil
		}(),
		TechTeamSignals:     c.TechTeamSignals.String,
		PrimaryContactID:    c.PrimaryContactID.Int64,
		LegalName:           c.LegalName.String,
		Acronym:             c.Acronym.String,
		NameNormalized:      c.NameNormalized.String,
		CompanyEmail:        c.CompanyEmail.String,
	})
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
			company_type, has_internal_tech_team, tech_team_signals,
			legal_name, acronym, name_normalized, company_email
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		c.Name, c.Siren, c.Siret, c.NAFCode, c.NAFLabel, c.City, c.Department, c.Address,
		c.HeadcountRange, c.HeadcountExact, c.CreationYear, c.LegalForm,
		c.Website, c.LinkedinURL, c.TwitterURL, c.GithubURL, c.TechStack, c.Description,
		c.CareersPageURL, c.Source, c.Status, c.RelevanceScore, c.Notes, c.DateFound,
		c.CompanyType, c.HasInternalTechTeam, c.TechTeamSignals,
		c.LegalName, c.Acronym, c.NameNormalized, c.CompanyEmail,
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

const allCompanyCols = `id, name, siren, siret, naf_code, naf_label, city, department, address, headcount_range, headcount_exact, creation_year, legal_form, website, linkedin_url, twitter_url, github_url, tech_stack, description, contact_name, contact_role, contact_email, contact_linkedin, careers_page_url, source, status, relevance_score, email_draft, notes, date_found, updated_at, primary_contact_id, company_type, has_internal_tech_team, tech_team_signals, legal_name, acronym, name_normalized, company_email`

func (db *DB) GetCompany(id int) (*Company, error) {
	var c Company
	var contactName, contactRole, contactEmail, contactLinkedin sql.NullString
	query := fmt.Sprintf("SELECT %s FROM companies WHERE id=?", allCompanyCols)
	err := db.QueryRow(query, id).Scan(
		&c.ID, &c.Name, &c.Siren, &c.Siret, &c.NAFCode, &c.NAFLabel,
		&c.City, &c.Department, &c.Address,
		&c.HeadcountRange, &c.HeadcountExact, &c.CreationYear, &c.LegalForm,
		&c.Website, &c.LinkedinURL, &c.TwitterURL, &c.GithubURL,
		&c.TechStack, &c.Description,
		&contactName, &contactRole, &contactEmail, &contactLinkedin,
		&c.CareersPageURL, &c.Source, &c.Status, &c.RelevanceScore, &c.EmailDraft, &c.Notes,
		&c.DateFound, &c.UpdatedAt, &c.PrimaryContactID, &c.CompanyType, &c.HasInternalTechTeam, &c.TechTeamSignals,
		&c.LegalName, &c.Acronym, &c.NameNormalized, &c.CompanyEmail,
	)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (db *DB) GetCompaniesForEnrichment() ([]Company, error) {
	query := fmt.Sprintf("SELECT %s FROM companies WHERE status = 'NEW' AND (primary_contact_id IS NULL OR company_type = 'UNKNOWN')", allCompanyCols)
	rows, err := db.Query(query)
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
			&c.LegalName, &c.Acronym, &c.NameNormalized, &c.CompanyEmail,
		)
		if err != nil {
			return nil, err
		}
		companies = append(companies, c)
	}
	return companies, nil
}
