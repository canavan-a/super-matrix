package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"main/version"
)

const cliUsage = `supermatrix-relay %s

  supermatrix-relay [run]                       run the relay (and its local API on %s)

Manage this relay's deployments (turn on "Manage its deployments" for the
relay in the app first). These talk to the running relay's local API:

  supermatrix-relay status                      this relay, and whether it may manage deployments
  supermatrix-relay domains                     the domains deployments can live on
  supermatrix-relay list                        this relay's deployments
  supermatrix-relay get HOST|ID                 one deployment, as JSON
  supermatrix-relay create HOST TARGET [flags]  e.g. create grafana.example.com http://127.0.0.1:3000
  supermatrix-relay update HOST|ID [flags]
  supermatrix-relay delete HOST|ID
  supermatrix-relay disable HOST|ID              shut it off: visitors get a "disabled" page
  supermatrix-relay enable HOST|ID               turn it back on, sign-in as it was
  supermatrix-relay token create HOST|ID NAME [--expires 30d]   prints the token once
  supermatrix-relay token delete HOST|ID TOKEN_ID
  supermatrix-relay version

TARGET is http://host:port, https://host:port or ssh://host:port.

create/update flags:
  --target URL            a new target (update)
  --host HOST             a new hostname (update)
  --auth MODE             none | login | otp | basic | token   (new deployments: login, root password allowed)
  --password PW           the login page's own password ("" clears it)
  --root-login=BOOL       whether the root password also signs in
  --basic USER:PASS       basic auth credentials
  --emails A,B            addresses that can get an email code (*@example.com works)
  --allow CIDR,CIDR       IP allow list ("" allows everyone)
  --session DURATION      sign-in length, e.g. 12h, 7d
  --enabled=BOOL          off shows a "disabled" page and stops forwarding
  --insecure=BOOL         https targets: don't verify the certificate
  --sni NAME              https targets: the TLS server name
  --preserve-host=BOOL    pass the public Host header on to the target
  --ssh-user NAME         ssh targets: the username the browser shell suggests
  --json JSON|@FILE       any other fields of the API, merged in last

--local ADDR (or SUPERMATRIX_LOCAL_ADDR) is where the relay's local API is.
`

func cli(args []string) int {
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "status", "me":
		err = cmdStatus(rest)
	case "domains":
		err = cmdDomains(rest)
	case "list", "ls":
		err = cmdList(rest)
	case "get":
		err = cmdGet(rest)
	case "create", "add":
		err = cmdCreate(rest)
	case "update", "set":
		err = cmdUpdate(rest)
	case "delete", "rm":
		err = cmdDelete(rest)
	case "enable":
		err = cmdSetEnabled(rest, true)
	case "disable":
		err = cmdSetEnabled(rest, false)
	case "token":
		err = cmdToken(rest)
	case "version", "--version":
		fmt.Println(version.Version)
	case "help", "-h", "--help":
		fmt.Printf(cliUsage, version.Version, DefaultLocalAddr)
	default:
		fmt.Fprintf(os.Stderr, cliUsage, version.Version, DefaultLocalAddr)
		return 2
	}
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "supermatrix-relay:", err)
		}
		return 1
	}
	return 0
}

// ---- the local API ----

type client struct{ base string }

func newFlags(name string) (*flag.FlagSet, *client) {
	fs := flag.NewFlagSet("supermatrix-relay "+name, flag.ContinueOnError)
	c := &client{}
	fs.Func("local", "the relay's local API (default "+DefaultLocalAddr+")", func(s string) error { c.base = s; return nil })
	return fs, c
}

// parse reads flags wherever they are among the positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func (c *client) do(method, path string, body, out any) error {
	addr := c.base
	if addr == "" {
		addr = localAddr()
	}
	if addr == "off" {
		return errors.New("the relay's local API is off (SUPERMATRIX_LOCAL_ADDR=off); turn it on to manage deployments from here")
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://"+addr+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("can't reach the relay's local API on %s; is supermatrix-relay running? (%v)", addr, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		var e struct{ Error string }
		if json.Unmarshal(b, &e) != nil || e.Error == "" {
			// Not the API: most likely SUPERMATRIX_SERVER_URL isn't the
			// server's admin URL.
			e.Error = fmt.Sprintf("the server answered %s, not from its API; is the relay's SUPERMATRIX_SERVER_URL the admin URL?", res.Status)
		}
		return &apiError{res.StatusCode, e.Error}
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

type domain struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Wildcard bool   `json:"wildcard"`
	Label    string `json:"label"`
}

type token struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
}

type deployment struct {
	ID         string  `json:"id"`
	Host       string  `json:"host"`
	URL        string  `json:"url"`
	Protocol   string  `json:"protocol"`
	TargetHost string  `json:"targetHost"`
	TargetPort int     `json:"targetPort"`
	Enabled    bool    `json:"enabled"`
	AuthMode   string  `json:"authMode"`
	Tokens     []token `json:"tokens"`
}

func (d deployment) target() string {
	return fmt.Sprintf("%s://%s", d.Protocol, joinHostPort(d.TargetHost, d.TargetPort))
}

func joinHostPort(h string, p int) string {
	if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		h = "[" + h + "]"
	}
	return h + ":" + strconv.Itoa(p)
}

// find is the deployment whose ID or hostname is ref.
func (c *client) find(ref string) (*deployment, error) {
	var ds []deployment
	if err := c.do("GET", "/deployments", nil, &ds); err != nil {
		return nil, err
	}
	ref = strings.TrimSuffix(strings.ToLower(ref), ".")
	for _, d := range ds {
		if d.ID == ref || d.Host == ref {
			return &d, nil
		}
	}
	return nil, fmt.Errorf("%s isn't one of this relay's deployments (supermatrix-relay list)", ref)
}

// ---- commands ----

func cmdStatus(args []string) error {
	fs, c := newFlags("status")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	var me struct {
		ID                string `json:"id"`
		Name              string `json:"name"`
		Online            bool   `json:"online"`
		Transport         string `json:"transport"`
		Deployments       int    `json:"deployments"`
		ManageDeployments bool   `json:"manageDeployments"`
	}
	if err := c.do("GET", "/me", nil, &me); err != nil {
		return err
	}
	fmt.Printf("relay        %s (%s)\n", me.Name, me.ID)
	state := "offline"
	if me.Online {
		state = "online over " + me.Transport
	}
	fmt.Printf("connection   %s\n", state)
	fmt.Printf("deployments  %d\n", me.Deployments)
	if me.ManageDeployments {
		fmt.Println("manage       yes: this relay can create, change and delete its deployments")
	} else {
		fmt.Println(`manage       no: turn on "Manage its deployments" for this relay in the app`)
	}
	return nil
}

func cmdDomains(args []string) error {
	fs, c := newFlags("domains")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	var ds []domain
	if err := c.do("GET", "/domains", nil, &ds); err != nil {
		return err
	}
	for _, d := range ds {
		fmt.Println(d.Label)
	}
	return nil
}

func cmdList(args []string) error {
	fs, c := newFlags("list")
	asJSON := fs.Bool("json", false, "print the API's JSON")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	var raw json.RawMessage
	if err := c.do("GET", "/deployments", nil, &raw); err != nil {
		return err
	}
	if *asJSON {
		return printJSON(raw)
	}
	var ds []deployment
	if err := json.Unmarshal(raw, &ds); err != nil {
		return err
	}
	if len(ds) == 0 {
		fmt.Println("no deployments on this relay yet")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tTARGET\tAUTH\tON\tID")
	for _, d := range ds {
		on := "yes"
		if !d.Enabled {
			on = "no"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", d.Host, d.target(), d.AuthMode, on, d.ID)
	}
	return tw.Flush()
}

func cmdGet(args []string) error {
	fs, c := newFlags("get")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: supermatrix-relay get HOST|ID")
	}
	d, err := c.find(pos[0])
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := c.do("GET", "/deployments/"+d.ID, nil, &raw); err != nil {
		return err
	}
	return printJSON(raw)
}

func cmdCreate(args []string) error {
	fs, c := newFlags("create")
	f := deploymentFlags(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return errors.New("usage: supermatrix-relay create HOST TARGET [flags], e.g. create grafana.example.com http://127.0.0.1:3000")
	}
	f.host, f.target = &pos[0], &pos[1]
	body, err := f.body(c)
	if err != nil {
		return err
	}
	var d deployment
	if err := c.do("POST", "/deployments", body, &d); err != nil {
		return err
	}
	fmt.Printf("created %s → %s (%s)\n%s\n", d.Host, d.target(), d.ID, d.URL)
	return nil
}

func cmdUpdate(args []string) error {
	fs, c := newFlags("update")
	f := deploymentFlags(fs)
	fs.Func("target", "a new target URL", func(s string) error { f.target = &s; return nil })
	fs.Func("host", "a new hostname", func(s string) error { f.host = &s; return nil })
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: supermatrix-relay update HOST|ID [flags]")
	}
	body, err := f.body(c)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return errors.New("nothing to change (supermatrix-relay help lists the flags)")
	}
	d, err := c.find(pos[0])
	if err != nil {
		return err
	}
	var out deployment
	if err := c.do("PATCH", "/deployments/"+d.ID, body, &out); err != nil {
		return err
	}
	fmt.Printf("updated %s → %s (%s)\n", out.Host, out.target(), out.ID)
	return nil
}

func cmdDelete(args []string) error {
	fs, c := newFlags("delete")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: supermatrix-relay delete HOST|ID")
	}
	d, err := c.find(pos[0])
	if err != nil {
		return err
	}
	if err := c.do("DELETE", "/deployments/"+d.ID, nil, nil); err != nil {
		return err
	}
	fmt.Printf("deleted %s\n", d.Host)
	return nil
}

func cmdSetEnabled(args []string, on bool) error {
	name := map[bool]string{true: "enable", false: "disable"}[on]
	fs, c := newFlags(name)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: supermatrix-relay %s HOST|ID", name)
	}
	d, err := c.find(pos[0])
	if err != nil {
		return err
	}
	if err := c.do("PATCH", "/deployments/"+d.ID, map[string]any{"enabled": on}, nil); err != nil {
		return err
	}
	fmt.Printf("%sd %s\n", name, d.Host)
	return nil
}

func cmdToken(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: supermatrix-relay token create HOST|ID NAME [--expires 30d] | token delete HOST|ID TOKEN_ID")
	}
	sub := args[0]
	fs, c := newFlags("token " + sub)
	var expires time.Duration
	if sub == "create" {
		fs.Func("expires", "how long until it stops working, e.g. 30d (default never)", func(s string) error {
			d, err := parseDuration(s)
			expires = d
			return err
		})
	}
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	switch {
	case sub == "create" && len(pos) == 2:
		d, err := c.find(pos[0])
		if err != nil {
			return err
		}
		var out struct {
			ID    string `json:"id"`
			Token string `json:"token"`
		}
		body := map[string]any{"name": pos[1], "expiresInS": int64(expires / time.Second)}
		if err := c.do("POST", "/deployments/"+d.ID+"/tokens", body, &out); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "token %s for %s (shown once):\n", out.ID, d.Host)
		fmt.Println(out.Token)
		return nil
	case sub == "delete" && len(pos) == 2, sub == "rm" && len(pos) == 2:
		d, err := c.find(pos[0])
		if err != nil {
			return err
		}
		if err := c.do("DELETE", "/deployments/"+d.ID+"/tokens/"+pos[1], nil, nil); err != nil {
			return err
		}
		fmt.Printf("deleted token %s of %s\n", pos[1], d.Host)
		return nil
	}
	return errors.New("usage: supermatrix-relay token create HOST|ID NAME [--expires 30d] | token delete HOST|ID TOKEN_ID")
}

// ---- create/update fields ----

// depFlags are the deployment fields set on the command line; nil (or
// unset) ones are left out of the request.
type depFlags struct {
	host, target *string
	set          map[string]any
	raw          string
}

func deploymentFlags(fs *flag.FlagSet) *depFlags {
	f := &depFlags{set: map[string]any{}}
	str := func(name, field, usage string) {
		fs.Func(name, usage, func(s string) error { f.set[field] = s; return nil })
	}
	boolean := func(name, field, usage string) {
		fs.BoolFunc(name, usage, func(s string) error {
			v, err := strconv.ParseBool(s)
			f.set[field] = v
			return err
		})
	}
	list := func(name, field, usage string) {
		fs.Func(name, usage, func(s string) error {
			out := []string{}
			for _, p := range strings.Split(s, ",") {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
			}
			f.set[field] = out
			return nil
		})
	}
	str("auth", "authMode", "none | login | otp | basic | token")
	str("password", "loginPassword", "the login page's own password")
	boolean("root-login", "allowRootLogin", "whether the root password also signs in")
	fs.Func("basic", "USER:PASS for basic auth", func(s string) error {
		u, p, ok := strings.Cut(s, ":")
		if !ok {
			return errors.New("--basic takes USER:PASS")
		}
		f.set["basicUser"], f.set["basicPassword"] = u, p
		return nil
	})
	list("emails", "otpEmails", "addresses that can get an email code")
	list("allow", "ipAllowlist", "IP allow list, comma-separated CIDRs")
	fs.Func("session", "sign-in length, e.g. 12h or 7d", func(s string) error {
		d, err := parseDuration(s)
		f.set["sessionTtlS"] = int(d / time.Second)
		return err
	})
	boolean("enabled", "enabled", "off shows a \"disabled\" page and stops forwarding")
	boolean("insecure", "tlsSkipVerify", "don't verify an https target's certificate")
	str("sni", "sni", "the TLS server name for an https target")
	boolean("preserve-host", "preserveHost", "pass the public Host header on")
	str("ssh-user", "sshUser", "the username the browser shell suggests")
	fs.StringVar(&f.raw, "json", "", "other API fields as JSON, or @FILE")
	return f
}

// body is the API request for the flags given.
func (f *depFlags) body(c *client) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range f.set {
		out[k] = v
	}
	if f.target != nil {
		proto, host, port, err := parseTargetURL(*f.target)
		if err != nil {
			return nil, err
		}
		out["protocol"], out["targetHost"], out["targetPort"] = proto, host, port
	}
	if f.host != nil {
		var ds []domain
		if err := c.do("GET", "/domains", nil, &ds); err != nil {
			return nil, err
		}
		id, sub, err := placeHost(*f.host, ds)
		if err != nil {
			return nil, err
		}
		out["domainId"], out["subdomain"] = id, sub
	}
	if f.raw != "" {
		raw := []byte(f.raw)
		if name, ok := strings.CutPrefix(f.raw, "@"); ok {
			b, err := os.ReadFile(name)
			if err != nil {
				return nil, err
			}
			raw = b
		}
		extra := map[string]any{}
		if err := json.Unmarshal(raw, &extra); err != nil {
			return nil, fmt.Errorf("--json: %v", err)
		}
		for k, v := range extra {
			out[k] = v
		}
	}
	return out, nil
}

// parseTargetURL reads http://host:port, https://host:port or
// ssh://host:port (host:port alone is http); the port defaults to the
// scheme's.
func parseTargetURL(s string) (proto, host string, port int, err error) {
	proto, rest, ok := strings.Cut(s, "://")
	if !ok {
		proto, rest = "http", s
	}
	proto = strings.ToLower(proto)
	defaults := map[string]int{"http": 80, "https": 443, "ssh": 22}
	port, known := defaults[proto]
	if !known {
		return "", "", 0, fmt.Errorf("target %q: use http://, https:// or ssh://", s)
	}
	rest = strings.TrimRight(rest, "/")
	if strings.Contains(rest, "/") {
		return "", "", 0, fmt.Errorf("target %q: just a host and port, no path", s)
	}
	host = rest
	if i := strings.LastIndex(rest, ":"); i >= 0 && !strings.HasSuffix(rest, "]") {
		host = rest[:i]
		if port, err = strconv.Atoi(rest[i+1:]); err != nil {
			return "", "", 0, fmt.Errorf("target %q: bad port", s)
		}
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return "", "", 0, fmt.Errorf("target %q: no host", s)
	}
	return proto, host, port, nil
}

// placeHost finds the domain a hostname lives on: an exact domain of that
// name, else a wildcard domain of everything after its first label.
func placeHost(host string, ds []domain) (domainID, sub string, err error) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	for _, d := range ds {
		if !d.Wildcard && d.Name == host {
			return d.ID, "", nil
		}
	}
	if label, rest, ok := strings.Cut(host, "."); ok {
		for _, d := range ds {
			if d.Wildcard && d.Name == rest {
				return d.ID, label, nil
			}
		}
	}
	labels := make([]string, 0, len(ds))
	for _, d := range ds {
		labels = append(labels, d.Label)
	}
	sort.Strings(labels)
	return "", "", fmt.Errorf("%s isn't on any of the server's domains (%s)", host, strings.Join(labels, ", "))
}

// parseDuration is time.ParseDuration plus days ("7d").
func parseDuration(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil {
			return 0, fmt.Errorf("bad duration %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("bad duration %q (e.g. 12h, 7d)", s)
	}
	return d, nil
}

func printJSON(raw json.RawMessage) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return err
	}
	buf.WriteByte('\n')
	_, err := os.Stdout.Write(buf.Bytes())
	return err
}
