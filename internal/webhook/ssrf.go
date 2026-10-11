package webhook

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

// blockedIP reports addresses an outbound webhook must not reach: loopback,
// private ranges, link-local (including cloud metadata at 169.254.169.254),
// multicast and unspecified.
func blockedIP(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		// 100.64.0.0/10 carrier-grade NAT, 192.0.0.0/24, 198.18.0.0/15.
		switch {
		case ip4[0] == 100 && ip4[1]&0xC0 == 64:
			return true
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0:
			return true
		case ip4[0] == 198 && ip4[1]&0xFE == 18:
			return true
		}
	}
	return false
}

// newHTTPClient builds the client used for outbound deliveries. The address
// check happens in the dialer's Control hook, on the IP actually being
// connected to, so a hostname that resolves to a private address (or changes
// its answer between lookup and connect) is still refused.
func newHTTPClient(allowPrivate bool, timeout time.Duration) *http.Client {
	d := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	if !allowPrivate {
		d.Control = func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || blockedIP(ip) {
				return fmt.Errorf("destination %s is not allowed (private or local address)", host)
			}
			return nil
		}
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           func(ctx context.Context, n, a string) (net.Conn, error) { return d.DialContext(ctx, n, a) },
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       60 * time.Second,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // a redirect is a response, never followed
		},
	}
}

var errNoHost = errors.New("url needs a host")
