package main

import (
	"encoding/json"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Web serves the status page: what arrived, where it went, and a way to
// download or resend a job.
type Web struct {
	Store   *Store
	Server  *Server
	Allow   Allowlist
	Backend string
	Target  string // where jobs are handed off, for display
	Listen  string
}

func (w *Web) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", w.index)
	mux.HandleFunc("GET /api/status", w.api)
	mux.HandleFunc("GET /jobs/{id}", w.download)
	mux.HandleFunc("POST /jobs/{id}/send", w.resend)
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		addr, err := net.ResolveTCPAddr("tcp", r.RemoteAddr)
		if err != nil || !w.Allow.Permits(addr) {
			http.Error(rw, "not allowed from this address", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(rw, r)
	})
}

type pageData struct {
	Version  string
	Backend  string
	Target   string
	Listen   string
	Clients  int
	LastSeen string
	Jobs     []Job
}

func (w *Web) data() pageData {
	d := pageData{
		Version: version,
		Backend: w.Backend,
		Target:  w.Target,
		Listen:  w.Listen,
		Clients: w.Server.Clients(),
		Jobs:    w.Store.Jobs(),
	}
	if t := w.Server.lastSeen.Load(); t > 0 {
		d.LastSeen = time.Unix(t, 0).Format("2 Jan 15:04:05")
	}
	return d
}

func (w *Web) index(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	pageTmpl.Execute(rw, w.data())
}

func (w *Web) api(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(w.data())
}

func (w *Web) download(rw http.ResponseWriter, r *http.Request) {
	path := w.Store.Path(r.PathValue("id"))
	if path == "" {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	http.ServeFile(rw, r, path)
}

func (w *Web) resend(rw http.ResponseWriter, r *http.Request) {
	// Refuse posts made by a page on another site.
	if origin := r.Header.Get("Origin"); origin != "" {
		if u, err := url.Parse(origin); err != nil || !strings.EqualFold(u.Host, r.Host) {
			http.Error(rw, "cross-site request refused", http.StatusForbidden)
			return
		}
	}
	if err := w.Store.Resend(r.PathValue("id")); err != nil {
		http.Error(rw, err.Error(), http.StatusNotFound)
		return
	}
	http.Redirect(rw, r, "/", http.StatusSeeOther)
}

var pageTmpl = template.Must(template.New("page").Funcs(template.FuncMap{
	"when": func(t time.Time) string { return t.Format("2 Jan 15:04:05") },
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="5">
<title>MillMage Shapeoko Bridge</title>
<style>
:root { color-scheme: light dark; --line: #8884; --muted: #777; }
body { font: 16px/1.5 system-ui, sans-serif; margin: 0 auto; max-width: 60rem; padding: 24px 16px 48px; }
h1 { font-size: 1.5rem; margin: 0 0 4px; }
.sub { color: var(--muted); margin: 0 0 20px; }
dl { display: grid; grid-template-columns: max-content 1fr; gap: 4px 16px; margin: 0 0 28px; }
dt { color: var(--muted); }
dd { margin: 0; overflow-wrap: anywhere; }
.scroll { overflow-x: auto; }
table { border-collapse: collapse; width: 100%; min-width: 40rem; }
th, td { text-align: left; padding: 8px 10px; border-bottom: 1px solid var(--line); vertical-align: top; }
th { color: var(--muted); font-weight: 500; font-size: .85rem; }
td.num { font-variant-numeric: tabular-nums; white-space: nowrap; }
.status { font-weight: 600; white-space: nowrap; }
.delivered, .saved { color: #2a8a4a; }
.failed, .held { color: #c0392b; }
.detail { color: var(--muted); font-size: .9rem; }
button { font: inherit; padding: 4px 12px; cursor: pointer; }
form { margin: 0; }
.empty { color: var(--muted); padding: 24px 0; }
</style>
</head>
<body>
<h1>MillMage Shapeoko Bridge</h1>
<p class="sub">Jobs sent from MillMage land here and are passed to the machine's sender. Nothing moves until someone presses Start at the machine.</p>
<dl>
<dt>MillMage connects to</dt><dd>this computer, port {{.Listen}}</dd>
<dt>Jobs go to</dt><dd>{{.Target}}</dd>
<dt>Senders connected</dt><dd>{{.Clients}}{{if .LastSeen}} (last activity {{.LastSeen}}){{end}}</dd>
<dt>Version</dt><dd>{{.Version}}</dd>
</dl>
{{if .Jobs}}
<div class="scroll">
<table>
<tr><th>Received</th><th>From</th><th>Lines</th><th>Bytes</th><th>Status</th><th></th></tr>
{{range .Jobs}}
<tr>
<td class="num">{{when .Received}}</td>
<td>{{.Source}}</td>
<td class="num">{{if .Lines}}{{.Lines}}{{end}}</td>
<td class="num">{{.Bytes}}</td>
<td><span class="status {{.Status}}">{{.Status}}</span><div class="detail">{{.Detail}}</div></td>
<td><a href="/jobs/{{.ID}}">{{.File}}</a>
{{if ne .Status "sending"}}{{if eq $.Backend "carbide"}}<form method="post" action="/jobs/{{.ID}}/send"><button>{{if eq .Status "held"}}Send anyway{{else}}Send again{{end}}</button></form>{{end}}{{end}}</td>
</tr>
{{end}}
</table>
</div>
{{else}}
<p class="empty">No jobs yet. In MillMage, connect to this computer as a GRBL network device and press Start.</p>
{{end}}
</body>
</html>
`))
