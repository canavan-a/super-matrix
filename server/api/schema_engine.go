package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"gorm.io/gorm"

	"main/database"
	"main/schema"
	"main/version"
)

// The schema engine: plan and apply are one run over a transaction that
// makes every change a document asks for, through the same validators the
// API's handlers use, recording what it did as ops. Plan rolls the
// transaction back; apply commits it, together with the new ownership
// (schema_owned) and revision (schema_state). So a plan is exactly what
// apply would do, and an apply is all or nothing.
//
// Order matters: domains and groups are made before deployments and
// bridges that name them, undeclared deployments and bridges are deleted
// before new ones claim their hostnames and ports, and undeclared groups
// and domains are deleted last, once nothing uses them.

type schemaChange struct {
	Field string          `json:"field"`
	From  json.RawMessage `json:"from"`
	To    json.RawMessage `json:"to"`
}

// schemaOp is one thing an apply does.
type schemaOp struct {
	// Action: create, update, delete, or adopt (an existing resource the
	// schema now takes charge of, perhaps changing it).
	Action string `json:"action"`
	// Kind: domain, ip_group, email_group, deployment, path_rule or bridge.
	Kind string `json:"kind"`
	// Key is the resource's name in the document.
	Key     string         `json:"key"`
	Changes []schemaChange `json:"changes,omitempty"`
	// Drift: the values were changed outside the schema (in override mode)
	// since this same document was applied, and go back.
	Drift bool `json:"drift,omitempty"`
}

type schemaPlanOut struct {
	Ops []schemaOp `json:"ops"`
	// Changed: applying would do something (ops, or a new document whose
	// markers change what the schema manages).
	Changed      bool   `json:"changed"`
	Creates      int    `json:"creates"`
	Updates      int    `json:"updates"`
	Deletes      int    `json:"deletes"`
	PlanHash     string `json:"planHash"`
	BaseRevision int    `json:"baseRevision"`
	DocVersion   int    `json:"docVersion"`
}

type planner struct {
	a     *API
	tx    *gorm.DB
	doc   *schema.Document
	old   *ownIndex
	names *schemaNames
	// same: the document is the one last applied, so updates are drift.
	same bool

	own      []database.SchemaOwned
	ops      []schemaOp
	errs     schema.Errors
	internal error
}

// runSchema makes doc's changes in tx.
func (a *API) runSchema(tx *gorm.DB, doc *schema.Document, docHash string) (*planner, error) {
	old, err := loadOwned(tx)
	if err != nil {
		return nil, err
	}
	p := &planner{a: a, tx: tx, doc: doc, old: old}
	if st, err := database.First[database.SchemaState](tx.Where("id = 1")); err != nil {
		return nil, err
	} else if st != nil && st.DocHash == docHash {
		p.same = true
	}
	steps := []func(){
		p.domains,
		func() { p.groups(kindIPGroup) },
		func() { p.groups(kindEmailGroup) },
		p.reloadNames,
		p.deleteUndeclaredBridges,
		p.deleteUndeclaredDeployments,
		p.deployments,
		p.bridges,
		func() { p.deleteUndeclaredGroups(kindIPGroup) },
		func() { p.deleteUndeclaredGroups(kindEmailGroup) },
		p.deleteUndeclaredDomains,
	}
	for _, step := range steps {
		step()
		if p.internal != nil {
			return nil, p.internal
		}
	}
	return p, nil
}

func (p *planner) result(base int) *schemaPlanOut {
	out := &schemaPlanOut{Ops: p.ops, BaseRevision: base, DocVersion: p.doc.From}
	if out.Ops == nil {
		out.Ops = []schemaOp{}
	}
	for _, op := range p.ops {
		switch op.Action {
		case "create":
			out.Creates++
		case "delete":
			out.Deletes++
		default:
			out.Updates++
		}
	}
	return out
}

// planHash fingerprints what an apply would do, on which server version,
// from which revision, with which document: if any of it changes between
// plan and apply, the apply is refused.
func planHash(out *schemaPlanOut, docHash string) string {
	b, _ := json.Marshal(struct {
		Server, DocHash string
		DocVersion      int
		Base            int
		Ops             []schemaOp
	}{version.Version, docHash, out.DocVersion, out.BaseRevision, out.Ops})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:16])
}

// fail records a problem with the document at path, or a server error.
func (p *planner) fail(path string, err error) {
	var ie inputError
	var ce conflictError
	var se schema.Errors
	switch {
	case errors.As(err, &se):
		p.errs = append(p.errs, se...)
	case errors.As(err, &ie), errors.As(err, &ce):
		p.errs = append(p.errs, schema.Error{Path: path, Msg: err.Error()})
	default:
		if p.internal == nil {
			p.internal = fmt.Errorf("%s: %w", path, err)
		}
	}
}

func (p *planner) failf(path, format string, args ...any) {
	p.errs = append(p.errs, schema.Error{Path: path, Msg: fmt.Sprintf(format, args...)})
}

func (p *planner) dbErr(err error) bool {
	if err != nil && p.internal == nil {
		p.internal = err
	}
	return err != nil
}

func (p *planner) ownRow(kind, id, field, entry string) {
	p.own = append(p.own, database.SchemaOwned{Kind: kind, ResourceID: id, Field: field, Entry: entry})
}

func (p *planner) op(action, kind, key string, changes []schemaChange) {
	op := schemaOp{Action: action, Kind: kind, Key: key, Changes: changes}
	op.Drift = p.same && action == "update"
	p.ops = append(p.ops, op)
}

// write records an update or adoption of an existing resource: adopt when
// the schema didn't manage it before, update when something changed.
func (p *planner) write(kind, id, key string, changes []schemaChange) bool {
	switch {
	case !p.old.owns(kind, id):
		p.op("adopt", kind, key, changes)
	case len(changes) > 0:
		p.op("update", kind, key, changes)
	}
	return len(changes) > 0
}

// collection records how the document lists a collection: in full (strict:
// nothing may be added to it outside the schema) or not.
func (p *planner) collection(name string, spread bool) {
	if !spread {
		p.ownRow(kindCollection, name, "", "")
	}
}

// diff is how two document items differ, field by field.
func diff(before, after any, skip ...string) []schemaChange {
	b, a := fieldsOf(before), fieldsOf(after)
	var keys []string
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	order := schema.Fields(after)
	slices.SortFunc(keys, func(x, y string) int { return slices.Index(order, x) - slices.Index(order, y) })
	var out []schemaChange
	for _, k := range keys {
		if slices.Contains(skip, k) || string(b[k]) == string(a[k]) {
			continue
		}
		ch := schemaChange{Field: k, From: b[k], To: a[k]}
		if strings.HasSuffix(k, "_password") {
			// Hashes aren't secrets, but they're noise in a plan.
			ch.From, ch.To = maskHash(b[k]), maskHash(a[k])
		}
		out = append(out, ch)
	}
	return out
}

func maskHash(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return json.RawMessage(`"(a password)"`)
}

// fieldCtx decides, field by field, what a document item sets.
type fieldCtx struct {
	p          *planner
	kind, id   string
	item       any
	ui         []string
	create     bool
	unmanaged  bool // the whole item is the app's (a seed)
	docVersion int
}

func (p *planner) fields(kind, id string, item any, ui []string, create bool) *fieldCtx {
	return &fieldCtx{p: p, kind: kind, id: id, item: item, ui: ui, create: create, docVersion: p.doc.From}
}

// managed reports whether the schema sets field f: it isn't handed to the
// app with ui, and the document is new enough to know it.
func (fc *fieldCtx) managed(f string) bool {
	return !fc.unmanaged && !slices.Contains(fc.ui, f) && schema.Since(fc.item, f) <= fc.docVersion
}

// take reports whether field f is set from the document: always when the
// schema manages it (a left-out value means its default); when the app
// does, only to seed a new resource with a value the document gives.
func (fc *fieldCtx) take(f string, given bool) bool {
	if fc.managed(f) {
		fc.p.ownRow(fc.kind, fc.id, f, "")
		return true
	}
	return fc.create && given
}

// value sets *dst from v (or def when v is left out) if field f is taken.
func value[T any](fc *fieldCtx, f string, v, def *T, dst **T) {
	if !fc.take(f, v != nil) {
		return
	}
	if v == nil {
		v = def
	}
	*dst = v
}

// list works out a list field's new entries. norm checks and normalizes
// entries; cur is what the resource has now. With the spread marker the
// app's entries stay and the schema owns its own; dropping an entry from
// the document removes it only if the schema put it there.
func (fc *fieldCtx) list(f string, l *schema.List, cur []string, norm func([]string) ([]string, error), path string) *[]string {
	managed := fc.managed(f)
	if !managed && !(fc.create && l != nil) {
		return nil
	}
	var items []string
	spread := false
	if l != nil {
		var err error
		if items, err = norm(l.Items); err != nil {
			fc.p.fail(path+"."+f, err)
			return nil
		}
		spread = l.Spread
	}
	if !managed || !spread {
		if managed {
			fc.p.ownRow(fc.kind, fc.id, f, "")
		}
		return &items
	}
	prev := fc.p.old.entries(fc.kind, fc.id, f)
	out := []string{}
	for _, e := range cur {
		if slices.Contains(prev, e) && !slices.Contains(items, e) {
			continue
		}
		out = append(out, e)
	}
	for _, e := range items {
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
		fc.p.ownRow(fc.kind, fc.id, f, e)
	}
	return &out
}

func normIPs(entries []string) ([]string, error) {
	joined, _, err := normalizeAllowlist(entries)
	if err != nil {
		return nil, inputError(err.Error())
	}
	return splitAllowlist(joined), nil
}

func normEmails(entries []string) ([]string, error) {
	joined, err := normalizeEmails(entries, "entry")
	if err != nil {
		return nil, err
	}
	return splitAllowlist(joined), nil
}

// normGroups resolves group names (any case) to the groups' own names.
func normGroups(names map[string]string, what string) func([]string) ([]string, error) {
	return func(entries []string) ([]string, error) {
		var out []string
		for _, e := range entries {
			e = strings.TrimSpace(e)
			found := ""
			for _, n := range names {
				if strings.EqualFold(n, e) {
					found = n
				}
			}
			if found == "" {
				return nil, inputError(fmt.Sprintf("no %s called %q (add it under groups)", what, e))
			}
			if !slices.Contains(out, found) {
				out = append(out, found)
			}
		}
		return out, nil
	}
}

// idsOf maps group names back to IDs.
func idsOf(names []string, byID map[string]string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		for id, name := range byID {
			if name == n {
				out = append(out, id)
			}
		}
	}
	return out
}

func namesList(ids []string, byID map[string]string) []string {
	return namesOf(ids, byID).Items
}

// --- domains ---

func (p *planner) domains() {
	l := p.doc.Domains
	if l == nil {
		return
	}
	p.collection(collDomains, l.Spread)
	var all []database.Domain
	if p.dbErr(p.tx.Find(&all).Error) {
		return
	}
	for i, label := range l.Items {
		path := fmt.Sprintf("domains[%d]", i)
		name, wildcard, err := parseDomain(label)
		if err != nil {
			p.fail(path, err)
			continue
		}
		if !wildcard && name == p.a.cfg.AdminHost {
			p.failf(path, "%s is the admin host", name)
			continue
		}
		idx := slices.IndexFunc(all, func(d database.Domain) bool { return d.Name == name && d.Wildcard == wildcard })
		if idx < 0 {
			d := database.Domain{ID: database.NewID(), Name: name, Wildcard: wildcard, CreatedAt: p.a.now()}
			if p.dbErr(p.tx.Create(&d).Error) {
				return
			}
			all = append(all, d)
			p.op("create", kindDomain, d.Label(), nil)
			idx = len(all) - 1
		} else {
			p.write(kindDomain, all[idx].ID, all[idx].Label(), nil)
		}
		p.ownRow(kindDomain, all[idx].ID, "", "")
	}
}

func (p *planner) declaredDomain(d database.Domain) bool {
	for _, label := range p.doc.Domains.Items {
		if name, wildcard, err := parseDomain(label); err == nil && name == d.Name && wildcard == d.Wildcard {
			return true
		}
	}
	return false
}

func (p *planner) deleteUndeclaredDomains() {
	l := p.doc.Domains
	if l == nil {
		return
	}
	var all []database.Domain
	if p.dbErr(p.tx.Find(&all).Error) {
		return
	}
	for _, d := range all {
		if p.declaredDomain(d) || (l.Spread && !p.old.owns(kindDomain, d.ID)) {
			continue
		}
		var n, nb int64
		p.tx.Model(&database.Deployment{}).Where("domain_id = ?", d.ID).Count(&n)
		p.tx.Model(&database.Bridge{}).Where("domain_id = ?", d.ID).Count(&nb)
		if n > 0 || nb > 0 {
			p.failf("domains", "%s isn't listed, but %d deployments and %d bridges still use it", d.Label(), n, nb)
			continue
		}
		if p.dbErr(p.tx.Delete(&d).Error) {
			return
		}
		p.op("delete", kindDomain, d.Label(), nil)
	}
}

// --- groups ---

// groupRow is an IP or email group, either kind.
type groupRow struct {
	ID, Name, Entries string
}

func (p *planner) groupMap(kind string) (*schema.Map[schema.Group], string, string) {
	if p.doc.Groups == nil {
		return nil, "", ""
	}
	if kind == kindIPGroup {
		return p.doc.Groups.IP, collIPGroups, "ip_groups"
	}
	return p.doc.Groups.Email, collEmailGroups, "email_groups"
}

func (p *planner) groups(kind string) {
	m, coll, table := p.groupMap(kind)
	if m == nil {
		return
	}
	p.collection(coll, m.Spread)
	var rows []groupRow
	if p.dbErr(p.tx.Table(table).Select("id", "name", "entries").Scan(&rows).Error) {
		return
	}
	norm := normIPs
	if kind == kindEmailGroup {
		norm = normEmails
	}
	for _, name := range m.Keys {
		g := m.Items[name]
		path := coll + "." + name
		idx := slices.IndexFunc(rows, func(r groupRow) bool { return strings.EqualFold(r.Name, name) })
		create := idx < 0
		var cur groupRow
		if create {
			cur = groupRow{ID: database.NewID()}
		} else {
			cur = rows[idx]
		}
		fc := p.fields(kind, cur.ID, g, g.UI, create)
		entries := fc.list("entries", g.Entries, splitAllowlist(cur.Entries), norm, path)
		if entries == nil && create {
			p.failf(path+".entries", "a new group needs entries")
			continue
		}
		now := p.a.now()
		var err error
		var after string
		if kind == kindIPGroup {
			row := database.IPGroup{ID: cur.ID, Name: cur.Name, Entries: cur.Entries, CreatedAt: now, UpdatedAt: now}
			err = applyIPGroup(&row, ipGroupInput{Name: &name, Entries: entries})
			after = row.Entries
		} else {
			row := database.EmailGroup{ID: cur.ID, Name: cur.Name, Entries: cur.Entries, CreatedAt: now, UpdatedAt: now}
			err = applyEmailGroup(&row, emailGroupInput{Name: &name, Entries: entries})
			after = row.Entries
		}
		if err != nil {
			p.fail(path, err)
			continue
		}
		p.ownRow(kind, cur.ID, "", "")
		if create {
			err = p.tx.Table(table).Create(map[string]any{"id": cur.ID, "name": name, "entries": after, "created_at": now, "updated_at": now}).Error
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				p.failf(path, "listed twice (names ignore case)")
				continue
			}
			if p.dbErr(err) {
				return
			}
			rows = append(rows, groupRow{ID: cur.ID, Name: name, Entries: after})
			p.op("create", kind, name, nil)
			continue
		}
		changes := diff(groupItem(cur.Entries), groupItem(after))
		if cur.Name != name {
			from, _ := json.Marshal(cur.Name)
			to, _ := json.Marshal(name)
			changes = append([]schemaChange{{Field: "name", From: from, To: to}}, changes...)
		}
		if p.write(kind, cur.ID, name, changes) {
			if p.dbErr(p.tx.Table(table).Where("id = ?", cur.ID).Updates(map[string]any{"name": name, "entries": after, "updated_at": now}).Error) {
				return
			}
		}
	}
}

func (p *planner) deleteUndeclaredGroups(kind string) {
	m, coll, table := p.groupMap(kind)
	if m == nil {
		return
	}
	var rows []groupRow
	if p.dbErr(p.tx.Table(table).Select("id", "name").Scan(&rows).Error) {
		return
	}
	for _, r := range rows {
		declared := slices.ContainsFunc(m.Keys, func(k string) bool { return strings.EqualFold(k, r.Name) })
		if declared || (m.Spread && !p.old.owns(kind, r.ID)) {
			continue
		}
		var n int64
		if kind == kindIPGroup {
			var nb int64
			p.tx.Model(&database.DeploymentIPGroup{}).Where("ip_group_id = ?", r.ID).Count(&n)
			p.tx.Model(&database.BridgeIPGroup{}).Where("ip_group_id = ?", r.ID).Count(&nb)
			n += nb
		} else {
			p.tx.Model(&database.DeploymentEmailGroup{}).Where("email_group_id = ?", r.ID).Count(&n)
		}
		if n > 0 {
			p.failf(coll, "%s isn't listed, but %d deployments or bridges still include it", r.Name, n)
			continue
		}
		if p.dbErr(p.tx.Table(table).Where("id = ?", r.ID).Delete(nil).Error) {
			return
		}
		p.op("delete", kind, r.Name, nil)
	}
}

func (p *planner) reloadNames() {
	n, err := loadNames(p.tx)
	if p.dbErr(err) {
		return
	}
	p.names = n
}

// --- deployments ---

func normHost(key string) string { return strings.ToLower(strings.TrimSpace(key)) }

func (p *planner) deleteUndeclaredDeployments() {
	m := p.doc.Deployments
	if m == nil {
		return
	}
	p.collection(collDeployments, m.Spread)
	var all []database.Deployment
	if p.dbErr(p.tx.Where("host IS NOT NULL").Find(&all).Error) {
		return
	}
	for _, d := range all {
		declared := slices.ContainsFunc(m.Keys, func(k string) bool { return normHost(k) == *d.Host })
		if declared || (m.Spread && !p.old.owns(kindDeployment, d.ID)) {
			continue
		}
		if p.dbErr(p.tx.Delete(&d).Error) {
			return
		}
		p.op("delete", kindDeployment, *d.Host, nil)
	}
}

// hostDomain finds the domain host is on (label names it, else it's
// inferred) and host's subdomain there.
func (p *planner) hostDomain(host string, label *string) (*database.Domain, string, error) {
	var dom *database.Domain
	if label != nil {
		name, wildcard, err := parseDomain(*label)
		if err != nil {
			return nil, "", err
		}
		for i, d := range p.names.domains {
			if d.Name == name && d.Wildcard == wildcard {
				dom = &p.names.domains[i]
			}
		}
		if dom == nil {
			return nil, "", inputError("there's no domain " + *label + " (add it under domains)")
		}
	} else if dom = inferDomain(p.names.domains, host); dom == nil {
		return nil, "", inputError("no domain fits " + host + ": add one under domains, or name it with domain")
	}
	if !dom.Wildcard {
		if host != dom.Name {
			return nil, "", inputError(host + " isn't on " + dom.Label())
		}
		return dom, "", nil
	}
	sub, ok := strings.CutSuffix(host, "."+dom.Name)
	if !ok || strings.Contains(sub, ".") {
		return nil, "", inputError(host + " isn't one name under " + dom.Label())
	}
	return dom, sub, nil
}

func (p *planner) deployments() {
	m := p.doc.Deployments
	if m == nil {
		return
	}
	defaults := p.names.deploymentItem(p.a.newDeployment(), nil, nil, nil)
	for _, key := range m.Keys {
		p.deployment(normHost(key), "deployments."+key, m.Items[key], defaults)
		if p.internal != nil {
			return
		}
	}
}

func (p *planner) deployment(host, path string, sd *schema.Deployment, def *schema.Deployment) {
	existing, err := database.First[database.Deployment](p.tx.Where("host = ?", host))
	if p.dbErr(err) {
		return
	}
	create := existing == nil
	d := p.a.newDeployment()
	var curIP, curEmail []string
	var before *schema.Deployment
	if !create {
		d = *existing
		curIP = groupIDsIn(p.tx, &database.DeploymentIPGroup{}, "deployment_id", "ip_group_id", "ip_groups", d.ID)[d.ID]
		curEmail = groupIDsIn(p.tx, &database.DeploymentEmailGroup{}, "deployment_id", "email_group_id", "email_groups", d.ID)[d.ID]
		before = p.names.deploymentItem(d, curIP, curEmail, nil)
	}
	fc := p.fields(kindDeployment, d.ID, sd, sd.UI, create)
	in := deploymentInput{}

	dom, sub, err := p.hostDomain(host, sd.Domain)
	if err != nil {
		p.fail(path, err)
		return
	}
	in.DomainID, in.Subdomain = &dom.ID, &sub

	if fc.take("relay", sd.Relay != nil) {
		if sd.Relay == nil {
			p.failf(path+".relay", "required: the slug of the relay that reaches the target")
		} else if id, ok := p.names.relayID[*sd.Relay]; !ok {
			p.failf(path+".relay", "no relay with the slug %q", *sd.Relay)
		} else {
			in.RelayID = &id
		}
	}
	value(fc, "protocol", sd.Protocol, def.Protocol, &in.Protocol)
	value(fc, "target_host", sd.TargetHost, def.TargetHost, &in.TargetHost)
	protocol := d.Protocol // the app's, when the document doesn't set it
	if in.Protocol != nil {
		protocol = *in.Protocol
	}
	if fc.take("target_port", sd.TargetPort != nil) {
		switch {
		case sd.TargetPort != nil:
			in.TargetPort = sd.TargetPort
		case protocol == database.ProtocolSSH:
			in.TargetPort = ptr(22)
		default:
			p.failf(path+".target_port", "required")
		}
	}
	value(fc, "tls_skip_verify", sd.TLSSkipVerify, def.TLSSkipVerify, &in.TLSSkipVerify)
	value(fc, "sni", sd.SNI, def.SNI, &in.SNI)
	value(fc, "preserve_host", sd.PreserveHost, def.PreserveHost, &in.PreserveHost)
	value(fc, "enabled", sd.Enabled, def.Enabled, &in.Enabled)
	value(fc, "auth_mode", sd.AuthMode, def.AuthMode, &in.AuthMode)
	value(fc, "allow_root_login", sd.AllowRootLogin, def.AllowRootLogin, &in.AllowRootLogin)
	value(fc, "session_ttl_s", sd.SessionTTLS, def.SessionTTLS, &in.SessionTTLS)
	value(fc, "basic_user", sd.BasicUser, def.BasicUser, &in.BasicUser)
	value(fc, "ssh_cli", sd.SSHCLI, def.SSHCLI, &in.SSHCLI)
	value(fc, "ssh_shell", sd.SSHShell, def.SSHShell, &in.SSHShell)
	value(fc, "ssh_user", sd.SSHUser, def.SSHUser, &in.SSHUser)
	value(fc, "rate_limit_enabled", sd.RateLimitEnabled, def.RateLimitEnabled, &in.RateLimitEnabled)
	value(fc, "rate_limit_requests", sd.RateLimitRequests, def.RateLimitRequests, &in.RateLimitRequests)
	value(fc, "rate_limit_period_s", sd.RateLimitPeriodS, def.RateLimitPeriodS, &in.RateLimitPeriodS)
	value(fc, "rate_limit_burst", sd.RateLimitBurst, def.RateLimitBurst, &in.RateLimitBurst)
	// A password left out is the app's: no password is ever written down.
	if sd.LoginPassword != nil && fc.take("login_password", true) {
		in.loginPassHash = &sd.LoginPassword.Hash
	}
	if sd.BasicPassword != nil && fc.take("basic_password", true) {
		in.basicPassHash = &sd.BasicPassword.Hash
	}
	in.OTPEmails = fc.list("otp_emails", sd.OTPEmails, splitAllowlist(d.OTPEmails), normEmails, path)
	in.IPAllowlist = fc.list("ip_allowlist", sd.IPAllowlist, splitAllowlist(d.IPAllowlist), normIPs, path)
	newIP, newEmail := curIP, curEmail
	if l := fc.list("ip_groups", sd.IPGroups, namesList(curIP, p.names.ipGroup), normGroups(p.names.ipGroup, "IP group"), path); l != nil {
		newIP = idsOf(*l, p.names.ipGroup)
	}
	if l := fc.list("email_groups", sd.EmailGroups, namesList(curEmail, p.names.emailGroup), normGroups(p.names.emailGroup, "email group"), path); l != nil {
		newEmail = idsOf(*l, p.names.emailGroup)
	}
	if len(p.errs) > 0 && slices.ContainsFunc(p.errs, func(e schema.Error) bool { return strings.HasPrefix(e.Path, path) }) {
		return
	}

	if err := p.a.apply(p.tx, &d, in, len(newEmail)); err != nil {
		p.fail(path, err)
		return
	}
	if !p.a.cfg.TLSOn() {
		d.CertStatus = database.CertOff
	}
	p.ownRow(kindDeployment, d.ID, "", "")
	after := p.names.deploymentItem(d, newIP, newEmail, nil)
	if create {
		if p.dbErr(p.tx.Create(&d).Error) {
			return
		}
		p.op("create", kindDeployment, host, nil)
	} else if p.write(kindDeployment, d.ID, host, diff(before, after, "path_rules")) {
		d.UpdatedAt = p.a.now()
		if p.dbErr(p.tx.Save(&d).Error) {
			return
		}
	}
	if !slices.Equal(newIP, curIP) && p.dbErr(setDeploymentIPGroups(p.tx, d.ID, newIP)) {
		return
	}
	if !slices.Equal(newEmail, curEmail) && p.dbErr(setDeploymentEmailGroups(p.tx, d.ID, newEmail)) {
		return
	}
	p.pathRules(host, path, d.ID, sd, fc)
}

func (p *planner) pathRules(host, path, depID string, sd *schema.Deployment, fc *fieldCtx) {
	m := sd.PathRules
	managed := fc.managed("path_rules")
	if !managed && !(fc.create && m != nil) {
		return
	}
	if m == nil {
		m = &schema.Map[schema.PathRule]{}
	}
	if managed && !m.Spread {
		p.ownRow(kindDeployment, depID, "path_rules", "")
	}
	var cur []database.PathRule
	if p.dbErr(p.tx.Where("deployment_id = ?", depID).Find(&cur).Error) {
		return
	}
	declared := map[string]bool{}
	for _, k := range m.Keys {
		if pre, err := normalizePathPrefix(k); err == nil {
			declared[pre] = true
		}
	}
	if managed {
		for _, r := range cur {
			if declared[r.PathPrefix] || (m.Spread && !p.old.owns(kindPathRule, r.ID)) {
				continue
			}
			if p.dbErr(p.tx.Delete(&r).Error) {
				return
			}
			p.op("delete", kindPathRule, host+r.PathPrefix, nil)
		}
	}
	for _, key := range m.Keys {
		at := path + ".path_rules." + key
		pr := m.Items[key]
		prefix, err := normalizePathPrefix(key)
		if err != nil {
			p.fail(at, err)
			continue
		}
		idx := slices.IndexFunc(cur, func(r database.PathRule) bool { return r.PathPrefix == prefix })
		now := p.a.now()
		r := database.PathRule{ID: database.NewID(), DeploymentID: depID, CreatedAt: now, UpdatedAt: now}
		if idx >= 0 {
			r = cur[idx]
		}
		before := pathRuleItem(r)
		rfc := p.fields(kindPathRule, r.ID, pr, pr.UI, idx < 0)
		rfc.unmanaged = !managed
		in := pathRuleInput{PathPrefix: &prefix}
		in.IPAllowlist = rfc.list("ip_allowlist", pr.IPAllowlist, splitAllowlist(r.IPAllowlist), normIPs, at)
		if err := p.a.applyPathRule(&r, in); err != nil {
			p.fail(at, err)
			continue
		}
		if managed {
			p.ownRow(kindPathRule, r.ID, "", "")
		}
		switch {
		case idx < 0:
			if p.dbErr(p.tx.Create(&r).Error) {
				return
			}
			p.op("create", kindPathRule, host+prefix, nil)
		case !managed:
		case p.write(kindPathRule, r.ID, host+prefix, diff(before, pathRuleItem(r))):
			r.UpdatedAt = now
			if p.dbErr(p.tx.Save(&r).Error) {
				return
			}
		}
	}
}

// --- bridges ---

func (p *planner) deleteUndeclaredBridges() {
	m := p.doc.Bridges
	if m == nil {
		return
	}
	p.collection(collBridges, m.Spread)
	var all []database.Bridge
	if p.dbErr(p.tx.Find(&all).Error) {
		return
	}
	for _, b := range all {
		if m.Get(b.Slug) != nil || (m.Spread && !p.old.owns(kindBridge, b.ID)) {
			continue
		}
		if p.dbErr(p.tx.Delete(&b).Error) {
			return
		}
		p.op("delete", kindBridge, b.Slug, nil)
	}
}

func (p *planner) bridges() {
	m := p.doc.Bridges
	if m == nil {
		return
	}
	defaults := p.names.bridgeItem(p.a.newBridge(), nil)
	for _, key := range m.Keys {
		p.bridge(key, "bridges."+key, m.Items[key], defaults)
		if p.internal != nil {
			return
		}
	}
}

func (p *planner) relayRef(fc *fieldCtx, f, path string, slug *string, dst **string) {
	if !fc.take(f, slug != nil) || slug == nil {
		return
	}
	id, ok := p.names.relayID[*slug]
	if !ok {
		p.failf(path+"."+f, "no relay with the slug %q", *slug)
		return
	}
	*dst = &id
}

func (p *planner) bridge(slug, path string, sb *schema.Bridge, def *schema.Bridge) {
	if !database.ValidSlug(slug) {
		p.failf(path, "bridge keys are slugs: 1-63 lowercase letters, digits and dashes")
		return
	}
	existing, err := database.First[database.Bridge](p.tx.Where("slug = ?", slug))
	if p.dbErr(err) {
		return
	}
	create := existing == nil
	b := p.a.newBridge()
	var curIP []string
	var before *schema.Bridge
	if !create {
		b = *existing
		curIP = groupIDsIn(p.tx, &database.BridgeIPGroup{}, "bridge_id", "ip_group_id", "ip_groups", b.ID)[b.ID]
		before = p.names.bridgeItem(b, curIP)
	}
	fc := p.fields(kindBridge, b.ID, sb, sb.UI, create)
	in := bridgeInput{}
	value(fc, "name", sb.Name, &slug, &in.Name)
	value(fc, "protocol", sb.Protocol, ptr(database.ProtocolTCP), &in.Protocol)
	value(fc, "enabled", sb.Enabled, def.Enabled, &in.Enabled)
	value(fc, "public", sb.Public, ptr(false), &in.Public)
	public := b.Public()
	if in.Public != nil {
		public = *in.Public
	}
	p.relayRef(fc, "from_relay", path, sb.FromRelay, &in.FromRelayID)
	if !public && fc.managed("from_relay") && sb.FromRelay == nil {
		p.failf(path+".from_relay", "required unless public: true")
	}
	value(fc, "listen_host", sb.ListenHost, ptr("127.0.0.1"), &in.ListenHost)
	if fc.take("listen_port", sb.ListenPort != nil) {
		if sb.ListenPort == nil {
			p.failf(path+".listen_port", "required")
		}
		in.ListenPort = sb.ListenPort
	}
	p.relayRef(fc, "to_relay", path, sb.ToRelay, &in.ToRelayID)
	if fc.managed("to_relay") && sb.ToRelay == nil {
		p.failf(path+".to_relay", "required")
	}
	if fc.take("target_host", sb.TargetHost != nil) {
		if sb.TargetHost == nil {
			p.failf(path+".target_host", "required")
		}
		in.TargetHost = sb.TargetHost
	}
	// target_port defaults to the port the bridge listens on, which the app
	// may set when listen_port is left to it.
	listen := in.ListenPort
	if listen == nil && b.ListenPort != 0 {
		listen = ptr(b.ListenPort)
	}
	if fc.take("target_port", sb.TargetPort != nil) {
		switch {
		case sb.TargetPort != nil:
			in.TargetPort = sb.TargetPort
		case listen != nil:
			in.TargetPort = listen
		default:
			p.failf(path+".target_port", "required (or set listen_port)")
		}
	}
	value(fc, "udp_idle_s", sb.UDPIdleS, def.UDPIdleS, &in.UDPIdleS)
	if fc.take("domain", sb.Domain != nil) {
		in.DomainID = ptr("")
		if sb.Domain != nil {
			name, wildcard, err := parseDomain(*sb.Domain)
			if err != nil {
				p.fail(path+".domain", err)
			}
			for _, d := range p.names.domains {
				if d.Name == name && d.Wildcard == wildcard {
					in.DomainID = ptr(d.ID)
				}
			}
			if *in.DomainID == "" && err == nil {
				p.failf(path+".domain", "there's no domain %s (add it under domains)", *sb.Domain)
			}
		}
	}
	value(fc, "subdomain", sb.Subdomain, ptr(""), &in.Subdomain)
	if l := fc.list("ip_allowlist", sb.IPAllowlist, splitAllowlist(b.IPAllowlist), normIPs, path); l != nil {
		in.IPAllowlist = l
	}
	newIP := curIP
	if l := fc.list("ip_groups", sb.IPGroups, namesList(curIP, p.names.ipGroup), normGroups(p.names.ipGroup, "IP group"), path); l != nil {
		newIP = idsOf(*l, p.names.ipGroup)
	}
	if slices.ContainsFunc(p.errs, func(e schema.Error) bool { return strings.HasPrefix(e.Path, path) }) {
		return
	}
	if err := p.a.applyBridge(p.tx, &b, in, create); err != nil {
		p.fail(path, err)
		return
	}
	p.ownRow(kindBridge, b.ID, "", "")
	if create {
		b.Slug = slug
		if err := p.tx.Create(&b).Error; errors.Is(err, gorm.ErrDuplicatedKey) {
			p.failf(path, "another bridge already listens on %s port %d there", b.Protocol, b.ListenPort)
			return
		} else if p.dbErr(err) {
			return
		}
		p.op("create", kindBridge, slug, nil)
	} else if p.write(kindBridge, b.ID, slug, diff(before, p.names.bridgeItem(b, newIP))) {
		b.UpdatedAt = p.a.now()
		if p.dbErr(p.tx.Select("*").Save(&b).Error) {
			return
		}
	}
	if !slices.Equal(newIP, curIP) && p.dbErr(setBridgeIPGroups(p.tx, b.ID, newIP)) {
		return
	}
}
