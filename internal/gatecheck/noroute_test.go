package gatecheck

import "testing"

func TestNoRouteClassification(t *testing.T) {
	for detail, want := range map[string]bool{
		// Real outputs from the gate stack (2026-10-11).
		`unreachable: Get "https://1.1.1.1": dial tcp 1.1.1.1:443: connect: no route to host`:                                                       true,
		`unreachable: Get "https://ec2.us-west-2.amazonaws.com": dial tcp: lookup ec2.us-west-2.amazonaws.com on 127.0.0.11:53: server misbehaving`: true,
		`wget: can't connect to remote host (1.1.1.1): Host is unreachable`:                                                                         true,
		`wget: can't connect to remote host (1.1.1.1): Network is unreachable`:                                                                      true,
		`wget: bad address 'ec2.us-west-2.amazonaws.com'`:                                                                                           true,
		`unreachable: Get "https://x": dial tcp 10.0.0.1:443: i/o timeout`:                                                                          true,
		`wget: download timed out`: true,
		// A route exists: these must NOT count as "unreachable".
		`unreachable: Get "https://x": dial tcp 10.0.0.1:443: connect: connection refused`:                               false,
		`unreachable: Get "https://x": tls: failed to verify certificate: x509: certificate signed by unknown authority`: false,
		`wget: can't connect to remote host (10.0.0.1): Connection refused`:                                              false,
		`unreachable: Get "https://x": EOF`:                                                                              false,
		`unreachable: Get "https://x": read tcp: connection reset by peer`:                                               false,
		`reachable: HTTP 403`: false,
		``:                    false,
	} {
		if got := NoRoute(detail); got != want {
			t.Errorf("NoRoute(%q) = %v, want %v", detail, got, want)
		}
	}
}

func TestMixedEvidenceIsNotNoRoute(t *testing.T) {
	// A refusal anywhere in the text means a route exists, even if the text also says "timeout".
	if NoRoute(`dial tcp: i/o timeout; then connection refused`) {
		t.Fatal("a refusal must veto the timeout")
	}
}
