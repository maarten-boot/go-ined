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
	sum, n := int64(0), int64(0)
	for _, v := range load {
		if isNumber(v) {
			sum += gosnmp.ToBigInt(v.Value).Int64()
			n++
		}
	}
	if n > 0 {
		cpu := int(math.Round(float64(sum) / float64(n)))
		out.CPUPct = &cpu
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
		if e.kind == "" || e.size <= 0 || e.units <= 0 {
			continue
		}
		out = append(out, apitypes.SNMPStorage{
			Name: e.name, Kind: e.kind, SizeBytes: e.size * e.units,
			UsedPct: int(math.Round(float64(e.used) * 100 / float64(e.size))),
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

// text is an OctetString as text (invalid UTF-8 replaced) or an OID; ""
// for anything else, such as noSuchObject.
func text(v gosnmp.SnmpPDU) string {
	switch v.Type {
	case gosnmp.OctetString:
		b, _ := v.Value.([]byte)
		return strings.ToValidUTF8(strings.TrimRight(string(b), "\x00"), "�")
	case gosnmp.ObjectIdentifier:
		s, _ := v.Value.(string)
		return s
	}
	return ""
}

func describe(ip netip.Addr, version string, i, n int) string {
	return fmt.Sprintf("%s (%s, community %d of %d)", ip, version, i+1, n)
}
