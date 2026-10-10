package ec2sim

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tazzledazzle/meridian/internal/catalog"
)

// pageAll follows nextToken until it runs out, returning the pages' type names.
func pageAll(t *testing.T, h http.Handler, size string, extra ...string) [][]string {
	t.Helper()
	var pages [][]string
	token := ""
	for i := 0; i < 50; i++ {
		kv := append([]string{"MaxResults", size}, extra...)
		if token != "" {
			kv = append(kv, "NextToken", token)
		}
		code, body := call(t, h, "DescribeInstanceTypes", kv...)
		if code != 200 {
			t.Fatalf("page %d: status %d: %s", i, code, body)
		}
		r := parse(t, body)
		pages = append(pages, names(r))
		if r.Next == "" {
			return pages
		}
		token = r.Next
	}
	t.Fatal("pagination did not terminate")
	return nil
}

func TestPaginationWalksEveryTypeOnceInOrder(t *testing.T) {
	pages := pageAll(t, New(catalog.Types), "5")
	var flat []string
	for _, p := range pages {
		if len(p) > 5 {
			t.Errorf("page of %d exceeds MaxResults", len(p))
		}
		flat = append(flat, p...)
	}
	if got, want := strings.Join(flat, ","), strings.Join(allNames(), ","); got != want {
		t.Errorf("paged = %s\nwant    = %s", got, want)
	}
	if len(pages) != 3 {
		t.Errorf("%d pages, want 3 (5+5+2)", len(pages))
	}
}

func TestPaginationHasNoTokenWhenResultFitsExactly(t *testing.T) {
	pages := pageAll(t, New(catalog.Types), "100")
	if len(pages) != 1 || len(pages[0]) != len(catalog.Types) {
		t.Errorf("pages = %v", pages)
	}
	// exactly one full last page: 12 types, MaxResults 6 -> two pages, no empty third
	if p := pageAll(t, New(catalog.Types), "6"); len(p) != 2 {
		t.Errorf("%d pages, want 2", len(p))
	}
}

func TestPaginationAppliesFiltersAcrossPages(t *testing.T) {
	pages := pageAll(t, New(catalog.Types), "5", "Filter.1.Name", "processor-info.supported-architecture", "Filter.1.Value.1", "x86_64")
	n := 0
	for _, p := range pages {
		n += len(p)
	}
	if n != 10 {
		t.Errorf("x86_64 types = %d, want 10", n)
	}
}

func TestBadPaginationParameters(t *testing.T) {
	h := New(catalog.Types)
	for name, kv := range map[string][]string{
		"too small":      {"MaxResults", "4"},
		"too large":      {"MaxResults", "101"},
		"not a number":   {"MaxResults", "many"},
		"garbage token":  {"MaxResults", "5", "NextToken", "!!!"},
		"foreign token":  {"MaxResults", "5", "NextToken", "aGVsbG8"},
		"token no limit": {"NextToken", "x"},
		"max with types": {"MaxResults", "5", "InstanceType.1", "m5.large"},
	} {
		t.Run(name, func(t *testing.T) {
			code, body := call(t, h, "DescribeInstanceTypes", kv...)
			wantError(t, code, body, 400, "InvalidParameterValue")
		})
	}
}

func TestOtherActionsAreInvalidAction(t *testing.T) {
	h := New(catalog.Types)
	for _, a := range []string{"RunInstances", "CreateFleet", "DescribeInstances", "describeinstancetypes", "DescribeInstanceTypeOfferings"} {
		code, body := call(t, h, a)
		e := wantError(t, code, body, 400, "InvalidAction")
		if !strings.Contains(e.Message, a) {
			t.Errorf("%s: message %q does not name the action", a, e.Message)
		}
	}
	code, body := call(t, h, "")
	wantError(t, code, body, 400, "MissingAction")
}

func TestMethodsBodiesAndTypesOutsideTheCatalog(t *testing.T) {
	h := New(catalog.Types)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/", nil))
	wantError(t, rec.Code, rec.Body.String(), 405, "InvalidAction")

	// GET with the query string is legal for the query protocol.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?Action=DescribeInstanceTypes&InstanceType.1=m5.large", nil))
	if rec.Code != 200 || len(parse(t, rec.Body.String()).Items) != 1 {
		t.Errorf("GET: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("Action=%zz")))
	wantError(t, rec.Code, rec.Body.String(), 400, "MalformedQueryString")

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("Action=DescribeInstanceTypes&x="+strings.Repeat("a", 2<<20))))
	wantError(t, rec.Code, rec.Body.String(), 413, "InvalidParameterValue")

	// A handler built from a subset serves only that subset, whatever the catalog holds.
	small := New(catalog.Types[:2])
	_, body := call(t, small, "DescribeInstanceTypes")
	if got := len(parse(t, body).Items); got != 2 {
		t.Errorf("subset handler returned %d", got)
	}
	code, body := call(t, small, "DescribeInstanceTypes", "InstanceType.1", catalog.Types[5].Name)
	wantError(t, code, body, 400, "InvalidInstanceType")
}
