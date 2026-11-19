package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
	"github.com/maarten-boot/go-ined/pkg/client"
)

// snmpCheck starts an SNMP check of a group or hosts (SPEC-515); --wait
// follows it until it ends (exit 1 when cancelled).
func snmpCheck(a *app, name string, args []string) error {
	fs := a.flags(name)
	group := fs.Int64("group", 0, "check the hosts of this group and its subgroups")
	wait := fs.Bool("wait", false, "follow the check until it ends")
	pos, err := a.parse(fs, args, 0, -1)
	if err != nil {
		return err
	}
	var in apitypes.SNMPCheckCreate
	switch {
	case *group != 0 && len(pos) > 0, *group == 0 && len(pos) == 0:
		return usagef("%s: give --group ID or host ids", name)
	case *group != 0:
		in.GroupID = group
	default:
		for _, p := range pos {
			id, err := parseID(p)
			if err != nil {
				return err
			}
			in.HostIDs = append(in.HostIDs, id)
		}
	}
	if !*wait {
		var j apitypes.SNMPCheck
		return a.call(http.MethodPost, "/snmp/checks", in, &j, func() { fmt.Fprintln(a.out, client.SNMPCheckLine(j)) })
	}
	// With --wait the job is read in --json mode too: one JSON line for the
	// start and one for the end (crosscheck 2026-11-19-01 C2).
	var j apitypes.SNMPCheck
	if err := a.c.Do(a.ctx, http.MethodPost, "/snmp/checks", in, &j); err != nil {
		return err
	}
	a.jobOut(j)
	last := client.SNMPCheckLine(j)
	for j.State == apitypes.DiscoveryRunning {
		select {
		case <-a.ctx.Done():
			return interrupted{fmt.Sprintf("interrupted; check %s keeps running on the server (inedctl snmp cancel %s)", j.ID, j.ID)}
		case <-time.After(time.Second):
		}
		if err := a.c.Do(a.ctx, http.MethodGet, "/snmp/checks/"+url.PathEscape(j.ID), nil, &j); err != nil {
			return err
		}
		if l := client.SNMPCheckLine(j); l != last && !a.g.json {
			fmt.Fprintln(a.out, l)
			last = l
		}
	}
	if a.g.json {
		a.jobOut(j)
	}
	if j.State != apitypes.DiscoveryDone {
		return errJob
	}
	return nil
}

func snmpList(a *app, name string, args []string) error {
	if _, err := a.parse(a.flags(name), args, 0, 0); err != nil {
		return err
	}
	var js []apitypes.SNMPCheck
	return a.call(http.MethodGet, "/snmp/checks", nil, &js, func() {
		for _, j := range js {
			fmt.Fprintln(a.out, client.SNMPCheckLine(j))
		}
	})
}

func snmpStatus(a *app, name string, args []string) error {
	pos, err := a.parse(a.flags(name), args, 1, 1)
	if err != nil {
		return err
	}
	var j apitypes.SNMPCheck
	return a.call(http.MethodGet, "/snmp/checks/"+url.PathEscape(pos[0]), nil, &j, func() { fmt.Fprintln(a.out, client.SNMPCheckLine(j)) })
}

func snmpCancel(a *app, name string, args []string) error {
	pos, err := a.parse(a.flags(name), args, 1, 1)
	if err != nil {
		return err
	}
	var j apitypes.SNMPCheck
	return a.call(http.MethodDelete, "/snmp/checks/"+url.PathEscape(pos[0]), nil, &j, func() { fmt.Fprintln(a.out, client.SNMPCheckLine(j)) })
}

// communitiesArg reads a community list (SPEC-315): "a,b", "-" for one
// per line from stdin (kept out of the shell history), or --clear.
func (a *app) communitiesArg(arg string, clear bool) ([]string, error) {
	switch {
	case clear && arg != "":
		return nil, usagef("give a list or --clear, not both")
	case clear:
		return []string{}, nil
	case arg == "":
		return nil, usagef("give the community strings (a,b or - for stdin) or --clear")
	case arg == "-":
		var out []string
		sc := bufio.NewScanner(io.LimitReader(a.stdin, 64<<10))
		for sc.Scan() {
			if l := strings.TrimSpace(sc.Text()); l != "" { // spaces at the ends are mistakes (L7)
				out = append(out, l)
			}
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("reading the community strings from stdin: %w", err)
		}
		if len(out) == 0 {
			return nil, usagef("no community strings on stdin (use --clear to inherit)")
		}
		return out, nil
	}
	list := strings.Split(arg, ",")
	for i := range list {
		list[i] = strings.TrimSpace(list[i]) // "a, b" is a and b (L7)
	}
	return list, nil
}

// hostCommunities and groupCommunities set the SNMP community strings of a
// host or a network-view group (SPEC-315); they are never printed back.
func hostCommunities(a *app, name string, args []string) error {
	return setCommunities(a, name, args, "host")
}

func groupCommunities(a *app, name string, args []string) error {
	return setCommunities(a, name, args, "group")
}

func setCommunities(a *app, name string, args []string, kind string) error {
	fs := a.flags(name)
	clear := fs.Bool("clear", false, "remove the list set here: inherit")
	pos, err := a.parse(fs, args, 1, 2)
	if err != nil {
		return err
	}
	id, err := parseID(pos[0])
	if err != nil {
		return err
	}
	arg := ""
	if len(pos) == 2 {
		arg = pos[1]
	}
	list, err := a.communitiesArg(arg, *clear)
	if err != nil {
		return err
	}
	if err := apitypes.CheckCommunities(list); err != nil {
		return usagef("%v", err)
	}
	if kind == "host" {
		var h apitypes.Host
		return a.call(http.MethodPatch, path("host", id), apitypes.HostPatch{SNMPCommunities: apitypes.Some(list)}, &h,
			func() {
				fmt.Fprintln(a.out, "SNMP communities: "+communitiesText(h.SNMPCommunitiesSet, h.SNMPCommunitiesFrom))
			})
	}
	var g apitypes.Group
	return a.call(http.MethodPatch, path("group", id), apitypes.GroupPatch{SNMPCommunities: apitypes.Some(list)}, &g,
		func() { fmt.Fprintln(a.out, "SNMP communities: "+communitiesText(g.SNMPCommunitiesSet, "")) })
}

// communitiesText is "2 set here", "from group 12", "the server's list" or
// "none set here" (for a group) (SPEC-315).
func communitiesText(set int, from string) string {
	switch {
	case set > 0:
		return fmt.Sprintf("%d set here", set)
	case strings.HasPrefix(from, "group "):
		return "from " + from
	case from == "server":
		return "the server's list"
	}
	return "none set here"
}

// jobOut prints a check job: its line, or with --json one line of JSON.
func (a *app) jobOut(j apitypes.SNMPCheck) {
	if !a.g.json {
		fmt.Fprintln(a.out, client.SNMPCheckLine(j))
		return
	}
	b, _ := json.Marshal(j)
	fmt.Fprintf(a.out, "%s\n", b)
}
