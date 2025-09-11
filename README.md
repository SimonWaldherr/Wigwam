# Wigwam 🏕️

![](./logo.png)

An experimental **Go web server** that runs .go-files, similar to [Apache](https://httpd.apache.org/) which runs .php-files.

It uses **Go plugins** (-buildmode=plugin) for pages and modules (mods) and offers some **Apache-like features**.

⚠️ **Note:** Go plugins are only available on **Linux, macOS, and FreeBSD**, not on Windows.

Host and plugins must be built with **the same Go version/flags**. Plugins cannot be unloaded – hot swapping works via new hashed filenames.

---

## Features

- **Virtual Hosts**: via `sites-enabled/<hostname>/`
- **DirectoryIndex**: `index.go`, `index.html`, configurable
- **AutoIndex**: optional directory listing
- **Aliases**: `Alias ​​/urlpath realpath`
- **Static Files**: with `ETag`, `Last-Modified`, Conditional GET, **Range** (Partial Content)
- **Gzip** compression (on-the-fly, from 1 KB)
- **Mods** (plugins, hot-swappable):
- `mod_rewrite`: YAML rules for redirects & internal rewrites
- `mod_basicauth`: YAML-based user/password authentication (bcrypt, htpasswd compatible)
- `mod_accesslog`: access log
- **Script Pages**: `.go` files export `func Handle(api.Request) api.Response`
- **Per-Site Config**: `vhost.conf`, `rewrite.yaml`, `users.yaml`

---

## Project structure

```

wigwam/
├─ api/
│ └─ api.go # Stable API/ABI for host, mods, scripts
├─ main.go # Host server
├─ mods-available/ # Mods (Plugins)
│ ├─ 02\_mod\_rewrite.go
│ ├─ 05\_mod\_basicauth.go
│ └─ 10\_mod\_accesslog.go
└─ sites-enabled/
└─ example.com/
├─ vhost.conf
├─ index.go
├─ about.go
├─ rewrite.yaml
├─ users.yaml
└─ assets/style.css

````

---

## Installation & Start

```bash
git clone https://github.com/SimonWaldherr/Wigwam.git
cd Wigwam
go mod tidy
go run. -port 8080
````

Set hosts entry:

```
127.0.0.1 example.com
127.0.0.1 demo.localhost
```

Then:

```
http://demo.localhost:8080/
```

or via header:

```bash
curl -H 'Host: example.com' http://localhost:8080/
```

---

## Configuration

### vhost.conf

```ini
ServerName example.com
DocumentRoot .
DirectoryIndex index.go index.html
AutoIndex off
Alias ​​/static assets
SetEnv APP_TITLE "Hello Wigwam"
```

### rewrite.yaml

```yaml
rules: 
- pattern: "^/blog$" 
target: "/blog/" 
redirect: 301 
- pattern: "^/post/(\\d+)$" 
target: "/posts/show.go?id=$1" 
redirect: 0 
- pattern: "^/old/(.*)$" 
target: "https://new.example.com/$1" 
redirect: 308
```

### users.yaml (for BasicAuth)

```yaml
users: 
Alice: "$2y$12$KbQiQW5c0hEw2wTtE4yHku1gA0y0kG6m0GqF2uPq7W3i9m8d8l6nK"
```

You can generate passwords with `htpasswd -nB alice`.

---

## Example pages

`index.go`:

```go
func Handle(req api.Request) api.Response { 
return api.Response{ 
Status: 200, 
Header: map[string][]string{"Content-Type": {"text/html"}}, 
Body: []byte("<h1>Hello Wigwam</h1>"), 
}
}
```

`about.go`:

```go
func Handle(req api.Request) api.Response { 
payload, _ := json.Marshal(map[string]any{"ok": true, "path": req.Path}) 
return api.Response{Status: 200, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: payload}
}
```

---

## Known Limitations

* **Plugins only for Linux/macOS/FreeBSD** – no Windows support
* Host and mods **must be built with the same Go version**
* **No plugin unload** – old versions remain in memory
* and many more ...
