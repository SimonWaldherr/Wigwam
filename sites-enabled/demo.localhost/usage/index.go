package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SimonWaldherr/Wigwam/api"
)

var (
	startTime     = time.Now()
	reqCount uint64
)

// Handle is the Wigwam plugin entrypoint.
func Handle(req api.Request) api.Response {
	atomic.AddUint64(&reqCount, 1)

	// Parse helpers
	q := req.Query
	refreshDur := parseDuration(first(q["refresh"]))
	format := strings.ToLower(first(q["format"]))
	wantJSON := format == "json" || strings.Contains(strings.ToLower(first(req.Header["Accept"])), "application/json")

	// Collect stats
	view := collectView(req)

	if wantJSON {
		js, _ := json.MarshalIndent(view, "", "  ")
		return api.Response{
			Status: 200,
			Header: map[string][]string{"Content-Type": {"application/json; charset=utf-8"}},
			Body:   js,
		}
	}

	// HTML (default)
	var b bytes.Buffer
	fmt.Fprint(&b, "<!doctype html><meta charset='utf-8'>")
	if refreshDur > 0 {
		// meta refresh (seconds)
		fmt.Fprintf(&b, "<meta http-equiv='refresh' content='%d'>", int(refreshDur.Round(time.Second).Seconds()))
	}
	fmt.Fprint(&b, "<link rel='stylesheet' href='/assets/style.css'>")
	fmt.Fprint(&b, "<title>Server Usage & Stats</title>")

	// Minimal inline extras for nicer tables if site CSS is absent
	fmt.Fprint(&b, `<style>
table{border-collapse:collapse;width:100%;max-width:1000px}
td,th{border:1px solid #ddd;padding:.35rem;vertical-align:top}
thead tr{background:#f6f6f6}
.kv td:first-child{font-weight:600;width:22ch}
code,pre{font-family:ui-monospace,Consolas,Monaco,monospace}
.badge{display:inline-block;padding:.1rem .4rem;border:1px solid #ddd;border-radius:.3rem;font-size:.8em;background:#fafafa}
ul.inline{list-style:none;padding:0;margin:0}
ul.inline li{display:inline;margin-right:.5rem}
.section{margin:1.2rem 0}
small.muted{color:#666}
</style>`)

	// Header & nav
	fmt.Fprintf(&b, "<h1>Server Usage & Statistics</h1>")
	fmt.Fprint(&b, `<nav class="section"><a href="/">Home</a> · <a href="/about.go">About</a> · <a href="/wiki/index.go">Wiki</a>`)
	// Refresh controls
	if refreshDur > 0 {
		fmt.Fprintf(&b, ` · <span class="badge">auto-refresh: %s</span>`, html.EscapeString(refreshDur.String()))
	} else {
		fmt.Fprint(&b, ` · <a href="?refresh=5s">auto-refresh 5s</a>`)
	}
	// JSON link
	selfPath := req.Path
	if selfPath == "" {
		selfPath = "/stats.go"
	}
	fmt.Fprintf(&b, ` · <a href="%s?format=json">JSON</a></nav>`, html.EscapeString(selfPath))

	// Summary bar
	fmt.Fprint(&b, `<div class="section">`)
	fmt.Fprintf(&b, `<span class="badge">uptime %s</span> `, html.EscapeString(humanDuration(time.Since(startTime))))
	fmt.Fprintf(&b, `<span class="badge">%s %s</span> `, runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, `<span class="badge">go %s</span> `, runtime.Version())
	fmt.Fprintf(&b, `<span class="badge">%d CPU</span> `, runtime.NumCPU())
	fmt.Fprintf(&b, `<span class="badge">%d goroutines</span> `, runtime.NumGoroutine())
	fmt.Fprintf(&b, `<span class="badge">~%.2f req/s</span>`, rps())
	fmt.Fprint(&b, `</div>`)

	// Server info
	fmt.Fprint(&b, `<div class="section"><h2>Server Information</h2><table class="kv">`)
	addRow(&b, "Uptime", humanDuration(time.Since(startTime)))
	addRow(&b, "Go Version", runtime.Version())
	addRow(&b, "OS/Arch", runtime.GOOS+"/"+runtime.GOARCH)
	addRow(&b, "CPUs", strconv.Itoa(runtime.NumCPU()))
	addRow(&b, "Goroutines", strconv.Itoa(runtime.NumGoroutine()))
	if bi, ok := debug.ReadBuildInfo(); ok && bi != nil {
		addRow(&b, "Main Module", fmt.Sprintf("%s %s", bi.Main.Path, bi.Main.Version))
	}
	fmt.Fprint(&b, "</table></div>")

	// Memory stats
	fmt.Fprint(&b, `<div class="section"><h2>Memory & GC</h2>`)
	fmt.Fprint(&b, "<table class='kv'>")
	addRow(&b, "Heap Alloc", humanBytes(view.Mem.HeapAlloc))
	addRow(&b, "Heap Sys", humanBytes(view.Mem.HeapSys))
	addRow(&b, "Heap InUse / Idle", humanBytes(view.Mem.HeapInuse)+" / "+humanBytes(view.Mem.HeapIdle))
	addRow(&b, "Stack InUse", humanBytes(view.Mem.StackInuse))
	addRow(&b, "Live Objects", fmt.Sprintf("%d", view.Mem.Mallocs-view.Mem.Frees))
	addRow(&b, "Total Alloc", humanBytes(view.Mem.TotalAlloc))
	addRow(&b, "Sys Memory", humanBytes(view.Mem.Sys))
	addRow(&b, "Last GC", view.Mem.LastGC)
	addRow(&b, "GC Runs", strconv.FormatUint(uint64(view.Mem.NumGC), 10))
	addRow(&b, "Last Pause", view.Mem.LastPause)
	fmt.Fprint(&b, "</table></div>")

	// Environment
	fmt.Fprint(&b, `<div class="section"><h2>Environment</h2>`)
	fmt.Fprint(&b, "<table class='kv'>")
	if siteRoot := req.Env["SITE_ROOT"]; siteRoot != "" {
		addRow(&b, "Site Root", siteRoot)
	}
	if docRoot := req.Env["DOC_ROOT"]; docRoot != "" {
		addRow(&b, "Document Root", docRoot)
	}
	if remoteAddr := req.Env["REMOTE_ADDR"]; remoteAddr != "" {
		addRow(&b, "Remote Address", remoteAddr)
	}
	addRow(&b, "Working Directory", view.WorkDir)
	addRow(&b, "Process ID", strconv.Itoa(os.Getpid()))
	fmt.Fprint(&b, "</table>")

	// Process env (sorted, masked)
	if len(view.ProcessEnv) > 0 {
		fmt.Fprint(&b, "<details class='section'><summary><strong>Process Environment</strong> <small class='muted'>(sorted)</small></summary>")
		fmt.Fprint(&b, "<table>")
		fmt.Fprint(&b, "<thead><tr><th>Key</th><th>Value</th></tr></thead><tbody>")
		for _, kv := range view.ProcessEnv {
			fmt.Fprintf(&b, "<tr><td>%s</td><td>%s</td></tr>", html.EscapeString(kv.Key), html.EscapeString(kv.Value))
		}
		fmt.Fprint(&b, "</tbody></table></details>")
	}
	fmt.Fprint(&b, "</div>")

	// Request info
	fmt.Fprint(&b, `<div class="section"><h2>Current Request</h2><table class="kv">`)
	addRow(&b, "Method", req.Method)
	addRow(&b, "Path", req.Path)
	addRow(&b, "Host", first(req.Header["Host"]))
	addRow(&b, "User-Agent", first(req.Header["User-Agent"]))
	addRow(&b, "Accept", first(req.Header["Accept"]))
	addRow(&b, "Body Size", fmt.Sprintf("%d bytes", len(req.Body)))
	addRow(&b, "Query Params", formatQuery(req.Query))
	if len(req.Body) > 0 {
		snippet := string(req.Body)
		if len(snippet) > 800 {
			snippet = snippet[:800] + "…"
		}
		addRow(&b, "Body (snippet)", snippet)
	}
	fmt.Fprint(&b, "</table>")
	// Headers
	if len(req.Header) > 0 {
		fmt.Fprint(&b, "<details class='section'><summary><strong>Headers</strong></summary>")
		fmt.Fprint(&b, "<table><thead><tr><th>Header</th><th>Value</th></tr></thead><tbody>")
		// deterministic order
		keys := make([]string, 0, len(req.Header))
		for k := range req.Header { keys = append(keys, k) }
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "<tr><td>%s</td><td>%s</td></tr>",
				html.EscapeString(k),
				html.EscapeString(strings.Join(req.Header[k], ", ")),
			)
		}
		fmt.Fprint(&b, "</tbody></table></details>")
	}
	fmt.Fprint(&b, "</div>")

	// Files
	fmt.Fprint(&b, "<div class='section'><h2>Site Files</h2>")
	if docRoot := req.Env["DOC_ROOT"]; docRoot != "" {
		listFiles(&b, docRoot) // depth=1, cap entries to avoid spam
	} else {
		fmt.Fprint(&b, "<p><em>DOC_ROOT not available</em></p>")
	}
	fmt.Fprint(&b, "</div>")

	// Plugin artifacts
	fmt.Fprint(&b, "<div class='section'><h2>Plugin Artifacts</h2>")
	listPlugins(&b)
	fmt.Fprint(&b, "</div>")

	// Footer
	fmt.Fprintf(&b, "<hr><p><small>Generated at %s</small></p>", time.Now().Format("2006-01-02 15:04:05 MST"))

	return api.Response{
		Status: 200,
		Header: map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
		Body:   b.Bytes(),
	}
}

/* ---------- data collection ---------- */

type viewModel struct {
	StartTime string         `json:"start_time"`
	Uptime    string         `json:"uptime"`
	GoVersion string         `json:"go_version"`
	OS        string         `json:"os"`
	Arch      string         `json:"arch"`
	CPUs      int            `json:"cpus"`
	Goroutine int            `json:"goroutines"`
	ReqPerSec float64        `json:"req_per_sec"`
	WorkDir   string         `json:"work_dir"`
	Mem       memView        `json:"mem"`
	ProcessEnv []kv          `json:"process_env,omitempty"`
	Req       reqView        `json:"request"`
	Plugins   pluginView     `json:"plugins"`
	Files     filesView      `json:"files"`
}

type memView struct {
	Alloc       uint64 `json:"alloc"`
	TotalAlloc  uint64 `json:"total_alloc"`
	Sys         uint64 `json:"sys"`
	HeapAlloc   uint64 `json:"heap_alloc"`
	HeapSys     uint64 `json:"heap_sys"`
	HeapIdle    uint64 `json:"heap_idle"`
	HeapInuse   uint64 `json:"heap_inuse"`
	StackInuse  uint64 `json:"stack_inuse"`
	NumGC       uint32 `json:"num_gc"`
	LastGC      string `json:"last_gc"`
	LastPause   string `json:"last_pause"`
	Mallocs     uint64 `json:"mallocs"`
	Frees       uint64 `json:"frees"`
	CgoCalls    int64  `json:"cgo_calls"`
}

type kv struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type reqView struct {
	Method     string              `json:"method"`
	Path       string              `json:"path"`
	Host       string              `json:"host"`
	BodySize   int                 `json:"body_size"`
	Query      map[string][]string `json:"query"`
	Headers    map[string][]string `json:"headers"`
	UserAgent  string              `json:"user_agent"`
	Accept     string              `json:"accept"`
}

type pluginView struct {
	ScriptPlugins []fileInfo `json:"script_plugins,omitempty"`
	ModPlugins    []fileInfo `json:"mod_plugins,omitempty"`
}

type filesView struct {
	DocRoot     string     `json:"doc_root,omitempty"`
	Dirs        []fileInfo `json:"dirs,omitempty"`
	Files       []fileInfo `json:"files,omitempty"`
	TotalSize   uint64     `json:"total_size"`
	TotalDirs   int        `json:"total_dirs"`
	TotalFiles  int        `json:"total_files"`
	Truncated   bool       `json:"truncated"`
}

type fileInfo struct {
	Name  string `json:"name"`
	Size  uint64 `json:"size,omitempty"`
	MTime string `json:"mtime,omitempty"`
}

func collectView(req api.Request) viewModel {
	// mem
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// last pause
	lastPause := time.Duration(0)
	if m.NumGC > 0 {
		idx := (m.NumGC - 1) % uint32(len(m.PauseNs))
		lastPause = time.Duration(m.PauseNs[idx])
	}

	// env vars (sorted, masked)
	env := os.Environ()
	kvs := make([]kv, 0, len(env))
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		key := parts[0]
		val := ""
		if len(parts) > 1 {
			val = parts[1]
		}
		if likelySecret(key) {
			val = "(hidden)"
		}
		kvs = append(kvs, kv{Key: key, Value: val})
	}
	sort.Slice(kvs, func(i, j int) bool { return kvs[i].Key < kvs[j].Key })

	// request view
	hcopy := make(map[string][]string, len(req.Header))
	for k, v := range req.Header {
		cp := append([]string(nil), v...)
		hcopy[k] = cp
	}

	rv := reqView{
		Method:    req.Method,
		Path:      req.Path,
		Host:      first(req.Header["Host"]),
		BodySize:  len(req.Body),
		Query:     req.Query,
		Headers:   hcopy,
		UserAgent: first(req.Header["User-Agent"]),
		Accept:    first(req.Header["Accept"]),
	}

	// plugins + files
	pv := pluginView{
		ScriptPlugins: scanForPlugins(os.TempDir(), "site_", ".so"),
		ModPlugins:    scanForPlugins(".mods-built", "", ".so"),
	}

	fv := filesView{}
	if dr := req.Env["DOC_ROOT"]; dr != "" {
		fv = scanDocRoot(dr, 1, 500) // depth 1, max 500 entries
	}

	return viewModel{
		StartTime: startTime.Format(time.RFC3339),
		Uptime:    humanDuration(time.Since(startTime)),
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		CPUs:      runtime.NumCPU(),
		Goroutine: runtime.NumGoroutine(),
		ReqPerSec: rps(),
		WorkDir:   getWd(),
		ProcessEnv: kvs,
		Req:       rv,
		Plugins:   pv,
		Files:     fv,
		Mem: memView{
			Alloc:      m.Alloc,
			TotalAlloc: m.TotalAlloc,
			Sys:        m.Sys,
			HeapAlloc:  m.HeapAlloc,
			HeapSys:    m.HeapSys,
			HeapIdle:   m.HeapIdle,
			HeapInuse:  m.HeapInuse,
			StackInuse: m.StackInuse,
			NumGC:      m.NumGC,
			LastGC:     time.Unix(0, int64(m.LastGC)).Format("2006-01-02 15:04:05"),
			LastPause:  humanDuration(lastPause),
			Mallocs:    m.Mallocs,
			Frees:      m.Frees,
			CgoCalls:   runtime.NumCgoCall(),
		},
	}
}

/* ---------- HTML helpers ---------- */

func addRow(b *bytes.Buffer, key, value string) {
	fmt.Fprintf(b, "<tr><td>%s</td><td>%s</td></tr>",
		html.EscapeString(key), html.EscapeString(value))
}

func formatQuery(q map[string][]string) string {
	if len(q) == 0 {
		return "(none)"
	}
	keys := make([]string, 0, len(q))
	for k := range q { keys = append(keys, k) }
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+strings.Join(q[k], ","))
	}
	return strings.Join(parts, "&")
}

/* ---------- generic utils ---------- */

func parseDuration(s string) time.Duration {
	if s == "" { return 0 }
	d, err := time.ParseDuration(s)
	if err != nil { return 0 }
	return d
}

func humanDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%.1fm", d.Minutes())
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}

func humanBytes(b uint64) string {
	const unit = 1024.0
	if b < 1024 {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := unit, 0
	for n := float64(b) / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/div, "KMGTPE"[exp])
}

func getWd() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "(unknown)"
}

func first(v []string) string {
	if len(v) > 0 { return v[0] }
	return ""
}

func rps() float64 {
	elapsed := time.Since(startTime).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return float64(atomic.LoadUint64(&reqCount)) / elapsed
}

func likelySecret(key string) bool {
	k := strings.ToUpper(key)
	suspects := []string{"KEY", "SECRET", "TOKEN", "PASSWORD", "PASS", "PRIVATE", "AWS_", "GCP_", "AZURE_", "DB_", "PG", "MYSQL", "REDIS"}
	for _, needle := range suspects {
		if strings.Contains(k, needle) {
			return true
		}
	}
	return false
}

/* ---------- file & plugin helpers ---------- */

func listFiles(b *bytes.Buffer, dir string) {
	// kept for backward compatibility; now delegates to richer scan + render
	fv := scanDocRoot(dir, 1, 500)
	fmt.Fprintf(b, "<p>%d directories, %d files (%s total) in %s</p>",
		fv.TotalDirs, fv.TotalFiles, humanBytes(fv.TotalSize), html.EscapeString(dir))

	// dirs
	if len(fv.Dirs) > 0 {
		fmt.Fprint(b, "<p><strong>Directories:</strong> ")
		for i, d := range fv.Dirs {
			if i > 0 { fmt.Fprint(b, ", ") }
			fmt.Fprintf(b, "<a href='/%s/'>%s/</a>", html.EscapeString(d.Name), html.EscapeString(d.Name))
		}
		fmt.Fprint(b, "</p>")
	}

	// files (limit display)
	if n := len(fv.Files); n > 0 {
		const maxShow = 30
		show := fv.Files
		trunc := ""
		if n > maxShow {
			show = fv.Files[:maxShow]
			trunc = fmt.Sprintf(" (showing %d of %d)", maxShow, n)
		}
		fmt.Fprintf(b, "<p><strong>Files%s:</strong></p>", trunc)
		fmt.Fprint(b, "<table><thead><tr><th>Name</th><th>Size</th><th>Modified</th></tr></thead><tbody>")
		for _, f := range show {
			nameEsc := html.EscapeString(f.Name)
			if strings.HasSuffix(nameEsc, ".go") || strings.HasSuffix(nameEsc, ".html") {
				fmt.Fprintf(b, "<tr><td><a href='/%s'>%s</a></td><td>%s</td><td>%s</td></tr>",
					nameEsc, nameEsc, humanBytes(f.Size), f.MTime)
			} else {
				fmt.Fprintf(b, "<tr><td>%s</td><td>%s</td><td>%s</td></tr>", nameEsc, humanBytes(f.Size), f.MTime)
			}
		}
		fmt.Fprint(b, "</tbody></table>")
	}
}

func scanDocRoot(dir string, maxDepth int, maxEntries int) filesView {
	out := filesView{DocRoot: dir}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.IsDir() {
			out.Dirs = append(out.Dirs, fileInfo{Name: e.Name()})
			out.TotalDirs++
		} else {
			info, err := e.Info()
			if err != nil { continue }
			out.Files = append(out.Files, fileInfo{
				Name:  e.Name(),
				Size:  uint64(info.Size()),
				MTime: info.ModTime().Format("2006-01-02 15:04:05"),
			})
			out.TotalFiles++
			out.TotalSize += uint64(info.Size())
		}
		if out.TotalFiles+out.TotalDirs >= maxEntries {
			out.Truncated = true
			break
		}
	}
	// sort by name
	sort.Slice(out.Dirs, func(i, j int) bool { return out.Dirs[i].Name < out.Dirs[j].Name })
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Name < out.Files[j].Name })
	return out
}

func listPlugins(b *bytes.Buffer) {
	tempDir := os.TempDir()
	builtDir := ".mods-built"

	// Temp script plugins
	sp := scanForPlugins(tempDir, "site_", ".so")
	fmt.Fprintf(b, "<p><strong>Script Plugins:</strong> %d in %s</p>", len(sp), html.EscapeString(tempDir))
	if len(sp) > 0 {
		renderPluginTable(b, sp)
	}

	// Built mods
	mp := scanForPlugins(builtDir, "", ".so")
	if mp == nil {
		fmt.Fprintf(b, "<p><strong>Mod Plugins:</strong> directory %s not accessible</p>", html.EscapeString(builtDir))
		return
	}
	fmt.Fprintf(b, "<p><strong>Mod Plugins:</strong> %d in %s</p>", len(mp), html.EscapeString(builtDir))
	if len(mp) > 0 {
		renderPluginTable(b, mp)
	}
}

func scanForPlugins(dir, prefix, suffix string) []fileInfo {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []fileInfo
	for _, e := range entries {
		name := e.Name()
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		if !strings.HasSuffix(name, suffix) {
			continue
		}
		info, err := e.Info()
		if err != nil { continue }
		out = append(out, fileInfo{
			Name:  name,
			Size:  uint64(info.Size()),
			MTime: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func renderPluginTable(b *bytes.Buffer, list []fileInfo) {
	fmt.Fprint(b, "<table><thead><tr><th>Name</th><th>Size</th><th>Modified</th></tr></thead><tbody>")
	for _, f := range list {
		fmt.Fprintf(b, "<tr><td>%s</td><td>%s</td><td>%s</td></tr>",
			html.EscapeString(f.Name), humanBytes(f.Size), f.MTime)
	}
	fmt.Fprint(b, "</tbody></table>")
}
