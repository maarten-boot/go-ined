package inventory

import (
	"cmp"
	"context"
	"math"
	"slices"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Cell size limits of Arrange (SPEC-510).
const (
	minCell = 20.0
	maxCell = 5000.0
)

// Arrange lays out the direct members of a group (parent nil: the root)
// in a view on a grid of cellW × cellH map units, so that long names and
// wide frames have room (SPEC-510, "Reflow" in the GUI). Members keep
// their rough order: top to bottom, then left to right. A member group
// takes everything in it along. The grid starts where new members go
// (SPEC-106) and has about √n columns. Zero sizes mean the normal grid
// (100 × 80). It is one transaction with one map.arranged event, and
// returns how many members moved.
func (s *Service) Arrange(ctx context.Context, view int64, parent *int64, cellW, cellH float64) (int, error) {
	if cellW == 0 {
		cellW = gridDX
	}
	if cellH == 0 {
		cellH = gridDY
	}
	for _, c := range []float64{cellW, cellH} {
		if math.IsNaN(c) || c < minCell || c > maxCell {
			return 0, invalidf("cell_width and cell_height must be between %g and %g", minCell, maxCell)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m.Views[view] == nil {
		return 0, notFoundf("view %d not found", view)
	}
	p := Root
	ox, oy := rootOriginX, rootOriginY
	if parent != nil {
		p = *parent
		if err := s.checkParent(p, view); err != nil {
			return 0, err
		}
		g := s.m.Groups[p]
		ox, oy = g.X, g.Y+childDY
	}

	// The members: groups and hosts (placements in a custom view) whose
	// parent is p.
	type member struct {
		group bool
		id    int64
		x, y  float64
	}
	vm := s.m.In(view)
	var ms []member
	for _, g := range vm.Groups {
		if g.ParentID == p {
			ms = append(ms, member{true, g.ID, g.X, g.Y})
		}
	}
	for _, h := range vm.Hosts {
		if h.ParentID == p {
			ms = append(ms, member{false, h.ID, h.X, h.Y})
		}
	}
	slices.SortFunc(ms, func(a, b member) int {
		return cmp.Or(cmp.Compare(a.y, b.y), cmp.Compare(a.x, b.x), cmp.Compare(btoi(a.group), btoi(b.group)), cmp.Compare(a.id, b.id))
	})
	cols := max(1, int(math.Ceil(math.Sqrt(float64(len(ms))))))
	now := s.stamp()
	var ch Changes
	moved := 0
	for i, m := range ms {
		nx, ny := ox+float64(i%cols)*cellW, oy+float64(i/cols)*cellH
		if nx == m.x && ny == m.y {
			continue
		}
		moved++
		switch {
		case m.group:
			g := *s.m.Groups[m.id]
			sub := s.subtreeShift(&g, nx-g.X, ny-g.Y, now)
			g.X, g.Y, g.UpdatedAt = nx, ny, now
			ch.Groups = append(append(ch.Groups, g), sub.Groups...)
			ch.Hosts = append(ch.Hosts, sub.Hosts...)
			ch.Placements = append(ch.Placements, sub.Placements...)
		case view == NetworkView:
			h := *s.m.Hosts[m.id]
			h.X, h.Y, h.UpdatedAt = nx, ny, now
			ch.Hosts = append(ch.Hosts, h)
		default:
			pl := *s.m.Placements[view][m.id]
			pl.X, pl.Y = nx, ny
			ch.Placements = append(ch.Placements, pl)
		}
	}
	if moved == 0 {
		return 0, nil
	}
	if err := s.repo.Apply(ctx, ch); err != nil {
		return 0, err
	}
	s.applyShift(ch, view)
	s.notify.Publish(apitypes.EventMapArranged, apitypes.MapArrangedEvent{ViewID: view, ParentID: parent})
	return moved, nil
}

func btoi(b bool) int {
	if b {
		return 0 // groups first on the same spot
	}
	return 1
}
