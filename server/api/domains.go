package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"main/database"
)

type domainDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Wildcard bool   `json:"wildcard"`
	// Label is how it's written: "*.example.com" or "status.foo.org".
	Label       string    `json:"label"`
	Deployments int64     `json:"deployments"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (a *API) domainOut(d database.Domain) domainDTO {
	var n int64
	a.db.Model(&database.Deployment{}).Where("domain_id = ?", d.ID).Count(&n)
	return domainDTO{ID: d.ID, Name: d.Name, Wildcard: d.Wildcard, Label: d.Label(), Deployments: n, CreatedAt: d.CreatedAt}
}

// parseDomain reads "*.example.com" (wildcard) or "status.foo.org" (exact).
func parseDomain(s string) (name string, wildcard bool, err error) {
	s = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
	if rest, ok := strings.CutPrefix(s, "*."); ok {
		s, wildcard = rest, true
	}
	if s == "" || len(s) > 240 {
		return "", false, inputError("enter a domain, like *.example.com or status.example.com")
	}
	for _, l := range strings.Split(s, ".") {
		if !labelRe.MatchString(l) {
			return "", false, inputError(`"` + s + `" isn't a domain name (letters, digits, dashes and dots; a leading *. for a wildcard)`)
		}
	}
	return s, wildcard, nil
}

func (a *API) listDomains(c *gin.Context) {
	var ds []database.Domain
	if err := a.db.Order("name").Find(&ds).Error; err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	out := make([]domainDTO, 0, len(ds))
	for _, d := range ds {
		out = append(out, a.domainOut(d))
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) createDomain(c *gin.Context) {
	var b struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&b); err != nil {
		abort(c, http.StatusBadRequest, "bad json")
		return
	}
	name, wildcard, err := parseDomain(b.Name)
	if err != nil {
		a.inputFail(c, err)
		return
	}
	if !wildcard && name == a.cfg.AdminHost {
		abort(c, http.StatusBadRequest, name+" is the admin host")
		return
	}
	// "*.foo.org" and "foo.org" can both be added: the wildcard's hosts
	// are always <label>.foo.org, never foo.org itself.
	if old, err := database.First[database.Domain](a.db.Where("name = ? AND wildcard = ?", name, wildcard)); err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	} else if old != nil {
		abort(c, http.StatusConflict, old.Label()+" is already added")
		return
	}
	d := database.Domain{ID: database.NewID(), Name: name, Wildcard: wildcard, CreatedAt: a.now()}
	if err := a.db.Create(&d).Error; err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	a.changed()
	c.JSON(http.StatusCreated, a.domainOut(d))
}

func (a *API) deleteDomain(c *gin.Context) {
	d, err := database.First[database.Domain](a.db.Where("id = ?", c.Param("id")))
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	if d == nil {
		abort(c, http.StatusNotFound, "no such domain")
		return
	}
	if a.domainOut(*d).Deployments > 0 {
		abort(c, http.StatusConflict, "move or delete the deployments on "+d.Label()+" first")
		return
	}
	a.db.Delete(d)
	a.changed()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// domainDNS checks that a domain points at this server: it resolves the
// domain (for a wildcard, a random name under it) and the admin host,
// which by definition points here, and compares the addresses.
func (a *API) domainDNS(c *gin.Context) {
	d, err := database.First[database.Domain](a.db.Where("id = ?", c.Param("id")))
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	if d == nil {
		abort(c, http.StatusNotFound, "no such domain")
		return
	}
	probe := d.Name
	if d.Wildcard {
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		probe = "sm-check-" + hex.EncodeToString(b) + "." + d.Name
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	want, werr := a.Lookup(ctx, a.cfg.AdminHost)
	got, gerr := a.Lookup(ctx, probe)
	slices.Sort(want)
	slices.Sort(got)
	res := gin.H{"probe": probe, "addresses": nonNilStrings(got), "expected": nonNilStrings(want), "ok": false}
	switch {
	case werr != nil:
		res["detail"] = "Couldn't resolve the admin host (" + a.cfg.AdminHost + ") to compare with: " + werr.Error()
	case gerr != nil:
		res["detail"] = probe + " doesn't resolve yet. Add the record below at your DNS provider; it can take a few minutes to show up."
	case !slices.ContainsFunc(got, func(ip string) bool { return slices.Contains(want, ip) }):
		res["detail"] = probe + " points at " + strings.Join(got, ", ") + ", not this server (" + strings.Join(want, ", ") + ")."
	default:
		res["ok"] = true
		res["detail"] = probe + " points at this server."
	}
	c.JSON(http.StatusOK, res)
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
