package gateway

import "testing"

func TestCheckBind(t *testing.T) {
	cases := []struct {
		addr     string
		allowAny bool
		wantErr  bool
	}{
		{"127.0.0.1:4566", false, false},
		{"localhost:4566", false, false},
		{"[::1]:4566", false, false},
		{":4566", false, true},
		{"0.0.0.0:4566", false, true},
		{"192.168.1.20:4566", false, true},
		{"0.0.0.0:4566", true, false},
		{"not-an-address", false, true},
	}
	for _, c := range cases {
		err := CheckBind(c.addr, c.allowAny)
		if (err != nil) != c.wantErr {
			t.Errorf("CheckBind(%q, %v) error = %v, wantErr %v", c.addr, c.allowAny, err, c.wantErr)
		}
	}
}

// A malformed address must stay an error even with the opt-in: the opt-in only
// widens the interface, it never excuses garbage.
func TestCheckBindOptInDoesNotAcceptGarbage(t *testing.T) {
	if err := CheckBind("not-an-address", true); err == nil {
		t.Fatal("garbage address accepted with allowAnyInterface")
	}
}

// Hostnames other than localhost resolve to arbitrary interfaces, so they are refused.
func TestCheckBindRefusesOtherHostnames(t *testing.T) {
	for _, a := range []string{"example.com:4566", "localhost.evil.com:4566", "[::]:4566", "127.0.0.1.nip.io:4566"} {
		if err := CheckBind(a, false); err == nil {
			t.Errorf("CheckBind(%q) accepted", a)
		}
	}
}
