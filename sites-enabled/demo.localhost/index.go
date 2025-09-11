package main

import (
	"bytes"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SimonWaldherr/Wigwam/api"
)

// Dynamic index: lists files and directories in the DocumentRoot
func Handle(req api.Request) api.Response {
	title := req.Env["APP_TITLE"]
	if title == "" { title = "Wigwam Demo" }
	host := first(req.Header["Host"])
	docRoot := req.Env["DOC_ROOT"]
	if docRoot == "" { docRoot = "." }

	entries, _ := os.ReadDir(docRoot)
	var dirs, pages, others []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") { continue }
		// Skip built artifacts and internal files
		if strings.HasSuffix(name, ".so") { continue }
		if e.IsDir() {
			dirs = append(dirs, name)
			continue
		}
		switch strings.ToLower(filepath.Ext(name)) {
		case ".go", ".html":
			pages = append(pages, name)
		default:
			// only show a few pleasant types
			if ext := strings.ToLower(filepath.Ext(name)); ext == ".css" || ext == ".js" || ext == ".json" || ext == ".md" || ext == ".txt" || ext == ".png" || ext == ".jpg" {
				others = append(others, name)
			}
		}
	}
	sort.Strings(dirs)
	sort.Strings(pages)
	sort.Strings(others)

	var b bytes.Buffer
	fmt.Fprint(&b, "<!doctype html><meta charset='utf-8'>")
	fmt.Fprint(&b, "<link rel='stylesheet' href='/assets/style.css'>")
	fmt.Fprintf(&b, "<h1>%s</h1>", html.EscapeString(title))
	fmt.Fprintf(&b, "<p><small>Host: %s · Root: %s</small></p>", html.EscapeString(host), html.EscapeString(docRoot))
	fmt.Fprint(&b, `<nav style="margin:.5rem 0">`)
	fmt.Fprint(&b, `<a href="/about.go">About</a> · `)
	fmt.Fprint(&b, `<a href="/fractal.go">Fractal</a> · `)
	fmt.Fprint(&b, `<a href="/markdown/">Markdown</a> · `)
	fmt.Fprint(&b, `<a href="/wiki/index.go">Wiki</a>`)
	fmt.Fprint(&b, `</nav>`)

	if len(dirs) > 0 {
		fmt.Fprint(&b, "<h2>Directories</h2><ul>")
		for _, d := range dirs {
			// Trailing slash so DirectoryIndex picks index.go/index.html
			fmt.Fprintf(&b, `<li><a href="/%s/">/%s/</a></li>`, urlPath(d), html.EscapeString(d))
		}
		fmt.Fprint(&b, "</ul>")
	}

	if len(pages) > 0 {
		fmt.Fprint(&b, "<h2>Pages</h2><ul>")
		for _, f := range pages {
			fmt.Fprintf(&b, `<li><a href="/%s">/%s</a></li>`, urlPath(f), html.EscapeString(f))
		}
		fmt.Fprint(&b, "</ul>")
	}

	if len(others) > 0 {
		fmt.Fprint(&b, "<h2>Files</h2><ul>")
		for _, f := range others {
			fmt.Fprintf(&b, `<li><a href="/%s">/%s</a></li>`, urlPath(f), html.EscapeString(f))
		}
		fmt.Fprint(&b, "</ul>")
	}

	return api.Response{Status: 200, Header: map[string][]string{"Content-Type": {"text/html; charset=utf-8"}}, Body: b.Bytes()}
}

func urlPath(s string) string { return strings.TrimLeft(s, "/") }
func first(v []string) string { if len(v) > 0 { return v[0] }; return "" }
