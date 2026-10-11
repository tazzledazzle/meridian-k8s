package gatecheck

import "strings"

// NoRoute reports whether a failed probe's error text shows the absence of a route: no route or network, an
// unreachable host, a timeout, or a name that cannot be resolved. Anything that proves a peer answered (connection
// refused or reset, a TLS or certificate error, EOF) is NOT absence of a route, so a broken client or CA bundle
// cannot make an open network look closed (C7).
func NoRoute(detail string) bool {
	d := strings.ToLower(detail)
	if strings.Contains(d, "connection refused") || strings.Contains(d, "connection reset") || strings.Contains(d, "x509") || strings.Contains(d, "tls:") ||
		strings.Contains(d, "certificate") || strings.HasSuffix(d, "eof") {
		return false
	}
	for _, none := range []string{"no route to host", "network is unreachable", "host is unreachable", "i/o timeout",
		"download timed out", "server misbehaving", "no such host", "bad address", "name resolution", "deadline exceeded"} {
		if strings.Contains(d, none) {
			return true
		}
	}
	return false
}
