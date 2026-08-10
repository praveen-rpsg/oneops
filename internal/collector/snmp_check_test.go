package collector

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

// mockSNMPAgent is a minimal SNMP v2c responder for tests: it answers every
// GET it receives with a fixed sysUpTime.0 value, so runSNMPCheck (and,
// through it, the scheduler) can be exercised without a real device —
// mirroring httptest.NewServer's role for the "http" checks' tests.
//
// It does not decode the request at all (not even to check the OID or
// community): gosnmp's own encoder is what runSNMPCheck relies on for a
// correct request, and gosnmp's own extensive test suite already covers
// that. What this proves is the platform's side of a full v2c
// GET/GetResponse round trip over a real UDP socket: request out, response
// parsed back, value extracted.
//
// Responding with RequestID 0 is deliberate, not an oversight: gosnmp's
// client accepts RequestID==0 as always matching whatever it sent
// (see (*GoSNMP).sendOneRequest's validID check in the gosnmp source), which
// is what lets this mock skip parsing the request to echo back its ID.
type mockSNMPAgent struct {
	conn *net.UDPConn
	addr string
}

// newMockSNMPAgent starts an agent that always answers with uptimeTicks. It
// stops when the test ends (t.Cleanup closes the socket, which unblocks the
// serve loop's blocking read with an error and returns it).
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
			return // socket closed — test is done
		}
		resp := gosnmp.SnmpPacket{
			Version:   gosnmp.Version2c,
			Community: "mock",
			PDUType:   gosnmp.GetResponse,
			RequestID: 0,
			Error:     gosnmp.NoError,
			Variables: []gosnmp.SnmpPDU{
				{Name: sysUpTimeOID, Type: gosnmp.TimeTicks, Value: uptimeTicks},
			},
		}
		out, err := resp.MarshalMsg()
		if err != nil {
			continue
		}
		_, _ = a.conn.WriteToUDP(out, raddr)
	}
}

// freeUDPAddr returns a UDP address on 127.0.0.1 that nothing is bound to
// (the socket is opened, its ephemeral port read, then closed) — a stand-in
// for a real device that never answers.
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

func TestRunSNMPCheck_Reachable_ReturnsUpAndUptime(t *testing.T) {
	agent := newMockSNMPAgent(t, 123456) // 1234.56 seconds
	result := runSNMPCheck(context.Background(), agent.addr, "public", 2*time.Second)

	if !result.Up {
		t.Fatalf("result = %+v, want Up=true", result)
	}
	if result.UptimeTicks != 123456 {
		t.Errorf("UptimeTicks = %d, want 123456", result.UptimeTicks)
	}
	if result.UptimeSeconds != 1234.56 {
		t.Errorf("UptimeSeconds = %v, want 1234.56", result.UptimeSeconds)
	}
}

// TestRunSNMPCheck_NoResponder_ReturnsDownBounded is the mutation-critical
// timeout-discipline proof for SNMP, mirroring
// TestScheduler_HangingTarget_TimesOutAndDoesNotStallTheScheduler for HTTP:
// nothing answers, and the check still returns well inside its bound.
func TestRunSNMPCheck_NoResponder_ReturnsDownBounded(t *testing.T) {
	addr := freeUDPAddr(t)

	start := time.Now()
	result := runSNMPCheck(context.Background(), addr, "public", 300*time.Millisecond)
	elapsed := time.Since(start)

	if result.Up {
		t.Error("result.Up = true, want false — nothing is listening at this address")
	}
	// Retries:1 means up to two rounds of the timeout; generous headroom
	// above that keeps this robust to CI scheduling jitter while still
	// catching an unbounded stall.
	if elapsed > 3*time.Second {
		t.Fatalf("runSNMPCheck took %v against a dead target with a 300ms timeout — it must never stall the scheduler", elapsed)
	}
}

// TestRunSNMPCheck_ContextAlreadyDone_ReturnsDownImmediately proves the ctx
// deadline governs even when the caller's per-check timeout would allow
// longer — the same "ctx wins" discipline the scheduler's outer
// context.WithTimeout relies on.
func TestRunSNMPCheck_ContextAlreadyDone_ReturnsDownImmediately(t *testing.T) {
	addr := freeUDPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	result := runSNMPCheck(ctx, addr, "public", 10*time.Second)
	elapsed := time.Since(start)

	if result.Up {
		t.Error("result.Up = true, want false")
	}
	if elapsed > time.Second {
		t.Fatalf("runSNMPCheck took %v against an already-cancelled context, want near-immediate", elapsed)
	}
}

func TestRunSNMPCheck_MalformedTarget_ReturnsDownWithoutDialing(t *testing.T) {
	for _, target := range []string{"", "   "} {
		result := runSNMPCheck(context.Background(), target, "public", time.Second)
		if result.Up {
			t.Errorf("target %q: result.Up = true, want false", target)
		}
	}
}

func TestSplitSNMPTarget(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantHost string
		wantPort uint16
		wantErr  bool
	}{
		{"bare ipv4", "10.0.0.1", "10.0.0.1", defaultSNMPPort, false},
		{"ipv4 with port", "10.0.0.1:1161", "10.0.0.1", 1161, false},
		{"bare hostname", "switch1.example.test", "switch1.example.test", defaultSNMPPort, false},
		{"bare ipv6", "::1", "::1", defaultSNMPPort, false},
		{"bracketed ipv6 with port", "[::1]:1161", "::1", 1161, false},
		{"empty", "", "", 0, true},
		{"whitespace only", "   ", "", 0, true},
		{"non-numeric port", "10.0.0.1:not-a-port", "", 0, true},
		{"empty host with port", ":161", "", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			host, port, err := splitSNMPTarget(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("splitSNMPTarget(%q) = (%q, %d, nil), want an error", c.in, host, port)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitSNMPTarget(%q): %v", c.in, err)
			}
			if host != c.wantHost || port != c.wantPort {
				t.Errorf("splitSNMPTarget(%q) = (%q, %d), want (%q, %d)", c.in, host, port, c.wantHost, c.wantPort)
			}
		})
	}
}
