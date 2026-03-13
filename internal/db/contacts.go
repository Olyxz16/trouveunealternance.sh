package db

import (
	"database/sql"
)

type Contact struct {
	ID          int            `json:"id"`
	CompanyID   int            `json:"company_id"`
	Name        sql.NullString `json:"name"`
	Role        sql.NullString `json:"role"`
	Email       sql.NullString `json:"email"`
	LinkedinURL sql.NullString `json:"linkedin_url"`
	Source      sql.NullString `json:"source"`
	Confidence  sql.NullString `json:"confidence"`
	Status      string         `json:"status"`
	Notes       sql.NullString `json:"notes"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
}

func (db *DB) AddContact(c *Contact, isPrimary bool) (int, error) {
	if c.Status == "" {
		c.Status = "active"
	}

	res, err := db.Exec(`
		INSERT INTO contacts (
			company_id, name, role, email, linkedin_url, source, confidence, status, notes
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		c.CompanyID, c.Name, c.Role, c.Email, c.LinkedinURL, c.Source, c.Confidence, c.Status, c.Notes,
	)
	if err != nil {
		return 0, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	if isPrimary {
		_, err = db.Exec("UPDATE companies SET primary_contact_id = ? WHERE id = ?", id, c.CompanyID)
		if err != nil {
			return int(id), err
		}
	}

	return int(id), nil
}

func (db *DB) GetContacts(companyID int) ([]Contact, error) {
	rows, err := db.Query("SELECT * FROM contacts WHERE company_id = ? ORDER BY created_at DESC", companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contacts []Contact
	for rows.Next() {
		var c Contact
		err := rows.Scan(
			&c.ID, &c.CompanyID, &c.Name, &c.Role, &c.Email, &c.LinkedinURL,
			&c.Source, &c.Confidence, &c.Status, &c.Notes, &c.CreatedAt, &c.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, nil
}
