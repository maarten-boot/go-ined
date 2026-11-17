package inventory

import (
	"cmp"
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Export and import of a group of the network view with everything below
// it (Design-network-export.md N1-N11; SPEC-125).

// ErrImportConflicts is returned by Import, with the report, when the
// file conflicts with the inventory and Update is off (N8).
var ErrImportConflicts = &Error{Kind: Conflict, Msg: "the import conflicts with what is there; nothing was changed"}

// pathSep joins group names in messages and From.
const pathSep = " › "

// ---- export ----

// ExportGroup returns group id of the network view with its subgroups and
// hosts as an export file (N1-N5). ServerVersion is left to the caller.
func (s *Service) ExportGroup(id int64) (apitypes.NetworkExport, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	top := s.m.Groups[id]
	switch {
	case top == nil:
		return apitypes.NetworkExport{}, notFoundf("group %d not found", id)
	case top.ViewID != NetworkView:
		return apitypes.NetworkExport{}, invalidf("only groups of the network view can be exported; group %d is in view %d", id, top.ViewID)
	}
	kids, hosts := map[int64][]int64{}, map[int64][]int64{}
	for _, gid := range sortedKeys(s.m.Groups) {
		if g := s.m.Groups[gid]; g.ViewID == NetworkView {
			kids[g.ParentID] = append(kids[g.ParentID], gid)
		}
	}
	for _, hid := range sortedKeys(s.m.Hosts) {
		h := s.m.Hosts[hid]
		hosts[h.ParentID] = append(hosts[h.ParentID], hid)
	}
	views := s.customViewsInOrder()
	vgSeen := map[string]bool{}
	var vgs []apitypes.ExportViewGroup
	var build func(g *Group) apitypes.ExportGroup
	build = func(g *Group) apitypes.ExportGroup {
		e := apitypes.ExportGroup{
			Name: g.Name, Description: g.Description, Note: g.Note, Labels: g.Labels, Color: g.Color,
			Collapsed: g.Collapsed, X: g.X - top.X, Y: g.Y - top.Y,
			Groups: []apitypes.ExportGroup{}, Hosts: []apitypes.ExportHost{},
		}
		if g.IsNetwork() {
			e.CIDR = g.CIDR.String()
		}
		for _, k := range kids[g.ID] {
			e.Groups = append(e.Groups, build(s.m.Groups[k]))
		}
		for _, hid := range hosts[g.ID] {
			h := s.m.Hosts[hid]
			eh := apitypes.ExportHost{
				IP: h.IP.String(), Name: h.Name, Description: h.Description, Note: h.Note, Monitored: h.Monitored,
				Watch: []string{}, X: h.X - top.X, Y: h.Y - top.Y, Views: []apitypes.ExportPlacement{},
			}
			for _, w := range h.Watch {
				eh.Watch = append(eh.Watch, w.Name)
			}
			for _, v := range views {
				p := s.m.Placements[v.ID][hid]
				if p == nil {
					continue
				}
				chain := s.chain(p.ParentID)
				path := make([]string, len(chain))
				for i, cg := range chain {
					path[i] = cg.Name
					key := v.Name + "\x00" + strings.Join(path[:i+1], "\x00")
					if !vgSeen[key] {
						vgSeen[key] = true
						vgs = append(vgs, apitypes.ExportViewGroup{
							View: v.Name, Path: slices.Clone(path[:i+1]), Description: cg.Description, Note: cg.Note,
							Labels: cg.Labels, Color: cg.Color, Collapsed: cg.Collapsed, X: cg.X, Y: cg.Y,
						})
					}
				}
				eh.Views = append(eh.Views, apitypes.ExportPlacement{View: v.Name, Path: path, X: p.X, Y: p.Y})
			}
			e.Hosts = append(e.Hosts, eh)
		}
		return e
	}
	out := apitypes.NetworkExport{
		Format: apitypes.ExportFormat, Version: apitypes.ExportVersion, ExportedAt: s.stamp(),
		From: s.pathOf(top), Group: build(top), ViewGroups: vgs,
	}
	if out.ViewGroups == nil {
		out.ViewGroups = []apitypes.ExportViewGroup{}
	}
	return out, nil
}

// customViewsInOrder returns the custom views in tab order. Caller holds
// the lock.
func (s *Service) customViewsInOrder() []*View {
	var vs []*View
	for _, id := range sortedKeys(s.m.Views) {
		if v := s.m.Views[id]; v.ID != NetworkView {
			vs = append(vs, v)
		}
	}
	slices.SortStableFunc(vs, func(a, b *View) int {
		switch {
		case a.Order < b.Order:
			return -1
		case a.Order > b.Order:
			return 1
		}
		return 0
	})
	return vs
}

// chain returns the groups from the top level down to group id (empty for
// Root). Caller holds the lock.
func (s *Service) chain(id int64) []*Group {
	var out []*Group
	for seen := 0; id != Root && seen <= len(s.m.Groups); seen++ {
		g := s.m.Groups[id]
		if g == nil {
			break
		}
		out = append(out, g)
		id = g.ParentID
	}
	slices.Reverse(out)
	return out
}

// pathOf names a group with its view: "Networks › dc › lan". Caller holds
// the lock.
func (s *Service) pathOf(g *Group) string {
	parts := []string{}
	if v := s.m.Views[g.ViewID]; v != nil {
		parts = append(parts, v.Name)
	}
	for _, c := range s.chain(g.ID) {
		parts = append(parts, c.Name)
	}
	return strings.Join(parts, pathSep)
}

// where names a parent for messages: "the top level" or its path.
func (s *Service) where(parent int64) string {
	if g := s.m.Groups[parent]; g != nil {
		return s.pathOf(g)
	}
	return "the top level"
}

// ---- import ----

// ImportOptions say where an import goes and how (N6-N8).
type ImportOptions struct {
	ParentID int64    // a group of the network view, or Root
	X, Y     *float64 // the top group's position; nil = as a new group
	DryRun   bool     // only report
	Update   bool     // update matching items instead of refusing
}

// impGroup and impHost are the checked items of the file, groups parents
// first.
type impGroup struct {
	src    *apitypes.ExportGroup
	cidr   netip.Prefix
	parent int // index in the group list; -1 for the top group
	loc    string
}

type impHost struct {
	src    *apitypes.ExportHost
	ip     netip.Addr
	parent int
	watch  []string
}

// checkFile checks an export file as API input is checked (N5) and
// flattens it.
func checkFile(f *apitypes.NetworkExport) ([]impGroup, []impHost, map[string]apitypes.ExportViewGroup, error) {
	if f.Format != apitypes.ExportFormat {
		return nil, nil, nil, invalidf("not a go-ined network export (format %q, want %q)", f.Format, apitypes.ExportFormat)
	}
	if f.Version != apitypes.ExportVersion {
		return nil, nil, nil, invalidf("export file version %d; this server reads version %d", f.Version, apitypes.ExportVersion)
	}
	var gs []impGroup
	var hs []impHost
	cidrs, ips := map[netip.Prefix]string{}, map[netip.Addr]string{}
	var walk func(g *apitypes.ExportGroup, parent, depth int, loc string) error
	walk = func(g *apitypes.ExportGroup, parent, depth int, loc string) error {
		name := strings.TrimSpace(g.Name)
		if name == "" {
			name = g.CIDR
		}
		if loc == "" {
			loc = fmt.Sprintf("group %q", name)
		} else {
			loc += pathSep + fmt.Sprintf("%q", name)
		}
		if depth > apitypes.MaxImportDepth {
			return invalidf("%s: more than %d levels of groups", loc, apitypes.MaxImportDepth)
		}
		if len(gs)+len(hs) >= apitypes.MaxImportItems {
			return invalidf("more than %d groups and hosts", apitypes.MaxImportItems)
		}
		cidr, err := ParseCIDR(g.CIDR)
		if err != nil {
			return invalidf("%s: %v", loc, err)
		}
		if cidr.IsValid() {
			if other, dup := cidrs[cidr]; dup {
				return invalidf("%s: CIDR %s is also in %s", loc, cidr, other)
			}
			cidrs[cidr] = loc
		}
		probe := Group{Name: name, Description: g.Description, Note: g.Note, Labels: g.Labels, Color: g.Color}
		if err := checkGroupText(&probe); err != nil {
			return invalidf("%s: %v", loc, err)
		}
		g.Color = probe.Color
		if err := checkPos(g.X, g.Y); err != nil {
			return invalidf("%s: %v", loc, err)
		}
		me := len(gs)
		gs = append(gs, impGroup{src: g, cidr: cidr, parent: parent, loc: loc})
		for i := range g.Hosts {
			h := &g.Hosts[i]
			if len(gs)+len(hs) >= apitypes.MaxImportItems {
				return invalidf("more than %d groups and hosts", apitypes.MaxImportItems)
			}
			ip, err := ParseIP(h.IP)
			hloc := loc + ", host " + h.IP
			if err != nil {
				return invalidf("%s: %v", hloc, err)
			}
			if other, dup := ips[ip]; dup {
				return invalidf("%s: IP address %s is also in %s", hloc, ip, other)
			}
			ips[ip] = loc
			probe := Host{Name: strings.TrimSpace(h.Name), Description: h.Description, Note: h.Note}
			if err := checkHostText(&probe); err != nil {
				return invalidf("%s: %v", hloc, err)
			}
			if err := checkPos(h.X, h.Y); err != nil {
				return invalidf("%s: %v", hloc, err)
			}
			ws, err := apitypes.ParseWatchList(h.Watch)
			if err != nil {
				return invalidf("%s: watch: %v", hloc, err)
			}
			watch := make([]string, len(ws))
			for i, w := range ws {
				watch[i] = w.Name
			}
			seen := map[string]bool{}
			for _, p := range h.Views {
				if strings.TrimSpace(p.View) == "" {
					return invalidf("%s: a placement without a view name", hloc)
				}
				if seen[p.View] {
					return invalidf("%s: view %q is given twice", hloc, p.View)
				}
				seen[p.View] = true
				if err := checkPath(p.Path); err != nil {
					return invalidf("%s: view %q: %v", hloc, p.View, err)
				}
				if err := checkPos(p.X, p.Y); err != nil {
					return invalidf("%s: view %q: %v", hloc, p.View, err)
				}
			}
			hs = append(hs, impHost{src: h, ip: ip, parent: me, watch: watch})
		}
		for i := range g.Groups {
			if err := walk(&g.Groups[i], me, depth+1, loc); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(&f.Group, -1, 1, ""); err != nil {
		return nil, nil, nil, err
	}
	vgs := map[string]apitypes.ExportViewGroup{}
	for i, vg := range f.ViewGroups {
		loc := fmt.Sprintf("view group %d (%q)", i+1, vg.View)
		if err := checkPath(vg.Path); err != nil || len(vg.Path) == 0 {
			if err == nil {
				err = invalidf("an empty path")
			}
			return nil, nil, nil, invalidf("%s: %v", loc, err)
		}
		probe := Group{Name: vg.Path[len(vg.Path)-1], Description: vg.Description, Note: vg.Note, Labels: vg.Labels, Color: vg.Color}
		if err := checkGroupText(&probe); err != nil {
			return nil, nil, nil, invalidf("%s: %v", loc, err)
		}
		vg.Color = probe.Color
		if err := checkPos(vg.X, vg.Y); err != nil {
			return nil, nil, nil, invalidf("%s: %v", loc, err)
		}
		vgs[vgKey(vg.View, vg.Path)] = vg
	}
	return gs, hs, vgs, nil
}

func checkPath(path []string) error {
	if len(path) > apitypes.MaxImportDepth {
		return invalidf("a path of more than %d groups", apitypes.MaxImportDepth)
	}
	for _, n := range path {
		if strings.TrimSpace(n) == "" {
			return invalidf("an empty group name in the path")
		}
	}
	return nil
}

func vgKey(view string, path []string) string {
	return view + "\x00" + strings.Join(path, "\x00")
}

// Import checks an export file and adds it below opt.ParentID of the
// network view in one transaction, or only reports with DryRun (N6-N9).
// With conflicts and Update off it changes nothing and returns the report
// with ErrImportConflicts. A dry run never returns that error; its report
// lists the conflicts.
func (s *Service) Import(ctx context.Context, f *apitypes.NetworkExport, o ImportOptions) (apitypes.ImportReport, error) {
	rep := apitypes.ImportReport{DryRun: o.DryRun, Conflicts: []string{}, Skipped: []string{}, Warnings: []string{}}
	gs, hs, vgs, err := checkFile(f)
	if err != nil {
		return rep, err
	}
	if o.X != nil && o.Y != nil {
		if err := checkPos(*o.X, *o.Y); err != nil {
			return rep, err
		}
	} else if (o.X == nil) != (o.Y == nil) {
		return rep, invalidf("give both x and y, or neither")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if o.ParentID != Root {
		if err := s.checkParent(o.ParentID, NetworkView); err != nil {
			return rep, err
		}
	}
	p := &importPlan{s: s, m: s.m.clone(), o: o, rep: &rep,
		nextGroup: s.nextGroup, nextHost: s.nextHost, created: map[int64]bool{}}
	p.index()
	p.groups(gs)
	p.hosts(hs)
	p.placements(hs, vgs)
	p.warnings(gs[0])

	if n := len(p.conflicts); n > 0 && !o.Update {
		rep.Conflicts = p.conflicts[:min(n, apitypes.MaxListedConflicts)]
		rep.MoreConflicts = n - len(rep.Conflicts)
		if o.DryRun {
			return rep, nil
		}
		return rep, ErrImportConflicts
	}
	// SPEC-114 holds for the new and updated network groups.
	for _, id := range p.nestRoots() {
		if err := p.m.CheckNesting(id); err != nil {
			return rep, err
		}
	}
	if o.DryRun {
		return rep, nil
	}

	ch := p.ch
	ch.NextGroupID, ch.NextHostID = p.nextGroup, p.nextHost
	if err := s.repo.Apply(ctx, ch); err != nil {
		return rep, err
	}
	before := s.m.AggregateAll()
	s.nextGroup, s.nextHost = p.nextGroup, p.nextHost
	for i := range ch.Groups {
		g := ch.Groups[i]
		if cur := s.m.Groups[g.ID]; cur != nil {
			*cur = g
		} else {
			s.m.Groups[g.ID] = &g
		}
	}
	for i := range ch.Hosts {
		h := ch.Hosts[i]
		if cur := s.m.Hosts[h.ID]; cur != nil {
			*cur = h
		} else {
			s.m.Hosts[h.ID] = &h
		}
	}
	for i := range ch.Placements {
		pl := ch.Placements[i]
		if s.m.Placements[pl.ViewID] == nil {
			s.m.Placements[pl.ViewID] = map[int64]*Placement{}
		}
		s.m.Placements[pl.ViewID][pl.HostID] = &pl
	}
	counts := s.m.AggregateAll()
	for _, g := range ch.Groups {
		typ := apitypes.EventGroupCreated
		if !p.created[-g.ID] {
			typ = apitypes.EventGroupUpdated
		}
		s.notify.Publish(typ, groupView(s.m.Groups[g.ID], counts[g.ID]))
	}
	for _, h := range ch.Hosts {
		typ := apitypes.EventHostCreated
		if !p.created[h.ID] {
			typ = apitypes.EventHostUpdated
		}
		cur := s.m.Hosts[h.ID]
		s.notify.Publish(typ, s.hostView(cur))
		s.publishWatch(cur, p.watchReset[h.ID])
	}
	for _, pl := range ch.Placements {
		s.notify.Publish(apitypes.EventPlacementUpdated, placementView(s.m.Placements[pl.ViewID][pl.HostID]))
	}
	s.emitGroupStatus(before)
	rep.Done = true
	top := p.ids[0]
	rep.GroupID = &top
	return rep, nil
}

// importPlan works out an import on a copy of the model. Caller holds the
// lock.
type importPlan struct {
	s   *Service
	m   *Model // the copy, with the planned changes
	o   ImportOptions
	rep *apitypes.ImportReport

	ch                  Changes
	nextGroup, nextHost int64
	ids                 []int64        // per file group: its id on the server
	hostIDs             []int64        // per file host
	matched             []int64        // groups matched by the file
	created             map[int64]bool // new hosts by id, new groups by -id
	conflicts           []string
	watchReset          map[int64][]int // updated hosts: watch entries reset to unknown
	dx, dy              float64

	// Lookups of the groups that were there before the import (the lowest
	// id wins), and of every group of the custom views.
	byCIDR  map[netip.Prefix]*Group
	byName  map[groupKey]*Group // plain groups of the network view
	inViews map[groupKey]int64  // groups of custom views
}

// groupKey finds a group by view, parent and name.
type groupKey struct {
	view, parent int64
	name         string
}

func (p *importPlan) index() {
	p.byCIDR, p.byName, p.inViews = map[netip.Prefix]*Group{}, map[groupKey]*Group{}, map[groupKey]int64{}
	for _, id := range sortedKeys(p.m.Groups) {
		g := p.m.Groups[id]
		k := groupKey{g.ViewID, g.ParentID, g.Name}
		switch {
		case g.ViewID != NetworkView:
			if _, ok := p.inViews[k]; !ok {
				p.inViews[k] = id
			}
		case g.IsNetwork():
			if p.byCIDR[g.CIDR] == nil {
				p.byCIDR[g.CIDR] = g
			}
		default:
			if p.byName[k] == nil {
				p.byName[k] = g
			}
		}
	}
}

// groups matches or creates the file's groups, parents first (N6, N8).
func (p *importPlan) groups(gs []impGroup) {
	nv := p.m.In(NetworkView)
	p.ids = make([]int64, len(gs))
	for i, ig := range gs {
		parent := p.o.ParentID
		if ig.parent >= 0 {
			parent = p.ids[ig.parent]
		}
		src := ig.src
		match := p.byName[groupKey{NetworkView, parent, strings.TrimSpace(src.Name)}]
		if ig.cidr.IsValid() {
			match = p.byCIDR[ig.cidr]
		}
		if i == 0 {
			x, y := 0.0, 0.0
			switch {
			case match != nil:
				x, y = match.X, match.Y
			case p.o.X != nil:
				x, y = *p.o.X, *p.o.Y
			default:
				x, y = nv.FreePosition(parent)
			}
			p.dx, p.dy = x-src.X, y-src.Y
		}
		if match != nil {
			p.ids[i] = match.ID
			p.matched = append(p.matched, match.ID)
			if !p.o.Update {
				if ig.cidr.IsValid() {
					p.conflict("network group %s exists already: group %d in %s", ig.cidr, match.ID, p.s.pathOf(match))
				} else {
					p.conflict("group %q exists already in %s: group %d", match.Name, p.s.where(parent), match.ID)
				}
				continue
			}
			g := *match
			g.Name = cmp.Or(strings.TrimSpace(src.Name), g.Name)
			g.Description, g.Note, g.Labels, g.Color, g.Collapsed = src.Description, src.Note, src.Labels, src.Color, src.Collapsed
			g.UpdatedAt = p.s.stamp()
			if i == 0 && parent != match.ParentID {
				p.rep.Warnings = append(p.rep.Warnings, fmt.Sprintf("%s is updated where it is, in %s, not moved below %s",
					match.label(), p.s.where(match.ParentID), p.s.where(parent)))
			}
			p.m.Groups[g.ID] = &g
			p.ch.Groups = append(p.ch.Groups, g)
			p.rep.Update.Groups++
			continue
		}
		g := Group{
			ID: p.nextGroup, ViewID: NetworkView, ParentID: parent, CIDR: ig.cidr,
			Name: strings.TrimSpace(src.Name), Description: src.Description, Note: src.Note,
			Labels: src.Labels, Color: src.Color, Collapsed: src.Collapsed,
			X: src.X + p.dx, Y: src.Y + p.dy, CreatedAt: p.s.stamp(),
		}
		if g.Name == "" {
			g.Name = ig.cidr.String()
		}
		g.UpdatedAt = g.CreatedAt
		p.nextGroup++
		p.ids[i] = g.ID
		p.created[-g.ID] = true
		p.m.Groups[g.ID] = &g
		p.ch.Groups = append(p.ch.Groups, g)
		p.rep.Create.Groups++
	}
}

// hosts matches (by IP address) or creates the file's hosts (N8).
func (p *importPlan) hosts(hs []impHost) {
	p.hostIDs = make([]int64, len(hs))
	p.watchReset = map[int64][]int{}
	byIP := make(map[netip.Addr]*Host, len(p.m.Hosts))
	for _, h := range p.m.Hosts {
		byIP[h.IP] = h
	}
	for i, ih := range hs {
		src := ih.src
		parent := p.ids[ih.parent]
		x, y := src.X+p.dx, src.Y+p.dy
		if match := byIP[ih.ip]; match != nil {
			p.hostIDs[i] = match.ID
			if !p.o.Update {
				p.conflict("IP address %s exists already: host %d %q in %s", ih.ip, match.ID, match.DisplayName(), p.s.where(match.ParentID))
				continue
			}
			h := *match
			h.Name, h.Description, h.Note = strings.TrimSpace(src.Name), src.Description, src.Note
			h.ParentID, h.X, h.Y = parent, x, y
			h.Watch, _ = parseWatch(ih.watch, match.Watch)
			if src.Monitored != h.Monitored {
				h.Monitored, h.Stale = src.Monitored, src.Monitored
				if !h.Monitored {
					h.Watch, p.watchReset[h.ID] = resetWatch(h.Watch, p.s.stamp(), false)
				}
			}
			h.UpdatedAt = p.s.stamp()
			p.m.Hosts[h.ID] = &h
			p.ch.Hosts = append(p.ch.Hosts, h)
			p.rep.Update.Hosts++
			continue
		}
		ws, _ := parseWatch(ih.watch, nil)
		h := Host{
			ID: p.nextHost, IP: ih.ip, Name: strings.TrimSpace(src.Name), Description: src.Description, Note: src.Note,
			Monitored: src.Monitored, Status: apitypes.StatusUnknown, ParentID: parent, X: x, Y: y, Watch: ws,
			CreatedAt: p.s.stamp(),
		}
		h.UpdatedAt = h.CreatedAt
		p.nextHost++
		p.hostIDs[i] = h.ID
		p.created[h.ID] = true
		byIP[h.IP] = &h
		p.m.Hosts[h.ID] = &h
		p.ch.Hosts = append(p.ch.Hosts, h)
		p.rep.Create.Hosts++
	}
}

// placements puts the hosts into custom views by name and path, making
// missing groups on the path; views that don't exist are skipped (N4).
func (p *importPlan) placements(hs []impHost, vgs map[string]apitypes.ExportViewGroup) {
	byName := map[string]*View{}
	for _, v := range p.s.customViewsInOrder() {
		if byName[v.Name] == nil {
			byName[v.Name] = v
		}
	}
	skipped := map[string]int{}
	var skippedOrder []string
	for i, ih := range hs {
		hid := p.hostIDs[i]
		if !p.o.Update && !p.created[hid] {
			continue // a conflict: placed nowhere
		}
		for _, pl := range ih.src.Views {
			v := byName[pl.View]
			if v == nil {
				if skipped[pl.View] == 0 {
					skippedOrder = append(skippedOrder, pl.View)
				}
				skipped[pl.View]++
				continue
			}
			parent := Root
			for j, name := range pl.Path {
				parent = p.viewGroup(v, parent, pl.Path[:j+1], strings.TrimSpace(name), vgs)
			}
			np := Placement{ViewID: v.ID, HostID: hid, ParentID: parent, X: pl.X, Y: pl.Y}
			if p.m.Placements[v.ID][hid] != nil {
				p.rep.Update.Placements++
			} else {
				p.rep.Create.Placements++
			}
			if p.m.Placements[v.ID] == nil {
				p.m.Placements[v.ID] = map[int64]*Placement{}
			}
			p.m.Placements[v.ID][hid] = &np
			p.ch.Placements = append(p.ch.Placements, np)
		}
	}
	for _, name := range skippedOrder {
		n := skipped[name]
		p.rep.Skipped = append(p.rep.Skipped, fmt.Sprintf("view %q doesn't exist (%d placement%s)", name, n, plural(n)))
	}
}

// viewGroup finds group name below parent in custom view v, or makes it.
func (p *importPlan) viewGroup(v *View, parent int64, path []string, name string, vgs map[string]apitypes.ExportViewGroup) int64 {
	k := groupKey{v.ID, parent, name}
	if id, ok := p.inViews[k]; ok {
		return id
	}
	g := Group{ID: p.nextGroup, ViewID: v.ID, ParentID: parent, Name: name, Collapsed: true, CreatedAt: p.s.stamp()}
	if vg, ok := vgs[vgKey(v.Name, path)]; ok {
		g.Description, g.Note, g.Labels, g.Color, g.Collapsed, g.X, g.Y = vg.Description, vg.Note, vg.Labels, vg.Color, vg.Collapsed, vg.X, vg.Y
	} else {
		g.X, g.Y = p.m.In(v.ID).FreePosition(parent)
	}
	g.UpdatedAt = g.CreatedAt
	p.nextGroup++
	p.created[-g.ID] = true
	p.inViews[k] = g.ID
	p.m.Groups[g.ID] = &g
	p.ch.Groups = append(p.ch.Groups, g)
	p.rep.Create.ViewGroups++
	return g.ID
}

// warnings: a more specific network group for the imported CIDR (N6).
func (p *importPlan) warnings(top impGroup) {
	if !top.cidr.IsValid() {
		return
	}
	var best *Group
	for _, id := range sortedKeys(p.s.m.Groups) {
		g := p.s.m.Groups[id]
		if !g.IsNetwork() || g.CIDR == top.cidr || !PrefixContains(g.CIDR, top.cidr) {
			continue
		}
		if best == nil || g.CIDR.Bits() > best.CIDR.Bits() {
			best = g
		}
	}
	if best != nil && !p.s.m.IsInSubtree(p.o.ParentID, best.ID) {
		p.rep.Warnings = append(p.rep.Warnings, fmt.Sprintf("%s (%s) would be a more specific home for %s than %s",
			best.CIDR, p.s.pathOf(best), top.cidr, p.s.where(p.o.ParentID)))
	}
}

// nestRoots are the groups whose subtrees hold every new or updated network
// group: the top group, and matched groups outside it.
func (p *importPlan) nestRoots() []int64 {
	roots := []int64{p.ids[0]}
	for _, id := range p.matched {
		if !p.m.IsInSubtree(id, p.ids[0]) && !slices.Contains(roots, id) {
			roots = append(roots, id)
		}
	}
	return roots
}

func (p *importPlan) conflict(format string, a ...any) {
	p.conflicts = append(p.conflicts, fmt.Sprintf(format, a...))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
