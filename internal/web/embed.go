// Package web empacota a PWA gerada em apps/web/dist via go:embed.
package web

import (
	"embed"
	"io"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var distFS embed.FS

// DistFS expõe o conteúdo de apps/web/dist.
func DistFS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		log.Fatalf("sub dist: %v", err)
	}
	return sub
}

// Handler seguro para SPA/PWA com headers de segurança e fallback offline.
func Handler() http.Handler {
	static := http.FileServer(http.FS(DistFS()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		// SPA fallback: rotas desconhecidas -> index.html (exceto /api e arquivos reais).
		p := path.Clean("/" + r.URL.Path)
		if !strings.HasPrefix(p, "/api/") && p != "/" {
			if _, err := fs.Stat(DistFS(), strings.TrimPrefix(p, "/")); err != nil {
				// fallback para index.html
				r.URL.Path = "/"
			}
		}
		static.ServeHTTP(w, r)
	})
}

// OfflinePage retorna conteúdo de fallback quando o host (Mac) está offline.
func OfflinePage() []byte {
	f, err := DistFS().Open("index.html")
	if err != nil {
		return []byte(`<!doctype html><html lang="pt-BR"><body><h1>Relay</h1><p>Mac offline.</p></body></html>`)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	return b
}

func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; font-src 'self'; manifest-src 'self'; worker-src 'self';")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Expires", time.Unix(0, 0).UTC().Format(http.TimeFormat))
}
