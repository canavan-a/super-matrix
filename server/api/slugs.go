package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"main/database"
)

// slugFor picks the slug to store for a row of table being created
// (current == "") or updated. A requested slug (nil when none was asked
// for) must be valid and free: a clash is a 409, not a silent -2, since
// whoever asked for it is about to reference it. On create, a missing or
// blank one is made from the name; on update, nil keeps current and blank
// is refused. Call it inside the transaction that writes the row.
func slugFor(tx *gorm.DB, table string, requested *string, name, fallback, id, current string) (string, error) {
	if requested == nil || strings.TrimSpace(*requested) == "" {
		switch {
		case requested != nil && current != "":
			return "", inputError("slug can't be empty")
		case current != "":
			return current, nil
		}
		return database.UniqueSlug(tx, table, database.Slugify(name, fallback), id)
	}
	s := strings.ToLower(strings.TrimSpace(*requested))
	if !database.ValidSlug(s) {
		return "", inputError("slug: use 1-63 lowercase letters, digits and dashes, not starting or ending with a dash")
	}
	taken, err := database.SlugTaken(tx, table, s, id)
	if err != nil {
		return "", err
	}
	if taken {
		return "", slugTaken(s)
	}
	return s, nil
}

func slugTaken(s string) conflictError { return conflictError("the slug " + s + " is already taken") }

// slugClash reports whether a failed write of row id with slug s hit
// table's slug index: another process sharing the database file (the admin
// CLI, say) took s between slugFor's check and the write.
func (a *API) slugClash(err error, table, s, id string) bool {
	if !errors.Is(err, gorm.ErrDuplicatedKey) || s == "" {
		return false
	}
	taken, qerr := database.SlugTaken(a.db, table, s, id)
	return qerr == nil && taken
}

// writeFail answers a failed write: 409 for a conflictError, 400 for an
// inputError, 409 for a slug that was just taken, else 500.
func (a *API) writeFail(c *gin.Context, err error, table, slug, id string) {
	var ce conflictError
	switch {
	case errors.As(err, &ce):
		abort(c, http.StatusConflict, ce.Error())
	case a.slugClash(err, table, slug, id):
		abort(c, http.StatusConflict, slugTaken(slug).Error())
	default:
		a.inputFail(c, err)
	}
}
