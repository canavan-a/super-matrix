package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"main/database"
	"main/edge"
)

// maxEmails bounds one email list, a deployment's own or a group's.
const maxEmails = 1000

type emailGroupDTO struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Entries []string `json:"entries"`
	// Deployments counts the sign-in lists that include it.
	Deployments int64     `json:"deployments"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (a *API) emailGroupOut(g database.EmailGroup) emailGroupDTO {
	var nd int64
	a.db.Model(&database.DeploymentEmailGroup{}).Where("email_group_id = ?", g.ID).Count(&nd)
	return emailGroupDTO{
		ID: g.ID, Name: g.Name, Entries: splitAllowlist(g.Entries),
		Deployments: nd, CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
	}
}

func (a *API) findEmailGroup(c *gin.Context) *database.EmailGroup {
	g, err := database.First[database.EmailGroup](a.db.Where("id = ?", c.Param("id")))
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return nil
	}
	if g == nil {
		abort(c, http.StatusNotFound, "no such email group")
		return nil
	}
	return g
}

// normalizeEmails checks and normalizes addresses (lowercased; *@domain
// allows a whole domain), drops blanks and repeats, and returns them one
// per line. what names the list in error messages.
func normalizeEmails(entries []string, what string) (string, error) {
	seen := map[string]bool{}
	var list []string
	for _, raw := range entries {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		m := edge.NormalizeEmail(raw)
		// "*" only works as the whole name part: *@domain.com.
		if name, domain, _ := strings.Cut(m, "@"); name != "*" && strings.Contains(name, "*") || strings.Contains(domain, "*") {
			m = ""
		}
		if m == "" {
			return "", inputError(what + ": " + strings.TrimSpace(raw) + " isn't an email address")
		}
		if !seen[m] {
			seen[m] = true
			list = append(list, m)
		}
	}
	if len(list) > maxEmails {
		return "", inputError(fmt.Sprintf("%s: at most %d addresses", what, maxEmails))
	}
	return strings.Join(list, "\n"), nil
}

type emailGroupInput struct {
	Name    *string   `json:"name"`
	Entries *[]string `json:"entries"`
}

func applyEmailGroup(g *database.EmailGroup, in emailGroupInput) error {
	if in.Name != nil {
		g.Name = cleanName(*in.Name)
	}
	if in.Entries != nil {
		joined, err := normalizeEmails(*in.Entries, "entries")
		if err != nil {
			return err
		}
		g.Entries = joined
	}
	switch {
	case g.Name == "":
		return inputError("name required")
	case g.Entries == "":
		// A deployment may rely on its groups alone for who can sign in;
		// emptying one would lock everyone out as a side effect.
		return inputError("add at least one email address")
	}
	return nil
}

func (a *API) listEmailGroups(c *gin.Context) {
	var gs []database.EmailGroup
	if err := a.db.Order("name COLLATE NOCASE").Find(&gs).Error; err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	out := make([]emailGroupDTO, 0, len(gs))
	for _, g := range gs {
		out = append(out, a.emailGroupOut(g))
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) getEmailGroup(c *gin.Context) {
	if g := a.findEmailGroup(c); g != nil {
		c.JSON(http.StatusOK, a.emailGroupOut(*g))
	}
}

func (a *API) createEmailGroup(c *gin.Context) {
	var in emailGroupInput
	if err := c.ShouldBindJSON(&in); err != nil {
		abort(c, http.StatusBadRequest, "bad json")
		return
	}
	now := a.now()
	g := database.EmailGroup{ID: database.NewID(), CreatedAt: now, UpdatedAt: now}
	if err := applyEmailGroup(&g, in); err != nil {
		a.inputFail(c, err)
		return
	}
	if err := a.db.Create(&g).Error; err != nil {
		emailGroupWriteFail(c, &g, err)
		return
	}
	a.changed()
	c.JSON(http.StatusCreated, a.emailGroupOut(g))
}

func (a *API) updateEmailGroup(c *gin.Context) {
	g := a.findEmailGroup(c)
	if g == nil {
		return
	}
	var in emailGroupInput
	if err := c.ShouldBindJSON(&in); err != nil {
		abort(c, http.StatusBadRequest, "bad json")
		return
	}
	if err := applyEmailGroup(g, in); err != nil {
		a.inputFail(c, err)
		return
	}
	g.UpdatedAt = a.now()
	if err := a.db.Save(g).Error; err != nil {
		emailGroupWriteFail(c, g, err)
		return
	}
	// Reloading drops sessions of anyone just taken off the group: the edge
	// checks the list on every request.
	a.changed()
	c.JSON(http.StatusOK, a.emailGroupOut(*g))
}

func (a *API) deleteEmailGroup(c *gin.Context) {
	g := a.findEmailGroup(c)
	if g == nil {
		return
	}
	inUse := g.Name + " is in use: take it off those sign-in lists first"
	if a.emailGroupOut(*g).Deployments > 0 {
		abort(c, http.StatusConflict, inUse)
		return
	}
	// The foreign key refuses this if a deployment took the group up since
	// the check above.
	if err := a.db.Delete(g).Error; err != nil {
		if errors.Is(err, gorm.ErrForeignKeyViolated) {
			abort(c, http.StatusConflict, inUse)
			return
		}
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	a.changed()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func emailGroupWriteFail(c *gin.Context, g *database.EmailGroup, err error) {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		abort(c, http.StatusConflict, "there's already an email group called "+g.Name)
		return
	}
	abort(c, http.StatusInternalServerError, "server error")
}

// cleanEmailGroupIDs checks that every id names an email group and returns
// them deduplicated, in order. nil (field not sent) stays nil: leave as is.
func (a *API) cleanEmailGroupIDs(ids *[]string) ([]string, error) {
	if ids == nil {
		return nil, nil
	}
	out := []string{}
	seen := map[string]bool{}
	for _, id := range *ids {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return out, nil
	}
	var n int64
	if err := a.db.Model(&database.EmailGroup{}).Where("id IN ?", out).Count(&n).Error; err != nil {
		return nil, err
	}
	if int(n) != len(out) {
		return nil, inputError("the sign-in list names an email group that doesn't exist")
	}
	return out, nil
}

func (a *API) deploymentEmailGroups(ids ...string) map[string][]string {
	return a.groupIDsOf(&database.DeploymentEmailGroup{}, "deployment_id", "email_group_id", "email_groups", ids...)
}

// emailGroupCount is how many email groups deployment id will include
// after a write: len(ids) when the write sets them, else what it has now.
func (a *API) emailGroupCount(id string, ids []string) (int, error) {
	if ids != nil {
		return len(ids), nil
	}
	var n int64
	err := a.db.Model(&database.DeploymentEmailGroup{}).Where("deployment_id = ?", id).Count(&n).Error
	return int(n), err
}

// setDeploymentEmailGroups replaces a deployment's email groups with ids.
func setDeploymentEmailGroups(tx *gorm.DB, deploymentID string, ids []string) error {
	if err := tx.Where("deployment_id = ?", deploymentID).Delete(&database.DeploymentEmailGroup{}).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	rows := make([]database.DeploymentEmailGroup, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, database.DeploymentEmailGroup{DeploymentID: deploymentID, EmailGroupID: id})
	}
	return tx.Create(&rows).Error
}
