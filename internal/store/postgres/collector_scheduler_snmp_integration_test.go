//go:build integration

package postgres

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/rpsg/oneops/internal/collector"
	"github.com/rpsg/oneops/internal/domain"
)

// mockSNMPAgent is a minimal SNMP v2c responder, duplicated (deliberately —
// each package's tests are self-contained, the same choice httptest.
// NewServer's inline use makes for "http" checks) from internal/collector's
// test-only agent of the same shape. It answers every GET with a fixed
// sysUpTime.0 so this package's real-database, real-scheduler test can prove
// the SNMP telemetry path end-to-end without a real device.
type mockSNMPAgent struct {
	conn *net.UDPConn
	addr string
}

func newMockSNMPAgent(t *testing.T, uptimeTicks uint32) *mockSNMPAgent {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	a := &mockSNMPAgent{conn: conn, addr: conn.LocalAddr().String()}
	go a.serve(uptimeTicks)
	t.Cleanup(func() { _ = conn.Close() })
	return a
}

func (a *mockSNMPAgent) serve(uptimeTicks uint32) {
	buf := make([]byte, 4096)
	for {
		_, raddr, err := a.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		resp := gosnmp.SnmpPacket{
			Version: gosnmp.Version2c, Community: "mock", PDUType: gosnmp.GetResponse,
			RequestID: 0, Error: gosnmp.NoError,
			Variables: []gosnmp.SnmpPDU{{Name: ".1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uptimeTicks}},
		}
		out, err := resp.MarshalMsg()
		if err != nil {
			continue
		}
		_, _ = a.conn.WriteToUDP(out, raddr)
	}
}

// freeUDPAddr returns a UDP address on 127.0.0.1 nothing is bound to.
func freeUDPAddr(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	addr := conn.LocalAddr().String()
	_ = conn.Close()
	return addr
}

// TestCollectorScheduler_EndToEnd_SNMPCheckWritesReachableAndUptime is A.1's
// central proof: an enabled "snmp" check against a real (mock) agent, run by
// the real leader-gated scheduler over the real database, produces
// reachable=1 and uptime_seconds telemetry readable back through the
// tenant-scoped store — the exact signal an alert rule on
// "<prefix>_reachable" would evaluate, so a device-down condition would
// reach the existing alert→incident→escalate spine unchanged.
func TestCollectorScheduler_EndToEnd_SNMPCheckWritesReachableAndUptime(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "collector-snmp-e2e")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	scopedChecks := NewCollectorCheckStore(scoped)
	scopedTelemetry := NewTelemetryStore(scoped)
	ctx := assetTestCtx(tn)
	host := telemetryAsset(t, assets, ctx, tn.TenantID, "snmp-e2e-host")

	agent := newMockSNMPAgent(t, 987654) // 9876.54 seconds

	c, err := domain.NewCollectorCheck(tn.TenantID, host.AssetID, domain.CollectorCheckTypeSNMP, agent.addr, 30, "snmpe2e", "public")
	if err != nil {
		t.Fatalf("new collector check: %v", err)
	}
	created, err := scopedChecks.Create(ctx, c)
	if err != nil {
		t.Fatalf("create check: %v", err)
	}

	privChecks := NewCollectorCheckStore(priv)
	privTelemetry := NewTelemetryStore(priv)
	sched := collector.NewScheduler(privChecks, privTelemetry, http.DefaultClient, nil, quietRollupLogger(),
		collector.Config{CheckTimeout: 2 * time.Second})

	sched.RunOnce(context.Background())

	base := time.Now().UTC()
	gotReachable, err := scopedTelemetry.QueryRange(ctx, host.AssetID, "snmpe2e_reachable", base.Add(-time.Hour), base.Add(time.Hour), 0, time.Time{}, domain.ResolutionRaw)
	if err != nil {
		t.Fatalf("query snmpe2e_reachable: %v", err)
	}
	if len(gotReachable) != 1 || gotReachable[0].Value != 1 {
		t.Fatalf("snmpe2e_reachable samples = %+v, want exactly one with value 1", gotReachable)
	}

	gotUptime, err := scopedTelemetry.QueryRange(ctx, host.AssetID, "snmpe2e_uptime_seconds", base.Add(-time.Hour), base.Add(time.Hour), 0, time.Time{}, domain.ResolutionRaw)
	if err != nil {
		t.Fatalf("query snmpe2e_uptime_seconds: %v", err)
	}
	if len(gotUptime) != 1 || gotUptime[0].Value != 9876.54 {
		t.Fatalf("snmpe2e_uptime_seconds samples = %+v, want exactly one with value 9876.54", gotUptime)
	}

	updated, err := scopedChecks.Get(ctx, created.CheckID)
	if err != nil {
		t.Fatalf("get after run: %v", err)
	}
	if updated.LastStatus != domain.CollectorCheckStatusOK {
		t.Errorf("last_status = %q, want ok", updated.LastStatus)
	}
	if updated.SNMPCommunity != "public" {
		t.Errorf("SNMPCommunity round-trip through the store = %q, want %q", updated.SNMPCommunity, "public")
	}
}

// TestCollectorScheduler_EndToEnd_SNMPDeadTargetRecordsReachableZero proves
// the "device down" half against the real database: nothing answers, so
// reachable=0 is written, no uptime_seconds sample exists, and last_status
// is down.
func TestCollectorScheduler_EndToEnd_SNMPDeadTargetRecordsReachableZero(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "collector-snmp-e2e-down")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	scopedChecks := NewCollectorCheckStore(scoped)
	scopedTelemetry := NewTelemetryStore(scoped)
	ctx := assetTestCtx(tn)
	host := telemetryAsset(t, assets, ctx, tn.TenantID, "snmp-e2e-down-host")

	deadAddr := freeUDPAddr(t)

	c, err := domain.NewCollectorCheck(tn.TenantID, host.AssetID, domain.CollectorCheckTypeSNMP, deadAddr, 30, "snmpdown", "public")
	if err != nil {
		t.Fatalf("new collector check: %v", err)
	}
	if _, err := scopedChecks.Create(ctx, c); err != nil {
		t.Fatalf("create check: %v", err)
	}

	privChecks := NewCollectorCheckStore(priv)
	privTelemetry := NewTelemetryStore(priv)
	sched := collector.NewScheduler(privChecks, privTelemetry, http.DefaultClient, nil, quietRollupLogger(),
		collector.Config{CheckTimeout: 500 * time.Millisecond})

	sched.RunOnce(context.Background())

	base := time.Now().UTC()
	got, err := scopedTelemetry.QueryRange(ctx, host.AssetID, "snmpdown_reachable", base.Add(-time.Hour), base.Add(time.Hour), 0, time.Time{}, domain.ResolutionRaw)
	if err != nil {
		t.Fatalf("query snmpdown_reachable: %v", err)
	}
	if len(got) != 1 || got[0].Value != 0 {
		t.Fatalf("snmpdown_reachable samples = %+v, want exactly one with value 0", got)
	}

	gotUptime, err := scopedTelemetry.QueryRange(ctx, host.AssetID, "snmpdown_uptime_seconds", base.Add(-time.Hour), base.Add(time.Hour), 0, time.Time{}, domain.ResolutionRaw)
	if err != nil {
		t.Fatalf("query snmpdown_uptime_seconds: %v", err)
	}
	if len(gotUptime) != 0 {
		t.Errorf("snmpdown_uptime_seconds samples = %+v, want none — no response was ever received", gotUptime)
	}
}

// TestCollectorCheckStore_SNMPCommunityIsRedactedFromTheAdminDTOButPersisted
// proves the credential survives the full store round trip (Create -> Get)
// unredacted at the domain/store layer — httpapi's DTO layer is what
// redacts it (proven in internal/httpapi's tests), not the store, which
// must return the real value for the scheduler to use it.
func TestCollectorCheckStore_SNMPCommunityPersistsAcrossCreateAndGet(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "collector-snmp-community")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	checks := NewCollectorCheckStore(scoped)
	ctx := assetTestCtx(tn)
	host := telemetryAsset(t, assets, ctx, tn.TenantID, "snmp-community-host")

	c, err := domain.NewCollectorCheck(tn.TenantID, host.AssetID, domain.CollectorCheckTypeSNMP, "10.0.0.1", 60, "chk", "s3cr3t")
	if err != nil {
		t.Fatalf("new collector check: %v", err)
	}
	created, err := checks.Create(ctx, c)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.SNMPCommunity != "s3cr3t" {
		t.Errorf("Create's returned SNMPCommunity = %q, want %q", created.SNMPCommunity, "s3cr3t")
	}

	got, err := checks.Get(ctx, created.CheckID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SNMPCommunity != "s3cr3t" {
		t.Errorf("Get's SNMPCommunity = %q, want %q", got.SNMPCommunity, "s3cr3t")
	}

	rotated := "new-secret"
	updated, err := checks.Update(ctx, created.CheckID, created.RowVersion, domain.CollectorCheckPatch{SNMPCommunity: &rotated})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.SNMPCommunity != rotated {
		t.Errorf("Update's SNMPCommunity = %q, want %q", updated.SNMPCommunity, rotated)
	}
}
