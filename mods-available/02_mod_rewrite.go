package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"github.com/SimonWaldherr/Wigwam/api"
)

var PluginAPIVersion = api.APIVersion

type rule struct {
	Pattern  string `yaml:"pattern"`
	Target   string `yaml:"target"`
	Redirect int    `yaml:"redirect"`
}

type config struct { Rules []rule `yaml:"rules"` }

type compiled struct {
	re       *regexp.Regexp
	target   string
	redirect int
}

type hostRules struct {
	rules []compiled
	mtime time.Time
}

type mod struct {
	sitesDir string
	mu   sync.RWMutex
	cache map[string]hostRules // host -> rules cache
}

func New() api.Middleware { return &mod{cache: map[string]hostRules{}} }

func (m *mod) Init(conf map[string]string) error {
	m.sitesDir = conf["SitesDir"]
	return nil
}

func (m *mod) Handle(next api.Handler) api.Handler {
	return func(req api.Request) api.Response {
		host := first(req.Header["Host"])
		host = strings.Split(host, ":")[0]
		if host == "" { return next(req) }

		rules := m.getRulesForHost(host)
		orig := req.Path
		for _, r := range rules {
			if !r.re.MatchString(orig) { continue }
			dst := r.re.ReplaceAllString(orig, r.target)
			if r.redirect != 0 {
				loc := dst
				if !strings.HasPrefix(dst, "http://") && !strings.HasPrefix(dst, "https://") {
					loc = fmt.Sprintf("http://%s%s", host, dst)
				}
				return api.Response{Status: r.redirect, Header: map[string][]string{"Location": {loc}}}
			}
			// interner Rewrite: Pfad & Query mergen
			if i := strings.IndexByte(dst, '?'); i >= 0 {
				q, _ := url.ParseQuery(dst[i+1:])
				if req.Query == nil { req.Query = map[string][]string{} }
				for k, v := range q { req.Query[k] = append(req.Query[k], v...) }
				req.Path = dst[:i]
			} else {
				req.Path = dst
			}
			break
		}
		return next(req)
	}
}

func (m *mod) getRulesForHost(host string) []compiled {
	path := filepath.Join(m.sitesDir, host, "rewrite.yaml")

	// Fast path: cached and up-to-date
	m.mu.RLock()
	if ent, ok := m.cache[host]; ok {
		if fi, err := os.Stat(path); err == nil && fi.ModTime().Equal(ent.mtime) {
			m.mu.RUnlock()
			return ent.rules
		}
	}
	m.mu.RUnlock()

	// load from sites-enabled/<host>/rewrite.yaml if exists
	b, err := os.ReadFile(path)
	if err != nil { return nil }
	var cfg config
	if yaml.Unmarshal(b, &cfg) != nil { return nil }
	rules := make([]compiled, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		c := compiled{re: regexp.MustCompile(r.Pattern), target: r.Target, redirect: r.Redirect}
		rules = append(rules, c)
	}
	var mt time.Time
	if fi, err := os.Stat(path); err == nil { mt = fi.ModTime() } else { mt = time.Now() }
	m.mu.Lock(); m.cache[host] = hostRules{rules: rules, mtime: mt}; m.mu.Unlock()
	return rules
}

func first(v []string) string { if len(v) > 0 { return v[0] }; return "" }
