package database

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
)

// MaxSlug is the longest a slug can be: one DNS label.
const MaxSlug = 63

var slugRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidSlug reports whether s is a slug: 1-63 lowercase letters, digits and
// dashes, not starting or ending with a dash.
func ValidSlug(s string) bool { return slugRE.MatchString(s) }

// Slugify turns a display name into a slug: lowercased, accents dropped
// ("Zürich" → "zurich"), every run of other characters made one dash, and
// dashes trimmed from the ends. It returns fallback when nothing is left
// (a name of only symbols, or in a script with no Latin letters).
func Slugify(name, fallback string) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFKD.String(strings.ToLower(name)) {
		if unicode.Is(unicode.Mn, r) {
			continue // a combining accent split off its letter
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	s := clipSlug(b.String(), MaxSlug)
	if s == "" {
		return fallback
	}
	return s
}

func clipSlug(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	return strings.Trim(s, "-")
}

// UniqueSlug is base, or base-2, base-3… when that's taken in table by a
// row other than exceptID. Run it in the transaction that writes the row:
// the database has one connection, so nothing can take the slug between
// the check and the write.
func UniqueSlug(tx *gorm.DB, table, base, exceptID string) (string, error) {
	for i := 1; ; i++ {
		s := base
		if i > 1 {
			suffix := fmt.Sprintf("-%d", i)
			s = clipSlug(base, MaxSlug-len(suffix)) + suffix
		}
		taken, err := SlugTaken(tx, table, s, exceptID)
		if err != nil {
			return "", err
		}
		if !taken {
			return s, nil
		}
	}
}

// SlugTaken reports whether a row of table other than exceptID has slug s.
func SlugTaken(tx *gorm.DB, table, s, exceptID string) (bool, error) {
	var n int64
	err := tx.Table(table).Where("slug = ? AND id <> ?", s, exceptID).Count(&n).Error
	return n > 0, err
}

// BackfillSlugs gives every relay and bridge without a slug one made from
// its name. Rows get one when they're created, so this only finds rows from
// before slugs existed, or written by an older binary since.
func BackfillSlugs(db *gorm.DB) error {
	for _, t := range []struct{ table, fallback string }{{"relays", "relay"}, {"bridges", "bridge"}} {
		var rows []struct{ ID, Name string }
		if err := db.Table(t.table).Select("id, name").Where("slug = ''").Order("created_at, id").Find(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			err := db.Transaction(func(tx *gorm.DB) error {
				s, err := UniqueSlug(tx, t.table, Slugify(r.Name, t.fallback), r.ID)
				if err != nil {
					return err
				}
				return tx.Table(t.table).Where("id = ?", r.ID).Update("slug", s).Error
			})
			if err != nil {
				return fmt.Errorf("backfill %s slugs: %w", t.table, err)
			}
		}
	}
	return nil
}
