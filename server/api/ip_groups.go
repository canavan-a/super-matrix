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

// maxIPGroupEntries bounds one group; allow lists are scanned per request.
const maxIPGroupEntries = 1000

type ipGroupDTO struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Entries []string `json:"entries"`
	// Deployments and Bridges count the allow lists that include it.
	Deployments int64       `json:"deployments"`
	Bridges     int64       `json:"bridges"`
	CreatedAt   time.Time   `json:"createdAt"`
	UpdatedAt   time.Time   `json:"updatedAt"`
	Managed     *managedDTO `json:"managed"`
}

func (a *API) ipGroupOut(g database.IPGroup) ipGroupDTO {
	var nd, nb int64
	a.db.Model(&database.DeploymentIPGroup{}).Where("ip_group_id = ?", g.ID).Count(&nd)
	a.db.Model(&database.BridgeIPGroup{}).Where("ip_group_id = ?", g.ID).Count(&nb)
	return ipGroupDTO{
		ID: g.ID, Name: g.Name, Entries: splitAllowlist(g.Entries),
		Deployments: nd, Bridges: nb, CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
		Managed: a.owned().managed(kindIPGroup, g.ID),
	}
}

func (a *API) findIPGroup(c *gin.Context) *database.IPGroup {
	g, err := database.First[database.IPGroup](a.db.Where("id = ?", c.Param("id")))
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return nil
	}
	if g == nil {
		abort(c, http.StatusNotFound, "no such IP group")
		return nil
	}
	return g
}

// normalizeAllowlist parses addresses and CIDRs (bare addresses become
// /32 or /128) and returns them one per line, deduplicated.
func normalizeAllowlist(entries []string) (string, int, error) {
	nets, err := registry.ParseAllowlist(strings.Join(entries, "\n"))
	if err != nil {
		return "", 0, err
	}
	seen := map[string]bool{}
	lines := make([]string, 0, len(nets))
	for _, n := range nets {
		if s := n.String(); !seen[s] {
			seen[s] = true
			lines = append(lines, s)
		}
	}
	return strings.Join(lines, "\n"), len(lines), nil
}

type ipGroupInput struct {
	Name    *string   `json:"name"`
	Entries *[]string `json:"entries"`
}

func applyIPGroup(g *database.IPGroup, in ipGroupInput) error {
	if in.Name != nil {
		g.Name = cleanName(*in.Name)
	}
	if in.Entries != nil {
		joined, n, err := normalizeAllowlist(*in.Entries)
		if err != nil {
			return inputError("entries: " + err.Error())
		}
		if n > maxIPGroupEntries {
			return inputError("an IP group holds at most 1000 entries")
		}
		g.Entries = joined
	}
	switch {
	case g.Name == "":
		return inputError("name required")
	case g.Entries == "":
		// An empty group in an allow list would let nobody in; make that a
		// deliberate choice (an allow list nobody matches) rather than a
		// side effect of emptying a group.
		return inputError("add at least one address or CIDR range")
	}
	return nil
}

func (a *API) listIPGroups(c *gin.Context) {
	var gs []database.IPGroup
	if err := a.db.Order("name COLLATE NOCASE").Find(&gs).Error; err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	out := make([]ipGroupDTO, 0, len(gs))
	for _, g := range gs {
		out = append(out, a.ipGroupOut(g))
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) getIPGroup(c *gin.Context) {
	if g := a.findIPGroup(c); g != nil {
		c.JSON(http.StatusOK, a.ipGroupOut(*g))
	}
}

func (a *API) createIPGroup(c *gin.Context) {
	var in ipGroupInput
	if err := c.ShouldBindJSON(&in); err != nil {
		abort(c, http.StatusBadRequest, "bad json")
		return
	}
	if !a.schemaAllowsCreate(c, collIPGroups) {
		return
	}
	now := a.now()
	g := database.IPGroup{ID: database.NewID(), CreatedAt: now, UpdatedAt: now}
	if err := applyIPGroup(&g, in); err != nil {
		a.inputFail(c, err)
		return
	}
	if err := a.db.Create(&g).Error; err != nil {
		ipGroupWriteFail(c, &g, err)
		return
	}
	a.changed()
	c.JSON(http.StatusCreated, a.ipGroupOut(g))
}

func (a *API) updateIPGroup(c *gin.Context) {
	g := a.findIPGroup(c)
	if g == nil {
		return
	}
	var in ipGroupInput
	if err := c.ShouldBindJSON(&in); err != nil {
		abort(c, http.StatusBadRequest, "bad json")
		return
	}
	before := *g
	if err := applyIPGroup(g, in); err != nil {
		a.inputFail(c, err)
		return
	}
	if !a.schemaAllowsUpdate(c, kindIPGroup, g.ID, before.Name, g.Name, groupItem(before.Entries), groupItem(g.Entries)) {
		return
	}
	if !a.schemaAllowsGroupRename(c, kindIPGroup, g.ID, before.Name, g.Name) {
		return
	}
	g.UpdatedAt = a.now()
	if err := a.db.Save(g).Error; err != nil {
		ipGroupWriteFail(c, g, err)
		return
	}
	a.changed()
	c.JSON(http.StatusOK, a.ipGroupOut(*g))
}

func (a *API) deleteIPGroup(c *gin.Context) {
	g := a.findIPGroup(c)
	if g == nil {
		return
	}
	if !a.schemaAllowsDelete(c, kindIPGroup, g.ID, g.Name) {
		return
	}
	if out := a.ipGroupOut(*g); out.Deployments > 0 || out.Bridges > 0 {
		abort(c, http.StatusConflict, g.Name+" is in use: take it off those allow lists first")
		return
	}
	// The foreign keys refuse this if an allow list took the group up since
	// the check above.
	if err := a.db.Delete(g).Error; err != nil {
		if errors.Is(err, gorm.ErrForeignKeyViolated) {
			abort(c, http.StatusConflict, g.Name+" is in use: take it off those allow lists first")
			return
		}
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	a.changed()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func ipGroupWriteFail(c *gin.Context, g *database.IPGroup, err error) {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		abort(c, http.StatusConflict, "there's already an IP group called "+g.Name)
		return
	}
	abort(c, http.StatusInternalServerError, "server error")
}

// groupWriteFail answers a failed deployment or bridge write that also set
// its IP or email groups: a group deleted since it was checked is a 400.
func groupWriteFail(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrForeignKeyViolated) {
		abort(c, http.StatusBadRequest, "it names a group that was just deleted; reload and try again")
		return
	}
	abort(c, http.StatusInternalServerError, "server error")
}

// cleanIPGroupIDs checks that every id names an IP group and returns them
// deduplicated, in order. nil (field not sent) stays nil: leave as is.
func (a *API) cleanIPGroupIDs(ids *[]string) ([]string, error) {
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
	if err := a.db.Model(&database.IPGroup{}).Where("id IN ?", out).Count(&n).Error; err != nil {
		return nil, err
	}
	if int(n) != len(out) {
		return nil, inputError("the allow list names an IP group that doesn't exist")
	}
	return out, nil
}

// ipGroupIDsOf maps each owner to its groups' IDs, ordered by group name.
// model is DeploymentIPGroup or BridgeIPGroup; col its owner column.
func (a *API) ipGroupIDsOf(model any, col string, ids ...string) map[string][]string {
	return a.groupIDsOf(model, col, "ip_group_id", "ip_groups", ids...)
}

// groupIDsOf maps each owner to the IDs of its groups, ordered by group
// name. model is a join table's model, ownerCol and groupCol its columns,
// and groupTable the table groupCol points at.
func (a *API) groupIDsOf(model any, ownerCol, groupCol, groupTable string, ids ...string) map[string][]string {
	return groupIDsIn(a.db, model, ownerCol, groupCol, groupTable, ids...)
}

// groupIDsIn is groupIDsOf on db (a transaction, say).
func groupIDsIn(db *gorm.DB, model any, ownerCol, groupCol, groupTable string, ids ...string) map[string][]string {
	var rows []struct {
		OwnerID string
		GroupID string
	}
	db.Model(model).
		Select(ownerCol+" AS owner_id", groupCol+" AS group_id").
		Joins("JOIN "+groupTable+" ON "+groupTable+".id = "+groupCol).
		Where(ownerCol+" IN ?", ids).
		Order(groupTable + ".name COLLATE NOCASE").
		Scan(&rows)
	m := map[string][]string{}
	for _, r := range rows {
		m[r.OwnerID] = append(m[r.OwnerID], r.GroupID)
	}
	return m
}

// setDeploymentIPGroups replaces a deployment's groups with ids.
func setDeploymentIPGroups(tx *gorm.DB, deploymentID string, ids []string) error {
	if err := tx.Where("deployment_id = ?", deploymentID).Delete(&database.DeploymentIPGroup{}).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	rows := make([]database.DeploymentIPGroup, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, database.DeploymentIPGroup{DeploymentID: deploymentID, IPGroupID: id})
	}
	return tx.Create(&rows).Error
}

// setBridgeIPGroups replaces a bridge's groups with ids.
func setBridgeIPGroups(tx *gorm.DB, bridgeID string, ids []string) error {
	if err := tx.Where("bridge_id = ?", bridgeID).Delete(&database.BridgeIPGroup{}).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	rows := make([]database.BridgeIPGroup, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, database.BridgeIPGroup{BridgeID: bridgeID, IPGroupID: id})
	}
	return tx.Create(&rows).Error
}
