package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"main/database"
	"main/edge"
	"main/registry"
)

type tokenDTO struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
}

type deploymentDTO struct {
	ID               string     `json:"id"`
	RelayID          string     `json:"relayId"`
	DomainID         string     `json:"domainId"`
	Subdomain        string     `json:"subdomain"`
	Host             string     `json:"host"`
	URL              string     `json:"url"`
	Protocol         string     `json:"protocol"`
	TargetHost       string     `json:"targetHost"`
	TargetPort       int        `json:"targetPort"`
	TLSSkipVerify    bool       `json:"tlsSkipVerify"`
	SNI              string     `json:"sni"`
	PreserveHost     bool       `json:"preserveHost"`
	Enabled          bool       `json:"enabled"`
	AuthMode         string     `json:"authMode"`
	HasLoginPassword bool       `json:"hasLoginPassword"`
	AllowRootLogin   bool       `json:"allowRootLogin"`
	SessionTTLS      int        `json:"sessionTtlS"`
	BasicUser        string     `json:"basicUser"`
	HasBasicPassword bool       `json:"hasBasicPassword"`
	OTPEmails        []string   `json:"otpEmails"`
	IPAllowlist      []string   `json:"ipAllowlist"`
	SSHCLI           bool       `json:"sshCli"`
	SSHShell         bool       `json:"sshShell"`
	SSHUser          string     `json:"sshUser"`
	SSHHostKey       string     `json:"sshHostKey"`
	CertStatus       string     `json:"certStatus"`
	CertExpiresAt    *time.Time `json:"certExpiresAt"`
	CertError        string     `json:"certError"`
	Tokens           []tokenDTO `json:"tokens"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

func (a *API) deploymentOut(c *gin.Context, d database.Deployment, toks []database.DeploymentToken) deploymentDTO {
	host, domainID := "", ""
	if d.Host != nil {
		host = *d.Host
	}
	if d.DomainID != nil {
		domainID = *d.DomainID
	}
	out := deploymentDTO{
		ID: d.ID, RelayID: d.RelayID, DomainID: domainID, Subdomain: d.Subdomain, Host: host, URL: a.publicURL(c, host),
		Protocol: d.Protocol, TargetHost: d.TargetHost, TargetPort: d.TargetPort,
		TLSSkipVerify: d.TLSSkipVerify, SNI: d.SNI, PreserveHost: d.PreserveHost, Enabled: d.Enabled,
		AuthMode: d.AuthMode, HasLoginPassword: d.LoginPassHash != "", AllowRootLogin: d.AllowRootLogin,
		SessionTTLS: d.SessionTTLS, BasicUser: d.BasicUser, HasBasicPassword: d.BasicPassHash != "",
		OTPEmails: splitAllowlist(d.OTPEmails), IPAllowlist: splitAllowlist(d.IPAllowlist), CertStatus: d.CertStatus, CertExpiresAt: d.CertExpiresAt,
		SSHCLI: d.SSHCLI, SSHShell: d.SSHShell, SSHUser: d.SSHUser, SSHHostKey: d.SSHHostKey,
		CertError: d.CertError, Tokens: []tokenDTO{}, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
	for _, t := range toks {
		out.Tokens = append(out.Tokens, tokenDTO{ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, ExpiresAt: t.ExpiresAt})
	}
	return out
}

func splitAllowlist(s string) []string {
	out := []string{}
	for _, f := range strings.Split(s, "\n") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func (a *API) findDeployment(c *gin.Context) *database.Deployment {
	d, err := database.First[database.Deployment](a.db.Where("id = ?", c.Param("id")))
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return nil
	}
	if d == nil {
		abort(c, http.StatusNotFound, "no such deployment")
		return nil
	}
	return d
}

func (a *API) tokensOf(ids ...string) map[string][]database.DeploymentToken {
	var toks []database.DeploymentToken
	a.db.Where("deployment_id IN ?", ids).Order("created_at").Find(&toks)
	m := map[string][]database.DeploymentToken{}
	for _, t := range toks {
		m[t.DeploymentID] = append(m[t.DeploymentID], t)
	}
	return m
}

func (a *API) listDeployments(c *gin.Context) {
	var ds []database.Deployment
	if err := a.db.Order("host").Find(&ds).Error; err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	ids := make([]string, 0, len(ds))
	for _, d := range ds {
		ids = append(ids, d.ID)
	}
	toks := a.tokensOf(ids...)
	out := make([]deploymentDTO, 0, len(ds))
	for _, d := range ds {
		out = append(out, a.deploymentOut(c, d, toks[d.ID]))
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) getDeployment(c *gin.Context) {
	d := a.findDeployment(c)
	if d == nil {
		return
	}
	c.JSON(http.StatusOK, a.deploymentOut(c, *d, a.tokensOf(d.ID)[d.ID]))
}

// deploymentInput is the body of create (missing fields take defaults) and
// update (missing fields stay as they are). Passwords are plaintext here
// and stored only as bcrypt hashes; "" clears one.
type deploymentInput struct {
	RelayID        *string   `json:"relayId"`
	DomainID       *string   `json:"domainId"`
	Subdomain      *string   `json:"subdomain"`
	Protocol       *string   `json:"protocol"`
	TargetHost     *string   `json:"targetHost"`
	TargetPort     *int      `json:"targetPort"`
	TLSSkipVerify  *bool     `json:"tlsSkipVerify"`
	SNI            *string   `json:"sni"`
	PreserveHost   *bool     `json:"preserveHost"`
	Enabled        *bool     `json:"enabled"`
	AuthMode       *string   `json:"authMode"`
	LoginPassword  *string   `json:"loginPassword"`
	AllowRootLogin *bool     `json:"allowRootLogin"`
	SessionTTLS    *int      `json:"sessionTtlS"`
	BasicUser      *string   `json:"basicUser"`
	BasicPassword  *string   `json:"basicPassword"`
	OTPEmails      *[]string `json:"otpEmails"`
	IPAllowlist    *[]string `json:"ipAllowlist"`
	SSHCLI         *bool     `json:"sshCli"`
	SSHShell       *bool     `json:"sshShell"`
	SSHUser        *string   `json:"sshUser"`
	// SSHHostKey can only be cleared (""), so the shell trusts the next key.
	SSHHostKey *string `json:"sshHostKey"`
}

var (
	labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	hostRe  = regexp.MustCompile(`^[A-Za-z0-9._:\-\[\]%]{1,253}$`)
)

type inputError string

func (e inputError) Error() string { return string(e) }

func hashPassword(pw string) (string, error) {
	if pw == "" {
		return "", nil
	}
	if len(pw) < 8 {
		return "", inputError("passwords need at least 8 characters")
	}
	if len(pw) > 72 {
		return "", inputError("passwords can be at most 72 characters")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(h), err
}

// apply copies the set fields of in onto d and checks the result.
func (a *API) apply(d *database.Deployment, in deploymentInput) error {
	if in.RelayID != nil {
		d.RelayID = *in.RelayID
	}
	if in.DomainID != nil {
		id := *in.DomainID
		d.DomainID = &id
	}
	if in.Subdomain != nil {
		d.Subdomain = strings.ToLower(strings.TrimSpace(*in.Subdomain))
	}
	// A new target, or a new relay reaching it, most likely has another
	// host key.
	before := d.RelayID + "\x00" + d.TargetHost + "\x00" + strconv.Itoa(d.TargetPort)
	if in.Protocol != nil {
		d.Protocol = *in.Protocol
	}
	if in.TargetHost != nil {
		d.TargetHost = strings.TrimSpace(*in.TargetHost)
	}
	if in.TargetPort != nil {
		d.TargetPort = *in.TargetPort
	} else if d.Protocol == database.ProtocolSSH && d.TargetPort == 0 {
		d.TargetPort = 22
	}
	if in.SSHCLI != nil {
		d.SSHCLI = *in.SSHCLI
	}
	if in.SSHShell != nil {
		d.SSHShell = *in.SSHShell
	}
	if in.SSHUser != nil {
		d.SSHUser = strings.TrimSpace(*in.SSHUser)
	}
	if in.SSHHostKey != nil {
		if *in.SSHHostKey != "" {
			return inputError("the host key can only be reset (\"\")")
		}
		d.SSHHostKey = ""
	}
	if d.RelayID+"\x00"+d.TargetHost+"\x00"+strconv.Itoa(d.TargetPort) != before {
		d.SSHHostKey = ""
	}
	if in.TLSSkipVerify != nil {
		d.TLSSkipVerify = *in.TLSSkipVerify
	}
	if in.SNI != nil {
		d.SNI = strings.TrimSpace(*in.SNI)
	}
	if in.PreserveHost != nil {
		d.PreserveHost = *in.PreserveHost
	}
	if d.Protocol == database.ProtocolSSH {
		// Only for proxying HTTP(S).
		d.TLSSkipVerify, d.SNI, d.PreserveHost = false, "", false
	}
	if in.Enabled != nil {
		d.Enabled = *in.Enabled
	}
	if in.AuthMode != nil {
		d.AuthMode = *in.AuthMode
	}
	if in.AllowRootLogin != nil {
		d.AllowRootLogin = *in.AllowRootLogin
	}
	if in.SessionTTLS != nil {
		d.SessionTTLS = *in.SessionTTLS
	}
	if in.BasicUser != nil {
		d.BasicUser = strings.TrimSpace(*in.BasicUser)
	}
	var err error
	if in.LoginPassword != nil {
		if d.LoginPassHash, err = hashPassword(*in.LoginPassword); err != nil {
			return err
		}
	}
	if in.BasicPassword != nil {
		if d.BasicPassHash, err = hashPassword(*in.BasicPassword); err != nil {
			return err
		}
	}
	if in.OTPEmails != nil {
		seen := map[string]bool{}
		var list []string
		for _, raw := range *in.OTPEmails {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			m := edge.NormalizeEmail(raw)
			// "*" only works as the whole name part: *@domain.com.
			if name, domain, _ := strings.Cut(m, "@"); name != "*" && strings.Contains(name, "*") || strings.Contains(domain, "*") {
				m = ""
			}
			if m == "" {
				return inputError("email list: " + strings.TrimSpace(raw) + " isn't an email address")
			}
			if !seen[m] {
				seen[m] = true
				list = append(list, m)
			}
		}
		if len(list) > 1000 {
			return inputError("email list: at most 1000 addresses")
		}
		d.OTPEmails = strings.Join(list, "\n")
	}
	if in.IPAllowlist != nil {
		joined := strings.Join(*in.IPAllowlist, "\n")
		nets, err := registry.ParseAllowlist(joined)
		if err != nil {
			return inputError("IP allow list: " + err.Error())
		}
		lines := make([]string, 0, len(nets))
		for _, n := range nets {
			lines = append(lines, n.String())
		}
		d.IPAllowlist = strings.Join(lines, "\n")
	}

	dom, err := a.domainFor(d)
	if err != nil {
		return err
	}
	if !dom.Wildcard {
		d.Subdomain = ""
	}
	host := dom.HostFor(d.Subdomain)
	if d.Host == nil || *d.Host != host {
		d.Host = &host
		d.CertStatus, d.CertExpiresAt, d.CertError = database.CertPending, nil, ""
	}
	switch {
	case dom.Wildcard && !labelRe.MatchString(d.Subdomain):
		return inputError("subdomain must be lowercase letters, digits and dashes")
	case host == a.cfg.AdminHost:
		return inputError("that is the admin host")
	case d.Protocol != database.ProtocolHTTP && d.Protocol != database.ProtocolHTTPS && d.Protocol != database.ProtocolSSH:
		return inputError(`protocol must be "http", "https" or "ssh"`)
	case d.Protocol == database.ProtocolSSH && d.AuthMode == database.AuthBasic:
		return inputError("ssh deployments can't use basic auth")
	case len(d.SSHUser) > 64 || strings.ContainsFunc(d.SSHUser, func(r rune) bool { return r < 32 || r == 127 || r == ' ' }):
		return inputError("SSH username: at most 64 characters, no spaces")
	case d.TargetHost == "" || !hostRe.MatchString(d.TargetHost):
		return inputError("target host must be a hostname or IP address")
	case d.TargetPort < 1 || d.TargetPort > 65535:
		return inputError("target port must be 1-65535")
	case d.SessionTTLS < 300 || d.SessionTTLS > 365*24*3600:
		return inputError("session length must be between 5 minutes and a year")
	case len(d.SNI) > 253:
		return inputError("SNI too long")
	}
	switch d.AuthMode {
	case database.AuthNone, database.AuthToken:
	case database.AuthOTP:
		if d.OTPEmails == "" {
			return inputError("email codes need at least one address on the list")
		}
	case database.AuthLogin:
		if d.LoginPassHash == "" && !d.AllowRootLogin {
			return inputError("login needs a password, or the root password allowed")
		}
	case database.AuthBasic:
		if d.BasicUser == "" || d.BasicPassHash == "" {
			return inputError("basic auth needs a username and password")
		}
		if strings.Contains(d.BasicUser, ":") {
			return inputError("basic auth usernames can't contain ':'")
		}
	default:
		return inputError("auth mode must be none, login, otp, basic or token")
	}
	r, err := database.First[database.Relay](a.db.Where("id = ?", d.RelayID))
	if err != nil {
		return err
	}
	if r == nil {
		return inputError("pick a relay")
	}
	var clash int64
	a.db.Model(&database.Deployment{}).Where("host = ? AND id <> ?", host, d.ID).Count(&clash)
	if clash > 0 {
		return inputError(host + " is taken")
	}
	return nil
}

func (a *API) inputFail(c *gin.Context, err error) {
	var ie inputError
	if errors.As(err, &ie) {
		abort(c, http.StatusBadRequest, ie.Error())
		return
	}
	abort(c, http.StatusInternalServerError, "server error")
}

func (a *API) createDeployment(c *gin.Context) {
	var in deploymentInput
	if err := c.ShouldBindJSON(&in); err != nil {
		abort(c, http.StatusBadRequest, "bad json")
		return
	}
	now := a.now()
	d := database.Deployment{
		ID: database.NewID(), Protocol: database.ProtocolHTTP, Enabled: true, AuthMode: database.AuthLogin, SSHCLI: true, SSHShell: true,
		AllowRootLogin: true, SessionTTLS: 7 * 24 * 3600, CertStatus: database.CertPending,
		CreatedAt: now, UpdatedAt: now,
	}
	if !a.cfg.TLSOn() {
		d.CertStatus = database.CertOff
	}
	if err := a.apply(&d, in); err != nil {
		a.inputFail(c, err)
		return
	}
	if !a.cfg.TLSOn() {
		d.CertStatus = database.CertOff
	}
	if err := a.db.Create(&d).Error; err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	a.changed()
	c.JSON(http.StatusCreated, a.deploymentOut(c, d, nil))
}

func (a *API) updateDeployment(c *gin.Context) {
	d := a.findDeployment(c)
	if d == nil {
		return
	}
	var in deploymentInput
	if err := c.ShouldBindJSON(&in); err != nil {
		abort(c, http.StatusBadRequest, "bad json")
		return
	}
	if err := a.apply(d, in); err != nil {
		a.inputFail(c, err)
		return
	}
	if !a.cfg.TLSOn() {
		d.CertStatus = database.CertOff
	}
	d.UpdatedAt = a.now()
	if err := a.db.Save(d).Error; err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	a.changed()
	c.JSON(http.StatusOK, a.deploymentOut(c, *d, a.tokensOf(d.ID)[d.ID]))
}

func (a *API) deleteDeployment(c *gin.Context) {
	d := a.findDeployment(c)
	if d == nil {
		return
	}
	a.db.Delete(d)
	a.changed()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// certRetryEvery spaces out manual retries for one host. Let's Encrypt
// allows 5 failed validations per hostname an hour, and a host with a
// cached certificate gains nothing from retrying sooner.
const certRetryEvery = 15 * time.Minute

// retryCert gets a deployment's certificate now rather than at the next
// 12-hourly warm-up: it loads a cached one or asks Let's Encrypt again. The
// outcome arrives like any warm-up's, through CertStatus.
func (a *API) retryCert(c *gin.Context) {
	d := a.findDeployment(c)
	if d == nil {
		return
	}
	switch {
	case a.Certs == nil:
		abort(c, http.StatusConflict, "TLS is off on this server")
		return
	case !d.Enabled || d.Host == nil:
		abort(c, http.StatusConflict, "turn the deployment on first")
		return
	}
	host, now := *d.Host, a.now()
	a.certMu.Lock()
	if last, ok := a.certRetried[host]; ok && now.Sub(last) < certRetryEvery {
		a.certMu.Unlock()
		mins := int((certRetryEvery-now.Sub(last))/time.Minute) + 1
		abort(c, http.StatusTooManyRequests, fmt.Sprintf("retried recently, try again in %d min", mins))
		return
	}
	a.certRetried[host] = now
	a.certMu.Unlock()

	a.db.Model(&database.Deployment{}).Where("id = ?", d.ID).Updates(map[string]any{"cert_status": database.CertPending, "cert_error": ""})
	a.changed() // warms every pending host, this one included
	c.JSON(http.StatusAccepted, gin.H{"ok": true})
}

// createToken adds an API token to a deployment; it's shown only here.
// expiresInS (seconds from now) makes it stop working then; 0 or missing
// never expires.
func (a *API) createToken(c *gin.Context) {
	d := a.findDeployment(c)
	if d == nil {
		return
	}
	var b struct {
		Name       string `json:"name"`
		ExpiresInS int64  `json:"expiresInS"`
	}
	if err := c.ShouldBindJSON(&b); err != nil || cleanName(b.Name) == "" {
		abort(c, http.StatusBadRequest, "name required")
		return
	}
	if b.ExpiresInS != 0 && (b.ExpiresInS < 300 || b.ExpiresInS > 5*365*24*3600) {
		abort(c, http.StatusBadRequest, "token expiry must be between 5 minutes and 5 years, or never")
		return
	}
	now := a.now()
	tok := database.NewSecret("smt")
	t := database.DeploymentToken{ID: database.NewID(), DeploymentID: d.ID, Name: cleanName(b.Name), TokenHash: database.HashSecret(tok), CreatedAt: now}
	if b.ExpiresInS > 0 {
		exp := now.Add(time.Duration(b.ExpiresInS) * time.Second)
		t.ExpiresAt = &exp
	}
	if err := a.db.Create(&t).Error; err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	a.changed()
	c.JSON(http.StatusCreated, gin.H{"id": t.ID, "name": t.Name, "token": tok, "createdAt": t.CreatedAt, "expiresAt": t.ExpiresAt})
}

func (a *API) deleteToken(c *gin.Context) {
	res := a.db.Where("id = ? AND deployment_id = ?", c.Param("tid"), c.Param("id")).Delete(&database.DeploymentToken{})
	if res.RowsAffected == 0 {
		abort(c, http.StatusNotFound, "no such token")
		return
	}
	a.changed()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// domainFor is d's domain; a new deployment with none named gets the only
// domain, if there's exactly one.
func (a *API) domainFor(d *database.Deployment) (*database.Domain, error) {
	if d.DomainID == nil {
		var ds []database.Domain
		if err := a.db.Limit(2).Find(&ds).Error; err != nil {
			return nil, err
		}
		if len(ds) != 1 {
			return nil, inputError("pick a domain")
		}
		d.DomainID = &ds[0].ID
		return &ds[0], nil
	}
	dom, err := database.First[database.Domain](a.db.Where("id = ?", *d.DomainID))
	if err != nil {
		return nil, err
	}
	if dom == nil {
		return nil, inputError("pick a domain")
	}
	return dom, nil
}
