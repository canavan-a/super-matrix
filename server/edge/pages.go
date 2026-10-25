package edge

import (
	"html/template"
	"net/http"
)

// Pages the edge renders itself: the deployment login screen and errors.
// Same palette as the app's dark/light themes (app/src/theme.tsx) and the
// broom mark from art/mark.txt.

const pageCSS = `
:root{--bg:#0d1117;--surface:#161b22;--border:#30363d;--text:#e6edf3;--muted:#8b949e;--primary:#2f81f7;--primaryText:#fff;--danger:#f85149;--dangerBg:#3d1418;--tile:#0d1117;--sq:#e6edf3}
@media (prefers-color-scheme: light){:root{--bg:#ffffff;--surface:#f6f8fa;--border:#e5e5e5;--text:#1b1f24;--muted:#57606a;--primary:#0969da;--primaryText:#fff;--danger:#cf222e;--dangerBg:#ffebe9;--tile:#1b1f24;--sq:#ffffff}}
*{box-sizing:border-box}
html,body{margin:0;height:100%}
body{background:var(--bg);color:var(--text);font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;display:flex;align-items:center;justify-content:center;padding:16px}
main{width:100%;max-width:360px;background:var(--surface);border:1px solid var(--border);border-radius:12px;padding:28px 24px;text-align:center}
.mark{width:56px;height:56px;display:block;margin:0 auto 14px}
.brand{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-weight:700;letter-spacing:.08em;text-transform:uppercase;font-size:13px;color:var(--muted);margin:0 0 18px}
h1{font-size:18px;margin:0 0 6px}
p{color:var(--muted);font-size:14px;line-height:1.45;margin:0 0 18px;word-break:break-word}
.host{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--text)}
input{width:100%;font:inherit;font-size:16px;padding:11px 12px;border-radius:8px;border:1px solid var(--border);background:var(--bg);color:var(--text);margin:0 0 12px}
input:focus{outline:2px solid var(--primary);outline-offset:-1px}
button{width:100%;font:inherit;font-weight:600;font-size:15px;padding:11px;border-radius:8px;border:0;background:var(--primary);color:var(--primaryText);cursor:pointer}
.err{background:var(--dangerBg);color:var(--danger);border-radius:8px;padding:9px 10px;font-size:14px;margin:0 0 12px}
.note{background:var(--bg);border:1px solid var(--border);border-radius:8px;padding:9px 10px;font-size:14px;margin:0 0 12px}
.alt{display:block;margin-top:14px;font-size:14px;color:var(--primary);text-decoration:none}
`

const markSVG = `<svg class="mark" viewBox="0 0 36 36" aria-hidden="true"><rect width="36" height="36" rx="3" fill="var(--tile)"/><g fill="var(--sq)"><rect x="4" y="3.5" width="6.3" height="6.4"/><rect x="14.9" y="3.5" width="6.3" height="6.4"/><rect x="4" y="14.3" width="6.3" height="6.4"/><rect x="25.7" y="14.3" width="6.3" height="6.4"/><rect x="14.9" y="25.1" width="6.3" height="6.4"/><rect x="25.7" y="25.1" width="6.3" height="6.4"/></g></svg>`

var pages = template.Must(template.New("").Parse(`
{{define "head"}}<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex"><title>{{.Title}} · Super Matrix</title><style>` + pageCSS + `</style></head><body><main>` + markSVG + `<p class="brand">Super Matrix</p>{{end}}
{{define "foot"}}</main></body></html>{{end}}

{{define "message"}}{{template "head" .}}<h1>{{.Title}}</h1><p>{{.Message}}</p>{{template "foot"}}{{end}}

{{define "alerts"}}{{if .Notice}}<div class="note" role="status">{{.Notice}}</div>{{end}}{{if .Error}}<div class="err" role="alert">{{.Error}}</div>{{end}}{{end}}

{{define "login"}}{{template "head" .}}<h1>Sign in</h1><p>to <span class="host">{{.Host}}</span></p>
{{template "alerts" .}}
<form method="post" action="/_supermatrix/login"><input type="hidden" name="next" value="{{.Next}}">
<input type="password" name="password" placeholder="Password" autocomplete="current-password" autofocus required aria-label="Password">
<button type="submit">Continue</button></form>{{template "foot"}}{{end}}

{{define "otp-email"}}{{template "head" .}}<h1>Sign in</h1><p>to <span class="host">{{.Host}}</span> with a code sent to your email</p>
{{template "alerts" .}}
<form method="post" action="/_supermatrix/login"><input type="hidden" name="next" value="{{.Next}}">
<input type="email" name="email" value="{{.Email}}" placeholder="you@example.com" autocomplete="email" autofocus required aria-label="Email">
<button type="submit">Email me a code</button></form>{{template "foot"}}{{end}}

{{define "otp-code"}}{{template "head" .}}<h1>Check your email</h1><p>If <span class="host">{{.Email}}</span> can sign in to <span class="host">{{.Host}}</span>, a code is on its way. It's good for 10 minutes.</p>
{{template "alerts" .}}
<form method="post" action="/_supermatrix/verify"><input type="hidden" name="next" value="{{.Next}}"><input type="hidden" name="email" value="{{.Email}}">
<input type="text" name="code" inputmode="numeric" pattern="[0-9 ]*" maxlength="8" placeholder="123456" autocomplete="one-time-code" autofocus required aria-label="Code">
<button type="submit">Sign in</button></form>
<a class="alt" href="/_supermatrix/login?next={{.Next}}">Use a different email or send a new code</a>{{template "foot"}}{{end}}
`))

type pageData struct {
	Title   string
	Message string
	Host    string
	Next    string
	Error   string
	// Notice is a neutral note above the form (e.g. the session expired).
	Notice string
	Email  string
}

func render(w http.ResponseWriter, status int, name string, d pageData) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	w.WriteHeader(status)
	_ = pages.ExecuteTemplate(w, name, d)
}

func message(w http.ResponseWriter, status int, title, msg string) {
	render(w, status, "message", pageData{Title: title, Message: msg})
}
