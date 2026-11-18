// Package snmp finds the SNMP agents of hosts and reads their values
// (SPEC-312, 313; design Design-snmp.md part 1): the system group, and
// CPU, storage and uptime from HOST-RESOURCES-MIB, over SNMP v2c or v1.
package snmp

import (
	"context"
	"fmt"
	"math"
	"net/netip"
	"strings"
	"time"
	"unicode"

	"github.com/gosnmp/gosnmp"

	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Versions tried, in this order (SPEC-312).
const (
	V2c = "v2c"
	V1  = "v1"
)

// OIDs used.
const (
	oidSysDescr    = ".1.3.6.1.2.1.1.1.0"
	oidSysObjectID = ".1.3.6.1.2.1.1.2.0"
	oidSysUpTime   = ".1.3.6.1.2.1.1.3.0"
	oidSysContact  = ".1.3.6.1.2.1.1.4.0"
	oidSysName     = ".1.3.6.1.2.1.1.5.0"
	oidSysLocation = ".1.3.6.1.2.1.1.6.0"

	oidProcessorLoad = ".1.3.6.1.2.1.25.3.3.1.2" // hrProcessorLoad
	oidStorageEntry  = ".1.3.6.1.2.1.25.2.3.1"   // hrStorageEntry
	oidStorageRAM    = ".1.3.6.1.2.1.25.2.1.2"   // hrStorageRam
	oidStorageDisk   = ".1.3.6.1.2.1.25.2.1.4"   // hrStorageFixedDisk
)

// Client talks SNMP to one address at a time.
type Client interface {
	// System reads the system group; an error means no answer.
	System(ctx context.Context, ip netip.Addr, community, version string) (inventory.SNMPSystem, error)
	// Values reads uptime, CPU and storage; an error means no answer.
	Values(ctx context.Context, ip netip.Addr, community, version string) (apitypes.SNMPValues, error)
}

// UDP is the Client over UDP with gosnmp.
type UDP struct {
	Port    uint16 // default 161
	Timeout time.Duration
	Retries int
}

func (c UDP) open(ctx context.Context, ip netip.Addr, community, version string) (*gosnmp.GoSNMP, error) {
	v := gosnmp.Version2c
	if version == V1 {
		v = gosnmp.Version1
	}
	g := &gosnmp.GoSNMP{
		Target: ip.Unmap().String(), Port: cmpOr(c.Port, 161), Transport: "udp", Community: community, Version: v,
		Timeout: c.Timeout, Retries: c.Retries, Context: ctx, MaxOids: gosnmp.MaxOids, MaxRepetitions: 25,
	}
	if err := g.Connect(); err != nil {
		return nil, err
	}
	return g, nil
}

func cmpOr(p, d uint16) uint16 {
	if p == 0 {
		return d
	}
	return p
}

// System implements Client.
func (c UDP) System(ctx context.Context, ip netip.Addr, community, version string) (inventory.SNMPSystem, error) {
	g, err := c.open(ctx, ip, community, version)
	if err != nil {
		return inventory.SNMPSystem{}, err
	}
	defer g.Conn.Close()
	var sys inventory.SNMPSystem
	dst := map[string]*string{
		oidSysDescr: &sys.Descr, oidSysObjectID: &sys.ObjectID, oidSysContact: &sys.Contact,
		oidSysName: &sys.Name, oidSysLocation: &sys.Location,
	}
	r, err := g.Get([]string{oidSysDescr, oidSysObjectID, oidSysContact, oidSysName, oidSysLocation})
	if err != nil {
		return inventory.SNMPSystem{}, err
	}
	if r.Error != gosnmp.NoError {
		// An SNMPv1 agent answers a request with one unknown name with an
		// error for all; it did answer, so ask for sysDescr alone.
		if r, err = g.Get([]string{oidSysDescr}); err != nil {
			return inventory.SNMPSystem{}, err
		}
		if r.Error != gosnmp.NoError {
			return inventory.SNMPSystem{}, fmt.Errorf("the agent answered %v", r.Error) // L4
		}
	}
	for _, v := range r.Variables {
		if p := dst[normOID(v.Name)]; p != nil {
			*p = text(v)
		}
	}
	return sys, nil
}

// Values implements Client. A device without HOST-RESOURCES-MIB gives
// uptime only.
func (c UDP) Values(ctx context.Context, ip netip.Addr, community, version string) (apitypes.SNMPValues, error) {
	g, err := c.open(ctx, ip, community, version)
	if err != nil {
		return apitypes.SNMPValues{}, err
	}
	defer g.Conn.Close()
	walk := g.BulkWalkAll
	if version == V1 {
		walk = g.WalkAll
	}
	r, err := g.Get([]string{oidSysUpTime})
	if err != nil {
		return apitypes.SNMPValues{}, err
	}
	var out apitypes.SNMPValues
	for _, v := range r.Variables {
		if v.Type == gosnmp.TimeTicks {
			out.UptimeS = gosnmp.ToBigInt(v.Value).Int64() / 100
		}
	}
	load, err := walk(oidProcessorLoad)
	if err != nil {
		return apitypes.SNMPValues{}, err
	}
	sum, n, busiest := int64(0), int64(0), int64(0)
	for _, v := range load {
		// A load outside 0–100 is a broken agent: left out (C4).
		if l := number(v); isNumber(v) && l >= 0 && l <= 100 {
			sum += l
			n++
			busiest = max(busiest, l)
		}
	}
	if n > 0 {
		// The average hides one busy CPU of many: the busiest one and the
		// count go along (v0.62, the user's question on CPU %).
		cpu, top := int(math.Round(float64(sum)/float64(n))), int(busiest)
		out.CPUPct, out.CPUMaxPct, out.CPUs = &cpu, &top, int(n)
	}
	rows, err := walk(oidStorageEntry)
	if err != nil {
		return apitypes.SNMPValues{}, err
	}
	out.Storage = storage(rows)
	return out, nil
}

// storage turns hrStorageEntry columns into disks and RAM, in table order.
func storage(rows []gosnmp.SnmpPDU) []apitypes.SNMPStorage {
	type entry struct {
		kind, name        string
		units, size, used int64
	}
	byIndex := map[string]*entry{}
	var order []string
	for _, v := range rows {
		name := normOID(v.Name)
		rest, ok := strings.CutPrefix(name, oidStorageEntry+".")
		if !ok {
			continue
		}
		col, idx, ok := strings.Cut(rest, ".")
		if !ok {
			continue
		}
		e := byIndex[idx]
		if e == nil {
			e = &entry{}
			byIndex[idx] = e
			order = append(order, idx)
		}
		switch col {
		case "2": // hrStorageType
			switch normOID(text(v)) {
			case oidStorageDisk:
				e.kind = apitypes.StorageDisk
			case oidStorageRAM:
				e.kind = apitypes.StorageMemory
			}
		case "3":
			e.name = text(v)
		case "4":
			e.units = number(v)
		case "5":
			e.size = number(v)
		case "6":
			e.used = number(v)
		}
	}
	out := []apitypes.SNMPStorage{}
	for _, idx := range order {
		e := byIndex[idx]
		// Rows a broken agent fills with nonsense are left out: a negative
		// size or used value, or a size in bytes beyond int64 (C4).
		if e.kind == "" || e.size <= 0 || e.units <= 0 || e.used < 0 || e.size > math.MaxInt64/e.units {
			continue
		}
		out = append(out, apitypes.SNMPStorage{
			Name: e.name, Kind: e.kind, SizeBytes: e.size * e.units,
			UsedPct: min(100, int(math.Round(float64(e.used)*100/float64(e.size)))),
		})
	}
	return out
}

func normOID(s string) string {
	if !strings.HasPrefix(s, ".") {
		return "." + s
	}
	return s
}

func isNumber(v gosnmp.SnmpPDU) bool {
	switch v.Type {
	case gosnmp.Integer, gosnmp.Counter32, gosnmp.Gauge32, gosnmp.TimeTicks, gosnmp.Counter64, gosnmp.Uinteger32:
		return true
	}
	return false
}

func number(v gosnmp.SnmpPDU) int64 {
	if !isNumber(v) {
		return 0
	}
	return gosnmp.ToBigInt(v.Value).Int64()
}

// maxText is the most runes kept of an agent's text (C1).
const maxText = 255

// text is an OctetString as one line of text, or an OID; "" for anything
// else, such as noSuchObject. Invalid UTF-8 is replaced, control
// characters (CR, LF, tab, ESC, …) become spaces, runs of spaces one, and
// the text is cut at maxText runes: a Cisco "\r\n" in sysDescr crashed
// the GUI's status bar, and an escape sequence would reach a terminal
// (crosscheck 2026-11-18-03 C1, C5).
func text(v gosnmp.SnmpPDU) string {
	switch v.Type {
	case gosnmp.OctetString:
		b, _ := v.Value.([]byte)
		return oneLine(strings.ToValidUTF8(string(b), "\uFFFD"))
	case gosnmp.ObjectIdentifier:
		s, _ := v.Value.(string)
		return oneLine(s)
	}
	return ""
}

func oneLine(s string) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }), " ")
	if r := []rune(s); len(r) > maxText {
		s = strings.TrimSpace(string(r[:maxText]))
	}
	return s
}

func describe(ip netip.Addr, version string, i, n int) string {
	return fmt.Sprintf("%s (%s, community %d of %d)", ip, version, i+1, n)
}
