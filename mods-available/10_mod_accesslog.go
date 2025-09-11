package main

import (
	"log"
	"time"
	"github.com/SimonWaldherr/Wigwam/api"
)

var PluginAPIVersion = api.APIVersion

type mod struct{}

func New() api.Middleware { return &mod{} }
func (m *mod) Init(conf map[string]string) error { return nil }

func (m *mod) Handle(next api.Handler) api.Handler {
	return func(req api.Request) api.Response {
		t0 := time.Now()
		res := next(req)
		ms := time.Since(t0).Milliseconds()
		host := "-"; if h := req.Header["Host"]; len(h) > 0 && h[0] != "" { host = h[0] }
		log.Printf(`%s "%s %s" %d %dms`, host, req.Method, req.Path, res.Status, ms)
		return res
	}
}
