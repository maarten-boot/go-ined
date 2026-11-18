package snmp

import (
	"net"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gosnmp/gosnmp"
)

// agent is a small SNMP agent on 127.0.0.1 for the tests: it answers Get,
// GetNext and GetBulk from a table, only for its community and the
// versions it speaks; anything else gets no answer, as with a real agent.
type agent struct {
	t         *testing.T
	conn      *net.UDPConn
	community string
	versions  []gosnmp.SnmpVersion
	mu        sync.Mutex
	table     map[string]gosnmp.SnmpPDU
	oids      []string // sorted
	seen      []string // communities of every request, in order
}

func newAgent(t *testing.T, community string, versions ...gosnmp.SnmpVersion) *agent {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	a := &agent{t: t, conn: c, community: community, versions: versions, table: map[string]gosnmp.SnmpPDU{}}
	t.Cleanup(func() { c.Close() })
	go a.serve()
	return a
}

func (a *agent) port() uint16 { return uint16(a.conn.LocalAddr().(*net.UDPAddr).Port) }

func (a *agent) set(oid string, typ gosnmp.Asn1BER, v any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.table[oid] = gosnmp.SnmpPDU{Name: oid, Type: typ, Value: v}
	a.oids = a.oids[:0]
	for k := range a.table {
		a.oids = append(a.oids, k)
	}
	slices.SortFunc(a.oids, oidCmp)
}

func (a *agent) del(prefix string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k := range a.table {
		if k == prefix || strings.HasPrefix(k, prefix+".") {
			delete(a.table, k)
		}
	}
	a.oids = slices.DeleteFunc(a.oids, func(k string) bool { return a.table[k].Name == "" })
}

func (a *agent) communities() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.seen)
}

// system fills the system group.
func (a *agent) system(name, descr string) {
	a.set(oidSysDescr, gosnmp.OctetString, []byte(descr))
	a.set(oidSysObjectID, gosnmp.ObjectIdentifier, ".1.3.6.1.4.1.8072.3.2.10")
	a.set(oidSysUpTime, gosnmp.TimeTicks, uint32(123456))
	a.set(oidSysContact, gosnmp.OctetString, []byte("ops@example.com"))
	a.set(oidSysName, gosnmp.OctetString, []byte(name))
	a.set(oidSysLocation, gosnmp.OctetString, []byte("rack 4"))
}

// hostResources adds two processors (10 % and 15 %), a disk / (91 %), RAM
// (63 %) and a virtual memory entry that is left out.
func (a *agent) hostResources() {
	a.set(oidProcessorLoad+".196608", gosnmp.Integer, 10)
	a.set(oidProcessorLoad+".196609", gosnmp.Integer, 15)
	row := func(idx, typ, descr string, units, size, used int) {
		a.set(oidStorageEntry+".2."+idx, gosnmp.ObjectIdentifier, typ)
		a.set(oidStorageEntry+".3."+idx, gosnmp.OctetString, []byte(descr))
		a.set(oidStorageEntry+".4."+idx, gosnmp.Integer, units)
		a.set(oidStorageEntry+".5."+idx, gosnmp.Integer, size)
		a.set(oidStorageEntry+".6."+idx, gosnmp.Integer, used)
	}
	row("1", oidStorageRAM, "Physical memory", 1024, 1000, 630)
	row("3", ".1.3.6.1.2.1.25.2.1.3", "Virtual memory", 1024, 2000, 100)
	row("31", oidStorageDisk, "/", 4096, 1000, 910)
}

func oidCmp(x, y string) int {
	xs, ys := strings.Split(strings.Trim(x, "."), "."), strings.Split(strings.Trim(y, "."), ".")
	for i := 0; i < len(xs) && i < len(ys); i++ {
		if len(xs[i]) != len(ys[i]) {
			return len(xs[i]) - len(ys[i])
		}
		if c := strings.Compare(xs[i], ys[i]); c != 0 {
			return c
		}
	}
	return len(xs) - len(ys)
}

func (a *agent) serve() {
	buf := make([]byte, 65535)
	dec := &gosnmp.GoSNMP{Version: gosnmp.Version2c, Community: "x"}
	for {
		n, from, err := a.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		req, err := dec.SnmpDecodePacket(buf[:n])
		if err != nil {
			continue
		}
		a.mu.Lock()
		a.seen = append(a.seen, req.Community)
		ok := req.Community == a.community && slices.Contains(a.versions, req.Version)
		a.mu.Unlock()
		if !ok {
			continue
		}
		resp := &gosnmp.SnmpPacket{Version: req.Version, Community: req.Community, PDUType: gosnmp.GetResponse, RequestID: req.RequestID}
		resp.Variables, resp.Error, resp.ErrorIndex = a.answer(req)
		out, err := resp.MarshalMsg()
		if err != nil {
			a.t.Errorf("agent: marshal: %v", err)
			continue
		}
		a.conn.WriteToUDP(out, from)
	}
}

func (a *agent) answer(req *gosnmp.SnmpPacket) ([]gosnmp.SnmpPDU, gosnmp.SNMPError, uint8) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []gosnmp.SnmpPDU
	next := func(oid string) (gosnmp.SnmpPDU, bool) {
		for _, k := range a.oids {
			if oidCmp(k, oid) > 0 {
				return a.table[k], true
			}
		}
		return gosnmp.SnmpPDU{Name: oid, Type: gosnmp.EndOfMibView}, false
	}
	for i, v := range req.Variables {
		name := normOID(v.Name)
		switch req.PDUType {
		case gosnmp.GetRequest:
			p, ok := a.table[name]
			if !ok {
				if req.Version == gosnmp.Version1 {
					return req.Variables, gosnmp.NoSuchName, uint8(i + 1)
				}
				p = gosnmp.SnmpPDU{Name: name, Type: gosnmp.NoSuchObject}
			}
			out = append(out, p)
		case gosnmp.GetNextRequest:
			p, ok := next(name)
			if !ok && req.Version == gosnmp.Version1 {
				return req.Variables, gosnmp.NoSuchName, uint8(i + 1)
			}
			out = append(out, p)
		case gosnmp.GetBulkRequest:
			for range max(int(req.MaxRepetitions), 1) {
				p, ok := next(name)
				out = append(out, p)
				if !ok {
					break
				}
				name = p.Name
			}
		}
	}
	return out, gosnmp.NoError, 0
}
