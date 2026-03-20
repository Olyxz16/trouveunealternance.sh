package db

import (
	"time"

	"gorm.io/gorm"
)

func (db *DB) UpsertCompany(c *Company) (uint, bool, error) {
	if c.DateFound == "" {
		c.DateFound = time.Now().Format("2006-01-02")
	}
	if c.Status == "" {
		c.Status = "NEW"
	}

	var existing Company
	var result *gorm.DB
	if c.Siren != "" {
		result = db.Where("siren = ?", c.Siren).Limit(1).Find(&existing)
	} else {
		result = db.Where("name = ? AND city = ?", c.Name, c.City).Limit(1).Find(&existing)
	}

	if result.Error == nil && existing.ID != 0 {
		return existing.ID, false, nil
	}
	
	err := db.Create(c).Error
	return c.ID, true, err
}

func (db *DB) UpdateCompany(id uint, fields map[string]interface{}) error {
	return db.Model(&Company{}).Where("id = ?", id).Updates(fields).Error
}

func (db *DB) GetCompany(id uint) (*Company, error) {
	var c Company
	err := db.First(&c, id).Error
	return &c, err
}

func (db *DB) GetCompaniesForEnrichment() ([]Company, error) {
	var companies []Company
	err := db.Where("status = 'NEW' AND (primary_contact_id = 0 OR company_type = 'UNKNOWN')").Find(&companies).Error
	return companies, err
}

func (db *DB) GetJobs(limit int) ([]Job, error) {
	var jobs []Job
	err := db.Order("relevance_score desc, date_found desc").Limit(limit).Find(&jobs).Error
	return jobs, err
}
