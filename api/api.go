package api

// Request/Response sind der stabile Vertrag zwischen Host, Mods und Script-Plugins.
type Request struct {
	Method string
	Path   string
	Query  map[string][]string
	Header map[string][]string
	Body   []byte
	Env    map[string]string // optional: aus vhost.conf
}

type Response struct {
	Status int
	Header map[string][]string
	Body   []byte
}

// Handler ist die Funktion, die ein Script-Plugin exportiert: func Handle(api.Request) api.Response
type Handler func(Request) Response

// Middleware-Kette wie bei Apache/Nginx-Phasen (in-proc Plugins).
type Middleware interface {
	Init(conf map[string]string) error
	Handle(next Handler) Handler
}

// APIVersion: Muss bei Host & Plugins übereinstimmen.
const APIVersion = "wigwam/1.0"
