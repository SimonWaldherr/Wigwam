package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"

	"github.com/SimonWaldherr/Wigwam/api"
	"github.com/yuin/goldmark"
)

var safeFile = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func Handle(req api.Request) api.Response {
	host := first(req.Header["Host"])
	host = splitHost(host)
	// default markdown file
	file := "demo.md"
	if v := req.Query["file"]; len(v) > 0 && v[0] != "" && safeFile.MatchString(v[0]) {
		file = v[0]
	}

	// Base directory for this plugin
	base := filepath.Join("sites-enabled", host, "markdown")
	// Prefer docs/<file>, fall back to <file> at plugin root
	p := filepath.Join(base, "docs", file)
	if _, err := os.Stat(p); err != nil {
		p = filepath.Join(base, file)
	}

	b, err := os.ReadFile(p)
	if err != nil {
		return api.Response{Status: 404, Body: []byte("file not found")}
	}
	var out bytes.Buffer
	_ = goldmark.Convert(b, &out)
	html := []byte("<!doctype html>" +
		"<link rel='stylesheet' href='/assets/style.css'>" +
		"<article>" + out.String() + "</article>")
	return api.Response{Status: 200, Header: map[string][]string{"Content-Type": {"text/html; charset=utf-8"}}, Body: html}
}

func splitHost(h string) string {
	if i := bytes.IndexByte([]byte(h), ':'); i > 0 {
		return h[:i]
	}
	return h
}
func first(v []string) string {
	if len(v) > 0 {
		return v[0]
	}
	return ""
}
