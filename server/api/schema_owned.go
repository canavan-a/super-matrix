package api

import (
	"bytes"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"main/database"
)

// What the last schema apply took charge of (schema_owned), and the guards
// that keep the app, relays and the API from changing it unless override
// mode is on. A guard refuses only an actual change: resending a managed
// value as it is (older apps send whole forms) goes through.

// Kinds of resource in schema_owned.
const (
	kindDomain     = "domain"
	kindIPGroup    = "ip_group"
	kindEmailGroup = "email_group"
	kindDeployment = "deployment"
	kindPathRule   = "path_rule"
	kindBridge     = "bridge"
	// kindCollection rows (resource_id a collection below) mark a
	// collection the schema lists in full: nothing can be added to it.
	kindCollection = "collection"
)

// Collections, by their path in the document.
const (
	collDomains     = "domains"
	collIPGroups    = "groups.ip"
	collEmailGroups = "groups.email"
	collDeployments = "deployments"
	collBridges     = "bridges"
)

type resKey struct{ kind, id string }

type ownIndex struct {
	revision int
	rows     map[resKey][]database.SchemaOwned
}

func loadOwned(db *gorm.DB) (*ownIndex, error) {
	o := &ownIndex{rows: map[resKey][]database.SchemaOwned{}}
	st, err := database.First[database.SchemaState](db.Where("id = 1"))
	if err != nil {
		return nil, err
	}
	if st == nil {
		return o, nil // never applied: nothing is managed
	}
	o.revision = st.Revision
	var rows []database.SchemaOwned
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		k := resKey{r.Kind, r.ResourceID}
		o.rows[k] = append(o.rows[k], r)
	}
	return o, nil
}

// owns reports whether the schema manages the resource itself.
func (o *ownIndex) owns(kind, id string) bool {
	return slices.ContainsFunc(o.rows[resKey{kind, id}], func(r database.SchemaOwned) bool { return r.Field == "" })
}

// ownsField reports whether the schema alone sets field of the resource.
func (o *ownIndex) ownsField(kind, id, field string) bool {
	return slices.ContainsFunc(o.rows[resKey{kind, id}], func(r database.SchemaOwned) bool { return r.Field == field && r.Entry == "" })
}

// entries is the schema's own entries of a list field the app may add to.
func (o *ownIndex) entries(kind, id, field string) []string {
	var out []string
	for _, r := range o.rows[resKey{kind, id}] {
		if r.Field == field && r.Entry != "" {
			out = append(out, r.Entry)
		}
	}
	return out
}

func (o *ownIndex) strict(coll string) bool { return o.owns(kindCollection, coll) }

// managedDTO tells the app what of a resource the schema manages; nil
// when it manages none of it.
type managedDTO struct {
	Revision int `json:"revision"`
	// Fields are the values only the schema sets, by document name
	// (target_host, ip_allowlist …); path_rules means the whole list of
	// path rules.
	Fields []string `json:"fields"`
	// Entries are, per list field the app may add to, the schema's own
	// entries, which the app can't remove.
	Entries map[string][]string `json:"entries"`
}

func (o *ownIndex) managed(kind, id string) *managedDTO {
	if o == nil || !o.owns(kind, id) {
		return nil
	}
	m := &managedDTO{Revision: o.revision, Fields: []string{}, Entries: map[string][]string{}}
	for _, r := range o.rows[resKey{kind, id}] {
		switch {
		case r.Field == "":
		case r.Entry == "":
			m.Fields = append(m.Fields, r.Field)
		default:
			m.Entries[r.Field] = append(m.Entries[r.Field], r.Entry)
		}
	}
	sort.Strings(m.Fields)
	return m
}

// owned is the index for a request; on a database error it's empty (the
// guards then fail closed through their own lookups).
func (a *API) owned() *ownIndex {
	o, err := loadOwned(a.db)
	if err != nil {
		return &ownIndex{rows: map[resKey][]database.SchemaOwned{}}
	}
	return o
}

func lockedMsg(what string, rev int) string {
	return fmt.Sprintf("%s is managed by schema rev %d: change it in the schema, or turn on override in Settings", what, rev)
}

// guardIndex is the ownership index for a guard, nil when nothing is
// locked (override is on or there's no schema). On a database error it
// answers 500 and returns ok false.
func (a *API) guardIndex(c *gin.Context) (o *ownIndex, ok bool) {
	if a.overrideOn() {
		return nil, true
	}
	o, err := loadOwned(a.db)
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return nil, false
	}
	if o.revision == 0 {
		return nil, true
	}
	return o, true
}

// schemaAllowsCreate refuses adding to a collection the schema lists in
// full.
func (a *API) schemaAllowsCreate(c *gin.Context, coll string) bool {
	o, ok := a.guardIndex(c)
	if !ok {
		return false
	}
	if o != nil && o.strict(coll) {
		abort(c, http.StatusConflict, lockedMsg("the list of "+coll, o.revision))
		return false
	}
	return true
}

// schemaAllowsDelete refuses deleting a resource the schema manages.
func (a *API) schemaAllowsDelete(c *gin.Context, kind, id, name string) bool {
	o, ok := a.guardIndex(c)
	if !ok {
		return false
	}
	if o != nil && o.owns(kind, id) {
		abort(c, http.StatusConflict, lockedMsg(name, o.revision))
		return false
	}
	return true
}

// schemaAllowsUpdate refuses an update that changes what the schema
// manages of a resource: its key (beforeKey → afterKey: a host, slug, name
// or path prefix), a field only the schema sets, or one of the schema's
// entries in a list the app may add to. before and after are the resource
// as document items.
func (a *API) schemaAllowsUpdate(c *gin.Context, kind, id, beforeKey, afterKey string, before, after any) bool {
	o, ok := a.guardIndex(c)
	if !ok {
		return false
	}
	if o == nil || len(o.rows[resKey{kind, id}]) == 0 {
		return true
	}
	if beforeKey != afterKey && o.owns(kind, id) {
		abort(c, http.StatusConflict, lockedMsg(beforeKey+"'s name", o.revision))
		return false
	}
	b, af := fieldsOf(before), fieldsOf(after)
	for _, r := range o.rows[resKey{kind, id}] {
		switch {
		case r.Field == "":
		case r.Entry == "":
			if !bytes.Equal(b[r.Field], af[r.Field]) {
				abort(c, http.StatusConflict, lockedMsg(r.Field, o.revision))
				return false
			}
		default:
			if _, isList := af[r.Field]; isList && !slices.Contains(listOf(af[r.Field]), r.Entry) {
				abort(c, http.StatusConflict, lockedMsg(r.Entry+" in "+r.Field, o.revision))
				return false
			}
		}
	}
	return true
}

// schemaAllowsPathRules refuses changing the path rules of a deployment
// whose whole list the schema sets.
func (a *API) schemaAllowsPathRules(c *gin.Context, deploymentID string) bool {
	o, ok := a.guardIndex(c)
	if !ok {
		return false
	}
	if o != nil && o.ownsField(kindDeployment, deploymentID, "path_rules") {
		abort(c, http.StatusConflict, lockedMsg("this deployment's path rules", o.revision))
		return false
	}
	return true
}

// relaySlugInUse reports whether the schema names relay id by its slug,
// so renaming it would break the next apply.
func (a *API) relaySlugInUse(id string) (bool, error) {
	const owned = "id IN (SELECT resource_id FROM schema_owned WHERE kind = ? AND field = '')"
	var n int64
	err := a.db.Model(&database.Deployment{}).Where("relay_id = ? AND "+owned, id, kindDeployment).Count(&n).Error
	if err != nil || n > 0 {
		return n > 0, err
	}
	err = a.db.Model(&database.Bridge{}).Where("(from_relay_id = ? OR to_relay_id = ?) AND "+owned, id, id, kindBridge).Count(&n).Error
	return n > 0, err
}

// schemaAllowsGroupRename refuses renaming a group that schema-managed
// deployments or bridges name in their group lists: the next apply
// wouldn't find it. (Renaming a group the schema owns is refused anyway.)
func (a *API) schemaAllowsGroupRename(c *gin.Context, kind, id, before, after string) bool {
	if strings.EqualFold(before, after) {
		return true
	}
	o, ok := a.guardIndex(c)
	if !ok || o == nil {
		return ok
	}
	type link struct{ table, col, kind string }
	links := []link{{"deployment_ip_groups", "deployment_id", kindDeployment}, {"bridge_ip_groups", "bridge_id", kindBridge}}
	field := "ip_groups"
	if kind == kindEmailGroup {
		links = []link{{"deployment_email_groups", "deployment_id", kindDeployment}}
		field = "email_groups"
	}
	for _, l := range links {
		var n int64
		err := a.db.Table("schema_owned").
			Where("kind = ? AND field = ? AND (entry = '' OR entry = ? COLLATE NOCASE)", l.kind, field, before).
			Where("resource_id IN (SELECT "+l.col+" FROM "+l.table+" WHERE "+strings.TrimSuffix(field, "s")+"_id = ?)", id).
			Count(&n).Error
		if err != nil {
			abort(c, http.StatusInternalServerError, "server error")
			return false
		}
		if n > 0 {
			abort(c, http.StatusConflict, lockedMsg("the name "+before+" (schema-managed allow lists name the group by it)", o.revision))
			return false
		}
	}
	return true
}

func hostOf(d database.Deployment) string {
	if d.Host == nil {
		return d.ID
	}
	return *d.Host
}

// deploymentGuard checks an update of a deployment from before to after;
// ipGroups and emailGroups are the groups it sets (nil: unchanged).
func (a *API) deploymentGuard(c *gin.Context, before, after database.Deployment, ipGroups, emailGroups []string) bool {
	if o, ok := a.guardIndex(c); !ok || o == nil || !o.owns(kindDeployment, before.ID) {
		return ok
	}
	n, err := loadNames(a.db)
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return false
	}
	curIP := a.deploymentIPGroups(before.ID)[before.ID]
	curEmail := a.deploymentEmailGroups(before.ID)[before.ID]
	newIP, newEmail := curIP, curEmail
	if ipGroups != nil {
		newIP = ipGroups
	}
	if emailGroups != nil {
		newEmail = emailGroups
	}
	return a.schemaAllowsUpdate(c, kindDeployment, before.ID, hostOf(before), hostOf(after),
		n.deploymentItem(before, curIP, curEmail, nil), n.deploymentItem(after, newIP, newEmail, nil))
}

// bridgeGuard checks an update of a bridge from before to after; ipGroups
// is the groups it sets (nil: unchanged).
func (a *API) bridgeGuard(c *gin.Context, before, after database.Bridge, ipGroups []string) bool {
	if o, ok := a.guardIndex(c); !ok || o == nil || !o.owns(kindBridge, before.ID) {
		return ok
	}
	n, err := loadNames(a.db)
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return false
	}
	cur := a.bridgeIPGroups(before.ID)[before.ID]
	next := cur
	if ipGroups != nil {
		next = ipGroups
	}
	return a.schemaAllowsUpdate(c, kindBridge, before.ID, before.Slug, after.Slug, n.bridgeItem(before, cur), n.bridgeItem(after, next))
}
