package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"main/database"
	"main/schema"
)

// maxSchemaBytes bounds a schema document.
const maxSchemaBytes = 4 << 20

var errRollback = errors.New("plan only")

func docHash(raw []byte) string {
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// readDocument reads and parses the request's document; on failure it
// answers 400 with the problems.
func readDocument(c *gin.Context) ([]byte, *schema.Document, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxSchemaBytes))
	if err != nil {
		abort(c, http.StatusRequestEntityTooLarge, "the document is too big (at most 4 MB)")
		return nil, nil, false
	}
	doc, err := schema.Parse(raw)
	if err != nil {
		schemaFail(c, err)
		return nil, nil, false
	}
	return raw, doc, true
}

// schemaFail answers 400 with every problem as {path, msg}.
func schemaFail(c *gin.Context, err error) {
	var errs schema.Errors
	if !errors.As(err, &errs) {
		errs = schema.Errors{{Msg: err.Error()}}
	}
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": errs[0].Error(), "errors": errs})
}

func (a *API) schemaState(db *gorm.DB) (*database.SchemaState, error) {
	return database.First[database.SchemaState](db.Where("id = 1"))
}

// schemaStatus is the last apply: {revision (0: never), appliedAt,
// appliedBy, docHash, strict (collections listed in full), override,
// versions}.
func (a *API) schemaStatus(c *gin.Context) {
	st, err := a.schemaState(a.db)
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	out := gin.H{
		"revision": 0, "appliedAt": nil, "appliedBy": "", "docHash": "", "strict": []string{},
		"override": a.overrideOut(),
		"versions": gin.H{"min": schema.MinVersion, "current": schema.CurrentVersion},
	}
	if st != nil {
		o := a.owned()
		strict := []string{}
		for _, coll := range []string{collDomains, collIPGroups, collEmailGroups, collDeployments, collBridges} {
			if o.strict(coll) {
				strict = append(strict, coll)
			}
		}
		out["revision"], out["appliedAt"], out["appliedBy"], out["docHash"], out["strict"] = st.Revision, st.AppliedAt, st.AppliedBy, st.DocHash, strict
	}
	c.JSON(http.StatusOK, out)
}

// planIn runs doc in a transaction and rolls it back.
func (a *API) planIn(raw []byte, doc *schema.Document) (*schemaPlanOut, schema.Errors, error) {
	var out *schemaPlanOut
	var errs schema.Errors
	hash := docHash(raw)
	err := a.db.Transaction(func(tx *gorm.DB) error {
		st, err := a.schemaState(tx)
		if err != nil {
			return err
		}
		p, err := a.runSchema(tx, doc, hash)
		if err != nil {
			return err
		}
		errs = p.errs
		base := 0
		if st != nil {
			base = st.Revision
		}
		out = p.result(base)
		out.Changed = len(out.Ops) > 0 || st == nil || st.DocHash != hash
		out.PlanHash = planHash(out, hash)
		return errRollback
	})
	if errors.Is(err, errRollback) {
		err = nil
	}
	return out, errs, err
}

// schemaPlan shows what applying the posted document would do.
func (a *API) schemaPlan(c *gin.Context) {
	raw, doc, ok := readDocument(c)
	if !ok {
		return
	}
	out, errs, err := a.planIn(raw, doc)
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	if len(errs) > 0 {
		schemaFail(c, errs)
		return
	}
	c.JSON(http.StatusOK, out)
}

// schemaApply applies the posted document, all or nothing. With a plan
// hash (X-Plan-Hash or ?planHash=) it applies only if the plan is still
// the same: a 409 with the new plan otherwise.
func (a *API) schemaApply(c *gin.Context) {
	raw, doc, ok := readDocument(c)
	if !ok {
		return
	}
	want := c.GetHeader("X-Plan-Hash")
	if want == "" {
		want = c.Query("planHash")
	}
	hash := docHash(raw)
	var out *schemaPlanOut
	var errs schema.Errors
	stale := false
	revision := 0
	err := a.db.Transaction(func(tx *gorm.DB) error {
		st, err := a.schemaState(tx)
		if err != nil {
			return err
		}
		p, err := a.runSchema(tx, doc, hash)
		if err != nil {
			return err
		}
		base := 0
		if st != nil {
			base = st.Revision
		}
		revision = base
		out = p.result(base)
		out.Changed = len(out.Ops) > 0 || st == nil || st.DocHash != hash
		out.PlanHash = planHash(out, hash)
		if errs = p.errs; len(errs) > 0 {
			return errRollback
		}
		if want != "" && want != out.PlanHash {
			stale = true
			return errRollback
		}
		if !out.Changed {
			return nil
		}
		if err := tx.Where("1 = 1").Delete(&database.SchemaOwned{}).Error; err != nil {
			return err
		}
		if len(p.own) > 0 {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(p.own, 200).Error; err != nil {
				return err
			}
		}
		revision = base + 1
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&database.SchemaState{
			ID: 1, Revision: revision, Document: string(raw), DocHash: hash, AppliedAt: a.now(), AppliedBy: actorOf(c),
		}).Error
	})
	switch {
	case errors.Is(err, errRollback) && stale:
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "something changed since the plan: review the new plan and apply again", "plan": out})
		return
	case errors.Is(err, errRollback):
		schemaFail(c, errs)
		return
	case err != nil:
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	if out.Changed {
		a.changed()
	}
	c.JSON(http.StatusOK, gin.H{"applied": out.Changed, "revision": revision, "plan": out})
}

// schemaExport writes the server as a document: by default the current
// state in full (no markers; applying it changes nothing), with
// ?source=applied the last applied document as it was sent (&upgrade=1
// converts it to the current version). ?format=json for JSON, else YAML.
func (a *API) schemaExport(c *gin.Context) {
	asJSON := c.Query("format") == "json"
	if c.Query("source") == "applied" {
		st, err := a.schemaState(a.db)
		if err != nil {
			abort(c, http.StatusInternalServerError, "server error")
			return
		}
		if st == nil {
			abort(c, http.StatusNotFound, "no schema has been applied yet")
			return
		}
		if c.Query("upgrade") == "" && !asJSON {
			c.Data(http.StatusOK, "application/yaml; charset=utf-8", []byte(st.Document))
			return
		}
		doc, err := schema.Parse([]byte(st.Document))
		if err != nil {
			abort(c, http.StatusInternalServerError, "the applied document no longer parses: "+err.Error())
			return
		}
		writeDocument(c, doc, asJSON)
		return
	}
	doc, err := exportDocument(a.db)
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	writeDocument(c, doc, asJSON)
}

func writeDocument(c *gin.Context, doc *schema.Document, asJSON bool) {
	if asJSON {
		b, err := doc.JSON()
		if err != nil {
			abort(c, http.StatusInternalServerError, "server error")
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", b)
		return
	}
	b, err := doc.YAML()
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	c.Data(http.StatusOK, "application/yaml; charset=utf-8", b)
}

// schemaJSONSchema describes the document format, for editors.
func (a *API) schemaJSONSchema(c *gin.Context) {
	if v := c.Query("version"); v != "" && v != strconv.Itoa(schema.CurrentVersion) {
		abort(c, http.StatusNotFound, "this server describes version "+strconv.Itoa(schema.CurrentVersion)+" only")
		return
	}
	c.JSON(http.StatusOK, schema.JSONSchema())
}
