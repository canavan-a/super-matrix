package nixagent

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"main/tunnel"
)

// progress reads Nix's --log-format internal-json (stderr lines starting
// "@nix "): it turns them into a plain build log and NixProgress reports,
// and remembers the error Nix gave.

// Nix's activity and result types (libutil/logging.hh).
const (
	actUnknown   = 0
	actCopyPath  = 100
	actTransfer  = 101
	actCopyPaths = 103
	actBuilds    = 104
	actBuild     = 105
	actSubst     = 108
	actFetchTree = 112

	resBuildLogLine = 101
	resProgress     = 105
	resSetExpected  = 106
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

type nixEvent struct {
	Action string            `json:"action"`
	ID     int64             `json:"id"`
	Type   int               `json:"type"`
	Level  int               `json:"level"`
	Text   string            `json:"text"`
	Msg    string            `json:"msg"`
	RawMsg string            `json:"raw_msg"`
	Fields []json.RawMessage `json:"fields"`
}

type progress struct {
	mu      sync.Mutex
	log     io.Writer
	on      func(tunnel.NixProgress)
	started time.Time
	acts    map[int64]int
	p       tunnel.NixProgress
	lastErr string
	part    string
}

func newProgress(log io.Writer, on func(tunnel.NixProgress)) *progress {
	if log == nil {
		log = io.Discard
	}
	return &progress{log: log, on: on, started: time.Now(), acts: map[int64]int{}, p: tunnel.NixProgress{Step: "evaluating"}}
}

// Write takes Nix's stderr.
func (pr *progress) Write(b []byte) (int, error) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.part += string(b)
	for {
		i := strings.IndexByte(pr.part, '\n')
		if i < 0 {
			break
		}
		pr.line(pr.part[:i])
		pr.part = pr.part[i+1:]
	}
	return len(b), nil
}

// storeName is a store path's name: /nix/store/<hash>-nodejs-20.drv → nodejs-20.
func storeName(p string) string {
	p = strings.Trim(p, "'\" ")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	if len(p) > 33 && p[32] == '-' {
		p = p[33:]
	}
	return strings.TrimSuffix(p, ".drv")
}

func (pr *progress) line(l string) {
	raw, ok := strings.CutPrefix(l, "@nix ")
	if !ok {
		fmt.Fprintln(pr.log, stripANSI(l))
		return
	}
	var e nixEvent
	if json.Unmarshal([]byte(raw), &e) != nil {
		return
	}
	changed := false
	switch e.Action {
	case "msg":
		text := stripANSI(e.Msg)
		if e.Level <= 3 {
			fmt.Fprintln(pr.log, text)
		}
		if e.Level == 0 {
			pr.lastErr = strings.TrimPrefix(strings.TrimSpace(text), "error: ")
		}
	case "start":
		pr.acts[e.ID] = e.Type
		text := stripANSI(e.Text)
		switch e.Type {
		case actUnknown:
			if strings.HasPrefix(text, "evaluating") || strings.HasPrefix(text, "copying") && strings.Contains(text, "to the store") {
				pr.p.Step, changed = "evaluating", true
			}
		case actFetchTree, actTransfer:
			pr.p.Step, changed = "fetching", true
			if text != "" {
				pr.p.Current = strings.TrimSpace(strings.TrimPrefix(text, "fetching"))
			}
		case actCopyPath, actSubst:
			pr.p.Step, changed = "downloading", true
			if len(e.Fields) > 0 {
				var p string
				if json.Unmarshal(e.Fields[0], &p) == nil {
					pr.p.Current = storeName(p)
				}
			}
		case actBuild:
			pr.p.Step, changed = "building", true
			var p string
			if len(e.Fields) > 0 && json.Unmarshal(e.Fields[0], &p) == nil {
				pr.p.Current = storeName(p)
			}
		}
		if text != "" && e.Level <= 3 {
			fmt.Fprintln(pr.log, text)
		}
	case "result":
		switch e.Type {
		case resBuildLogLine:
			if len(e.Fields) > 0 {
				var s string
				if json.Unmarshal(e.Fields[0], &s) == nil {
					fmt.Fprintln(pr.log, stripANSI(s))
				}
			}
		case resProgress:
			if len(e.Fields) >= 2 {
				var done, expected int
				_ = json.Unmarshal(e.Fields[0], &done)
				_ = json.Unmarshal(e.Fields[1], &expected)
				switch pr.acts[e.ID] {
				case actBuilds:
					pr.p.Built, pr.p.ToBuild, changed = done, expected, true
				case actCopyPaths:
					pr.p.Downloaded, pr.p.ToDownload, changed = done, expected, true
				}
			}
		case resSetExpected:
			if len(e.Fields) >= 2 {
				var kind, expected int
				_ = json.Unmarshal(e.Fields[0], &kind)
				_ = json.Unmarshal(e.Fields[1], &expected)
				if kind == actBuild && expected > pr.p.ToBuild {
					pr.p.ToBuild, changed = expected, true
				}
			}
		}
	}
	if changed && pr.on != nil {
		p := pr.p
		p.ElapsedS = int(time.Since(pr.started).Seconds())
		pr.on(p)
	}
}

// err is what Nix said went wrong ("" if it said nothing).
func (pr *progress) err() string {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if pr.part != "" {
		pr.line(pr.part)
		pr.part = ""
	}
	return pr.lastErr
}
