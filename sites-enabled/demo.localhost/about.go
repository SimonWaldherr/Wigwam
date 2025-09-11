package main

import (
	"encoding/json"
	"github.com/SimonWaldherr/Wigwam/api"
)

func Handle(req api.Request) api.Response {
	payload, _ := json.Marshal(map[string]any{"ok": true, "path": req.Path, "query": req.Query})
	return api.Response{Status: 200, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: payload}
}
