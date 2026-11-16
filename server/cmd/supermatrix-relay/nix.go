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
	"strings"
	"text/tabwriter"
	"time"

	"github.com/joho/godotenv"

	"main/nixsrc"
)

// supermatrix-relay nix …: this relay's own Nix services, once "Manage its
// Nix services" is on for it in the app. Unlike the deployment commands,
// these never go through the local API (which has no auth, and anything on
// the machine could use): they read the relay's token from its root-only
// file and talk to the server directly. Nix services run as root here, so
// managing them takes root too.

const nixUsage = `supermatrix-relay nix: this relay's own Nix services (needs root, and
"Manage its Nix services" on for the relay in the app)

  supermatrix-relay nix list                      its Nix services, and what each is doing here
  supermatrix-relay nix show SLUG                 one, as JSON
  supermatrix-relay nix create SLUG --dir DIR [flags] [--apply]
  supermatrix-relay nix update SLUG [--dir DIR] [flags]
  supermatrix-relay nix delete SLUG               torn down here if it runs
  supermatrix-relay nix apply SLUG                build and run it here
  supermatrix-relay nix remove SLUG               stop it and tear it down here (it stays defined)
  supermatrix-relay nix start|stop SLUG
  supermatrix-relay nix restart|rebuild|rollback SLUG
  supermatrix-relay nix logs SLUG [--build] [--lines N]

create/update flags:
  --dir DIR             the flake's directory: flake.nix, flake.lock (pins it) and the rest
  --name NAME           display name (the slug by default)
  --run MODE            package (default) | app | devshell
  --attr NAME           the output (default "default")
  --command "PROG ARGS" what to run (split on spaces); needed for devshell
  --env KEY=VALUE       an environment variable (repeat it; replaces them all)
  --relay-env A,B       names this machine must set in /etc/supermatrix/nix-services/SLUG.env
  --restart POLICY      always (default) | on-failure | no
  --keep-state          keep /var/lib/smx/SLUG when it's removed

The server and token come from SUPERMATRIX_SERVER_URL and
SUPERMATRIX_RELAY_TOKEN_FILE (default /etc/supermatrix/relay-token), and
/etc/supermatrix/relay.env if it's there.
`

// direct talks to the server's /v1/relay routes with the relay's token.
type direct struct{ server, token string }

func newDirect() (*direct, error) {
	_ = godotenv.Load(env("SUPERMATRIX_RELAY_ENV", "/etc/supermatrix/relay.env"))
	server := strings.TrimRight(os.Getenv("SUPERMATRIX_SERVER_URL"), "/")
	if server == "" {
		return nil, errors.New("set SUPERMATRIX_SERVER_URL (the server's admin URL), or put it in /etc/supermatrix/relay.env")
	}
	tok := strings.TrimSpace(os.Getenv("SUPERMATRIX_RELAY_TOKEN"))
	if tok == "" {
		f := env("SUPERMATRIX_RELAY_TOKEN_FILE", "/etc/supermatrix/relay-token")
		b, err := os.ReadFile(f)
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("can't read the relay's token in %s: run this as root (Nix services run as root here)", f)
		}
		if err != nil {
			return nil, fmt.Errorf("reading the relay's token: %w (set SUPERMATRIX_RELAY_TOKEN_FILE)", err)
		}
		tok = strings.TrimSpace(string(b))
	}
	return &direct{server: server, token: tok}, nil
}

func (d *direct) do(method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, d.server+"/v1/relay"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+d.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("can't reach %s: %v", d.server, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		var e struct{ Error string }
		if json.Unmarshal(b, &e) != nil || e.Error == "" {
			e.Error = fmt.Sprintf("the server answered %s, not from its API; is SUPERMATRIX_SERVER_URL the admin URL?", res.Status)
		}
		if res.StatusCode == http.StatusNotFound && strings.Contains(e.Error, "not from its API") {
			e.Error = "this server doesn't run Nix services yet: update it"
		}
		return &apiError{res.StatusCode, e.Error}
	}
	if s, ok := out.(*string); ok {
		*s = string(b)
		return nil
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

type nixApplied struct {
	ID        string `json:"id"`
	Phase     string `json:"phase"`
	Desired   string `json:"desired"`
	Removing  bool   `json:"removing"`
	LastError string `json:"lastError"`
	Serving   string `json:"serving"`
}

type nixService struct {
	ID         string       `json:"id"`
	Slug       string       `json:"slug"`
	Name       string       `json:"name"`
	Run        string       `json:"run"`
	FlakeLock  string       `json:"flakeLock"`
	LockPinned bool         `json:"lockPinned"`
	Applied    []nixApplied `json:"applied"`
}

// here is the service's applied service on this relay, nil if none.
func (s nixService) here() *nixApplied {
	for i := range s.Applied {
		if !s.Applied[i].Removing {
			return &s.Applied[i]
		}
	}
	return nil
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// nixInput reads create/update flags into the API's input; only flags given
// are set.
func nixInput(args []string, create bool) (string, map[string]any, bool, error) {
	fs := flag.NewFlagSet("supermatrix-relay nix", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, nixUsage) }
	dir := fs.String("dir", "", "")
	name := fs.String("name", "", "")
	run := fs.String("run", "", "")
	attr := fs.String("attr", "", "")
	command := fs.String("command", "", "")
	relayEnv := fs.String("relay-env", "", "")
	restart := fs.String("restart", "", "")
	keepState := fs.Bool("keep-state", false, "")
	apply := fs.Bool("apply", false, "")
	var envs multi
	fs.Var(&envs, "env", "")
	pos, err := parse(fs, args)
	if err != nil {
		return "", nil, false, err
	}
	if len(pos) != 1 {
		return "", nil, false, errors.New("give the service's slug")
	}
	in := map[string]any{}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if create {
		in["slug"] = pos[0]
		if *dir == "" {
			return "", nil, false, errors.New("--dir DIR: the flake's directory")
		}
	}
	if *dir != "" {
		files, err := nixsrc.ReadDir(*dir)
		if err != nil {
			return "", nil, false, err
		}
		if _, ok := files["flake.nix"]; !ok {
			return "", nil, false, fmt.Errorf("there's no flake.nix in %s", *dir)
		}
		in["files"] = files
	}
	if set["name"] {
		in["name"] = *name
	}
	if set["run"] {
		in["run"] = *run
	}
	if set["attr"] {
		in["attr"] = *attr
	}
	if set["command"] {
		in["command"] = strings.Fields(*command)
	}
	if set["relay-env"] {
		in["relayEnv"] = strings.FieldsFunc(*relayEnv, func(r rune) bool { return r == ',' || r == ' ' })
	}
	if set["restart"] {
		in["restart"] = *restart
	}
	if set["keep-state"] {
		in["removeState"] = !*keepState
	}
	if len(envs) > 0 {
		m := map[string]string{}
		for _, kv := range envs {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return "", nil, false, fmt.Errorf("--env %s: give KEY=VALUE", kv)
			}
			m[k] = v
		}
		in["env"] = m
	}
	return pos[0], in, *apply, nil
}

func cmdNix(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(nixUsage)
		return nil
	}
	d, err := newDirect()
	if err != nil {
		return err
	}
	sub, rest := args[0], args[1:]
	one := func() (string, error) {
		if len(rest) < 1 {
			return "", fmt.Errorf("supermatrix-relay nix %s SLUG", sub)
		}
		return rest[0], nil
	}
	get := func(slug string) (nixService, error) {
		var s nixService
		err := d.do("GET", "/nix-services/"+slug, nil, &s)
		return s, err
	}
	hereOf := func(slug string) (*nixApplied, error) {
		s, err := get(slug)
		if err != nil {
			return nil, err
		}
		ap := s.here()
		if ap == nil {
			return nil, fmt.Errorf("%s doesn't run here: supermatrix-relay nix apply %s", slug, slug)
		}
		return ap, nil
	}

	switch sub {
	case "list", "ls", "status":
		var list []nixService
		if err := d.do("GET", "/nix-services", nil, &list); err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("No Nix services of this relay's yet: supermatrix-relay nix create SLUG --dir DIR --apply")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "SLUG\tNAME\tRUN\tHERE\tLOCK")
		for _, s := range list {
			here := "not applied"
			if ap := s.here(); ap != nil {
				here = ap.Phase
				if ap.LastError != "" {
					here += ": " + ap.LastError
				}
			}
			lock := "none yet"
			if s.FlakeLock != "" {
				lock = map[bool]string{true: "pinned", false: "locked"}[s.LockPinned]
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.Slug, s.Name, s.Run, here, lock)
		}
		return w.Flush()
	case "show", "get":
		slug, err := one()
		if err != nil {
			return err
		}
		var out json.RawMessage
		if err := d.do("GET", "/nix-services/"+slug, nil, &out); err != nil {
			return err
		}
		var pretty bytes.Buffer
		_ = json.Indent(&pretty, out, "", "  ")
		fmt.Println(pretty.String())
		return nil
	case "create", "add":
		slug, in, apply, err := nixInput(rest, true)
		if err != nil {
			return err
		}
		if err := d.do("POST", "/nix-services", in, nil); err != nil {
			return err
		}
		fmt.Printf("defined %s\n", slug)
		if apply {
			if err := d.do("POST", "/nix-services/"+slug+"/applied", nil, nil); err != nil {
				return err
			}
			fmt.Printf("applied %s here: supermatrix-relay nix list shows how it's doing\n", slug)
		}
		return nil
	case "update", "set":
		slug, in, _, err := nixInput(rest, false)
		if err != nil {
			return err
		}
		if len(in) == 0 {
			return errors.New("nothing to change: give --dir or another flag")
		}
		if err := d.do("PATCH", "/nix-services/"+slug, in, nil); err != nil {
			return err
		}
		fmt.Printf("updated %s\n", slug)
		return nil
	case "delete", "rm":
		slug, err := one()
		if err != nil {
			return err
		}
		if err := d.do("DELETE", "/nix-services/"+slug, nil, nil); err != nil {
			return err
		}
		fmt.Printf("deleted %s (torn down here if it ran)\n", slug)
		return nil
	case "apply":
		slug, err := one()
		if err != nil {
			return err
		}
		if err := d.do("POST", "/nix-services/"+slug+"/applied", nil, nil); err != nil {
			return err
		}
		fmt.Printf("applied %s here: supermatrix-relay nix list shows how it's doing\n", slug)
		return nil
	case "remove", "unapply":
		slug, err := one()
		if err != nil {
			return err
		}
		ap, err := hereOf(slug)
		if err != nil {
			return err
		}
		if err := d.do("DELETE", "/applied-nix-services/"+ap.ID, nil, nil); err != nil {
			return err
		}
		fmt.Printf("removing %s from here\n", slug)
		return nil
	case "start", "stop":
		slug, err := one()
		if err != nil {
			return err
		}
		ap, err := hereOf(slug)
		if err != nil {
			return err
		}
		desired := map[string]string{"start": "running", "stop": "stopped"}[sub]
		return d.do("PATCH", "/applied-nix-services/"+ap.ID, map[string]any{"desired": desired}, nil)
	case "restart", "rebuild", "rollback":
		slug, err := one()
		if err != nil {
			return err
		}
		ap, err := hereOf(slug)
		if err != nil {
			return err
		}
		return d.do("POST", "/applied-nix-services/"+ap.ID+"/"+sub, nil, nil)
	case "logs", "log":
		fs := flag.NewFlagSet("supermatrix-relay nix logs", flag.ContinueOnError)
		build := fs.Bool("build", false, "")
		lines := fs.Int("lines", 200, "")
		pos, err := parse(fs, rest)
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return errors.New("supermatrix-relay nix logs SLUG [--build] [--lines N]")
		}
		ap, err := hereOf(pos[0])
		if err != nil {
			return err
		}
		q := fmt.Sprintf("?lines=%d", *lines)
		if *build {
			q = "?build=1"
		}
		var text string
		if err := d.do("GET", "/applied-nix-services/"+ap.ID+"/logs"+q, nil, &text); err != nil {
			return err
		}
		fmt.Print(text)
		return nil
	}
	fmt.Fprint(os.Stderr, nixUsage)
	return fmt.Errorf("unknown command nix %s", sub)
}
