package probe

import (
	"errors"
	"net"
	"net/http"
	"syscall"
	"time"
)

// ErrNotPublic is a connection a public-only client refused: the address is
// loopback, private, link-local, unspecified, multicast or shared.
var ErrNotPublic = errors.New("not a public address")

// notPublic are the ranges Public refuses beyond what net.IP reports:
// "this network" (0/8, which reaches the host on Linux) and the shared
// address space of carrier NAT and tailnets (100.64/10).
var notPublic = []*net.IPNet{mustCIDR("0.0.0.0/8"), mustCIDR("100.64.0.0/10")}

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// Public reports whether ip is an address a probe of untrusted origin (a
// repo's homepage) may reach: not loopback, private (RFC 1918, ULA),
// link-local, unspecified, multicast or shared. IPv4-mapped IPv6 addresses
// are judged as IPv4.
func Public(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, n := range notPublic {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

// NewPublicClient is NewClient for URLs anyone can set (repo homepages): it
// connects only to public addresses. The check runs on the address being
// dialled, after DNS resolution, so a name that resolves (or rebinds) to a
// local address is refused too. It uses no proxy, so the check sees the
// real destination, and it follows no redirects (NewClient's rule).
func NewPublicClient() *http.Client {
	d := &net.Dialer{
		Timeout: Timeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return ErrNotPublic
			}
			if !Public(net.ParseIP(host)) {
				return ErrNotPublic
			}
			return nil
		},
	}
	c := NewClient()
	c.Transport = &http.Transport{
		Proxy:               nil,
		DialContext:         d.DialContext,
		TLSHandshakeTimeout: Timeout,
		MaxIdleConns:        10,
		IdleConnTimeout:     30 * time.Second,
	}
	return c
}
