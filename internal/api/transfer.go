package api

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Export and import of a network (Design-network-export.md N10; SPEC-513).

// MaxImportBytes limits the body of POST /import; other requests keep
// MaxBodyBytes.
const MaxImportBytes = 32 << 20

// exportGroup: GET /groups/{id}/export. The read-only token may export.
func (s *server) exportGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	f, err := s.svc.ExportGroup(id)
	f.ServerVersion = s.opts.Version
	s.respond(w, http.StatusOK, f, err)
}

// importNetwork: POST /import?parent_id=&x=&y=&dry_run=&update=. 200 with
// the report when done or a dry run; 409 with the report when refused for
// conflicts.
func (s *server) importNetwork(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var o inventory.ImportOptions
	var bad string
	if v := q.Get("parent_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			bad = "parent_id must be a positive integer"
		}
		o.ParentID = id
	}
	for _, c := range []struct {
		key string
		dst **float64
	}{{"x", &o.X}, {"y", &o.Y}} {
		if v := q.Get(c.key); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				bad = c.key + " must be a number"
			}
			*c.dst = &f
		}
	}
	for _, c := range []struct {
		key string
		dst *bool
	}{{"dry_run", &o.DryRun}, {"update", &o.Update}} {
		if v := q.Get(c.key); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				bad = c.key + " must be true or false"
			}
			*c.dst = b
		}
	}
	if bad != "" {
		writeError(w, http.StatusBadRequest, apitypes.CodeInvalidArgument, bad)
		return
	}
	var f apitypes.NetworkExport
	if !decode(w, r, &f) {
		return
	}
	rep, err := s.svc.Import(r.Context(), &f, o)
	if errors.Is(err, inventory.ErrImportConflicts) {
		n := len(rep.Conflicts) + rep.MoreConflicts
		writeJSON(w, http.StatusConflict, apitypes.ImportConflict{
			Error: apitypes.ErrorDetail{Code: apitypes.CodeConflict, Message: fmt.Sprintf(
				"%d conflict%s with what is there; nothing was changed (see report.conflicts; update=true updates them)", n, plural(n))},
			Report: rep,
		})
		return
	}
	if err == nil && rep.Done {
		s.log.Info("network imported", "group", *rep.GroupID, "groups", rep.Create.Groups, "hosts", rep.Create.Hosts,
			"view_groups", rep.Create.ViewGroups, "placements", rep.Create.Placements,
			"updated_groups", rep.Update.Groups, "updated_hosts", rep.Update.Hosts)
	}
	s.respond(w, http.StatusOK, rep, err)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
