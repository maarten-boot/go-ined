package api

import (
	"net/http"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Views and placements (SPEC-117..122).

func (s *server) listViews(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Views())
}

func (s *server) getView(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := s.svc.View(id)
	s.respond(w, http.StatusOK, v, err)
}

func (s *server) createView(w http.ResponseWriter, r *http.Request) {
	var in apitypes.ViewCreate
	if !decode(w, r, &in) {
		return
	}
	v, err := s.svc.CreateView(r.Context(), in)
	s.respond(w, http.StatusCreated, v, err)
}

func (s *server) patchView(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var p apitypes.ViewPatch
	if !decode(w, r, &p) {
		return
	}
	v, err := s.svc.UpdateView(r.Context(), id, p)
	s.respond(w, http.StatusOK, v, err)
}

func (s *server) deleteView(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.respond(w, http.StatusNoContent, nil, s.svc.DeleteView(r.Context(), id))
}

// placeHost serves PUT /views/{id}/hosts/{host_id}.
func (s *server) placeHost(w http.ResponseWriter, r *http.Request) {
	view, ok := pathID(w, r)
	if !ok {
		return
	}
	host, ok := pathInt(w, r, "host_id")
	if !ok {
		return
	}
	var in apitypes.PlacementSet
	if !decode(w, r, &in) {
		return
	}
	p, err := s.svc.PlaceHost(r.Context(), view, host, in)
	s.respond(w, http.StatusOK, p, err)
}

func (s *server) unplaceHost(w http.ResponseWriter, r *http.Request) {
	view, ok := pathID(w, r)
	if !ok {
		return
	}
	host, ok := pathInt(w, r, "host_id")
	if !ok {
		return
	}
	s.respond(w, http.StatusNoContent, nil, s.svc.UnplaceHost(r.Context(), view, host))
}

// arrange serves POST /views/{id}/arrange (SPEC-510).
func (s *server) arrange(w http.ResponseWriter, r *http.Request) {
	view, ok := pathID(w, r)
	if !ok {
		return
	}
	var in apitypes.Arrange
	if !decode(w, r, &in) {
		return
	}
	n, err := s.svc.Arrange(r.Context(), view, in.ParentID, in.CellWidth, in.CellHeight)
	s.respond(w, http.StatusOK, apitypes.ArrangeResult{ViewID: view, ParentID: in.ParentID, Moved: n}, err)
}
