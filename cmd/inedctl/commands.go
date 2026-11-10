package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
	"github.com/maarten-boot/go-ined/pkg/client"
)

type command func(a *app, name string, args []string) error

var commands = map[string]map[string]command{
	"host": {
		"list": hostList, "add": hostAdd, "show": hostShow, "edit": hostEdit,
		"rm": hostRm, "move": hostMove,
	},
	"group": {
		"list": groupList, "add": groupAdd, "show": groupShow, "edit": groupEdit,
		"rm": groupRm, "move": groupMove, "collapse": groupCollapse(true), "expand": groupCollapse(false),
	},
	"note": {"get": noteGet, "set": noteSet, "clear": noteClear},
	"discover": {
		"start": discoverStart, "status": discoverStatus, "cancel": discoverCancel,
	},
}

func path(kind string, id int64) string { return fmt.Sprintf("/%ss/%d", kind, id) }

// ---- hosts ----

func hostList(a *app, name string, args []string) error {
	if _, err := a.parse(a.flags(name), args, 0, 0); err != nil {
		return err
	}
	var hs []apitypes.Host
	return a.call(http.MethodGet, "/hosts", nil, &hs, func() { a.hostTable(hs) })
}

func hostAdd(a *app, name string, args []string) error {
	fs := a.flags(name)
	var in apitypes.HostCreate
	fs.StringVar(&in.Name, "name", "", "name")
	fs.StringVar(&in.Description, "description", "", "description")
	fs.StringVar(&in.Note, "note", "", "note")
	parent := fs.Int64("parent", 0, "parent group id (default: the matching network group)")
	unmon := fs.Bool("unmonitored", false, "don't ping the host")
	pos, err := a.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	in.IP = pos[0]
	if set(fs)["parent"] {
		if *parent < 1 {
			return usagef("%s: --parent must be a positive group id", name)
		}
		in.ParentID = parent
	}
	if *unmon {
		in.Monitored = new(bool)
	}
	var h apitypes.Host
	return a.call(http.MethodPost, "/hosts", in, &h, func() { a.hostDetails(h) })
}

func hostShow(a *app, name string, args []string) error {
	id, err := a.oneID(name, args)
	if err != nil {
		return err
	}
	var h apitypes.Host
	return a.call(http.MethodGet, path("host", id), nil, &h, func() { a.hostDetails(h) })
}

func hostEdit(a *app, name string, args []string) error {
	fs := a.flags(name)
	ip := fs.String("ip", "", "new IP address")
	nm := fs.String("name", "", "name (empty: use the PTR name or IP)")
	desc := fs.String("description", "", "description")
	mon := fs.Bool("monitored", true, "ping the host")
	pos, err := a.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	id, err := parseID(pos[0])
	if err != nil {
		return err
	}
	var p apitypes.HostPatch
	given := set(fs)
	if given["ip"] {
		p.IP = apitypes.Some(*ip)
	}
	if given["name"] {
		p.Name = apitypes.Some(*nm)
	}
	if given["description"] {
		p.Description = apitypes.Some(*desc)
	}
	if given["monitored"] {
		p.Monitored = apitypes.Some(*mon)
	}
	if p == (apitypes.HostPatch{}) {
		return usagef("%s: nothing to change (give --ip, --name, --description or --monitored)", name)
	}
	var h apitypes.Host
	return a.call(http.MethodPatch, path("host", id), p, &h, func() { a.hostDetails(h) })
}

func hostRm(a *app, name string, args []string) error {
	id, err := a.oneID(name, args)
	if err != nil {
		return err
	}
	return a.call(http.MethodDelete, path("host", id), nil, nil, nil)
}

func hostMove(a *app, name string, args []string) error {
	id, parent, err := a.move(name, args)
	if err != nil {
		return err
	}
	var h apitypes.Host
	return a.call(http.MethodPatch, path("host", id), apitypes.HostPatch{ParentID: apitypes.Some(parent)}, &h,
		func() { a.hostDetails(h) })
}

// ---- groups ----

func groupList(a *app, name string, args []string) error {
	if _, err := a.parse(a.flags(name), args, 0, 0); err != nil {
		return err
	}
	var gs []apitypes.Group
	return a.call(http.MethodGet, "/groups", nil, &gs, func() { a.groupTable(gs) })
}

func groupAdd(a *app, name string, args []string) error {
	fs := a.flags(name)
	var in apitypes.GroupCreate
	fs.StringVar(&in.CIDR, "cidr", "", "network range: makes a network group")
	fs.StringVar(&in.Name, "name", "", "name (default: the CIDR)")
	fs.StringVar(&in.Description, "description", "", "description")
	fs.StringVar(&in.Note, "note", "", "note")
	parent := fs.Int64("parent", 0, "parent group id (default: the root)")
	expanded := fs.Bool("expanded", false, "create the group expanded")
	if _, err := a.parse(fs, args, 0, 0); err != nil {
		return err
	}
	if in.CIDR == "" && in.Name == "" {
		return usagef("%s: give --cidr or --name", name)
	}
	if set(fs)["parent"] {
		if *parent < 1 {
			return usagef("%s: --parent must be a positive group id", name)
		}
		in.ParentID = parent
	}
	if *expanded {
		in.Collapsed = new(bool)
	}
	var g apitypes.Group
	return a.call(http.MethodPost, "/groups", in, &g, func() { a.groupDetails(g) })
}

func groupShow(a *app, name string, args []string) error {
	id, err := a.oneID(name, args)
	if err != nil {
		return err
	}
	var g apitypes.Group
	return a.call(http.MethodGet, path("group", id), nil, &g, func() { a.groupDetails(g) })
}

func groupEdit(a *app, name string, args []string) error {
	fs := a.flags(name)
	nm := fs.String("name", "", "name")
	cidr := fs.String("cidr", "", `network range; "" makes it an ordinary group`)
	desc := fs.String("description", "", "description")
	pos, err := a.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	id, err := parseID(pos[0])
	if err != nil {
		return err
	}
	var p apitypes.GroupPatch
	given := set(fs)
	if given["name"] {
		p.Name = apitypes.Some(*nm)
	}
	if given["cidr"] {
		p.CIDR = apitypes.Some(*cidr)
	}
	if given["description"] {
		p.Description = apitypes.Some(*desc)
	}
	if p == (apitypes.GroupPatch{}) {
		return usagef("%s: nothing to change (give --name, --cidr or --description)", name)
	}
	var g apitypes.Group
	return a.call(http.MethodPatch, path("group", id), p, &g, func() { a.groupDetails(g) })
}

func groupRm(a *app, name string, args []string) error {
	fs := a.flags(name)
	reparent := fs.Bool("reparent", false, "move the contents to the parent group")
	cascade := fs.Bool("cascade", false, "delete the contents too")
	pos, err := a.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	id, err := parseID(pos[0])
	if err != nil {
		return err
	}
	children := ""
	switch {
	case *reparent && *cascade:
		return usagef("%s: give --reparent or --cascade, not both", name)
	case *reparent:
		children = apitypes.ChildrenReparent
	case *cascade:
		children = apitypes.ChildrenCascade
	}
	return a.call(http.MethodDelete, client.GroupDeletePath(id, children), nil, nil, nil)
}

func groupMove(a *app, name string, args []string) error {
	id, parent, err := a.move(name, args)
	if err != nil {
		return err
	}
	var g apitypes.Group
	return a.call(http.MethodPatch, path("group", id), apitypes.GroupPatch{ParentID: apitypes.Some(parent)}, &g,
		func() { a.groupDetails(g) })
}

func groupCollapse(collapsed bool) command {
	return func(a *app, name string, args []string) error {
		id, err := a.oneID(name, args)
		if err != nil {
			return err
		}
		var g apitypes.Group
		return a.call(http.MethodPatch, path("group", id), apitypes.GroupPatch{Collapsed: apitypes.Some(collapsed)}, &g,
			func() { a.groupDetails(g) })
	}
}

// ---- notes ----

// noteTarget parses "host|group <id>" and returns the item's path.
func noteTarget(name string, pos []string) (string, error) {
	kind := pos[0]
	if kind != "host" && kind != "group" {
		return "", usagef("%s: want host or group, not %q", name, kind)
	}
	id, err := parseID(pos[1])
	if err != nil {
		return "", err
	}
	return path(kind, id), nil
}

// noted is the part of a host or group that holds the note.
type noted struct {
	Note string `json:"note"`
}

func noteGet(a *app, name string, args []string) error {
	pos, err := a.parse(a.flags(name), args, 2, 2)
	if err != nil {
		return err
	}
	p, err := noteTarget(name, pos)
	if err != nil {
		return err
	}
	var n noted
	return a.call(http.MethodGet, p, nil, &n, func() {
		if n.Note != "" {
			io.WriteString(a.out, n.Note)
			if !strings.HasSuffix(n.Note, "\n") {
				io.WriteString(a.out, "\n")
			}
		}
	})
}

// noteSet sets the note from the arguments, or from stdin without them.
// Trailing newlines from stdin are removed.
func noteSet(a *app, name string, args []string) error {
	pos, err := a.parse(a.flags(name), args, 2, -1)
	if err != nil {
		return err
	}
	p, err := noteTarget(name, pos)
	if err != nil {
		return err
	}
	text := strings.Join(pos[2:], " ")
	if len(pos) == 2 {
		b, err := io.ReadAll(io.LimitReader(a.stdin, 1<<20))
		if err != nil {
			return fmt.Errorf("reading the note from stdin: %w", err)
		}
		text = strings.TrimRight(string(b), "\r\n")
	}
	return a.call(http.MethodPatch, p, map[string]string{"note": text}, nil, nil)
}

func noteClear(a *app, name string, args []string) error {
	pos, err := a.parse(a.flags(name), args, 2, 2)
	if err != nil {
		return err
	}
	p, err := noteTarget(name, pos)
	if err != nil {
		return err
	}
	return a.call(http.MethodPatch, p, map[string]string{"note": ""}, nil, nil)
}

// ---- discovery ----

func discoverStart(a *app, name string, args []string) error {
	fs := a.flags(name)
	group := fs.Int64("group", 0, "discover the range of this network group")
	parent := fs.Int64("parent", 0, "where a new network group for the CIDR is created")
	wait := fs.Bool("wait", false, "show progress until the job ends")
	pos, err := a.parse(fs, args, 0, 1)
	if err != nil {
		return err
	}
	var in apitypes.DiscoveryCreate
	given := set(fs)
	switch {
	case len(pos) == 1 && given["group"], len(pos) == 0 && !given["group"]:
		return usagef("%s: give a CIDR or --group, not both", name)
	case len(pos) == 1:
		in.CIDR = pos[0]
	default:
		if *group < 1 {
			return usagef("%s: --group must be a positive group id", name)
		}
		in.GroupID = group
	}
	if given["parent"] {
		if *parent < 1 {
			return usagef("%s: --parent must be a positive group id", name)
		}
		in.ParentID = parent
	}
	if !*wait {
		var d apitypes.Discovery
		return a.call(http.MethodPost, "/discovery", in, &d, func() { fmt.Fprintln(a.out, client.DiscoveryLine(d)) })
	}
	return a.discoverWait(in)
}

// discoverWait starts a job and follows it until it ends (SPEC-901). The
// event stream is opened first, so no event of the job is missed.
func (a *app) discoverWait(in apitypes.DiscoveryCreate) error {
	st := a.c.Events(a.ctx, client.StreamOptions{FailFirst: true, OnState: a.streamState})
	defer st.Close()
	select {
	case <-st.Ready():
	case <-st.C(): // ended before connecting
		if err := st.Err(); err != nil {
			return err
		}
		return a.ctx.Err()
	}
	var raw json.RawMessage
	if err := a.c.Do(a.ctx, http.MethodPost, "/discovery", in, &raw); err != nil {
		return err
	}
	var job apitypes.Discovery
	if err := json.Unmarshal(raw, &job); err != nil {
		return fmt.Errorf("decoding the job: %w", err)
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) == nil {
		raw = compact.Bytes()
	}
	a.jobLine(job, raw) // with --json: the API's response, on one line
	for ev := range st.C() {
		var d apitypes.Discovery
		switch ev.Type {
		case apitypes.EventDiscoveryProgress, apitypes.EventDiscoveryFinished:
			if ev.Decode(&d) != nil || d.ID != job.ID {
				continue
			}
			a.jobLine(d, ev.Data)
		case apitypes.EventResync:
			// Events were lost: ask for the job's state.
			var err error
			if d, err = a.c.Discovery(a.ctx, job.ID); err != nil {
				if a.ctx.Err() != nil {
					return jobInterrupted(job.ID)
				}
				return err
			}
			a.jobLine(d, nil)
		default:
			continue
		}
		if d.State != apitypes.DiscoveryRunning {
			if d.State != apitypes.DiscoveryDone {
				return errJob
			}
			return nil
		}
	}
	if err := st.Err(); err != nil {
		return err
	}
	if a.ctx.Err() != nil {
		return jobInterrupted(job.ID)
	}
	return errors.New("event stream ended")
}

func jobInterrupted(id string) error {
	return interrupted{fmt.Sprintf("interrupted; job %s keeps running on the server (inedctl discover cancel %s)", id, id)}
}

// jobLine prints a job's state: the summary line, or with --json the job
// as one line of JSON (data, the response or event payload, if not nil).
func (a *app) jobLine(d apitypes.Discovery, data json.RawMessage) {
	if !a.g.json {
		fmt.Fprintln(a.out, client.DiscoveryLine(d))
		return
	}
	if data == nil {
		data, _ = json.Marshal(d)
	}
	fmt.Fprintf(a.out, "%s\n", data)
}

func discoverStatus(a *app, name string, args []string) error {
	pos, err := a.parse(a.flags(name), args, 1, 1)
	if err != nil {
		return err
	}
	var d apitypes.Discovery
	return a.call(http.MethodGet, "/discovery/"+url.PathEscape(pos[0]), nil, &d,
		func() { fmt.Fprintln(a.out, client.DiscoveryLine(d)) })
}

func discoverCancel(a *app, name string, args []string) error {
	pos, err := a.parse(a.flags(name), args, 1, 1)
	if err != nil {
		return err
	}
	var d apitypes.Discovery
	return a.call(http.MethodDelete, "/discovery/"+url.PathEscape(pos[0]), nil, &d,
		func() { fmt.Fprintln(a.out, client.DiscoveryLine(d)) })
}

// ---- events and health ----

// events prints the event stream until interrupted (exit 0).
func (a *app) events(args []string) error {
	fs := a.flags("events")
	last := fs.String("last-id", "", "resume after this event id")
	if _, err := a.parse(fs, args, 0, 0); err != nil {
		return err
	}
	st := a.c.Events(a.ctx, client.StreamOptions{LastEventID: *last, FailFirst: true, OnState: a.streamState})
	defer st.Close()
	for ev := range st.C() {
		if a.g.json {
			b, _ := json.Marshal(struct {
				ID   string          `json:"id"`
				Type string          `json:"type"`
				Data json.RawMessage `json:"data"`
			}{ev.ID, ev.Type, ev.Data})
			fmt.Fprintf(a.out, "%s\n", b)
			continue
		}
		fmt.Fprintf(a.out, "%s %-18s %-12s %s\n", time.Now().Format("15:04:05"), ev.Type, ev.ID, ev.Short())
	}
	return st.Err()
}

// streamState reports a lost event stream on stderr.
func (a *app) streamState(s client.StreamState) {
	if !s.Connected {
		fmt.Fprintf(a.errOut, "inedctl: event stream lost (%v); retrying in %s\n", s.Err, s.RetryIn)
	}
}

func (a *app) health(args []string) error {
	if _, err := a.parse(a.flags("health"), args, 0, 0); err != nil {
		return err
	}
	var h apitypes.Health
	return a.call(http.MethodGet, "/health", nil, &h, func() {
		a.details([][2]string{
			{"Status", h.Status}, {"Version", h.Version}, {"ICMP mode", h.ICMPMode},
			{"ICMP error", h.ICMPError}, {"ICMPv6 error", h.ICMP6Error}, {"Database", h.DB}, {"Run id", h.RunID},
		}, true)
	})
}

// ---- shared argument helpers ----

func (a *app) oneID(name string, args []string) (int64, error) {
	pos, err := a.parse(a.flags(name), args, 1, 1)
	if err != nil {
		return 0, err
	}
	return parseID(pos[0])
}

func (a *app) move(name string, args []string) (int64, *int64, error) {
	pos, err := a.parse(a.flags(name), args, 2, 2)
	if err != nil {
		return 0, nil, err
	}
	id, err := parseID(pos[0])
	if err != nil {
		return 0, nil, err
	}
	parent, err := parseParent(pos[1])
	return id, parent, err
}
