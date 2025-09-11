package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/SimonWaldherr/Wigwam/api"
	"github.com/yuin/goldmark"
	bluemonday "github.com/microcosm-cc/bluemonday"
)

/*
Refactor highlights:
- App struct encapsulates per-request root, DB, template-dir, markdown and sanitizer.
- Consistent context timeouts for DB ops; PRAGMAs & busy_timeout for SQLite.
- Template parsing is cached per absolute file path (safe in plugin context).
- Centralized error/HTML helpers, redirects, and param extraction.
- Title validation + normalization in one place; identical routing semantics preserved.
*/

var (
	validTitle   = regexp.MustCompile(`^[A-Za-z0-9 _-]+$`)
	tplCache     sync.Map // map[string]*template.Template (key = absolute template path)
	mdRenderer   = goldmark.New()
	sanitizeUGC  = bluemonday.UGCPolicy()
	dbOpenOnceMu sync.Mutex
)

type app struct {
	root       string
	tplDir     string
	db         *sql.DB
	now        func() time.Time
	req        api.Request
	host       string
	ctxTimeout time.Duration
}

// Handle is the plugin entrypoint.
func Handle(req api.Request) api.Response {
	// Resolve site root for this host (like before)
	host := first(req.Header["Host"])
	host = strings.Split(host, ":")[0]
	if host == "" {
		return errResp(http.StatusBadRequest, "missing host")
	}
	root := filepath.Join("sites-enabled", host)

	// Open/init DB
	dbPath := filepath.Join(root, "pages.db")
	db, err := openOrInitDB(dbPath)
	if err != nil {
		return errResp(http.StatusInternalServerError, "DB open error: "+err.Error())
	}
	defer db.Close()

	a := &app{
		root:       root,
		tplDir:     filepath.Join(root, "wiki", "templates"),
		db:         db,
		now:        func() time.Time { return time.Now().UTC().Truncate(time.Second) },
		req:        req,
		host:       host,
		ctxTimeout: 2 * time.Second,
	}

	// Routing (behavior compatible with original)
	path := req.Path
	if strings.HasSuffix(path, "/wiki.go") {
		path = "/"
	}

	// POST quick-save entry from index/list/new/edit
	if (path == "/" || path == "" || path == "/wiki/" || path == "/wiki/index.go") && req.Method == "POST" {
		if !sameOrigin(req) {
			return errResp(http.StatusForbidden, "forbidden")
		}
		vals, _ := url.ParseQuery(string(req.Body))
		t := vals.Get("title")
		if t == "" {
			t = first(req.Query["title"])
		}
		if t != "" {
			return a.savePage(t, req.Body)
		}
	}

	switch {
	case req.Method == "GET" && path == "/recent":
		return a.recentPages("search.html")
	case req.Method == "GET" && strings.HasPrefix(path, "/search"):
		q := first(req.Query["q"])
		return a.searchPages(q, "search.html")
	case req.Method == "GET" && strings.HasPrefix(path, "/wiki/"):
		title := strings.TrimPrefix(path, "/wiki/")
		// Handle index.go entry with query params
		if title == "" || title == "index.go" {
			if q := first(req.Query["q"]); q != "" {
				return a.searchPages(q, "search.html")
			}
			if t := first(req.Query["title"]); t != "" {
				return a.viewPage(t, "view.html")
			}
			if t := first(req.Query["edit"]); t != "" {
				return a.editPage(t, "edit.html")
			}
			if first(req.Query["new"]) == "1" {
				return a.newPageForm("new.html")
			}
			return a.listPages("list.html")
		}
		return a.viewPage(title, "view.html")

	case req.Method == "GET" && strings.HasPrefix(path, "/edit/"):
		title := strings.TrimPrefix(path, "/edit/")
		return a.editPage(title, "edit.html")

	case req.Method == "GET" && strings.HasPrefix(path, "/history/"):
		title := strings.TrimPrefix(path, "/history/")
		return a.pageHistory(title, "history.html")

	case req.Method == "POST" && strings.HasPrefix(path, "/delete/"):
		if !sameOrigin(req) {
			return errResp(http.StatusForbidden, "forbidden")
		}
		title := strings.TrimPrefix(path, "/delete/")
		return a.deletePage(title)

	case req.Method == "POST" && strings.HasPrefix(path, "/save/"):
		if !sameOrigin(req) {
			return errResp(http.StatusForbidden, "forbidden")
		}
		title := strings.TrimPrefix(path, "/save/")
		return a.savePage(title, req.Body)

	default:
		return api.Response{Status: http.StatusNotFound, Body: []byte("Not found")}
	}
}

// ---------- app methods (handlers) ----------

func (a *app) listPages(tpl string) api.Response {
	ctx, cancel := context.WithTimeout(context.Background(), a.ctxTimeout)
	defer cancel()

	rows, err := a.db.QueryContext(ctx, `SELECT title FROM pages ORDER BY title`)
	if err != nil {
		return errResp(500, err.Error())
	}
	defer rows.Close()

	var titles []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err == nil {
			titles = append(titles, t)
		}
	}
	data := map[string]any{
		"Pages":  titles,
		"Query":  "",
		"Recent": false,
	}
	return a.renderHTML(tpl, data)
}

func (a *app) pageHistory(title, tpl string) api.Response {
	title = normalizeTitle(title)
	if !validTitle.MatchString(title) {
		return errResp(400, "invalid title")
	}
	
	ctx, cancel := context.WithTimeout(context.Background(), a.ctxTimeout)
	defer cancel()
	
	rows, err := a.db.QueryContext(ctx, `
		SELECT content, updated_at, version
		FROM page_history
		WHERE title = ?
		ORDER BY version DESC
	`, title)
	if err != nil {
		return errResp(500, err.Error())
	}
	defer rows.Close()
	
	type version struct {
		Content   string
		UpdatedAt string
		Version   int
	}
	var versions []version
	
	for rows.Next() {
		var (
			c       string
			updated time.Time
			v       int
		)
		if err := rows.Scan(&c, &updated, &v); err != nil {
			continue
		}
		// Trim preview like original
		if len(c) > 300 {
			c = c[:300] + "…"
		}
		versions = append(versions, version{
			Content:   c,
			UpdatedAt: updated.Format("2006-01-02 15:04"),
			Version:   v,
		})
	}
	
	return a.renderHTML(tpl, map[string]any{
		"Title":    title,
		"Versions": versions,
	})
}

func (a *app) viewPage(title, tpl string) api.Response {
	title = normalizeTitle(title)
	if !validTitle.MatchString(title) {
		return errResp(400, "invalid title")
	}

	ctx, cancel := context.WithTimeout(context.Background(), a.ctxTimeout)
	defer cancel()

	var content string
	err := a.db.QueryRowContext(ctx, `SELECT content FROM pages WHERE title=?`, title).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return redirect("/edit/" + url.QueryEscape(title))
	}
	if err != nil {
		return errResp(500, err.Error())
	}

	var md bytes.Buffer
	_ = mdRenderer.Convert([]byte(content), &md)
	safe := sanitizeUGC.SanitizeBytes(md.Bytes())

	data := map[string]any{
		"Title":       title,
		"ContentHTML": template.HTML(safe),
	}
	return a.renderHTML(tpl, data)
}

func (a *app) editPage(title, tpl string) api.Response {
	title = normalizeTitle(title)
	if !validTitle.MatchString(title) {
		return errResp(400, "invalid title")
	}

	ctx, cancel := context.WithTimeout(context.Background(), a.ctxTimeout)
	defer cancel()

	var content string
	_ = a.db.QueryRowContext(ctx, `SELECT content FROM pages WHERE title=?`, title).Scan(&content)

	data := map[string]any{
		"Title":   title,
		"Content": content,
	}
	return a.renderHTML(tpl, data)
}

func (a *app) savePage(title string, body []byte) api.Response {
	title = normalizeTitle(title)
	if !validTitle.MatchString(title) {
		return errResp(400, "invalid title")
	}
	vals, _ := url.ParseQuery(string(body))
	content := vals.Get("content")

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return errResp(500, err.Error())
	}
	defer func() {
		_ = tx.Rollback() // safe if already committed
	}()

	// Get current content and latest version
	var currentContent sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT content FROM pages WHERE title=?`, title).Scan(&currentContent); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return errResp(500, err.Error())
	}

	var version int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM page_history WHERE title=?`, title).Scan(&version); err != nil {
		return errResp(500, err.Error())
	}

	// Save to history if changed
	if currentContent.Valid && currentContent.String != content {
		if _, err := tx.ExecContext(ctx, `INSERT INTO page_history(title, content, updated_at, version) VALUES(?, ?, ?, ?)`,
			title, currentContent.String, a.now(), version+1); err != nil {
			return errResp(500, err.Error())
		}
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO pages(title, content, updated_at)
		VALUES(?, ?, ?)
		ON CONFLICT(title) DO UPDATE SET content=excluded.content, updated_at=excluded.updated_at
	`, title, content, a.now()); err != nil {
		return errResp(500, err.Error())
	}

	if err := tx.Commit(); err != nil {
		return errResp(500, err.Error())
	}

	loc := "/wiki/index.go?title=" + url.QueryEscape(title)
	return redirect(loc)
}

func (a *app) deletePage(title string) api.Response {
	title = normalizeTitle(title)
	if !validTitle.MatchString(title) {
		return errResp(400, "invalid title")
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.ctxTimeout)
	defer cancel()

	if _, err := a.db.ExecContext(ctx, `DELETE FROM pages WHERE title=?`, title); err != nil {
		return errResp(500, err.Error())
	}
	return redirect("/wiki/index.go")
}

func (a *app) searchPages(q, tpl string) api.Response {
	// No query -> render empty search form
	if strings.TrimSpace(q) == "" {
		return a.renderHTML(tpl, map[string]any{
			"Query":  "",
			"Results": nil,
			"Recent": false,
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	pat := "%" + q + "%"
	rows, err := a.db.QueryContext(ctx, `
		SELECT title, content, updated_at
		FROM pages
		WHERE title LIKE ? OR content LIKE ?
		ORDER BY updated_at DESC
	`, pat, pat)
	if err != nil {
		return errResp(500, err.Error())
	}
	defer rows.Close()

	type hit struct {
		Title   string
		Snippet string
	}
	var hits []hit
	for rows.Next() {
		var t, c string
		var updated time.Time
		if err := rows.Scan(&t, &c, &updated); err != nil {
			continue
		}
		snippet := c
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		hits = append(hits, hit{Title: t, Snippet: snippet})
	}

	return a.renderHTML(tpl, map[string]any{
		"Query":   q,
		"Results": hits,
		"Recent":  false,
	})
}

func (a *app) recentPages(tpl string) api.Response {
	ctx, cancel := context.WithTimeout(context.Background(), a.ctxTimeout)
	defer cancel()

	rows, err := a.db.QueryContext(ctx, `
		SELECT title, content
		FROM pages
		ORDER BY updated_at DESC
		LIMIT 20
	`)
	if err != nil {
		return errResp(500, err.Error())
	}
	defer rows.Close()

	type hit struct {
		Title   string
		Snippet string
	}
	var hits []hit
	for rows.Next() {
		var t, c string
		if err := rows.Scan(&t, &c); err != nil {
			continue
		}
		if len(c) > 200 {
			c = c[:200] + "…"
		}
		hits = append(hits, hit{Title: t, Snippet: c})
	}

	return a.renderHTML(tpl, map[string]any{
		"Query":   "",
		"Results": hits,
		"Recent":  true,
	})
}

func (a *app) newPageForm(tpl string) api.Response {
	return a.renderHTML(tpl, map[string]any{"Title": ""})
}

// ---------- helpers ----------

func (a *app) renderHTML(tplName string, data any) api.Response {
	abs := filepath.Join(a.tplDir, tplName)

	// Template caching (per absolute path)
	var (
		tpl any
		ok  bool
	)
	if tpl, ok = tplCache.Load(abs); !ok {
		// Parse with a FuncMap similar to original
		parsed, err := template.New(filepath.Base(abs)).
			Funcs(template.FuncMap{
				"urlquery": url.QueryEscape,
			}).
			ParseFiles(abs)
		if err != nil {
			return errResp(500, "Template parse error: "+err.Error()+" (path: "+abs+")")
		}
		tplCache.Store(abs, parsed)
		tpl = parsed
	}
	t := tpl.(*template.Template)

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return errResp(500, "Template execute error: "+err.Error())
	}
	return htmlResp(buf.Bytes())
}

func openOrInitDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// Make SQLite more robust for concurrent reads/writes
	// Note: modernc sqlite supports these PRAGMAs.
	pragmas := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
	}
	for _, p := range pragmas {
		if _, e := db.Exec(p); e != nil {
			// continue; not fatal on all hosts but try best effort
		}
	}

	// Schema
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS pages (
			title TEXT PRIMARY KEY,
			content TEXT,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
	`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS page_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT,
			content TEXT,
			updated_at TIMESTAMP,
			version INTEGER
		);
	`); err != nil {
		return nil, err
	}
	// Helpful index
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_pages_updated_at ON pages(updated_at);`)

	return db, nil
}

func sameOrigin(req api.Request) bool {
	host := strings.Split(first(req.Header["Host"]), ":")[0]
	if o := first(req.Header["Origin"]); o != "" {
		if u, err := url.Parse(o); err == nil {
			return u.Hostname() == host
		}
	}
	if r := first(req.Header["Referer"]); r != "" {
		if u, err := url.Parse(r); err == nil {
			return u.Hostname() == host
		}
	}
	// If neither header is present, deny to be safe
	return false
}

func normalizeTitle(t string) string {
	return strings.TrimSpace(t)
}

func first(v []string) string {
	if len(v) > 0 {
		return v[0]
	}
	return ""
}

func htmlResp(b []byte) api.Response {
	return api.Response{
		Status: http.StatusOK,
		Header: map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
		Body:   b,
	}
}

func errResp(code int, msg string) api.Response {
	// small HTML body so browsers render nicely
	body := "<!doctype html><meta charset='utf-8'><title>Error</title><pre>" + template.HTMLEscapeString(msg) + "</pre>"
	return api.Response{
		Status: code,
		Header: map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
		Body:   []byte(body),
	}
}

func redirect(loc string) api.Response {
	return api.Response{
		Status: http.StatusFound,
		Header: map[string][]string{"Location": {loc}},
	}
}
