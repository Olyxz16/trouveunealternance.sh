package db

func (db *DB) AddContact(c *Contact, isPrimary bool) (uint, error) {
	if c.Status == "" {
		c.Status = "active"
	}

	err := db.Create(c).Error
	if err != nil {
		return 0, err
	}

	if isPrimary {
		err = db.Model(&Company{}).Where("id = ?", c.CompanyID).Update("primary_contact_id", c.ID).Error
		if err != nil {
			return c.ID, err
		}
	}

	return c.ID, nil
}

func (db *DB) GetContacts(companyID uint) ([]Contact, error) {
	var contacts []Contact
	err := db.Where("company_id = ?", companyID).Order("created_at desc").Find(&contacts).Error
	return contacts, err
}
