package snmp

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

var lo = netip.MustParseAddr("127.0.0.1")

func udp(a *agent) UDP { return UDP{Port: a.port(), Timeout: 200 * time.Millisecond} }

// TC-324: the system group over v2c and v1; a wrong community or version
// gets no answer.
func TestClientSystem(t *testing.T) {
	ctx := context.Background()
	a := newAgent(t, "s3cret", gosnmp.Version2c)
	a.system("web", "Linux web 6.8.0")
	c := udp(a)
	sys, err := c.System(ctx, lo, "s3cret", V2c)
	want := inventory.SNMPSystem{Name: "web", Descr: "Linux web 6.8.0", ObjectID: ".1.3.6.1.4.1.8072.3.2.10", Contact: "ops@example.com", Location: "rack 4"}
	if err != nil || sys != want {
		t.Fatalf("v2c: %+v %v", sys, err)
	}
	if _, err := c.System(ctx, lo, "public", V2c); err == nil {
		t.Error("a wrong community answered")
	}
	if _, err := c.System(ctx, lo, "s3cret", V1); err == nil {
		t.Error("v1 answered by a v2c-only agent")
	}
	// An SNMPv1 agent without sysLocation answers the whole request with
	// an error; sysDescr alone is asked then.
	b := newAgent(t, "public", gosnmp.Version1)
	b.set(oidSysDescr, gosnmp.OctetString, []byte("printer"))
	if sys, err := udp(b).System(ctx, lo, "public", V1); err != nil || sys.Descr != "printer" {
		t.Errorf("v1: %+v %v", sys, err)
	}
}

// TC-325: uptime, the average CPU, fixed disks and RAM (virtual memory left
// out), over v2c (bulk) and v1 (get-next); an agent without
// HOST-RESOURCES-MIB gives uptime only.
func TestClientValues(t *testing.T) {
	ctx := context.Background()
	for _, v := range []gosnmp.SnmpVersion{gosnmp.Version2c, gosnmp.Version1} {
		a := newAgent(t, "public", v)
		a.system("web", "Linux")
		a.hostResources()
		ver := V2c
		if v == gosnmp.Version1 {
			ver = V1
		}
		got, err := udp(a).Values(ctx, lo, "public", ver)
		if err != nil {
			t.Fatalf("%s: %v", ver, err)
		}
		want := []apitypes.SNMPStorage{
			{Name: "Physical memory", Kind: apitypes.StorageMemory, SizeBytes: 1024000, UsedPct: 63},
			{Name: "/", Kind: apitypes.StorageDisk, SizeBytes: 4096000, UsedPct: 91},
		}
		if got.UptimeS != 1234 || got.CPUPct == nil || *got.CPUPct != 13 || *got.CPUMaxPct != 15 || got.CPUs != 2 || len(got.Storage) != 2 || got.Storage[0] != want[0] || got.Storage[1] != want[1] {
			t.Errorf("%s: %+v (cpu %v)", ver, got, got.CPUPct)
		}
		a.del(".1.3.6.1.2.1.25")
		got, err = udp(a).Values(ctx, lo, "public", ver)
		if err != nil || got.CPUPct != nil || got.CPUMaxPct != nil || got.CPUs != 0 || len(got.Storage) != 0 || got.UptimeS != 1234 {
			t.Errorf("%s without HOST-RESOURCES: %+v %v", ver, got, err)
		}
	}
}

// TC-324 (crosscheck 2026-11-18-03 C1, C5): an agent's text is one line:
// CR, LF, tab and ESC become spaces, runs of spaces one, at most 255
// runes, invalid UTF-8 replaced.
func TestClientText(t *testing.T) {
	ctx := context.Background()
	a := newAgent(t, "public", gosnmp.Version2c)
	a.system("core-1\r\n", "Cisco IOS Software\r\nTechnical Support: x\r\n\tCopyright \x1b[31m(c)\x00")
	a.set(oidSysLocation, gosnmp.OctetString, []byte(strings.Repeat("é", 300)+"\xff"))
	sys, err := udp(a).System(ctx, lo, "public", V2c)
	if err != nil {
		t.Fatal(err)
	}
	if sys.Name != "core-1" || sys.Descr != "Cisco IOS Software Technical Support: x Copyright [31m(c)" {
		t.Errorf("%q %q", sys.Name, sys.Descr)
	}
	if r := []rune(sys.Location); len(r) != 255 || strings.ContainsAny(sys.Location, "\r\n\x1b") {
		t.Errorf("location: %d runes", len(r))
	}
}

// TC-325 (crosscheck 2026-11-18-03 C4): a load outside 0–100 is left out;
// storage rows with a negative size or used value, or a size beyond int64,
// are left out; % used is at most 100.
func TestClientValuesOutOfRange(t *testing.T) {
	a := newAgent(t, "public", gosnmp.Version2c)
	a.system("x", "y")
	a.set(oidProcessorLoad+".1", gosnmp.Integer, -5)
	a.set(oidProcessorLoad+".2", gosnmp.Integer, 255)
	a.set(oidProcessorLoad+".3", gosnmp.Integer, 40)
	row := func(idx string, units, size, used any) {
		a.set(oidStorageEntry+".2."+idx, gosnmp.ObjectIdentifier, oidStorageDisk)
		a.set(oidStorageEntry+".3."+idx, gosnmp.OctetString, []byte("d"+idx))
		a.set(oidStorageEntry+".4."+idx, gosnmp.Integer, units)
		a.set(oidStorageEntry+".5."+idx, gosnmp.Integer, size)
		a.set(oidStorageEntry+".6."+idx, gosnmp.Integer, used)
	}
	row("1", 4096, 1000, -300)          // negative used
	row("2", 4096, 1000, 3000)          // more used than size: 100 %
	row("3", 2000000000, 2000000000, 1) // fine: 4e18 fits
	a.set(oidStorageEntry+".2.4", gosnmp.ObjectIdentifier, oidStorageDisk)
	a.set(oidStorageEntry+".4.4", gosnmp.Gauge32, uint(4000000000))
	a.set(oidStorageEntry+".5.4", gosnmp.Gauge32, uint(4000000000)) // 1.6e19: beyond int64
	a.set(oidStorageEntry+".6.4", gosnmp.Gauge32, uint(1))
	v, err := udp(a).Values(context.Background(), lo, "public", V2c)
	if err != nil {
		t.Fatal(err)
	}
	if v.CPUPct == nil || *v.CPUPct != 40 || len(v.Storage) != 2 || v.Storage[0].Name != "d2" || v.Storage[0].UsedPct != 100 ||
		v.Storage[1].Name != "d3" || v.Storage[1].SizeBytes != 4000000000000000000 {
		t.Errorf("%+v cpu %v", v.Storage, v.CPUPct)
	}
}

// TC-324 (crosscheck 2026-11-18-04 L5): format characters are dropped and
// a cut doesn't split a letter from its marks.
func TestOneLine(t *testing.T) {
	if got := oneLine("web\u202e1.0\u200b-\u2066x"); got != "web1.0-x" {
		t.Errorf("format characters: %q", got)
	}
	s := strings.Repeat("a", maxText-1) + "e\u0301\u0301b"
	if got := oneLine(s); got != strings.Repeat("a", maxText-1) {
		t.Errorf("cut: %q", got[len(got)-4:])
	}
}
