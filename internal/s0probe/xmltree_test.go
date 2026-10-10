package s0probe

import "testing"

func TestXMLTreeNavigation(t *testing.T) {
	n, err := ParseXML([]byte(`<?xml version="1.0"?><R xmlns="urn:x"><requestId>r1</requestId>
	<set><item><id>a</id><tags><item><k>x</k></item></tags></item><item><id>b</id></item></set></R>`))
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "R" || n.Text("requestId") != "r1" {
		t.Fatalf("root = %s, requestId = %q", n.Name, n.Text("requestId"))
	}
	if got := n.Texts("set/item/id"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("ids = %v", got)
	}
	if n.Text("set/item/tags/item/k") != "x" || n.Text("nope/at/all") != "" || len(n.All("set/missing")) != 0 {
		t.Fatal("path lookups misbehaved")
	}
	if _, err := ParseXML([]byte("")); err == nil {
		t.Fatal("empty input must fail")
	}
}

// A truncated or hostile body must be an error, never a panic.
func TestXMLTreeMalformedInput(t *testing.T) {
	for _, in := range []string{"<a><b></a>", "<a>", "</a>", "not xml", "<a></a></a>"} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("ParseXML(%q) panicked: %v", in, r)
				}
			}()
			if _, err := ParseXML([]byte(in)); err == nil {
				t.Errorf("ParseXML(%q) should fail", in)
			}
		}()
	}
}
