package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"

	"gordpool/pkg/api"
)

// Minimal handler to serve static web assets (built wasm) and the battery
// planning API (/v1/battery/*) on Cloud Run.
// If you want the reverse proxy too, deploy cmd/serve instead.
func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	webDir := os.Getenv("WEB_DIR")
	if webDir == "" {
		webDir = "./web"
	}

	// The image runs as nonroot with a read-only /app, so default the cache to tmp.
	cacheDB := os.Getenv("CACHE_DB")
	if cacheDB == "" {
		cacheDB = filepath.Join(os.TempDir(), "gordpool", "prices.db")
	}

	mux := http.NewServeMux()
	api.NewServer(cacheDB).Register(mux)
	mux.Handle("/", http.FileServer(http.Dir(webDir)))

	log.Printf("Listening on :%s, serving %s", port, webDir)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}
