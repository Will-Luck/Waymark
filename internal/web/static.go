package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
)

// asset is one file served from memory, with a gzip copy when that saves bytes.
type asset struct {
	body  []byte
	gz    []byte
	ctype string
	etag  string
}

// assetSet is everything under web/static, keyed by its path below static/.
type assetSet struct {
	hash  string
	files map[string]*asset
}

var contentTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".svg":   "image/svg+xml",
	".woff2": "font/woff2",
	".txt":   "text/plain; charset=utf-8",
	".json":  "application/json",
}

func loadAssets(fsys fs.FS) (*assetSet, error) {
	sub, err := fs.Sub(fsys, "static")
	if err != nil {
		return nil, err
	}
	var paths []string
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	set := &assetSet{files: map[string]*asset{}}
	all := sha256.New()
	for _, p := range paths {
		b, err := fs.ReadFile(sub, p)
		if err != nil {
			return nil, err
		}
		all.Write([]byte(p))
		all.Write([]byte{0})
		all.Write(b)
		set.files[p] = newAsset(b, contentTypes[path.Ext(p)])
	}
	set.hash = hex.EncodeToString(all.Sum(nil))[:12]
	return set, nil
}

func newAsset(b []byte, ctype string) *asset {
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	sum := sha256.Sum256(b)
	a := &asset{body: b, ctype: ctype, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
	if ctype != "font/woff2" && len(b) > 1024 {
		if gz := gzipBytes(b); len(gz) < len(b)*9/10 {
			a.gz = gz
		}
	}
	return a
}

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

// serveAsset writes a with caching that depends on whether the URL carried the
// current hash: a stale hash (an old page after a redeploy) still gets the
// file, but never an immutable one.
func (s *server) serveAsset(w http.ResponseWriter, r *http.Request, a *asset, current bool) {
	h := w.Header()
	h.Set("Content-Type", a.ctype)
	h.Set("ETag", a.etag)
	h.Set("Vary", "Accept-Encoding")
	if current && !s.opts.Dev {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" && inm == a.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := a.body
	if a.gz != nil && acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		body = a.gz
	}
	if r.Method == http.MethodHead {
		return
	}
	w.Write(body)
}

func (s *server) staticFile(w http.ResponseWriter, r *http.Request) {
	a := s.assets.files[r.PathValue("path")]
	if a == nil {
		http.NotFound(w, r)
		return
	}
	s.serveAsset(w, r, a, r.PathValue("hash") == s.assets.hash)
}

func (s *server) dynCSS(w http.ResponseWriter, r *http.Request) {
	d := s.derived(s.src.Snapshot())
	s.serveAsset(w, r, d.css, r.PathValue("hash") == d.cssHash)
}
