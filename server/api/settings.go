package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"main/database"
)

// Override mode lets the app (and relays) change values a schema manages,
// for an emergency fix. It switches itself off after a while; the next
// schema plan shows what was changed as drift, and the next apply puts the
// schema's values back.

const (
	settingOverrideUntil = "schema_override_until"
	defaultOverride      = time.Hour
	maxOverride          = 7 * 24 * time.Hour
)

func getSetting(db *gorm.DB, key string) string {
	s, err := database.First[database.Setting](db.Where("key = ?", key))
	if err != nil || s == nil {
		return ""
	}
	return s.Value
}

func putSetting(db *gorm.DB, key, value string) error {
	return db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&database.Setting{Key: key, Value: value}).Error
}

// overrideUntil is when override mode ends; zero when it's off.
func (a *API) overrideUntil() time.Time {
	t, err := time.Parse(time.RFC3339, getSetting(a.db, settingOverrideUntil))
	if err != nil || !a.now().Before(t) {
		return time.Time{}
	}
	return t
}

func (a *API) overrideOn() bool { return !a.overrideUntil().IsZero() }

func (a *API) overrideOut() gin.H {
	until := a.overrideUntil()
	if until.IsZero() {
		return gin.H{"on": false, "until": nil, "secondsLeft": 0}
	}
	// secondsLeft lets a client with a wrong clock time the end itself.
	return gin.H{"on": true, "until": until, "secondsLeft": int(until.Sub(a.now()).Seconds()) + 1}
}

func (a *API) getOverride(c *gin.Context) { c.JSON(http.StatusOK, a.overrideOut()) }

// putOverride turns override mode on (for minutes, default 60, at most a
// week) or off.
func (a *API) putOverride(c *gin.Context) {
	var b struct {
		On      bool `json:"on"`
		Minutes int  `json:"minutes"`
	}
	if err := c.ShouldBindJSON(&b); err != nil {
		abort(c, http.StatusBadRequest, "bad json")
		return
	}
	value := ""
	if b.On {
		d := defaultOverride
		if b.Minutes != 0 {
			d = time.Duration(b.Minutes) * time.Minute
		}
		if d < time.Minute || d > maxOverride {
			abort(c, http.StatusBadRequest, "override can last 1 minute to 7 days")
			return
		}
		value = a.now().Add(d).UTC().Format(time.RFC3339)
	}
	if err := putSetting(a.db, settingOverrideUntil, value); err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	a.watchers.send(gin.H{"type": "changed"})
	c.JSON(http.StatusOK, a.overrideOut())
}
