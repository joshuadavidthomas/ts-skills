package web

import "net/http"

// routes is the complete browser-facing HTTP surface.
func (h *webHandler) routes(static http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", static)
	mux.HandleFunc("GET /", h.catalogPage)
	mux.HandleFunc("GET /skills/{namespace}/{name}", h.skillPage)
	mux.HandleFunc("GET /upload", h.uploadPage)
	mux.HandleFunc("POST /candidates", h.createCandidate)
	mux.HandleFunc("GET /candidates/{candidate}", h.reviewCandidate)
	mux.HandleFunc("POST /candidates/{candidate}/publish", h.publishCandidate)
	mux.HandleFunc("POST /current", h.setCurrent)
	return mux
}
