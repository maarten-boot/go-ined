package snmp

import (
	"context"
	"net/netip"
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
		if got.UptimeS != 1234 || got.CPUPct == nil || *got.CPUPct != 13 || len(got.Storage) != 2 || got.Storage[0] != want[0] || got.Storage[1] != want[1] {
			t.Errorf("%s: %+v (cpu %v)", ver, got, got.CPUPct)
		}
		a.del(".1.3.6.1.2.1.25")
		got, err = udp(a).Values(ctx, lo, "public", ver)
		if err != nil || got.CPUPct != nil || len(got.Storage) != 0 || got.UptimeS != 1234 {
			t.Errorf("%s without HOST-RESOURCES: %+v %v", ver, got, err)
		}
	}
}
