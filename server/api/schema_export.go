package api

import (
	"encoding/json"
	"sort"
	"strings"

	"gorm.io/gorm"

	"main/database"
	"main/schema"
)

// This file turns database rows into schema document items. Plan diffs
// them, the lock guards compare them, and export writes them out, so all
// three agree on what each field's value is.

func ptr[T any](v T) *T { return &v }

// schemaNames resolves the IDs rows hold into the names documents use.
type schemaNames struct {
	relaySlug   map[string]string // relay ID → slug
	relayID     map[string]string // slug → relay ID
	domains     []database.Domain
	domainLabel map[string]string // domain ID → label
	ipGroup     map[string]string // IP group ID → name
	emailGroup  map[string]string // email group ID → name
}

func loadNames(db *gorm.DB) (*schemaNames, error) {
	n := &schemaNames{relaySlug: map[string]string{}, relayID: map[string]string{}, domainLabel: map[string]string{},
		ipGroup: map[string]string{}, emailGroup: map[string]string{}}
	var relays []database.Relay
	if err := db.Select("id", "slug").Find(&relays).Error; err != nil {
		return nil, err
	}
	for _, r := range relays {
		n.relaySlug[r.ID] = r.Slug
		n.relayID[r.Slug] = r.ID
	}
	if err := db.Order("name").Find(&n.domains).Error; err != nil {
		return nil, err
	}
	for _, d := range n.domains {
		n.domainLabel[d.ID] = d.Label()
	}
	var ips []database.IPGroup
	if err := db.Select("id", "name").Find(&ips).Error; err != nil {
		return nil, err
	}
	for _, g := range ips {
		n.ipGroup[g.ID] = g.Name
	}
	var emails []database.EmailGroup
	if err := db.Select("id", "name").Find(&emails).Error; err != nil {
		return nil, err
	}
	for _, g := range emails {
		n.emailGroup[g.ID] = g.Name
	}
	return n, nil
}

func namesOf(ids []string, m map[string]string) *schema.List {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, m[id])
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return schema.NewList(out...)
}

// inferDomain is the domain a hostname is on when the document doesn't
// say: an exact domain of that name, else the wildcard one level up.
func inferDomain(domains []database.Domain, host string) *database.Domain {
	for i, d := range domains {
		if !d.Wildcard && d.Name == host {
			return &domains[i]
		}
	}
	if _, rest, ok := strings.Cut(host, "."); ok {
		for i, d := range domains {
			if d.Wildcard && d.Name == rest {
				return &domains[i]
			}
		}
	}
	return nil
}

func passwordItem(hash string) *schema.Hash {
	if hash == "" {
		return nil
	}
	return &schema.Hash{Hash: hash}
}

// deploymentItem is d as a document item. rules nil leaves path_rules out.
func (n *schemaNames) deploymentItem(d database.Deployment, ipGroups, emailGroups []string, rules []database.PathRule) *schema.Deployment {
	item := &schema.Deployment{
		Relay: ptr(n.relaySlug[d.RelayID]), Protocol: ptr(d.Protocol),
		TargetHost: ptr(d.TargetHost), TargetPort: ptr(d.TargetPort),
		TLSSkipVerify: ptr(d.TLSSkipVerify), SNI: ptr(d.SNI), PreserveHost: ptr(d.PreserveHost),
		Enabled: ptr(d.Enabled), AuthMode: ptr(d.AuthMode),
		LoginPassword: passwordItem(d.LoginPassHash), AllowRootLogin: ptr(d.AllowRootLogin),
		SessionTTLS: ptr(d.SessionTTLS), BasicUser: ptr(d.BasicUser), BasicPassword: passwordItem(d.BasicPassHash),
		OTPEmails: schema.NewList(splitAllowlist(d.OTPEmails)...), EmailGroups: namesOf(emailGroups, n.emailGroup),
		IPAllowlist: schema.NewList(splitAllowlist(d.IPAllowlist)...), IPGroups: namesOf(ipGroups, n.ipGroup),
		SSHCLI: ptr(d.SSHCLI), SSHShell: ptr(d.SSHShell), SSHUser: ptr(d.SSHUser),
		RateLimitEnabled: ptr(d.RateLimitEnabled), RateLimitRequests: ptr(d.RateLimitRequests),
		RateLimitPeriodS: ptr(d.RateLimitPeriodS), RateLimitBurst: ptr(d.RateLimitBurst),
	}
	if d.DomainID != nil && d.Host != nil {
		if inf := inferDomain(n.domains, *d.Host); inf == nil || inf.ID != *d.DomainID {
			item.Domain = ptr(n.domainLabel[*d.DomainID])
		}
	}
	if rules != nil {
		item.PathRules = &schema.Map[schema.PathRule]{}
		for _, r := range rules {
			item.PathRules.Set(r.PathPrefix, pathRuleItem(r))
		}
		item.PathRules.Sort()
	}
	return item
}

func pathRuleItem(r database.PathRule) *schema.PathRule {
	return &schema.PathRule{IPAllowlist: schema.NewList(splitAllowlist(r.IPAllowlist)...)}
}

func groupItem(entries string) *schema.Group {
	return &schema.Group{Entries: schema.NewList(splitAllowlist(entries)...)}
}

func (n *schemaNames) bridgeItem(b database.Bridge, ipGroups []string) *schema.Bridge {
	item := &schema.Bridge{
		Name: ptr(b.Name), Protocol: ptr(b.Protocol), Enabled: ptr(b.Enabled), Public: ptr(b.Public()),
		ListenPort: ptr(b.ListenPort), ToRelay: ptr(n.relaySlug[b.ToRelayID]),
		TargetHost: ptr(b.TargetHost), TargetPort: ptr(b.TargetPort), UDPIdleS: ptr(b.UDPIdleS),
		IPAllowlist: schema.NewList(splitAllowlist(b.IPAllowlist)...), IPGroups: namesOf(ipGroups, n.ipGroup),
	}
	if b.FromRelayID != nil {
		item.FromRelay = ptr(n.relaySlug[*b.FromRelayID])
		item.ListenHost = ptr(b.ListenHost)
	}
	if b.DomainID != nil {
		item.Domain = ptr(n.domainLabel[*b.DomainID])
		if b.Subdomain != "" {
			item.Subdomain = ptr(b.Subdomain)
		}
	}
	return item
}

// fieldsOf is an item's fields as JSON, by name; a field that's left out
// (nil) is missing.
func fieldsOf(item any) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	if item == nil {
		return out
	}
	b, err := json.Marshal(item)
	if err == nil {
		_ = json.Unmarshal(b, &out)
	}
	delete(out, "ui")
	return out
}

func listOf(raw json.RawMessage) []string {
	var out []string
	_ = json.Unmarshal(raw, &out)
	return out
}

// exportDocument is the database as a document: every collection in
// full, concrete values, no markers. Applying it to the same server
// changes nothing.
func exportDocument(db *gorm.DB) (*schema.Document, error) {
	n, err := loadNames(db)
	if err != nil {
		return nil, err
	}
	doc := &schema.Document{Version: schema.CurrentVersion, Domains: schema.NewList(),
		Groups:      &schema.Groups{IP: &schema.Map[schema.Group]{}, Email: &schema.Map[schema.Group]{}},
		Deployments: &schema.Map[schema.Deployment]{}, Bridges: &schema.Map[schema.Bridge]{}}
	for _, d := range n.domains {
		doc.Domains.Items = append(doc.Domains.Items, d.Label())
	}
	var ips []database.IPGroup
	if err := db.Find(&ips).Error; err != nil {
		return nil, err
	}
	for _, g := range ips {
		doc.Groups.IP.Set(g.Name, groupItem(g.Entries))
	}
	var emails []database.EmailGroup
	if err := db.Find(&emails).Error; err != nil {
		return nil, err
	}
	for _, g := range emails {
		doc.Groups.Email.Set(g.Name, groupItem(g.Entries))
	}
	doc.Groups.IP.Sort()
	doc.Groups.Email.Sort()

	var ds []database.Deployment
	if err := db.Where("host IS NOT NULL").Find(&ds).Error; err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(ds))
	for _, d := range ds {
		ids = append(ids, d.ID)
	}
	ipg := groupIDsIn(db, &database.DeploymentIPGroup{}, "deployment_id", "ip_group_id", "ip_groups", ids...)
	emg := groupIDsIn(db, &database.DeploymentEmailGroup{}, "deployment_id", "email_group_id", "email_groups", ids...)
	var rules []database.PathRule
	if err := db.Where("deployment_id IN ?", ids).Find(&rules).Error; err != nil {
		return nil, err
	}
	byDep := map[string][]database.PathRule{}
	for _, r := range rules {
		byDep[r.DeploymentID] = append(byDep[r.DeploymentID], r)
	}
	for _, d := range ds {
		rs := byDep[d.ID]
		if rs == nil {
			rs = []database.PathRule{}
		}
		doc.Deployments.Set(*d.Host, n.deploymentItem(d, ipg[d.ID], emg[d.ID], rs))
	}
	doc.Deployments.Sort()

	var bs []database.Bridge
	if err := db.Find(&bs).Error; err != nil {
		return nil, err
	}
	bids := make([]string, 0, len(bs))
	for _, b := range bs {
		bids = append(bids, b.ID)
	}
	bg := groupIDsIn(db, &database.BridgeIPGroup{}, "bridge_id", "ip_group_id", "ip_groups", bids...)
	for _, b := range bs {
		doc.Bridges.Set(b.Slug, n.bridgeItem(b, bg[b.ID]))
	}
	doc.Bridges.Sort()
	return doc, nil
}
