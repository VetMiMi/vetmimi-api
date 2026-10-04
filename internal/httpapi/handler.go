// Package httpapi adapts the generated OpenAPI server interface to the domain
// packages. Handlers parse, call a domain function, and map the result.
package httpapi

import (
	"context"
	"net/http"

	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// Server implements gen.StrictServerInterface.
type Server struct{}

// Handler returns the HTTP handler for every route in openapi.yaml.
func Handler() http.Handler {
	return gen.Handler(gen.NewStrictHandler(&Server{}, nil))
}

// GetHealthz reports that the process is up. Readiness (database, Redis) is a
// separate endpoint added with the platform package.
func (s *Server) GetHealthz(_ context.Context, _ gen.GetHealthzRequestObject) (gen.GetHealthzResponseObject, error) {
	return gen.GetHealthz200JSONResponse{Status: gen.Ok}, nil
}
