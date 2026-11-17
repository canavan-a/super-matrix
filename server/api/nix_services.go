package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"

	"main/database"
	"main/tunnel"
)

// Nix services: a flake (inline) and how to run it, applied to relays whose
// machines run the Nix agent. The server keeps the definitions and pushes
// each relay the full set it should run (tunnel.NixService, the service
// with the relay's overrides merged in); the relay's agent builds and runs
// them and reports phases back. Removing one from a relay keeps its row
// (removing, phase tearing-down) until the relay says it's gone.

const (
	// maxNixBytes bounds a service's files and flake.lock together.
	maxNixBytes = 1 << 20
	maxNixFiles = 100
	// keepNixEvents is how many events are kept per applied service.
	keepNixEvents = 50
	// maxNixLog bounds the log tail kept with an event.
	maxNixLog = 64 << 10
)

var (
	envNameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	nixAttrRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_'-]*$`)
	nixFileSeg = regexp.MustCompile(`^[A-Za-z0-9_@+=,.-]+$`)
)

type nixServiceDTO struct {
	ID          string            `json:"id"`
	Slug        string            `json:"slug"`
	Name        string            `json:"name"`
	Run         string            `json:"run"`
	Attr        string            `json:"attr"`
	Command     []string          `json:"command"`
	Env         map[string]string `json:"env"`
	RelayEnv    []string          `json:"relayEnv"`
	Restart     string            `json:"restart"`
	RemoveState bool              `json:"removeState"`
	// Files is flake.nix and the rest by path; FlakeLock is flake.lock (""
	// until a relay makes one), LockPinned whether it was given.
	Files      map[string]string `json:"files"`
	FlakeLock  string            `json:"flakeLock"`
	LockPinned bool              `json:"lockPinned"`
	// PendingLock is a flake.lock a relay (PendingLockFrom, its name) made,
	// waiting to be accepted; "" for none.
	PendingLock     string    `json:"pendingLock"`
	PendingLockFrom string    `json:"pendingLockFrom"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	// Applied is where it runs (and is being removed from).
	Applied []appliedDTO `json:"applied"`
	Managed *managedDTO  `json:"managed"`
}

type appliedDTO struct {
	ID           string `json:"id"`
	NixServiceID string `json:"nixServiceId"`
	RelayID      string `json:"relayId"`
	RelaySlug    string `json:"relaySlug"`
	RelayName    string `json:"relayName"`
	Slug         string `json:"slug"`
	// Desired is running | stopped; Env the relay's overrides.
	Desired  string            `json:"desired"`
	Env      map[string]string `json:"env"`
	Removing bool              `json:"removing"`
	// Phase and the rest are what the relay last reported.
	Phase      string              `json:"phase"`
	PhaseSince time.Time           `json:"phaseSince"`
	Failing    bool                `json:"failing"`
	Serving    string              `json:"serving"`
	Previous   string              `json:"previous"`
	LastError  string              `json:"lastError"`
	MissingEnv []string            `json:"missingEnv"`
	Restarts   int                 `json:"restarts"`
	Progress   *tunnel.NixProgress `json:"progress"`
	// UpToDate: what's serving was built from the current definition.
	UpToDate bool        `json:"upToDate"`
	Online   bool        `json:"online"`
	Managed  *managedDTO `json:"managed"`
}

func jsonMap(s string) map[string]string {
	m := map[string]string{}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

func jsonList(s string) []string {
	l := []string{}
	_ = json.Unmarshal([]byte(s), &l)
	return l
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func lines(s string) []string {
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// nixFiles is a service's files by path (flake.lock is separate).
func nixFiles(s database.NixService) map[string]string { return jsonMap(s.Files) }

// nixSpec is applied service ap of s as its relay is told it.
func nixSpec(s database.NixService, ap database.AppliedNixService) tunnel.NixService {
	env := jsonMap(s.Env)
	for k, v := range jsonMap(ap.Env) {
		env[k] = v
	}
	return tunnel.NixService{
		ID: ap.ID, Slug: s.Slug, Run: s.Run, Attr: s.Attr, Command: jsonList(s.Command), Env: env,
		RelayEnv: lines(s.RelayEnv), Restart: s.Restart, RemoveState: s.RemoveState,
		Files: nixFiles(s), Lock: s.FlakeLock, LockPinned: s.LockPinned, Desired: ap.Desired,
	}
}

// nixRelays is relay ID → relay, for the DTOs.
func (a *API) nixRelays(db *gorm.DB) map[string]database.Relay {
	var rs []database.Relay
	db.Find(&rs)
	out := make(map[string]database.Relay, len(rs))
	for _, r := range rs {
		out[r.ID] = r
	}
	return out
}

func (a *API) appliedOut(o *ownIndex, ap database.AppliedNixService, s *database.NixService, relays map[string]database.Relay, online map[string]tunnel.RelayState) appliedDTO {
	r := relays[ap.RelayID]
	_, on := online[ap.RelayID]
	out := appliedDTO{
		ID: ap.ID, RelayID: ap.RelayID, RelaySlug: r.Slug, RelayName: r.Name, Slug: ap.Slug,
		Desired: ap.Desired, Env: jsonMap(ap.Env), Removing: ap.Removing,
		Phase: ap.Phase, PhaseSince: ap.PhaseSince, Failing: tunnel.FailedPhase(ap.Phase),
		Serving: ap.Serving, Previous: ap.Previous, LastError: ap.LastError,
		MissingEnv: lines(ap.MissingEnv), Restarts: ap.Restarts, Online: on,
		Managed: o.managed(kindAppliedNix, ap.ID),
	}
	if ap.NixServiceID != nil {
		out.NixServiceID = *ap.NixServiceID
	}
	if s != nil && ap.Serving != "" {
		out.UpToDate = ap.BuildHash == nixSpec(*s, ap).BuildHash()
	}
	if p, ok := a.nixProgress.Load(ap.ID); ok && ap.Phase == tunnel.PhaseBuilding {
		out.Progress = p.(*tunnel.NixProgress)
	}
	return out
}

func (a *API) nixServiceOut(o *ownIndex, s database.NixService, applied []database.AppliedNixService, relays map[string]database.Relay, online map[string]tunnel.RelayState) nixServiceDTO {
	out := nixServiceDTO{
		ID: s.ID, Slug: s.Slug, Name: s.Name, Run: s.Run, Attr: s.Attr, Command: jsonList(s.Command),
		Env: jsonMap(s.Env), RelayEnv: lines(s.RelayEnv), Restart: s.Restart, RemoveState: s.RemoveState,
		Files: nixFiles(s), FlakeLock: s.FlakeLock, LockPinned: s.LockPinned,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Applied: []appliedDTO{},
		Managed: o.managed(kindNixService, s.ID),
	}
	if s.PendingLock != "" {
		out.PendingLock, out.PendingLockFrom = s.PendingLock, relays[s.PendingLockFrom].Name
	}
	for _, ap := range applied {
		out.Applied = append(out.Applied, a.appliedOut(o, ap, &s, relays, online))
	}
	sort.Slice(out.Applied, func(i, j int) bool { return out.Applied[i].RelayName < out.Applied[j].RelayName })
	return out
}

// nixServiceFull is a service with where it's applied, as the caller may
// see it: a relay only sees itself there (not the others' env).
func (a *API) nixServiceFull(c *gin.Context, s database.NixService) nixServiceDTO {
	var applied []database.AppliedNixService
	a.appliedScope(c).Where("nix_service_id = ?", s.ID).Find(&applied)
	return a.nixServiceOut(a.owned(), s, applied, a.nixRelays(a.db), a.hub.State())
}

func (a *API) findNixService(c *gin.Context) *database.NixService {
	s, err := database.First[database.NixService](a.db.Where("id = ? OR slug = ?", c.Param("id"), c.Param("id")))
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return nil
	}
	if s == nil {
		abort(c, http.StatusNotFound, "no such Nix service")
		return nil
	}
	return s
}

func (a *API) findApplied(c *gin.Context) *database.AppliedNixService {
	ap, err := database.First[database.AppliedNixService](a.appliedScope(c).Where("id = ?", c.Param("aid")))
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return nil
	}
	if ap == nil {
		abort(c, http.StatusNotFound, "no such applied Nix service")
		return nil
	}
	return ap
}

func (a *API) listNixServices(c *gin.Context) {
	var ss []database.NixService
	if err := a.db.Order("name").Find(&ss).Error; err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	var applied []database.AppliedNixService
	a.appliedScope(c).Where("nix_service_id IS NOT NULL").Find(&applied)
	byService := map[string][]database.AppliedNixService{}
	for _, ap := range applied {
		byService[*ap.NixServiceID] = append(byService[*ap.NixServiceID], ap)
	}
	o, relays, online := a.owned(), a.nixRelays(a.db), a.hub.State()
	out := make([]nixServiceDTO, 0, len(ss))
	for _, s := range ss {
		out = append(out, a.nixServiceOut(o, s, byService[s.ID], relays, online))
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) getNixService(c *gin.Context) {
	if s := a.findNixService(c); s != nil {
		c.JSON(http.StatusOK, a.nixServiceFull(c, *s))
	}
}

// listApplied is every applied Nix service, those being removed from a
// deleted service too (?relayId= narrows it to one relay).
func (a *API) listApplied(c *gin.Context) {
	q := a.appliedScope(c).Order("slug")
	if r := c.Query("relayId"); r != "" {
		q = q.Where("relay_id = ?", r)
	}
	var applied []database.AppliedNixService
	if err := q.Find(&applied).Error; err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	services := a.nixServicesByID(a.db)
	o, relays, online := a.owned(), a.nixRelays(a.db), a.hub.State()
	out := make([]appliedDTO, 0, len(applied))
	for _, ap := range applied {
		out = append(out, a.appliedOut(o, ap, services[ptrVal(ap.NixServiceID)], relays, online))
	}
	c.JSON(http.StatusOK, out)
}

func ptrVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (a *API) nixServicesByID(db *gorm.DB) map[string]*database.NixService {
	var ss []database.NixService
	db.Find(&ss)
	out := make(map[string]*database.NixService, len(ss))
	for i := range ss {
		out[ss[i].ID] = &ss[i]
	}
	return out
}

func (a *API) getApplied(c *gin.Context) {
	ap := a.findApplied(c)
	if ap == nil {
		return
	}
	var s *database.NixService
	if ap.NixServiceID != nil {
		s, _ = database.First[database.NixService](a.db.Where("id = ?", *ap.NixServiceID))
	}
	c.JSON(http.StatusOK, a.appliedOut(a.owned(), *ap, s, a.nixRelays(a.db), a.hub.State()))
}

// nixServiceInput is the body of create (missing fields take defaults)
// and update (missing fields stay as they are).
type nixServiceInput struct {
	Name        *string            `json:"name"`
	Slug        *string            `json:"slug"`
	Run         *string            `json:"run"`
	Attr        *string            `json:"attr"`
	Command     *[]string          `json:"command"`
	Env         *map[string]string `json:"env"`
	RelayEnv    *[]string          `json:"relayEnv"`
	Restart     *string            `json:"restart"`
	RemoveState *bool              `json:"removeState"`
	// Files replaces the files (flake.nix required); a flake.lock among
	// them is taken as FlakeLock.
	Files *map[string]string `json:"files"`
	// FlakeLock pins the lock; "" unpins it, and the next relay to build
	// makes a fresh one (updating every input).
	FlakeLock *string `json:"flakeLock"`
}

func cleanEnv(env map[string]string, what string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range env {
		k = strings.TrimSpace(k)
		if !envNameRe.MatchString(k) {
			return nil, inputError(fmt.Sprintf("%s: %q isn't a variable name (letters, digits and _, not starting with a digit)", what, k))
		}
		if strings.ContainsRune(v, 0) || strings.ContainsAny(v, "\n\r") {
			return nil, inputError(fmt.Sprintf("%s: %s can't contain a line break", what, k))
		}
		out[k] = v
	}
	return out, nil
}

// cleanNixPath checks a file's path: relative, inside the flake's
// directory, plain names.
func cleanNixPath(p string) (string, error) {
	p = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p), "./"))
	if p == "" || strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return "", inputError(fmt.Sprintf("file %q: give a plain relative path, like config/app.json", p))
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." || seg == ".git" || !nixFileSeg.MatchString(seg) {
			return "", inputError(fmt.Sprintf("file %q: names can have letters, digits and _@+=,.- (no .., no .git)", p))
		}
	}
	return p, nil
}

// applyNixService copies the set fields of in onto s and checks the
// result.
func applyNixService(s *database.NixService, in nixServiceInput) error {
	if in.Name != nil {
		s.Name = cleanName(*in.Name)
	}
	if in.Run != nil {
		s.Run = strings.ToLower(strings.TrimSpace(*in.Run))
	}
	if in.Attr != nil {
		s.Attr = strings.TrimSpace(*in.Attr)
		if s.Attr == "" {
			s.Attr = "default"
		}
	}
	if in.Command != nil {
		cmd := []string{}
		for _, a := range *in.Command {
			if strings.ContainsRune(a, 0) || strings.ContainsAny(a, "\n\r") {
				return inputError("command arguments can't contain line breaks")
			}
			cmd = append(cmd, a)
		}
		if len(cmd) > 0 && strings.TrimSpace(cmd[0]) == "" {
			return inputError("the command's program is empty")
		}
		if len(cmd) > 64 {
			return inputError("the command has more than 64 arguments")
		}
		s.Command = mustJSON(cmd)
	}
	if in.Env != nil {
		env, err := cleanEnv(*in.Env, "env")
		if err != nil {
			return err
		}
		s.Env = mustJSON(env)
	}
	if in.RelayEnv != nil {
		var names []string
		for _, n := range *in.RelayEnv {
			if n = strings.TrimSpace(n); n == "" {
				continue
			}
			if !envNameRe.MatchString(n) {
				return inputError(fmt.Sprintf("relay env: %q isn't a variable name", n))
			}
			names = append(names, n)
		}
		sort.Strings(names)
		s.RelayEnv = strings.Join(names, "\n")
	}
	if in.Restart != nil {
		s.Restart = strings.TrimSpace(*in.Restart)
	}
	if in.RemoveState != nil {
		s.RemoveState = *in.RemoveState
	}
	if in.Files != nil {
		files := map[string]string{}
		for p, content := range *in.Files {
			cp, err := cleanNixPath(p)
			if err != nil {
				return err
			}
			if cp == "flake.lock" {
				if in.FlakeLock == nil {
					lock := content
					in.FlakeLock = &lock
				}
				continue
			}
			if _, dup := files[cp]; dup {
				return inputError(fmt.Sprintf("file %q is listed twice", cp))
			}
			files[cp] = content
		}
		s.Files = mustJSON(files)
	}
	if in.FlakeLock != nil {
		s.FlakeLock = *in.FlakeLock
		s.LockPinned = strings.TrimSpace(s.FlakeLock) != ""
		if !s.LockPinned {
			s.FlakeLock = ""
		}
	}

	files := jsonMap(s.Files)
	size := len(s.FlakeLock)
	for p, c := range files {
		size += len(p) + len(c)
	}
	switch {
	case s.Name == "":
		return inputError("name required")
	case s.Run != tunnel.NixRunPackage && s.Run != tunnel.NixRunApp && s.Run != tunnel.NixRunDevShell:
		return inputError(`run must be "package", "app" or "devshell"`)
	case !nixAttrRe.MatchString(s.Attr):
		return inputError("attr is an output's name, like default")
	case s.Restart != "always" && s.Restart != "on-failure" && s.Restart != "no":
		return inputError(`restart must be "always", "on-failure" or "no"`)
	case strings.TrimSpace(files["flake.nix"]) == "":
		return inputError("flake.nix required")
	case len(files) > maxNixFiles:
		return inputError(fmt.Sprintf("at most %d files", maxNixFiles))
	case size > maxNixBytes:
		return inputError("the files and flake.lock come to more than 1 MiB")
	case s.Run == tunnel.NixRunDevShell && len(jsonList(s.Command)) == 0:
		return inputError("a devshell service needs a command to run in the shell")
	}
	if s.FlakeLock != "" && !json.Valid([]byte(s.FlakeLock)) {
		return inputError("flake.lock isn't valid JSON")
	}
	return nil
}

// newNixService is a Nix service with every default a new one starts with.
func (a *API) newNixService() database.NixService {
	now := a.now()
	return database.NixService{ID: database.NewID(), Run: tunnel.NixRunPackage, Attr: "default", Command: "[]",
		Env: "{}", Restart: "always", RemoveState: true, Files: "{}", CreatedAt: now, UpdatedAt: now}
}

func (a *API) createNixService(c *gin.Context) {
	var in nixServiceInput
	if err := c.ShouldBindJSON(&in); err != nil {
		abort(c, http.StatusBadRequest, "bad json")
		return
	}
	if !a.schemaAllowsCreate(c, collNixServices) {
		return
	}
	// One a relay defines is like any other: it can run on every relay.
	s := a.newNixService()
	if in.Name == nil && in.Slug != nil {
		in.Name = in.Slug
	}
	if err := applyNixService(&s, in); err != nil {
		a.inputFail(c, err)
		return
	}
	err := a.db.Transaction(func(tx *gorm.DB) error {
		slug, err := slugFor(tx, "nix_services", in.Slug, s.Name, "nix-service", s.ID, "")
		if err != nil {
			return err
		}
		s.Slug = slug
		return tx.Create(&s).Error
	})
	if err != nil {
		a.writeFail(c, err, "nix_services", s.Slug, s.ID)
		return
	}
	a.nixChanged()
	c.JSON(http.StatusCreated, a.nixServiceFull(c, s))
}

func (a *API) updateNixService(c *gin.Context) {
	s := a.findNixService(c)
	if s == nil {
		return
	}
	var in nixServiceInput
	if err := c.ShouldBindJSON(&in); err != nil {
		abort(c, http.StatusBadRequest, "bad json")
		return
	}
	before := *s
	if err := applyNixService(s, in); err != nil {
		a.inputFail(c, err)
		return
	}
	after := *s
	if in.Slug != nil && strings.TrimSpace(*in.Slug) != "" {
		after.Slug = strings.ToLower(strings.TrimSpace(*in.Slug))
	}
	if !a.schemaAllowsUpdate(c, kindNixService, s.ID, before.Slug, after.Slug, nixServiceItem(before, true), nixServiceItem(after, true)) {
		return
	}
	s.UpdatedAt = a.now()
	err := a.db.Transaction(func(tx *gorm.DB) error {
		slug, err := slugFor(tx, "nix_services", in.Slug, s.Name, "nix-service", s.ID, s.Slug)
		if err != nil {
			return err
		}
		s.Slug = slug
		if err := tx.Select("*").Save(s).Error; err != nil {
			return err
		}
		return tx.Model(&database.AppliedNixService{}).Where("nix_service_id = ? AND removing = 0", s.ID).Update("slug", s.Slug).Error
	})
	if err != nil {
		a.writeFail(c, err, "nix_services", s.Slug, s.ID)
		return
	}
	a.nixChanged()
	c.JSON(http.StatusOK, a.nixServiceFull(c, *s))
}

// relockNixService drops the service's flake.lock (pinned or not): the
// next relay to build it locks every input afresh, and the others follow.
func (a *API) relockNixService(c *gin.Context) {
	s := a.findNixService(c)
	if s == nil {
		return
	}
	after := *s
	after.FlakeLock, after.LockPinned = "", false
	if !a.schemaAllowsUpdate(c, kindNixService, s.ID, s.Slug, s.Slug, nixServiceItem(*s, true), nixServiceItem(after, true)) {
		return
	}
	a.db.Model(s).Updates(map[string]any{"flake_lock": "", "lock_pinned": false, "pending_lock": "", "pending_lock_from": "", "updated_at": a.now()})
	s.FlakeLock, s.LockPinned, s.PendingLock, s.PendingLockFrom = "", false, "", ""
	a.nixChanged()
	c.JSON(http.StatusOK, a.nixServiceFull(c, *s))
}

func (a *API) deleteNixService(c *gin.Context) {
	s := a.findNixService(c)
	if s == nil {
		return
	}
	if !a.schemaAllowsDelete(c, kindNixService, s.ID, s.Slug) {
		return
	}
	err := a.db.Transaction(func(tx *gorm.DB) error { return a.removeNixService(tx, s) })
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	a.nixChanged()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// removeNixService deletes s; the relays it runs on tear it down.
func (a *API) removeNixService(tx *gorm.DB, s *database.NixService) error {
	if err := a.markRemoving(tx, tx.Where("nix_service_id = ? AND removing = 0", s.ID)); err != nil {
		return err
	}
	return tx.Delete(s).Error
}

// markRemoving sets the applied services q finds to be torn down.
func (a *API) markRemoving(tx *gorm.DB, q *gorm.DB) error {
	now := a.now()
	return q.Model(&database.AppliedNixService{}).Updates(map[string]any{
		"removing": true, "phase": tunnel.PhaseTearingDown, "phase_since": now, "updated_at": now,
	}).Error
}

// appliedInput applies a service to a relay (relayId, on create only), or
// changes how it runs there.
type appliedInput struct {
	RelayID *string            `json:"relayId"`
	Desired *string            `json:"desired"`
	Env     *map[string]string `json:"env"`
}

func applyApplied(ap *database.AppliedNixService, in appliedInput) error {
	if in.Desired != nil {
		ap.Desired = strings.TrimSpace(*in.Desired)
	}
	if in.Env != nil {
		env, err := cleanEnv(*in.Env, "env")
		if err != nil {
			return err
		}
		ap.Env = mustJSON(env)
	}
	if ap.Desired != tunnel.NixRunning && ap.Desired != tunnel.NixStopped {
		return inputError(`desired must be "running" or "stopped"`)
	}
	return nil
}

// nixRelayOK checks that Nix services can go on relay r (see nix_ready.go).
func nixRelayOK(r *database.Relay) error {
	if r == nil {
		return inputError("no such relay")
	}
	if rd := nixReadiness(*r); !rd.Ready {
		return conflictError(rd.Reason)
	}
	return nil
}

func (a *API) newApplied(s database.NixService, relayID string) database.AppliedNixService {
	now := a.now()
	return database.AppliedNixService{ID: database.NewID(), NixServiceID: &s.ID, RelayID: relayID, Slug: s.Slug,
		Desired: tunnel.NixRunning, Env: "{}", Phase: tunnel.PhasePending, PhaseSince: now, CreatedAt: now, UpdatedAt: now}
}

func (a *API) createApplied(c *gin.Context) {
	s := a.findNixService(c)
	if s == nil {
		return
	}
	var in appliedInput
	// No body is fine: a relay applying to itself has nothing to say.
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&in); err != nil {
			abort(c, http.StatusBadRequest, "bad json")
			return
		}
	}
	if self := relayOf(c); self != nil {
		// A relay applies services to itself, and only to itself.
		if in.RelayID != nil && *in.RelayID != self.ID && *in.RelayID != self.Slug {
			abort(c, http.StatusForbidden, "a relay can only run Nix services on itself")
			return
		}
		in.RelayID = &self.ID
	}
	if in.RelayID == nil {
		abort(c, http.StatusBadRequest, "relayId required")
		return
	}
	if o, ok := a.guardIndex(c); !ok {
		return
	} else if o != nil && o.ownsField(kindNixService, s.ID, "relays") {
		abort(c, http.StatusConflict, lockedMsg("the relays "+s.Slug+" runs on", o.revision))
		return
	}
	r, err := database.First[database.Relay](a.db.Where("id = ? OR slug = ?", *in.RelayID, *in.RelayID))
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	if err := nixRelayOK(r); err != nil {
		a.bridgeFail(c, err)
		return
	}
	ap := a.newApplied(*s, r.ID)
	if err := applyApplied(&ap, in); err != nil {
		a.inputFail(c, err)
		return
	}
	if err := a.db.Create(&ap).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			abort(c, http.StatusConflict, s.Slug+" is already applied to "+r.Name)
			return
		}
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	a.nixEvent(a.db, ap.ID, "applied", "applied to "+r.Name, "")
	a.nixChanged()
	c.JSON(http.StatusCreated, a.appliedOut(a.owned(), ap, s, a.nixRelays(a.db), a.hub.State()))
}

func (a *API) updateApplied(c *gin.Context) {
	ap := a.findApplied(c)
	if ap == nil {
		return
	}
	if ap.Removing {
		abort(c, http.StatusConflict, "it's being removed from this relay")
		return
	}
	var in appliedInput
	if err := c.ShouldBindJSON(&in); err != nil {
		abort(c, http.StatusBadRequest, "bad json")
		return
	}
	before := *ap
	if err := applyApplied(ap, in); err != nil {
		a.inputFail(c, err)
		return
	}
	n, err := loadNames(a.db)
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	key := n.relaySlug[ap.RelayID]
	if !a.schemaAllowsUpdate(c, kindAppliedNix, ap.ID, key, key, appliedItem(before), appliedItem(*ap)) {
		return
	}
	ap.UpdatedAt = a.now()
	if err := a.db.Select("desired", "env", "updated_at").Save(ap).Error; err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	if before.Desired != ap.Desired {
		a.nixEvent(a.db, ap.ID, "desired", "set to "+ap.Desired, "")
	}
	a.nixChanged()
	var s *database.NixService
	if ap.NixServiceID != nil {
		s, _ = database.First[database.NixService](a.db.Where("id = ?", *ap.NixServiceID))
	}
	c.JSON(http.StatusOK, a.appliedOut(a.owned(), *ap, s, a.nixRelays(a.db), a.hub.State()))
}

// deleteApplied removes a service from a relay: the relay tears it down,
// and the row goes once it says it's done. Deleting one that's already
// being removed forgets it now (the relay is gone for good, say).
func (a *API) deleteApplied(c *gin.Context) {
	ap := a.findApplied(c)
	if ap == nil {
		return
	}
	if ap.Removing {
		a.db.Delete(ap)
		a.watchers.send(gin.H{"type": "changed"})
		c.JSON(http.StatusOK, gin.H{"ok": true, "forgotten": true})
		return
	}
	if o, ok := a.guardIndex(c); !ok {
		return
	} else if o != nil && (o.owns(kindAppliedNix, ap.ID) || (ap.NixServiceID != nil && o.ownsField(kindNixService, *ap.NixServiceID, "relays"))) {
		abort(c, http.StatusConflict, lockedMsg(ap.Slug+" on this relay", o.revision))
		return
	}
	if err := a.markRemoving(a.db, a.db.Where("id = ?", ap.ID)); err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	a.nixEvent(a.db, ap.ID, tunnel.PhaseTearingDown, "removed from this relay", "")
	a.nixChanged()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// appliedAction sends the relay restart, rebuild or rollback.
func (a *API) appliedAction(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		ap := a.findApplied(c)
		if ap == nil {
			return
		}
		if ap.Removing {
			abort(c, http.StatusConflict, "it's being removed from this relay")
			return
		}
		if action == tunnel.NixRollback && ap.Previous == "" {
			abort(c, http.StatusConflict, "there's no previous build to roll back to")
			return
		}
		if err := a.hub.SendNixAction(ap.RelayID, tunnel.NixAction{ID: ap.ID, Action: action}); err != nil {
			abort(c, http.StatusConflict, nixRelayErr(err))
			return
		}
		a.nixEvent(a.db, ap.ID, action, action+" asked for", "")
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func nixRelayErr(err error) string {
	if errors.Is(err, tunnel.ErrOffline) {
		return "the relay is offline"
	}
	return err.Error()
}

type nixEventDTO struct {
	ID      int64     `json:"id"`
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	Log     string    `json:"log"`
}

// appliedEvents is an applied service's history, newest first.
func (a *API) appliedEvents(c *gin.Context) {
	ap := a.findApplied(c)
	if ap == nil {
		return
	}
	var evs []database.NixServiceEvent
	a.db.Where("applied_id = ?", ap.ID).Order("id DESC").Limit(keepNixEvents).Find(&evs)
	out := make([]nixEventDTO, 0, len(evs))
	for _, e := range evs {
		out = append(out, nixEventDTO{ID: e.ID, At: e.At, Kind: e.Kind, Message: e.Message, Log: e.Log})
	}
	c.JSON(http.StatusOK, out)
}

// nixEvent records an event and trims the history.
func (a *API) nixEvent(db *gorm.DB, appliedID, kind, msg, log string) {
	if len(log) > maxNixLog {
		log = log[len(log)-maxNixLog:]
	}
	db.Create(&database.NixServiceEvent{AppliedID: appliedID, At: a.now(), Kind: kind, Message: clip(msg, 2000), Log: log})
	db.Exec(`DELETE FROM nix_service_events WHERE applied_id = ? AND id NOT IN
		(SELECT id FROM nix_service_events WHERE applied_id = ? ORDER BY id DESC LIMIT ?)`, appliedID, appliedID, keepNixEvents)
}

func logsRequest(c *gin.Context) tunnel.NixLogs {
	n, _ := strconv.Atoi(c.DefaultQuery("lines", "200"))
	n = max(1, min(n, 5000))
	return tunnel.NixLogs{Lines: n, Build: c.Query("build") == "1" || c.Query("build") == "latest"}
}

// appliedLogs is the journal's last lines (?lines=, default 200), or the
// last build's full log (?build=1), as text, read from the relay.
func (a *API) appliedLogs(c *gin.Context) {
	ap := a.findApplied(c)
	if ap == nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	st, err := a.hub.OpenNixLogs(ctx, ap.RelayID, ap.ID, logsRequest(c))
	if err != nil {
		abort(c, http.StatusConflict, nixRelayErr(err))
		return
	}
	defer st.Close()
	_ = st.SetReadDeadline(time.Now().Add(20 * time.Second))
	b, _ := io.ReadAll(io.LimitReader(st, 8<<20))
	c.Data(http.StatusOK, "text/plain; charset=utf-8", b)
}

// nixLogsWS follows an applied service's journal: a WebSocket (auth as
// /v1/ws: the first message is {"type":"auth","token":…}) that gets the
// last ?lines= lines, then each new one, as text messages.
func (a *API) nixLogsWS(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(4096)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var auth struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	if err := conn.ReadJSON(&auth); err != nil || auth.Type != "auth" || !a.validSession(auth.Token) {
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "unauthorized"))
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	ap, err := database.First[database.AppliedNixService](a.db.Where("id = ?", c.Param("aid")))
	if err != nil || ap == nil {
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4004, "no such applied Nix service"))
		return
	}
	req := logsRequest(c)
	req.Follow = !req.Build
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := a.hub.OpenNixLogs(ctx, ap.RelayID, ap.ID, req)
	if err != nil {
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4009, nixRelayErr(err)))
		return
	}
	defer st.Close()
	// The browser closing ends the stream.
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				st.Close()
				return
			}
		}
	}()
	buf := make([]byte, 32<<10)
	for {
		n, err := st.Read(buf)
		if n > 0 {
			_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if conn.WriteMessage(websocket.TextMessage, buf[:n]) != nil {
				return
			}
		}
		if err != nil {
			_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "end of log"))
			return
		}
	}
}

// --- the relays' side ---

// nixSets is the Nix services each of relayIDs should run, read in a few
// queries for all of them (a service's files are read once, however many
// relays run it).
func (a *API) nixSets(relayIDs []string) map[string][]tunnel.NixService {
	out := make(map[string][]tunnel.NixService, len(relayIDs))
	for _, id := range relayIDs {
		out[id] = []tunnel.NixService{}
	}
	if len(relayIDs) == 0 {
		return out
	}
	var allowed []string
	a.db.Model(&database.Relay{}).Where("id IN ? AND allow_nix_services = 1", relayIDs).Pluck("id", &allowed)
	if len(allowed) == 0 {
		return out
	}
	var applied []database.AppliedNixService
	a.db.Where("relay_id IN ? AND removing = 0 AND nix_service_id IS NOT NULL", allowed).Order("slug").Find(&applied)
	ids := map[string]bool{}
	for _, ap := range applied {
		ids[*ap.NixServiceID] = true
	}
	services := a.nixServicesIn(a.db, ids)
	for _, ap := range applied {
		if s := services[*ap.NixServiceID]; s != nil {
			out[ap.RelayID] = append(out[ap.RelayID], nixSpec(*s, ap))
		}
	}
	return out
}

// nixServicesIn is the services with ids, by ID.
func (a *API) nixServicesIn(db *gorm.DB, ids map[string]bool) map[string]*database.NixService {
	out := map[string]*database.NixService{}
	if len(ids) == 0 {
		return out
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	var ss []database.NixService
	db.Where("id IN ?", list).Find(&ss)
	for i := range ss {
		out[ss[i].ID] = &ss[i]
	}
	return out
}

// nixSetFor is the Nix services relay should run.
func (a *API) nixSetFor(relayID string) []tunnel.NixService {
	return a.nixSets([]string{relayID})[relayID]
}

// nixChanged pushes every online relay its Nix services and tells the
// app.
func (a *API) nixChanged() {
	online := a.hub.Online()
	for id, set := range a.nixSets(online) {
		a.hub.PushNix(id, set)
	}
	a.watchers.send(gin.H{"type": "changed"})
}

// onNixStatus takes phases a relay reports for its applied services.
func (a *API) onNixStatus(relayID string, status map[string]tunnel.NixStatus) {
	changed := false
	var updated []string
	for id, st := range status {
		ap, err := database.First[database.AppliedNixService](a.db.Where("id = ? AND relay_id = ?", id, relayID))
		if err != nil || ap == nil {
			continue // not this relay's, or forgotten
		}
		if st.Phase == tunnel.PhaseBuilding && st.Progress != nil {
			p := *st.Progress
			a.nixProgress.Store(id, &p)
		} else {
			a.nixProgress.Delete(id)
		}
		updated = append(updated, id)
		phaseChanged := st.Phase != "" && st.Phase != ap.Phase
		if !phaseChanged && st.Event == "" && st.Serving == ap.Serving && st.Error == ap.LastError {
			continue // progress only
		}
		changed = true
		if ap.Removing && st.Phase == tunnel.PhaseRemoved {
			a.db.Delete(ap)
			a.nixProgress.Delete(id)
			continue
		}
		since := st.Since
		if since.IsZero() || !phaseChanged {
			since = ap.PhaseSince
			if phaseChanged {
				since = a.now()
			}
		}
		upd := map[string]any{
			"phase_since": since, "serving": clip(st.Serving, 500), "previous": clip(st.Previous, 500),
			"build_hash": clip(st.BuildHash, 100), "last_error": clip(st.Error, 2000),
			"missing_env": strings.Join(st.MissingEnv, "\n"), "restarts": st.Restarts, "updated_at": a.now(),
		}
		if st.Phase != "" {
			upd["phase"] = clip(st.Phase, 40)
		}
		a.db.Model(ap).Updates(upd)
		if phaseChanged || st.Event != "" {
			kind := st.Event
			if kind == "" {
				kind = st.Phase
			}
			msg := st.Error
			if msg == "" && len(st.MissingEnv) > 0 {
				msg = "not set on the relay: " + strings.Join(st.MissingEnv, ", ")
			}
			if msg == "" && len(st.Teardown) > 0 {
				msg = "failed: " + strings.Join(st.Teardown, "; ")
			}
			a.nixEvent(a.db, ap.ID, clip(kind, 40), msg, st.Log)
		}
	}
	if len(updated) > 0 {
		a.watchers.send(gin.H{"type": "nixStatus", "relayId": relayID, "applied": a.appliedByIDs(updated)})
	}
	if changed {
		a.watchers.send(gin.H{"type": "changed"})
	}
}

func (a *API) appliedByIDs(ids []string) []appliedDTO {
	var applied []database.AppliedNixService
	a.db.Where("id IN ?", ids).Find(&applied)
	// Only the services these belong to: this runs on every status report.
	want := map[string]bool{}
	for _, ap := range applied {
		if ap.NixServiceID != nil {
			want[*ap.NixServiceID] = true
		}
	}
	services := a.nixServicesIn(a.db, want)
	o, relays, online := a.owned(), a.nixRelays(a.db), a.hub.State()
	out := make([]appliedDTO, 0, len(applied))
	for _, ap := range applied {
		out = append(out, a.appliedOut(o, ap, services[ptrVal(ap.NixServiceID)], relays, online))
	}
	return out
}

// onNixKnown forgets applied services being removed from a relay whose
// agent doesn't have them (it never got them: nothing to tear down).
func (a *API) onNixKnown(relayID string, known []string) {
	q := a.db.Where("relay_id = ? AND removing = 1 AND phase <> ?", relayID, tunnel.PhaseRemovedWithErrors)
	if len(known) > 0 {
		q = q.Where("id NOT IN ?", known)
	}
	if res := q.Delete(&database.AppliedNixService{}); res.RowsAffected > 0 {
		a.watchers.send(gin.H{"type": "changed"})
	}
}

// onNixLock takes a flake.lock a relay made, if the service's lock isn't
// pinned and the relay built from the lock the server has now. A relay
// that manages Nix services (it could change the service anyway) and is
// the only one running it gets it taken at once. Otherwise it's held as
// pending until it's accepted, so the admin sees what the others would
// pick up: the first one waits, later ones are ignored until it's decided.
func (a *API) onNixLock(relayID string, l tunnel.NixLock) {
	ap, err := database.First[database.AppliedNixService](a.db.Where("id = ? AND relay_id = ? AND removing = 0", l.ID, relayID))
	if err != nil || ap == nil || ap.NixServiceID == nil {
		return
	}
	if len(l.Lock) > maxNixBytes || !json.Valid([]byte(l.Lock)) {
		return
	}
	// The base must be the current lock's hash (SQLite has no sha256, so
	// it's checked here); the update repeats the condition on the lock
	// itself, so of two relays racing only one wins.
	s, err := database.First[database.NixService](a.db.Where("id = ?", *ap.NixServiceID))
	if err != nil || s == nil || s.LockPinned || tunnel.HashLock(s.FlakeLock) != l.Base || s.FlakeLock == l.Lock {
		return
	}
	name, manages := relayID, false
	if r, _ := database.First[database.Relay](a.db.Where("id = ?", relayID)); r != nil {
		name, manages = r.Name, r.ManageNixServices
	}
	var others int64
	a.db.Model(&database.AppliedNixService{}).Where("nix_service_id = ? AND relay_id != ? AND removing = 0", s.ID, relayID).Count(&others)
	if manages && others == 0 {
		upd := a.db.Model(&database.NixService{}).Where("id = ? AND flake_lock = ? AND lock_pinned = 0", s.ID, s.FlakeLock).
			Updates(map[string]any{"flake_lock": l.Lock, "pending_lock": "", "pending_lock_from": "", "updated_at": a.now()})
		if upd.RowsAffected == 0 {
			return
		}
		msg := "flake.lock made on " + name
		if s.FlakeLock != "" {
			msg = "flake.lock updated on " + name + " (the flake's inputs changed)"
		}
		a.nixEvent(a.db, ap.ID, "locked", msg, "")
		a.nixChanged()
		return
	}
	upd := a.db.Model(&database.NixService{}).Where("id = ? AND flake_lock = ? AND lock_pinned = 0 AND pending_lock = ''", s.ID, s.FlakeLock).
		Updates(map[string]any{"pending_lock": l.Lock, "pending_lock_from": relayID, "updated_at": a.now()})
	if upd.RowsAffected > 0 {
		a.nixEvent(a.db, ap.ID, "lock-pending", "flake.lock from "+name+" is waiting for you to accept it", "")
		a.watchers.send(gin.H{"type": "changed"})
	}
}

func (a *API) nixRoutes(v1, admin *gin.RouterGroup) {
	admin.GET("/nix-services", a.listNixServices)
	admin.POST("/nix-services", a.createNixService)
	admin.GET("/nix-services/:id", a.getNixService)
	admin.PATCH("/nix-services/:id", a.updateNixService)
	admin.DELETE("/nix-services/:id", a.deleteNixService)
	admin.POST("/nix-services/:id/relock", a.relockNixService)
	admin.POST("/nix-services/:id/lock", a.nixLockDecision)
	admin.POST("/nix-services/:id/applied", a.createApplied)
	admin.GET("/applied-nix-services", a.listApplied)
	admin.GET("/applied-nix-services/:aid", a.getApplied)
	admin.PATCH("/applied-nix-services/:aid", a.updateApplied)
	admin.DELETE("/applied-nix-services/:aid", a.deleteApplied)
	admin.POST("/applied-nix-services/:aid/restart", a.appliedAction(tunnel.NixRestart))
	admin.POST("/applied-nix-services/:aid/rebuild", a.appliedAction(tunnel.NixRebuild))
	admin.POST("/applied-nix-services/:aid/rollback", a.appliedAction(tunnel.NixRollback))
	admin.GET("/applied-nix-services/:aid/events", a.appliedEvents)
	admin.GET("/applied-nix-services/:aid/logs", a.appliedLogs)
	v1.GET("/nix-logs/:aid", a.nixLogsWS)
}
