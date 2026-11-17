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
	"main/tunnel"
)

// Nix services in the schema: nix_services is keyed by slug, and each
// one's relays map (keyed by relay slug) is its applied services, handled
// like a deployment's path_rules: listed in full unless "...": true.
//
// flake_lock is only the schema's when the document gives it. Left out,
// the lock is the relays' (the first to build makes it), so an apply
// never clears it.

func nonEmpty[T any](v []T) *[]T {
	if len(v) == 0 {
		return nil
	}
	return &v
}

func nonEmptyMap(m map[string]string) *map[string]string {
	if len(m) == 0 {
		return nil
	}
	return &m
}

// nixServiceItem is s as a document item, without its relays; withLock
// includes flake_lock (when there is one).
func nixServiceItem(s database.NixService, withLock bool) *schema.NixService {
	files := jsonMap(s.Files)
	flake := files["flake.nix"]
	delete(files, "flake.nix")
	item := &schema.NixService{
		Name: ptr(s.Name), Run: ptr(s.Run), Attr: ptr(s.Attr), Command: nonEmpty(jsonList(s.Command)),
		Env: nonEmptyMap(jsonMap(s.Env)), RelayEnv: nonEmpty(lines(s.RelayEnv)), Restart: ptr(s.Restart),
		Teardown: &schema.NixTeardown{RemoveState: ptr(s.RemoveState)},
		Flake:    &schema.Text{Content: flake}, Files: nonEmptyMap(files),
	}
	if withLock && s.FlakeLock != "" {
		item.FlakeLock = &schema.Text{Content: s.FlakeLock}
	}
	return item
}

func appliedItem(ap database.AppliedNixService) *schema.AppliedNixService {
	return &schema.AppliedNixService{Env: nonEmptyMap(jsonMap(ap.Env)), Desired: ptr(ap.Desired)}
}

// warn notes something the plan goes ahead with but that won't work yet.
func (p *planner) warn(path, msg string) {
	p.warnings = append(p.warnings, schema.Error{Path: path, Msg: msg})
}

func (p *planner) deleteUndeclaredNixServices() {
	m := p.doc.NixServices
	if m == nil {
		return
	}
	p.collection(collNixServices, m.Spread)
	var all []database.NixService
	if p.dbErr(p.tx.Find(&all).Error) {
		return
	}
	for _, s := range all {
		if m.Get(s.Slug) != nil || (m.Spread && !p.old.owns(kindNixService, s.ID)) {
			continue
		}
		if p.dbErr(p.a.removeNixService(p.tx, &s)) {
			return
		}
		p.op("delete", kindNixService, s.Slug, nil)
	}
}

func (p *planner) nixServices() {
	m := p.doc.NixServices
	if m == nil {
		return
	}
	for _, key := range m.Keys {
		p.nixService(key, "nix_services."+key, m.Items[key])
		if p.internal != nil {
			return
		}
	}
}

func (p *planner) nixService(slug, path string, sn *schema.NixService) {
	if !database.ValidSlug(slug) {
		p.failf(path, "Nix service keys are slugs: 1-63 lowercase letters, digits and dashes")
		return
	}
	existing, err := database.First[database.NixService](p.tx.Where("slug = ?", slug))
	if p.dbErr(err) {
		return
	}
	create := existing == nil
	s := p.a.newNixService()
	if !create {
		s = *existing
	}
	// The lock is in the diff when the schema has it, now or before.
	withLock := sn.FlakeLock != nil || p.old.ownsField(kindNixService, s.ID, "flake_lock")
	before := nixServiceItem(s, withLock)
	fc := p.fields(kindNixService, s.ID, sn, sn.UI, create)
	in := nixServiceInput{}
	value(fc, "name", sn.Name, &slug, &in.Name)
	value(fc, "run", sn.Run, ptr(tunnel.NixRunPackage), &in.Run)
	value(fc, "attr", sn.Attr, ptr("default"), &in.Attr)
	value(fc, "command", sn.Command, &[]string{}, &in.Command)
	value(fc, "env", sn.Env, &map[string]string{}, &in.Env)
	value(fc, "relay_env", sn.RelayEnv, &[]string{}, &in.RelayEnv)
	value(fc, "restart", sn.Restart, ptr("always"), &in.Restart)
	var td *schema.NixTeardown
	value(fc, "teardown", sn.Teardown, &schema.NixTeardown{}, &td)
	if td != nil {
		in.RemoveState = td.RemoveState
		if in.RemoveState == nil {
			in.RemoveState = ptr(true)
		}
	}

	files := jsonMap(s.Files)
	filesChanged := false
	if fc.take("flake", sn.Flake != nil) {
		if sn.Flake == nil || strings.TrimSpace(sn.Flake.Content) == "" {
			p.failf(path+".flake", "required: flake.nix, inline (or source: a directory, with supermatrix-admin)")
		} else {
			files["flake.nix"] = sn.Flake.Content
			filesChanged = true
		}
	}
	if fc.take("files", sn.Files != nil) {
		flake := files["flake.nix"]
		files = map[string]string{"flake.nix": flake}
		if sn.Files != nil {
			for f, content := range *sn.Files {
				if f == "flake.nix" || f == "flake.lock" {
					p.failf(path+".files."+f, "give it as %s instead", strings.ReplaceAll(strings.TrimSuffix(f, ".nix"), ".", "_"))
					continue
				}
				files[f] = content
			}
		}
		filesChanged = true
	}
	if filesChanged {
		in.Files = &files
	}
	if sn.FlakeLock != nil && (fc.managed("flake_lock") || create) {
		if fc.managed("flake_lock") {
			p.ownRow(kindNixService, s.ID, "flake_lock", "")
		}
		in.FlakeLock = &sn.FlakeLock.Content
	}
	if slices.ContainsFunc(p.errs, func(e schema.Error) bool { return strings.HasPrefix(e.Path, path) }) {
		return
	}
	if err := applyNixService(&s, in); err != nil {
		p.fail(path, err)
		return
	}
	if sn.FlakeLock == nil && p.old.ownsField(kindNixService, s.ID, "flake_lock") {
		// The schema let go of the lock: it stays, but the relays may
		// update it from now on.
		s.LockPinned = false
	}
	p.ownRow(kindNixService, s.ID, "", "")
	if create {
		s.Slug = slug
		if p.dbErr(p.tx.Create(&s).Error) {
			return
		}
		p.op("create", kindNixService, slug, nil)
	} else if changes := diff(before, nixServiceItem(s, withLock)); p.write(kindNixService, s.ID, slug, changes) || s.LockPinned != existing.LockPinned {
		s.UpdatedAt = p.a.now()
		if p.dbErr(p.tx.Select("*").Save(&s).Error) {
			return
		}
	}
	p.nixRelays(slug, path, &s, sn, fc)
}

// nixRelays applies service s to the relays its document item lists.
func (p *planner) nixRelays(slug, path string, s *database.NixService, sn *schema.NixService, fc *fieldCtx) {
	m := sn.Relays
	managed := fc.managed("relays")
	if !managed && !(fc.create && m != nil) {
		return
	}
	if m == nil {
		m = &schema.Map[schema.AppliedNixService]{}
	}
	if managed && !m.Spread {
		p.ownRow(kindNixService, s.ID, "relays", "")
	}
	var cur []database.AppliedNixService
	if p.dbErr(p.tx.Where("nix_service_id = ? AND removing = 0", s.ID).Find(&cur).Error) {
		return
	}
	declared := map[string]bool{}
	for _, k := range m.Keys {
		if id, ok := p.names.relayID[k]; ok {
			declared[id] = true
		}
	}
	if managed {
		for _, ap := range cur {
			if declared[ap.RelayID] || (m.Spread && !p.old.owns(kindAppliedNix, ap.ID)) {
				continue
			}
			if p.dbErr(p.a.markRemoving(p.tx, p.tx.Where("id = ?", ap.ID))) {
				return
			}
			p.op("delete", kindAppliedNix, slug+"@"+p.names.relaySlug[ap.RelayID], nil)
		}
	}
	for _, key := range m.Keys {
		at := path + ".relays." + key
		item := m.Items[key]
		relayID, ok := p.names.relayID[key]
		if !ok {
			p.failf(at, "no relay with the slug %q", key)
			continue
		}
		r, err := database.First[database.Relay](p.tx.Where("id = ?", relayID))
		if p.dbErr(err) {
			return
		}
		idx := slices.IndexFunc(cur, func(ap database.AppliedNixService) bool { return ap.RelayID == relayID })
		ap := p.a.newApplied(*s, relayID)
		if idx >= 0 {
			ap = cur[idx]
		}
		rd := nixReadiness(*r)
		if idx < 0 && !rd.Ready {
			// Putting it on a relay that can't run it would only fail there.
			p.failf(at, "%s", rd.Reason)
			continue
		}
		if !rd.Ready {
			p.warn(at, rd.Reason)
		}
		before := appliedItem(ap)
		afc := p.fields(kindAppliedNix, ap.ID, item, item.UI, idx < 0)
		afc.unmanaged = !managed
		in := appliedInput{}
		value(afc, "env", item.Env, &map[string]string{}, &in.Env)
		value(afc, "desired", item.Desired, ptr(tunnel.NixRunning), &in.Desired)
		if err := applyApplied(&ap, in); err != nil {
			p.fail(at, err)
			continue
		}
		if managed {
			p.ownRow(kindAppliedNix, ap.ID, "", "")
		}
		opKey := slug + "@" + key
		switch {
		case idx < 0:
			err := p.tx.Create(&ap).Error
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				p.failf(at, "%s is already applied to %s", slug, key)
				continue
			}
			if p.dbErr(err) {
				return
			}
			p.op("create", kindAppliedNix, opKey, nil)
		case !managed:
		case p.write(kindAppliedNix, ap.ID, opKey, diff(before, appliedItem(ap))):
			ap.UpdatedAt = p.a.now()
			if p.dbErr(p.tx.Select("desired", "env", "updated_at").Save(&ap).Error) {
				return
			}
		}
	}
	if p.dbErr(p.tx.Model(&database.AppliedNixService{}).Where("nix_service_id = ? AND removing = 0", s.ID).Update("slug", s.Slug).Error) {
		return
	}
}

// exportNixServices adds every Nix service, with its lock and relays.
func exportNixServices(db *gorm.DB, n *schemaNames, doc *schema.Document) error {
	doc.NixServices = &schema.Map[schema.NixService]{}
	var ss []database.NixService
	if err := db.Find(&ss).Error; err != nil {
		return err
	}
	var applied []database.AppliedNixService
	if err := db.Where("removing = 0 AND nix_service_id IS NOT NULL").Find(&applied).Error; err != nil {
		return err
	}
	for _, s := range ss {
		item := nixServiceItem(s, true)
		item.Relays = &schema.Map[schema.AppliedNixService]{}
		for _, ap := range applied {
			if *ap.NixServiceID == s.ID {
				item.Relays.Set(n.relaySlug[ap.RelayID], appliedItem(ap))
			}
		}
		item.Relays.Sort()
		doc.NixServices.Set(s.Slug, item)
	}
	doc.NixServices.Sort()
	return nil
}

// digestFiles shows a file (a JSON string) or a map of them by their
// sha256 and size, for plans.
func digestFiles(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	digest := func(s string) string {
		h := sha256.Sum256([]byte(s))
		return fmt.Sprintf("sha256:%s (%d bytes)", hex.EncodeToString(h[:6]), len(s))
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return json.RawMessage(mustJSON(digest(s)))
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) == nil {
		out := map[string]string{}
		for k, v := range m {
			out[k] = digest(v)
		}
		return json.RawMessage(mustJSON(out))
	}
	return raw
}
