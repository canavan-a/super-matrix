package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"main/database"
	"main/registry"
)

type pathRuleDTO struct {
	ID          string      `json:"id"`
	PathPrefix  string      `json:"pathPrefix"`
	IPAllowlist []string    `json:"ipAllowlist"`
	CreatedAt   time.Time   `json:"createdAt"`
	UpdatedAt   time.Time   `json:"updatedAt"`
	Managed     *managedDTO `json:"managed"`
}

func pathRuleOut(r database.PathRule) pathRuleDTO {
	return pathRuleDTO{ID: r.ID, PathPrefix: r.PathPrefix, IPAllowlist: splitAllowlist(r.IPAllowlist), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// pathRulesOf bulk-fetches path rules for several deployments, like tokensOf.
func (a *API) pathRulesOf(ids ...string) map[string][]database.PathRule {
	var rules []database.PathRule
	a.db.Where("deployment_id IN ?", ids).Order("created_at").Find(&rules)
	m := map[string][]database.PathRule{}
	for _, r := range rules {
		m[r.DeploymentID] = append(m[r.DeploymentID], r)
	}
	return m
}

// pathRuleInput is the body of create (pathPrefix required) and update
// (missing fields stay as they are).
type pathRuleInput struct {
	PathPrefix  *string   `json:"pathPrefix"`
	IPAllowlist *[]string `json:"ipAllowlist"`
}

const pathPrefixMaxLen = 200

// normalizePathPrefix validates and trims a path prefix: must start with
// "/", can't end with "/" (except "/" itself, meaning the whole host),
// no whitespace, a sane length cap.
func normalizePathPrefix(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "" || s[0] != '/':
		return "", inputError(`path prefix must start with "/"`)
	case len(s) > pathPrefixMaxLen:
		return "", inputError("path prefix: at most 200 characters")
	case s != "/" && strings.HasSuffix(s, "/"):
		return "", inputError(`path prefix can't end with "/" (except "/" itself)`)
	case strings.ContainsAny(s, " \t\r\n"):
		return "", inputError("path prefix can't contain whitespace")
	}
	return s, nil
}

// applyPathRule copies the set fields of in onto r and checks the result.
func (a *API) applyPathRule(r *database.PathRule, in pathRuleInput) error {
	if in.PathPrefix != nil {
		p, err := normalizePathPrefix(*in.PathPrefix)
		if err != nil {
			return err
		}
		r.PathPrefix = p
	}
	if in.IPAllowlist != nil {
		nets, err := registry.ParseAllowlist(strings.Join(*in.IPAllowlist, "\n"))
		if err != nil {
			return inputError("IP allow list: " + err.Error())
		}
		lines := make([]string, 0, len(nets))
		for _, n := range nets {
			lines = append(lines, n.String())
		}
		r.IPAllowlist = strings.Join(lines, "\n")
	}
	if r.PathPrefix == "" {
		return inputError("pick a path prefix")
	}
	return nil
}

// pathRuleWriteFail answers a failed insert or save: applyPathRule only
// checks the prefix is well-formed, not that it's free, so a concurrent
// create/update on the same prefix is caught by the unique index instead.
func pathRuleWriteFail(c *gin.Context, r *database.PathRule, err error) {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		abort(c, http.StatusConflict, r.PathPrefix+" already has a rule on this deployment")
		return
	}
	abort(c, http.StatusInternalServerError, "server error")
}

func (a *API) findPathRule(c *gin.Context, d *database.Deployment) *database.PathRule {
	r, err := database.First[database.PathRule](a.db.Where("id = ? AND deployment_id = ?", c.Param("rid"), d.ID))
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return nil
	}
	if r == nil {
		abort(c, http.StatusNotFound, "no such path rule")
		return nil
	}
	return r
}

func (a *API) createPathRule(c *gin.Context) {
	d := a.findDeployment(c)
	if d == nil {
		return
	}
	var in pathRuleInput
	if err := c.ShouldBindJSON(&in); err != nil {
		abort(c, http.StatusBadRequest, "bad json")
		return
	}
	if in.PathPrefix == nil {
		abort(c, http.StatusBadRequest, "path prefix required")
		return
	}
	if !a.schemaAllowsPathRules(c, d.ID) {
		return
	}
	now := a.now()
	r := database.PathRule{ID: database.NewID(), DeploymentID: d.ID, CreatedAt: now, UpdatedAt: now}
	if err := a.applyPathRule(&r, in); err != nil {
		a.inputFail(c, err)
		return
	}
	var clash int64
	if err := a.db.Model(&database.PathRule{}).Where("deployment_id = ? AND path_prefix = ?", d.ID, r.PathPrefix).Count(&clash).Error; err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	if clash > 0 {
		abort(c, http.StatusConflict, r.PathPrefix+" already has a rule on this deployment")
		return
	}
	if err := a.db.Create(&r).Error; err != nil {
		pathRuleWriteFail(c, &r, err)
		return
	}
	a.changed()
	c.JSON(http.StatusCreated, pathRuleOut(r))
}

func (a *API) updatePathRule(c *gin.Context) {
	d := a.findDeployment(c)
	if d == nil {
		return
	}
	r := a.findPathRule(c, d)
	if r == nil {
		return
	}
	var in pathRuleInput
	if err := c.ShouldBindJSON(&in); err != nil {
		abort(c, http.StatusBadRequest, "bad json")
		return
	}
	before := *r
	if err := a.applyPathRule(r, in); err != nil {
		a.inputFail(c, err)
		return
	}
	if !a.schemaAllowsPathRules(c, d.ID) || !a.schemaAllowsUpdate(c, kindPathRule, r.ID, before.PathPrefix, r.PathPrefix, pathRuleItem(before), pathRuleItem(*r)) {
		return
	}
	r.UpdatedAt = a.now()
	if err := a.db.Save(r).Error; err != nil {
		pathRuleWriteFail(c, r, err)
		return
	}
	a.changed()
	c.JSON(http.StatusOK, pathRuleOut(*r))
}

func (a *API) deletePathRule(c *gin.Context) {
	d := a.findDeployment(c)
	if d == nil {
		return
	}
	if !a.schemaAllowsPathRules(c, d.ID) || !a.schemaAllowsDelete(c, kindPathRule, c.Param("rid"), "this path rule") {
		return
	}
	res := a.db.Where("id = ? AND deployment_id = ?", c.Param("rid"), d.ID).Delete(&database.PathRule{})
	if res.RowsAffected == 0 {
		abort(c, http.StatusNotFound, "no such path rule")
		return
	}
	a.changed()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
