package main

import (
	"log"
	"net/http"
	"os"
)

// Minimal handler to serve static web assets (built wasm) on Cloud Run.
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

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(webDir)))

	log.Printf("Listening on :%s, serving %s", port, webDir)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}
