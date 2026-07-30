package api

import (
	"net/http"

	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
)

// routes is the complete machine-readable HTTP surface. The protocol package
// owns the canonical patterns because clients and servers share that contract.
func (h *apiHandler) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(protocol.CurrentPattern, h.currentPublication)
	mux.HandleFunc(protocol.TreePattern, h.publicationTree)
	return mux
}
