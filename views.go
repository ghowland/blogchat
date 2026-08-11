package main

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"github.com/cbroglie/mustache"
)

//go:embed tmpl/*.mustache
var tmplFS embed.FS

// Views holds every template, parsed once at startup.
type Views struct {
	items map[string]*mustache.Template
}

// LoadViews parses each file in tmpl. Each file is also a partial, so a
// page can include another page fragment with the {{> name}} form.
func LoadViews() (*Views, error) {
	entries, err := fs.ReadDir(tmplFS, "tmpl")
	if err != nil {
		return nil, fmt.Errorf("read template directory: %w", err)
	}

	source := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".mustache") {
			continue
		}
		raw, err := tmplFS.ReadFile("tmpl/" + entry.Name())
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(entry.Name(), ".mustache")
		source[name] = string(raw)
	}

	provider := &mustache.StaticProvider{Partials: source}
	views := &Views{items: make(map[string]*mustache.Template, len(source))}
	for name, text := range source {
		tpl, err := mustache.ParseStringPartials(text, provider)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", name, err)
		}
		views.items[name] = tpl
	}
	return views, nil
}

// Base makes the context that every page needs. The caller adds the page
// fields to the returned map.
func (app *App) Base(req *http.Request, usr *User) map[string]any {
	conf := app.Conf()
	ctx := map[string]any{
		"site_name": conf.SiteName,
		"footer":    conf.Footer,
		"terms":     conf.Terms,
		"handle":    "",
		"authed":    false,
		"csrf":      CSRFFromContext(req),
	}
	if usr != nil {
		ctx["handle"] = usr.Handle
		ctx["authed"] = true
	}
	return ctx
}

// Render writes a page. The render goes into a buffer first, so a failure
// in the middle of a template does not produce a partial page with a
// success status.
func (app *App) Render(res http.ResponseWriter, req *http.Request, name string, ctx map[string]any) {
	tpl, found := app.views.items[name]
	if !found {
		log.Printf("render: template %q missing, path=%s", name, req.URL.Path)
		plainError(res, http.StatusInternalServerError, "server error")
		return
	}

	var buf bytes.Buffer
	if err := tpl.FRender(&buf, ctx); err != nil {
		log.Printf("render: template %q failed: %v, path=%s", name, err, req.URL.Path)
		plainError(res, http.StatusInternalServerError, "server error")
		return
	}

	res.Header().Set("Content-Type", "text/html; charset=utf-8")
	res.Header().Set("X-Content-Type-Options", "nosniff")
	res.Header().Set("Referrer-Policy", "same-origin")
	res.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'self'; form-action 'self'; base-uri 'none'")
	res.WriteHeader(http.StatusOK)
	res.Write(buf.Bytes())
}

// Fail sends an error page. The body never contains an internal error text.
func (app *App) Fail(res http.ResponseWriter, req *http.Request, code int, msg string) {
	tpl, found := app.views.items["error"]
	if !found {
		plainError(res, code, msg)
		return
	}
	ctx := app.Base(req, nil)
	ctx["code"] = code
	ctx["msg"] = msg

	var buf bytes.Buffer
	if err := tpl.FRender(&buf, ctx); err != nil {
		plainError(res, code, msg)
		return
	}
	res.Header().Set("Content-Type", "text/html; charset=utf-8")
	res.Header().Set("X-Content-Type-Options", "nosniff")
	res.WriteHeader(code)
	res.Write(buf.Bytes())
}

func plainError(res http.ResponseWriter, code int, msg string) {
	res.Header().Set("Content-Type", "text/plain; charset=utf-8")
	res.Header().Set("X-Content-Type-Options", "nosniff")
	res.WriteHeader(code)
	fmt.Fprintln(res, msg)
}

