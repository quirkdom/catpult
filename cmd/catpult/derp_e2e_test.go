package main

// Live local end-to-end tests with a real DERP server, exercising the
// custom-relay sender flags end to end over loopback:
//
// TestCustomDERPMapE2E:
//
//	catpult --derpmap <map-url> msg.txt   (JSON DERP map URL)
//
// TestCustomDERPEndpointE2E:
//
//	catpult --derp <host:port> msg.txt    (self-hosted endpoint, no map fetch)
//
// The DERP server is started in-process (STUN on loopback) and the DERP
// map is served over httptest, so the tests need no external network.
// The endpoint variant is forced onto plain HTTP with the
// TS_DEBUG_USE_DERP_HTTP debug knob, because self-hosted endpoints dial
// DERP over TLS and the loopback server only presents a self-signed cert.
// Both are skipped under -short like the other networked integration tests.
import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"tailscale.com/derp/derpserver"
	"tailscale.com/net/stun/stuntest"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
	"tailscale.com/types/nettype"
)

func TestCustomDERPMapE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("networked integration test")
	}
	mapURL, _ := startDERPAndSTUN(t, tlsFrontend)
	runDERPRelayE2E(t, []string{"--derpmap", mapURL}, nil)
}

func TestCustomDERPEndpointsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("networked integration test")
	}
	_, derpHostPort := startDERPAndSTUN(t, plainHTTPFrontend)
	runDERPRelayE2E(t, []string{"--derp", derpHostPort}, []string{"TS_DEBUG_USE_DERP_HTTP=1"})
}

// runDERPRelayE2E runs a full sender/receiver transcript with a custom
// relay, asserting the payload arrives byte-identical with a clean
// stdout manifest. extraEnv is applied to both catpult processes.
func runDERPRelayE2E(t *testing.T, relayArgs, extraEnv []string) {
	t.Helper()

	bin := buildCatpult(t)

	// Sender: room hosted on the custom relay.
	sendDir := t.TempDir()
	payload := "top secret derp-relayed bytes\n"
	if err := os.WriteFile(filepath.Join(sendDir, "msg.txt"), []byte(payload), 0644); err != nil {
		t.Fatal(err)
	}
	senderCmd := exec.Command(bin, append(relayArgs, "--expires", "5m", "msg.txt")...)
	senderCmd.Dir = sendDir
	senderCmd.Env = append(os.Environ(), extraEnv...)
	senderStderr, err := senderCmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := senderCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		senderCmd.Process.Kill()
		senderCmd.Wait()
	}()
	invite := scanInvite(t, senderStderr)
	t.Logf("invite: %s (%d bytes)", invite[:20]+"...", len(invite))

	// Receiver: joins the room, relaying through the custom DERP server.
	recvDir := t.TempDir()
	var out, errOut strings.Builder
	recvCmd := exec.Command(bin, invite, "--target", "got")
	recvCmd.Dir = recvDir
	recvCmd.Stdout = &out
	recvCmd.Stderr = &errOut
	recvCmd.Env = append(os.Environ(), extraEnv...)
	if err := recvCmd.Run(); err != nil {
		t.Fatalf("receiver failed: %v\nstderr: %s", err, errOut.String())
	}
	t.Logf("receiver stderr: %s", strings.TrimSpace(errOut.String()))

	got, err := os.ReadFile(filepath.Join(recvDir, "got", "msg.txt"))
	if err != nil {
		t.Fatalf("file did not arrive: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("payload mismatch: got %q want %q", got, payload)
	}
	if out.String() != "msg.txt\n" {
		t.Fatalf("stdout manifest = %q", out.String())
	}
}

// buildCatpult compiles the catpult binary into a temp dir and returns its
// path. The e2e runs a real sender/receiver pair, so it needs the CLI
// as an executable, not an in-process call.
func buildCatpult(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "catpult")
	cmd := exec.Command("go", "build", "-o", bin, "catpult/cmd/catpult")
	cmd.Env = append(os.Environ(), "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building catpult: %v\n%s", err, out)
	}
	return bin
}

// startDERPAndSTUN runs a DERP server plus STUN on 127.0.0.1 on random
// ports. With fs == tlsFrontend the DERP front-end speaks HTTPS with a
// self-signed cert (reachable via map nodes with InsecureForTests); with
// plainHTTPFrontend it speaks plain HTTP (reachable via the
// TS_DEBUG_USE_DERP_HTTP debug knob, which dials http://host:port). A
// JSON DERP map is served on a separate httptest server (always HTTP);
// it returns the map URL (for --derpmap) and the relay's host:port (for
// --derp with self-hosted endpoints).
func startDERPAndSTUN(t *testing.T, fs derpFrontend) (mapURL, derpHostPort string) {
	t.Helper()
	d := derpserver.New(key.NewNode(), logger.Discard)
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", "0"))
	if err != nil {
		t.Fatal(err)
	}
	httpsrv := fs(t, ln, derpserver.Handler(d))
	derpPort := httpsrv.Listener.Addr().(*net.TCPAddr).Port
	t.Logf("derp on 127.0.0.1:%d as %v (for DERP nodes)", derpPort, fs)

	stunAddr, stunCleanup := stuntest.ServeWithPacketListener(t, nettype.Std{})
	m := &tailcfg.DERPMap{
		Regions: map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{
			1: {
				RegionID:   1,
				RegionCode: "test",
				RegionName: "test",
				Nodes: []*tailcfg.DERPNode{
					{
						Name:             "t1",
						RegionID:         1,
						HostName:         "127.0.0.1",
						IPv4:             "127.0.0.1",
						IPv6:             "none",
						STUNPort:         stunAddr.Port,
						DERPPort:         derpPort,
						InsecureForTests: true,
						STUNTestIP:       "127.0.0.1",
					},
				},
			},
		},
	}
	j, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	mapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(j)
	}))
	t.Cleanup(func() {
		mapSrv.Close()
		httpsrv.Close()
		d.Close()
		stunCleanup()
	})
	return mapSrv.URL, net.JoinHostPort("127.0.0.1", strconv.Itoa(derpPort))
}

type derpFrontend func(t *testing.T, ln net.Listener, h http.Handler) *httptest.Server

func tlsFrontend(t *testing.T, ln net.Listener, h http.Handler) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(h)
	s.Listener.Close()
	s.Listener = ln
	s.Config.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))
	s.StartTLS()
	return s
}

func plainHTTPFrontend(t *testing.T, ln net.Listener, h http.Handler) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(h)
	s.Listener.Close()
	s.Listener = ln
	s.Start()
	return s
}

// scanInvite reads the sender's stderr until the "tc..." invite line.
func scanInvite(t *testing.T, r io.Reader) string {
	t.Helper()
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "tc") && len(line) > 8 {
			return line
		}
	}
	t.Fatal("no invite found on sender stderr")
	return ""
}
