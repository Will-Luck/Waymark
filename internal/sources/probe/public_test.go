package probe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"203.0.113.7": true, "8.8.8.8": true, "2001:db8::1": true, "2606:4700::1111": true,
		"127.0.0.1": false, "127.9.9.9": false, "::1": false, // loopback
		"10.1.2.3": false, "172.16.0.1": false, "172.31.255.254": false, "fc00::1": false, "fd12:3456::1": false, // private, ULA
		"169.254.169.254": false, "fe80::1": false, // link-local
		"0.0.0.0": false, "::": false, "0.1.2.3": false, // unspecified, this network
		"224.0.0.1": false, "239.255.255.250": false, "ff02::1": false, // multicast
		"100.64.0.1": false, "100.127.255.254": false, // shared address space (carrier NAT, tailnets)
		"::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false, // IPv4-mapped
	} {
		if got := Public(net.ParseIP(addr)); got != want {
			t.Errorf("Public(%s) = %v, want %v", addr, got, want)
		}
	}
	if Public(nil) {
		t.Error("Public(nil) = true")
	}
}

// The public client refuses a loopback server after resolving it: the server
// is never asked, and the result says refused, not down.
func TestPublicClientRefusesLocal(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	// By address, and by a name that resolves to it.
	port := strconv.Itoa(srv.Listener.Addr().(*net.TCPAddr).Port)
	for _, u := range []string{srv.URL + "/", "http://localhost:" + port + "/"} {
		r := Check(context.Background(), NewPublicClient(), u, "dev")
		if !r.Refused || r.Up || r.Code != 0 {
			t.Errorf("%s: %+v, want refused", u, r)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the local server was asked %d times", n)
	}
	// The plain client still reaches it (projects-file urls stay unrestricted).
	if r := Check(context.Background(), NewClient(), srv.URL+"/", "dev"); !r.Up || r.Refused {
		t.Errorf("plain client: %+v", r)
	}
}
