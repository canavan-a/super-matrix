package database

import (
	"time"

	"gorm.io/gorm"
)

// Seed makes baseDomain (SUPERMATRIX_BASE_DOMAIN, optional) the first
// wildcard domain when there are none yet, and gives deployments from
// before domains existed their domain and host.
func Seed(db *gorm.DB, baseDomain string) error {
	if baseDomain == "" {
		return nil
	}
	var n int64
	if err := db.Model(&Domain{}).Count(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		if err := db.Create(&Domain{ID: NewID(), Name: baseDomain, Wildcard: true, CreatedAt: time.Now()}).Error; err != nil {
			return err
		}
	}
	dom, err := First[Domain](db.Where("name = ? AND wildcard = ?", baseDomain, true))
	if err != nil || dom == nil {
		return err
	}
	var legacy []Deployment
	if err := db.Where("domain_id IS NULL OR host IS NULL").Find(&legacy).Error; err != nil {
		return err
	}
	for _, d := range legacy {
		host := dom.HostFor(d.Subdomain)
		if err := db.Model(&Deployment{}).Where("id = ?", d.ID).Updates(map[string]any{"domain_id": dom.ID, "host": host}).Error; err != nil {
			return err
		}
	}
	return nil
}
