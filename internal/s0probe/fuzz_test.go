package s0probe

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func FuzzParseXML(f *testing.F) {
	for _, s := range []string{
		"", "<a/>", "<a><b>x</b></a>", `<?xml version="1.0"?><a xmlns="u"><b k="v">x</b></a>`, "<a><!-- c -->x<![CDATA[y]]></a>",
		`<!DOCTYPE a [<!ENTITY x "y">]><a>&x;</a>`, `<!DOCTYPE l [<!ENTITY a "aa"><!ENTITY b "&a;&a;&a;&a;">]><l>&b;&b;</l>`,
		"<a></b>", "<a>", strings.Repeat("<a>", 300), "\xef\xbb\xbf<a/>", "<a>\x00</a>", "<ns:a xmlns:ns='u'><ns:b/></ns:a>",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		start := time.Now()
		n, err := ParseXML(b)
		if time.Since(start) > 5*time.Second {
			t.Fatalf("took %s for %d bytes", time.Since(start), len(b))
		}
		if err != nil {
			if n != nil {
				t.Fatal("both a node and an error")
			}
			return
		}
		if n == nil {
			t.Fatal("no node and no error")
		}
		// the navigation helpers must not panic on whatever came out
		_ = n.Text("a/b/c")
		_ = n.Texts("x")
		_ = n.All("")
		_ = n.All("//")
	})
}

func FuzzGetPath(f *testing.F) {
	f.Add(`{"a":{"b":"c"}}`, "a.b")
	f.Add(`{"a":[1,2]}`, "a.0")
	f.Add(`null`, "")
	f.Add(`{"":{"":1}}`, ".")
	f.Fuzz(func(t *testing.T, doc, path string) {
		m, err := decodeObject([]byte(doc))
		if err != nil {
			return
		}
		_ = Get(m, path)
		_ = GetString(m, path)
	})
}

func FuzzParsePrice(f *testing.F) {
	f.Add(priceDoc("m5.large", "0.096"))
	f.Add(`{}`)
	f.Add(`{"terms":{"OnDemand":{"a":{"priceDimensions":{"b":{"pricePerUnit":{"USD":"NaN"}}}}}}}`)
	f.Add(`{"product":{"attributes":{"instanceType":5}}}`)
	f.Fuzz(func(t *testing.T, raw string) {
		_, _, _ = parsePrice(raw)
	})
}

var fuzzStatuses = []string{"pass", "fail", "skip"}

func fuzzResults(names []string, details []string, seed uint8) []Result {
	var rs []Result
	for i, d := range details {
		rs = append(rs, Result{
			Name:   names[(i+int(seed))%len(names)],
			Group:  []string{"ec2", "iam", "sqs", "ssm", "sts", "pricing", "eks"}[(i+int(seed))%7],
			Status: fuzzStatuses[(i+int(seed))%3],
			Detail: d,
		})
	}
	return rs
}

func knownNames() []string {
	var n []string
	for _, c := range Checks() {
		n = append(n, c.Name)
	}
	return n
}

func FuzzSummarizeJSON(f *testing.F) {
	f.Add("plain", "a|b\nc", "\xff\xfe", uint8(0))
	f.Add("", "\r\n", " ", uint8(1))
	f.Add(`"quoted" \ back`, "<script>", "\x00", uint8(2))
	names := knownNames()
	f.Fuzz(func(t *testing.T, d1, d2, d3 string, seed uint8) {
		rs := fuzzResults(names, []string{d1, d2, d3}, seed)
		s := Summarize(rs)
		if s.Pass+s.Fail+s.Skip != len(rs) {
			t.Fatalf("summary %+v for %d results", s, len(rs))
		}
		b, err := JSON(rs)
		if err != nil {
			t.Fatalf("JSON: %v", err)
		}
		var out struct {
			Results []struct{ Name, Status, Detail string }
		}
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("JSON output is not valid JSON: %v", err)
		}
		if len(out.Results) != len(rs) {
			t.Fatalf("%d results came back for %d", len(out.Results), len(rs))
		}
		for i, r := range out.Results {
			if r.Name != rs[i].Name || r.Status != rs[i].Status {
				t.Fatalf("result %d changed: %+v vs %+v", i, r, rs[i])
			}
			if utf8.ValidString(rs[i].Detail) && rs[i].Detail != r.Detail {
				t.Fatalf("detail %d changed: %q vs %q", i, r.Detail, rs[i].Detail)
			}
		}
	})
}

func FuzzSummarizeMarkdown(f *testing.F) {
	f.Add("plain", "a|b\nc", "| x | y |", uint8(0))
	f.Add("", "\n\n", "`|`", uint8(1))
	f.Add("<b>", "\t", " |\u0085", uint8(2))
	names := knownNames()
	f.Fuzz(func(t *testing.T, d1, d2, d3 string, seed uint8) {
		rs := fuzzResults(names, []string{d1, d2, d3}, seed)
		md := Markdown("title", rs)
		rows := checkTableRows(md)
		if len(rows) != len(rs) {
			t.Fatalf("%d table rows for %d results in\n%s", len(rows), len(rs), md)
		}
		for i, r := range rows {
			if n := strings.Count(r, "|"); n != 5 {
				t.Fatalf("row %d has %d pipes (detail %q): %q", i, n, rs[i].Detail, r)
			}
		}
		if !strings.Contains(md, "How to read this report") {
			t.Fatal("the A4 note is missing")
		}
	})
}
