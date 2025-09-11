package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"plugin"
	"runtime"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/SimonWaldherr/Wigwam/api"
	"gopkg.in/yaml.v3"
)

// Salt for plugin filenames: derived from Go version + go.mod + api/api.go.
// This avoids ABI mismatch while preventing unbounded growth from time-based salts.
var pluginSalt = func() string {
	h := sha256.New()
	io.WriteString(h, "go="+runtime.Version()+"\n")
	gomod, _ := os.ReadFile(filepath.Join(moduleRoot, "go.mod"))
	h.Write(gomod)
	apiBytes, _ := os.ReadFile(filepath.Join(moduleRoot, "api", "api.go"))
	h.Write(apiBytes)
	return hex.EncodeToString(h.Sum(nil))[:16]
}()

// Determine module root (dir containing go.mod) to ensure plugin builds use the same go.mod/replace.
var moduleRoot = func() string {
	wd, _ := os.Getwd()
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir { // reached filesystem root
			return wd
		}
		dir = parent
	}
}()

// Verbose logging toggle
var verbose atomic.Bool

func vLogf(format string, args ...any) {
	if verbose.Load() {
		log.Printf("[verbose] "+format, args...)
	}
}

// Config mirrors conf.json / conf.yaml structure (subset used by main)
type Config struct {
	Server struct {
		Port               int      `json:"port" yaml:"port"`
		SitesDir           string   `json:"sites_dir" yaml:"sites_dir"`
		ModsDir            string   `json:"mods_dir" yaml:"mods_dir"`
		BuiltModsDir       string   `json:"built_mods_dir" yaml:"built_mods_dir"`
		BuiltSitesDir      string   `json:"built_sites_dir" yaml:"built_sites_dir"`
		UseTempForMods     bool     `json:"use_temp_for_mods" yaml:"use_temp_for_mods"`
		UseTempForSites    bool     `json:"use_temp_for_sites" yaml:"use_temp_for_sites"`
		Verbose            bool     `json:"verbose" yaml:"verbose"`
		ReadTimeoutSeconds int      `json:"read_timeout_seconds" yaml:"read_timeout_seconds"`
		WriteTimeoutSeconds int     `json:"write_timeout_seconds" yaml:"write_timeout_seconds"`
		IdleTimeoutSeconds int      `json:"idle_timeout_seconds" yaml:"idle_timeout_seconds"`
		MaxBodyBytes       int64    `json:"max_body_bytes" yaml:"max_body_bytes"`
		AutoIndexDefault   *bool    `json:"autoindex_default" yaml:"autoindex_default"`
		DirectoryIndex     []string `json:"directory_index" yaml:"directory_index"`
	} `json:"server" yaml:"server"`
	Compat struct {
		VhostConfEnabled bool `json:"vhost_conf_enabled" yaml:"vhost_conf_enabled"`
	} `json:"compat" yaml:"compat"`
}

// loadConfig reads conf.json or conf.yaml from repo root
func loadConfig() (Config, error) {
	var cfg Config
	if b, err := os.ReadFile("conf.json"); err == nil {
		if err := json.Unmarshal(b, &cfg); err == nil {
			return cfg, nil
		}
	}
	if b, err := os.ReadFile("conf.yaml"); err == nil {
		if err := yaml.Unmarshal(b, &cfg); err == nil {
			return cfg, nil
		}
	}
	return cfg, fmt.Errorf("no config file loaded")
}

// --------------------------------- Site-Config (vhost.conf) ---------------------------------

type siteCfg struct {
	ServerName     string
	DocumentRoot   string
	DirectoryIndex []string
	AutoIndex      bool
	Aliases        [][2]string // (urlPathPrefix, fsRelativePath)
	Env            map[string]string
}

func defaultSiteCfg(hostRoot string) siteCfg {
	return siteCfg{
		ServerName:     filepath.Base(hostRoot),
		DocumentRoot:   ".",
		DirectoryIndex: []string{"index.go", "index.html"},
		AutoIndex:      false,
		Aliases:        nil,
		Env:            map[string]string{},
	}
}

// Sehr einfacher INI-Parser (key value...), bewusst ohne externe Abhängigkeiten.
func readVHostConf(hostRoot string) siteCfg {
	cfg := defaultSiteCfg(hostRoot)
	f, err := os.Open(filepath.Join(hostRoot, "vhost.conf"))
	if err != nil {
		return cfg
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	reKV := regexp.MustCompile(`^\s*([A-Za-z][A-Za-z0-9]*)\s+(.*\S)\s*$`)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := reKV.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		k, v := strings.ToLower(m[1]), m[2]
		switch k {
		case "servername":
			cfg.ServerName = v
		case "documentroot":
			cfg.DocumentRoot = v
		case "directoryindex":
			fields := strings.Fields(v)
			if len(fields) > 0 {
				cfg.DirectoryIndex = fields
			}
		case "autoindex":
			cfg.AutoIndex = strings.EqualFold(v, "on") || v == "1" || strings.EqualFold(v, "true")
		case "alias":
			parts := strings.Fields(v)
			if len(parts) == 2 {
				cfg.Aliases = append(cfg.Aliases, [2]string{parts[0], parts[1]})
			}
		case "setenv":
			// SetEnv KEY VALUE (VALUE kann Leerzeichen enthalten → alles nach KEY)
			sp := strings.SplitN(v, " ", 2)
			if len(sp) == 2 {
				if cfg.Env == nil { cfg.Env = map[string]string{} }
				cfg.Env[sp[0]] = strings.TrimSpace(sp[1])
			}
		}
	}
	return cfg
}

// --------------------------------------- Mods Loader ---------------------------------------

type loadedMod struct {
	mw   api.Middleware
	when time.Time
	path string
}

type pluginLoader struct {
	mu   sync.RWMutex
	mods map[string]loadedMod // key: absolute .so path
}

func newPluginLoader() *pluginLoader { return &pluginLoader{mods: map[string]loadedMod{}} }

func (l *pluginLoader) load(path string, conf map[string]string) (api.Middleware, error) {
	abs, _ := filepath.Abs(path)
	l.mu.RLock()
	if ent, ok := l.mods[abs]; ok {
		l.mu.RUnlock()
		return ent.mw, nil
	}
	l.mu.RUnlock()

	p, err := plugin.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", abs, err)
	}

	if sym, err := p.Lookup("PluginAPIVersion"); err == nil {
		if pv, ok := sym.(*string); !(ok && *pv == api.APIVersion) {
			return nil, errors.New("plugin API version mismatch")
		}
	}

	symNew, err := p.Lookup("New")
	if err != nil {
		return nil, fmt.Errorf("lookup New: %w", err)
	}
	newFn, ok := symNew.(func() api.Middleware)
	if !ok {
		return nil, errors.New("new has wrong signature")
	}

	mw := newFn()
	if err := mw.Init(conf); err != nil {
		return nil, fmt.Errorf("init: %w", err)
	}

	l.mu.Lock()
	l.mods[abs] = loadedMod{mw: mw, when: time.Now(), path: abs}
	l.mu.Unlock()
	return mw, nil
}

// ----------------------------------------- Server -----------------------------------------

type Server struct {
	SitesDir     string
	ModsDir      string
	BuiltModsDir string
	BuiltSitesDir string
	UseTempForMods bool
	UseTempForSites bool
	MaxBody int64

	loader *pluginLoader

	chain atomic.Value // api.Handler
}

func newServer(cfg Config) *Server {
	// ensure built directories exist (mods)
	_ = os.MkdirAll(cfg.Server.BuiltModsDir, 0o755)
	// built sites dir (may be empty when using temp)
	if cfg.Server.BuiltSitesDir != "" && !cfg.Server.UseTempForSites {
		_ = os.MkdirAll(cfg.Server.BuiltSitesDir, 0o755)
	}
	s := &Server{
		SitesDir:     cfg.Server.SitesDir,
		ModsDir:      cfg.Server.ModsDir,
		BuiltModsDir: cfg.Server.BuiltModsDir,
		BuiltSitesDir: cfg.Server.BuiltSitesDir,
		UseTempForMods: cfg.Server.UseTempForMods,
		UseTempForSites: cfg.Server.UseTempForSites,
		MaxBody: cfg.Server.MaxBodyBytes,
		loader:       newPluginLoader(),
	}
	// Clean up old .so files that don't match the current salt
	s.cleanupOldMods()
	s.rebuildChain()
	_ = watchDir(s.ModsDir, s.rebuildChain)
	return s
}

var execCommand = func(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }

func (s *Server) rebuildChain() {
	core := s.coreDispatch()

	// Mods in definierter Reihenfolge: numerische Präfixe ↑, sonst alphabetisch
	files, _ := filepath.Glob(filepath.Join(s.ModsDir, "*.go"))
	sort.Strings(files)
	vLogf("rebuilding middleware chain, found %d mods", len(files))

	chain := core
	for i := len(files) - 1; i >= 0; i-- { // letztes wrappt zuerst
		src := files[i]
		so := s.buildMod(src)
		if so == "" { continue }
		conf := map[string]string{"SitesDir": s.SitesDir}
		if verbose.Load() { conf["Verbose"] = "1" }
		mw, err := s.loader.load(so, conf)
		if err != nil {
			log.Printf("[mods] %s: %v", filepath.Base(src), err)
			continue
		}
		chain = mw.Handle(chain)
		log.Printf("[mods] enabled: %s", filepath.Base(src))
		vLogf("mod %s loaded from %s", filepath.Base(src), so)
	}
	s.chain.Store(chain)
}

// coreDispatch: Apache-ähnliches Routing + statisches Serving mit ETag/Range + Script-Aufruf.
func (s *Server) coreDispatch() api.Handler {
	return func(req api.Request) api.Response {
		host := strings.Split(first(req.Header["Host"]), ":")[0]
		if host == "" { return api.Response{Status: 400, Body: []byte("missing Host")} }
		hostRoot := filepath.Join(s.SitesDir, host)
	vLogf("dispatch host=%s path=%s", host, req.Path)
		if fi, err := os.Stat(hostRoot); err != nil || !fi.IsDir() {
			return api.Response{Status: 404, Body: []byte("unknown host")}
		}

		cfg := readVHostConf(hostRoot)
		docRoot := filepath.Join(hostRoot, cfg.DocumentRoot)

		// Aliases (vorn bevorzugt)
		mappedPath := req.Path
		for _, a := range cfg.Aliases {
			if strings.HasPrefix(req.Path, a[0]) {
				mappedPath = strings.Replace(req.Path, a[0], "/"+strings.TrimPrefix(a[1], "/"), 1)
				break
			}
		}

	upath := filepath.Clean("/" + mappedPath)
	full := filepath.Join(docRoot, upath)

		// Sicherstellen: kein Escape aus dem Root (robust via Rel)
		if rel, err := filepath.Rel(docRoot, full); err != nil || strings.HasPrefix(rel, "..") {
			vLogf("forbidden path escape: %s -> %s (rel=%v err=%v)", upath, full, rel, err)
			return api.Response{Status: 403, Body: []byte("forbidden")}
		}

		if fi, err := os.Stat(full); err == nil && fi.IsDir() {
			// DirectoryIndex
			for _, idx := range cfg.DirectoryIndex {
				cand := filepath.Join(full, idx)
				if _, err := os.Stat(cand); err == nil { full = cand; goto RESOLVE }
			}
			if cfg.AutoIndex {
				// einfache Listing-Seite
				list, _ := os.ReadDir(full)
				var b bytes.Buffer
				fmt.Fprintf(&b, "<h1>Index of %s</h1><ul>", upath)
				for _, e := range list {
					name := e.Name()
					if e.IsDir() { name += "/" }
					fmt.Fprintf(&b, `<li><a href="%s">%s</a></li>`, strings.TrimSuffix(upath, "/")+"/"+name, name)
				}
				fmt.Fprint(&b, "</ul>")
				return api.Response{Status: 200, Header: map[string][]string{"Content-Type": {"text/html; charset=utf-8"}}, Body: b.Bytes()}
			}
			// kein Index und kein AutoIndex → 403
			vLogf("directory listing denied: %s", full)
			return api.Response{Status: 403, Body: []byte("directory listing denied")}
		}

	RESOLVE:
		// Script?
		if strings.HasSuffix(full, ".go") {
			vLogf("building script: %s", full)
			so := s.buildScript(full)
			if so == "" { return api.Response{Status: 500, Body: []byte("script build error")} }
			h, err := loadScript(so)
			if err != nil { return api.Response{Status: 500, Body: []byte("script load err: "+err.Error())} }
			req.Env = mergeEnv(req.Env, cfg.Env) // vhost.env dazugeben
			vLogf("executing script plugin: %s", so)
			return h(req)
		}

	// Statisch: ETag/Last-Modified/Range + Gzip
		vLogf("serving static: %s", full)
		return serveStatic(full, req)
	}
}

// HTTP-Adapter inkl. Gzip (on-the-fly, ab 1KB) und Sicherheitsgrenzen.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := s.chain.Load().(api.Handler)

	// Max Body from server config (fallback 16 MiB)
	maxBody := s.MaxBody
	if maxBody == 0 { maxBody = 16 << 20 }
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	}

	req := api.Request{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
		Header: map[string][]string(r.Header),
		Body:   body,
		Env:    map[string]string{},
	}
	// sicherstellen, dass Host gesetzt ist
	if _, ok := req.Header["Host"]; !ok || len(req.Header["Host"]) == 0 {
		req.Header["Host"] = []string{r.Host}
	}

	// Standard-ENV anreichern
	host := strings.Split(first(req.Header["Host"]), ":")[0]
	if host != "" {
		siteRoot := filepath.Join(s.SitesDir, host)
		cfg := readVHostConf(siteRoot)
		docRoot := filepath.Join(siteRoot, cfg.DocumentRoot)
		req.Env = mergeEnv(req.Env, map[string]string{
			"SITE_ROOT":   siteRoot,
			"DOC_ROOT":    docRoot,
			"REMOTE_ADDR": r.RemoteAddr,
		})
	} else {
		req.Env["REMOTE_ADDR"] = r.RemoteAddr
	}

	// Panic-Schutz um Handler-Kette
	var res api.Response
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic in handler: %v", rec)
				res = api.Response{Status: 500, Body: []byte("internal server error")}
			}
		}()
		vLogf("incoming %s %s host=%s body=%dB", r.Method, r.URL.Path, r.Host, len(req.Body))
		res = h(req)
	}()

	// Header + Status
	for k, vv := range res.Header {
		for _, v := range vv { w.Header().Add(k, v) }
	}
	if res.Status == 0 { res.Status = 200 }

	// Gzip nur wenn: Client will gzip, Content-Typ text/* oder json/js, Größe >= 1024B
	// HEAD: nur Header senden, keine Kompression/Body
	if strings.EqualFold(r.Method, "HEAD") {
		w.Header().Set("Content-Length", strconv.Itoa(len(res.Body)))
		w.WriteHeader(res.Status)
		vLogf("HEAD response %d for %s", res.Status, r.URL.Path)
		return
	}

	// Gzip nur wenn: Client will gzip, Content-Typ text/* oder json/js, Größe >= 1024B und noch nicht codiert
	if acceptsGzip(r.Header.Get("Accept-Encoding")) &&
		shouldGzip(w.Header().Get("Content-Type")) &&
		w.Header().Get("Content-Encoding") == "" &&
		len(res.Body) >= 1024 {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Del("Content-Length")
		w.WriteHeader(res.Status)
		gz := gzip.NewWriter(w)
		_, _ = gz.Write(res.Body)
		_ = gz.Close()
		vLogf("gzip response %d for %s (%dB -> gzip)", res.Status, r.URL.Path, len(res.Body))
		return
	}

	w.Header().Set("Content-Length", strconv.Itoa(len(res.Body)))
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
	vLogf("response %d for %s (%dB)", res.Status, r.URL.Path, len(res.Body))
}

// ----------------------------------- Build/Load (Plugins) ----------------------------------

func hashBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:])[:12] }

// Mods bauen (einzelne .go-Datei → .so)
func (s *Server) buildMod(src string) string {
	b, err := os.ReadFile(src)
	if err != nil { log.Printf("[build] read %s: %v", src, err); return "" }
	out := filepath.Join(s.BuiltModsDir, filepath.Base(src)+"."+hashBytes(b)+"."+pluginSalt+".so")
	if _, err := os.Stat(out); err == nil { return out }
	// Mods bauen
	args := []string{"build", "-buildmode=plugin", "-o", out, src}
	
	/*
	args := []string{
		"build", "-buildmode=plugin",
		"-trimpath",
		"-ldflags", "-s -w -buildid=",
		"-o", out, src,
	}*/
	cmd := execCommand("go", args...)
	cmd.Env = append(os.Environ(), "GOMOD="+filepath.Join(moduleRoot, "go.mod"))
	cmd.Dir = moduleRoot
	t0 := time.Now()
	if outb, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[build] mod %s failed: %v\n%s", src, err, string(outb))
		return ""
	}
	vLogf("built mod %s -> %s in %s", src, out, time.Since(t0))
	return out
}

// buildScript builds a site script into either the configured BuiltSitesDir or a temp dir.
func (s *Server) buildScript(src string) string {
	b, err := os.ReadFile(src)
	if err != nil { log.Printf("[build] read %s: %v", src, err); return "" }
	var outDir string
	if !s.UseTempForSites && s.BuiltSitesDir != "" {
		outDir = s.BuiltSitesDir
	} else {
		outDir = os.TempDir()
	}
	out := filepath.Join(outDir, "site_"+filepath.Base(src)+"."+hashBytes(b)+"."+pluginSalt+".so")
	if _, err := os.Stat(out); err == nil { return out }
	// Scripts bauen
	args := []string{"build", "-buildmode=plugin", "-o", out, src}
	
	/*
	args := []string{
		"build", "-buildmode=plugin",
		"-trimpath",
		"-ldflags", "-s -w -buildid=",
		"-o", out, src,
	}*/
	cmd := execCommand("go", args...)
	cmd.Env = append(os.Environ(), "GOMOD="+filepath.Join(moduleRoot, "go.mod"))
	cmd.Dir = moduleRoot
	t0 := time.Now()
	if outb, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[build] script %s failed: %v\n%s", src, err, string(outb))
		return ""
	}
	vLogf("built script %s -> %s in %s", src, out, time.Since(t0))
	return out
}

// cleanupOldMods removes previously built plugin .so files that don't match current salt
func (s *Server) cleanupOldMods() {
	entries, err := os.ReadDir(s.BuiltModsDir)
	if err != nil { return }
	for _, e := range entries {
		if e.IsDir() { continue }
		name := e.Name()
		// expect pattern: <src>.go.<hash>.<salt>.so
		if !strings.HasSuffix(name, ".so") { continue }
		if !strings.Contains(name, "."+pluginSalt+".") {
			full := filepath.Join(s.BuiltModsDir, name)
			_ = os.Remove(full)
			vLogf("removed old mod artifact: %s", full)
		}
	}
}

func loadScript(so string) (api.Handler, error) {
	vLogf("loading plugin: %s", so)
	p, err := plugin.Open(so); if err != nil { return nil, err }
	sym, err := p.Lookup("Handle"); if err != nil { return nil, err }
	h, ok := sym.(func(api.Request) api.Response)
	if !ok { return nil, fmt.Errorf("handle has wrong signature") }
	vLogf("plugin loaded: %s", so)
	return h, nil
}

// ------------------------------------ Static (ETag/Range) ----------------------------------

func serveStatic(full string, req api.Request) api.Response {
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() { return api.Response{Status: 404, Body: []byte("not found")} }

	modTime := fi.ModTime().UTC().Truncate(time.Second)
	etag := fmt.Sprintf(`W/"%x-%x"`, fi.Size(), modTime.Unix())
	h := map[string][]string{
		"Content-Type":  {mimeByExt(full)},
		"Last-Modified": {modTime.Format(http.TimeFormat)},
		"ETag":          {etag},
		"Accept-Ranges": {"bytes"},
		"Cache-Control": {"public, max-age=0"},
	}

	// Conditional GET
	if inm := first(req.Header["If-None-Match"]); inm != "" && inm == etag {
		vLogf("static 304 by ETag for %s", full)
		return api.Response{Status: http.StatusNotModified, Header: h, Body: nil}
	}
	if ims := first(req.Header["If-Modified-Since"]); ims != "" {
		if t, err := time.Parse(http.TimeFormat, ims); err == nil && !modTime.After(t) {
			vLogf("static 304 by Last-Modified for %s", full)
			return api.Response{Status: http.StatusNotModified, Header: h, Body: nil}
		}
	}

	// Range?
	data, err := os.ReadFile(full)
	if err != nil { return api.Response{Status: 500, Body: []byte("read error")} }
	if rh := first(req.Header["Range"]); rh != "" && strings.HasPrefix(strings.ToLower(rh), "bytes=") {
		start, end, ok := parseRange(rh, int64(len(data)))
		if ok {
			h["Content-Range"] = []string{fmt.Sprintf("bytes %d-%d/%d", start, end-1, len(data))}
			h["Content-Length"] = []string{strconv.FormatInt(end-start, 10)}
			vLogf("static 206 for %s range=%s -> %d-%d", full, rh, start, end-1)
			return api.Response{Status: http.StatusPartialContent, Header: h, Body: data[start:end]}
		}
		// Ungültige Range → 416
		h["Content-Range"] = []string{fmt.Sprintf("bytes */%d", len(data))}
		vLogf("static 416 for %s bad-range=%s", full, rh)
		return api.Response{Status: http.StatusRequestedRangeNotSatisfiable, Header: h, Body: nil}
	}

	h["Content-Length"] = []string{strconv.Itoa(len(data))}
	vLogf("static 200 for %s size=%dB", full, len(data))
	return api.Response{Status: 200, Header: h, Body: data}
}

func parseRange(h string, n int64) (start, end int64, ok bool) {
	// Erwartet "bytes=a-b" (inklusive a, exklusive b im Response)
	r := strings.TrimPrefix(h, "bytes=")
	parts := strings.SplitN(r, "-", 2)
	if len(parts) != 2 { return }
	if parts[0] == "" {
		// suffix: -N (letzte N Bytes)
		last, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || last <= 0 || last > n { return }
		return n - last, n, true
	}
	st, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || st < 0 || st >= n { return }
	if parts[1] == "" { return st, n, true }
	en, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || en < st { return }
	if en >= n { en = n - 1 }
	return st, en + 1, true
}

// ---------------------------------------- Utils -------------------------------------------

func mimeByExt(full string) string {
	ext := strings.ToLower(filepath.Ext(full))
	if ext == "" { return "application/octet-stream" }
	if t := mime.TypeByExtension(ext); t != "" { return t }
	switch ext {
	case ".wasm":
		return "application/wasm"
	default:
		return "application/octet-stream"
	}
}

func first(v []string) string { if len(v) > 0 { return v[0] }; return "" }
func acceptsGzip(h string) bool { return strings.Contains(h, "gzip") }
func shouldGzip(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.HasPrefix(ct, "text/") || strings.Contains(ct, "json") || strings.Contains(ct, "javascript")
}

func mergeEnv(dst, src map[string]string) map[string]string {
	if dst == nil { dst = map[string]string{} }
	for k, v := range src { dst[k] = v }
	return dst
}

func watchDir(dir string, onChange func()) error {
	w, err := fsnotify.NewWatcher()
	if err != nil { return err }
	go func() {
		defer w.Close()
		_ = w.Add(dir)
		for {
			select {
			case ev := <-w.Events:
				if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) != 0 {
					log.Printf("[watch] change in %s: %s", dir, ev.Name)
					onChange()
				}
			case err := <-w.Errors:
				log.Printf("[watch] error: %v", err)
			}
		}
	}()
	return nil
}

// ----------------------------------------- main -------------------------------------------

func main() {
	// Try to load conf.json / conf.yaml; fall back to flags if absent
	cfg, err := loadConfig()
	var port string
	if err != nil {
		portFlag := flag.String("port", "8080", "listen port")
		sites := flag.String("sites", "sites-enabled", "enabled sites dir")
		mods := flag.String("mods", "mods-enabled", "mods dir (all *.go files enabled)")
		built := flag.String("mods-built", ".mods-built", "output dir for built plugin .so files")
		sitesBuilt := flag.String("sites-built", ".sites-built", "output dir for built site script artifacts")
		useTmpMods := flag.Bool("use-tmp-mods", false, "use system tmp dir for mod builds")
		useTmpSites := flag.Bool("use-tmp-sites", false, "use system tmp dir for site script builds")
		v := flag.Bool("v", false, "enable verbose logging")
		flag.Parse()
		cfg = Config{}
		cfg.Server.Port = 8080
		cfg.Server.SitesDir = *sites
		cfg.Server.ModsDir = *mods
		cfg.Server.BuiltModsDir = *built
		cfg.Server.BuiltSitesDir = *sitesBuilt
		cfg.Server.UseTempForMods = *useTmpMods
		cfg.Server.UseTempForSites = *useTmpSites
		cfg.Server.Verbose = *v
		port = *portFlag
	} else {
		port = strconv.Itoa(cfg.Server.Port)
	}

	verbose.Store(cfg.Server.Verbose)
	if verbose.Load() {
		log.Printf("[verbose] enabled (moduleRoot=%s)", moduleRoot)
	}

	srv := newServer(cfg)

	// Apply configured timeouts to the HTTP server wrapper
	readTimeout := time.Duration(cfg.Server.ReadTimeoutSeconds) * time.Second
	writeTimeout := time.Duration(cfg.Server.WriteTimeoutSeconds) * time.Second
	idleTimeout := time.Duration(cfg.Server.IdleTimeoutSeconds) * time.Second

	httpSrv := &http.Server{
		Addr: ":" + port,
		Handler: srv,
		ReadTimeout: readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout: idleTimeout,
	}

	log.Printf("listening on :%s", port)
	log.Fatal(httpSrv.ListenAndServe())
}
