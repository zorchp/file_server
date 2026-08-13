package main

import (
	"html/template"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// DirHTML renders a plain, server side rendered dir listing, for clients
// that can't run the javascript ui (wget, curl, ...)
type DirHTML struct {
	w    http.ResponseWriter
	d    string // filesystem path of the dir, ends with /
	path string // url path of the dir, ends with /
}

type plainEntry struct {
	Name string
	Href string
}

var plainListing = template.Must(template.New("plain").Parse(
	`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Index of {{.Path}}</title></head>
<body>
<h1>Index of {{.Path}}</h1>
<ul>
{{range .Files}}<li><a href="{{.Href}}">{{.Name}}</a></li>
{{end}}</ul>
</body></html>
`))

// wantsPlainHTML reports whether the client needs a server side rendered
// listing. Browsers announce text/html in Accept, wget/curl don't.
func wantsPlainHTML(r *http.Request) bool {
	if r.FormValue("format") == "html" {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html") == false
}

// Get writes the listing as plain html with real <a href> links
func (t *DirHTML) Get() error {

	thedir, err := os.Open(t.d)
	if err != nil {
		return err
	}
	defer thedir.Close()

	finfo, err := thedir.Readdir(-1)
	if err != nil {
		return err
	}

	files := make([]*plainEntry, 0, len(finfo))
	for _, fi := range finfo {
		name := fi.Name()
		isDir := fi.IsDir()

		// symlinks to dirs must be crawled as dirs
		if fi.Mode()&os.ModeSymlink == os.ModeSymlink {
			if target, err := os.Stat(t.d + name); err == nil && target.IsDir() {
				isDir = true
			}
		}

		href := (&url.URL{Path: name}).String()
		if isDir {
			name = name + "/"
			href = href + "/"
		}
		files = append(files, &plainEntry{Name: name, Href: href})
	}

	t.w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// listings must never be cached, they change with the dir content
	t.w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	t.w.Header().Set("Pragma", "no-cache")
	return plainListing.Execute(t.w, map[string]interface{}{
		"Path":  t.path,
		"Files": files,
	})

}
