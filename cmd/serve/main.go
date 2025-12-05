package main

import (
	"flag"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// serve combines static file hosting for /web and a /api/* reverse proxy to avoid CORS.
func main() {
	var (
		listen  = flag.String("listen", ":8080", "address to listen on")
		webDir  = flag.String("web", "./web", "directory to serve static files from")
		target  = flag.String("target", "https://dataportal-api.nordpoolgroup.com", "upstream API base")
		apiBase = flag.String("api-base", "/api/", "API prefix to proxy")
	)
	flag.Parse()

	if env := os.Getenv("LISTEN"); env != "" {
		*listen = env
	}
	if env := os.Getenv("WEB_DIR"); env != "" {
		*webDir = env
	}
	if env := os.Getenv("TARGET"); env != "" {
		*target = env
	}

	u, err := url.Parse(*target)
	if err != nil {
		log.Fatalf("invalid target: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(u)
	orig := proxy.Director
	proxy.Director = func(r *http.Request) {
		orig(r)
		// keep the full /api/... path when forwarding, joined with upstream base path
		r.URL.Path = singleSlashJoin(u.Path, r.URL.Path)
		r.Host = u.Host
		if r.Header.Get("User-Agent") == "" {
			r.Header.Set("User-Agent", "gordpool-proxy/1.0")
		}
		r.Header.Set("Accept", "application/json")
	}

	mux := http.NewServeMux()
	mux.Handle(*apiBase, cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, *apiBase) {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	})))

	absWeb, err := filepath.Abs(*webDir)
	if err != nil {
		log.Fatalf("resolve web dir: %v", err)
	}
	fs := http.FileServer(http.Dir(absWeb))
	mux.Handle("/", fs)

	log.Printf("Serving static files from %s at %s", absWeb, *listen)
	log.Printf("Proxying %s at %s*", *target, *apiBase)
	if err := http.ListenAndServe(*listen, mux); err != nil {
		log.Fatal(err)
	}
}

// cors wraps a handler with permissive CORS (dev only).
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// singleSlashJoin joins base and path with exactly one slash.
func singleSlashJoin(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	default:
		return a + b
	}
}
