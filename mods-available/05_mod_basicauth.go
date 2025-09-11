package main

import (
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
	"github.com/SimonWaldherr/Wigwam/api"
)

var PluginAPIVersion = api.APIVersion

type userdb struct { Users map[string]string `yaml:"users"` }

type cached struct {
	users map[string]string
	mtime time.Time
}

type mod struct {
	sitesDir string
	mu   sync.RWMutex
	cache map[string]cached // host -> users
}

func New() api.Middleware { return &mod{cache: map[string]cached{}} }

func (m *mod) Init(conf map[string]string) error {
	m.sitesDir = conf["SitesDir"]
	return nil
}

func (m *mod) Handle(next api.Handler) api.Handler {
	return func(req api.Request) api.Response {
		host := first(req.Header["Host"])
		host = strings.Split(host, ":")[0]
		if host == "" { return next(req) }
		users := m.getUsers(host)
		if len(users) == 0 { return next(req) } // kein AuthUserFile -> frei

		auth := first(req.Header["Authorization"])
		if auth == "" || !strings.HasPrefix(auth, "Basic ") { return unauthorized() }
		dec, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "Basic "))
		if err != nil { return unauthorized() }
		parts := strings.SplitN(string(dec), ":", 2)
		if len(parts) != 2 { return unauthorized() }
		user, pass := parts[0], parts[1]
		hash, ok := users[user]
		if !ok { return unauthorized() }
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)) != nil { return unauthorized() }
		return next(req)
	}
}

func (m *mod) getUsers(host string) map[string]string {
	path := filepath.Join(m.sitesDir, host, "users.yaml")

	// Fast path if cache is fresh
	m.mu.RLock()
	if ent, ok := m.cache[host]; ok {
		if fi, err := os.Stat(path); err == nil && fi.ModTime().Equal(ent.mtime) {
			m.mu.RUnlock()
			return ent.users
		}
	}
	m.mu.RUnlock()

	b, err := os.ReadFile(path)
	if err != nil { return nil }
	var db userdb
	if yaml.Unmarshal(b, &db) != nil || len(db.Users) == 0 { return nil }
	var mt time.Time; if fi, err := os.Stat(path); err == nil { mt = fi.ModTime() }
	m.mu.Lock(); m.cache[host] = cached{users: db.Users, mtime: mt}; m.mu.Unlock()
	return db.Users
}

func unauthorized() api.Response {
	h := map[string][]string{"WWW-Authenticate": {`Basic realm="Restricted", charset="UTF-8"`}}
	return api.Response{Status: http.StatusUnauthorized, Header: h, Body: []byte("Unauthorized")}
}

func first(v []string) string { if len(v) > 0 { return v[0] }; return "" }
