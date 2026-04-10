package main

import (
	"strings"

	"github.com/SimonWaldherr/Wigwam/api"
)

var PluginAPIVersion = api.APIVersion

type mod struct {
	headers map[string]string // key → value to set on every response
}

func New() api.Middleware { return &mod{} }

// Init accepts config entries like "Header:X-Frame-Options" = "DENY".
// Any key prefixed with "Header:" is treated as a response header to inject.
func (m *mod) Init(conf map[string]string) error {
	m.headers = make(map[string]string)
	for k, v := range conf {
		if after, ok := strings.CutPrefix(k, "Header:"); ok {
			m.headers[after] = v
		}
	}
	// Sensible security defaults if nothing else is configured
	if _, ok := m.headers["X-Content-Type-Options"]; !ok {
		m.headers["X-Content-Type-Options"] = "nosniff"
	}
	return nil
}

func (m *mod) Handle(next api.Handler) api.Handler {
	return func(req api.Request) api.Response {
		res := next(req)
		if res.Header == nil {
			res.Header = map[string][]string{}
		}
		for k, v := range m.headers {
			res.Header[k] = []string{v}
		}
		return res
	}
}
