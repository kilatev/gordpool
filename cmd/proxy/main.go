package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

func main() {
	target := "https://dataportal-api.nordpoolgroup.com"
	if v := os.Getenv("TARGET"); v != "" {
		target = v
	}
	listen := ":8090"
	if v := os.Getenv("LISTEN"); v != "" {
		listen = v
	}

	u, err := url.Parse(target)
	if err != nil {
		log.Fatalf("invalid TARGET: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(u)
	originalDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		originalDirector(r)
		// Preserve the /api prefix so DayAheadPrices stays under /api/...
		r.URL.Path = singleSlashJoin(u.Path, r.URL.Path)
		r.Host = u.Host
		if r.Header.Get("User-Agent") == "" {
			r.Header.Set("User-Agent", "gordpool-proxy/1.0")
		}
		r.Header.Set("Accept", "application/json")
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// simple CORS for dev
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		proxy.ServeHTTP(w, r)
	})

	log.Printf("Proxying %s via %s (prefix /api)", target, listen)
	if err := http.ListenAndServe(listen, handler); err != nil {
		log.Fatal(err)
	}
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
