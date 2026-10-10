package awsapi

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Node is one position in a parameter tree built from flat dotted EC2/query
// parameters such as "LaunchTemplateConfigs.1.Overrides.2.InstanceType".
type Node struct {
	Val     string
	Kids    map[string]*Node
	present bool // distinguishes a real node from the empty node Get returns for misses
}

func newNode(present bool) *Node { return &Node{Kids: map[string]*Node{}, present: present} }

// Nest converts flat query parameters into a tree. A parameter's first value wins.
func Nest(v url.Values) *Node {
	root := newNode(true)
	for k, vals := range v {
		if len(vals) == 0 {
			continue
		}
		cur := root
		for _, seg := range strings.Split(k, ".") {
			next, ok := cur.Kids[seg]
			if !ok {
				next = newNode(true)
				cur.Kids[seg] = next
			}
			cur = next
		}
		cur.Val = vals[0]
	}
	return root
}

// Get walks a path; a missing path yields an empty node, so calls can be chained safely.
func (n *Node) Get(path ...string) *Node {
	cur := n
	for _, seg := range path {
		next, ok := cur.Kids[seg]
		if !ok {
			return newNode(false)
		}
		cur = next
	}
	return cur
}

// Str returns the string at a path, or "" if absent.
func (n *Node) Str(path ...string) string { return n.Get(path...).Val }

// Has reports whether the path exists.
func (n *Node) Has(path ...string) bool { return n.Get(path...).present }

// List returns the children whose keys are integers, ordered numerically
// (so "2" sorts before "10", unlike a plain string sort).
func (n *Node) List() []*Node {
	type kv struct {
		idx  int
		node *Node
	}
	var items []kv
	for k, child := range n.Kids {
		if i, err := strconv.Atoi(k); err == nil {
			items = append(items, kv{i, child})
		}
	}
	sort.Slice(items, func(a, b int) bool { return items[a].idx < items[b].idx }) // numeric, not lexical
	out := make([]*Node, len(items))
	for i, it := range items {
		out[i] = it.node
	}
	return out
}

// Strings returns the leaf values of a list node in order.
func (n *Node) Strings() []string {
	var out []string
	for _, c := range n.List() {
		out = append(out, c.Val)
	}
	return out
}

// ReadBody reads at most limit bytes and restores r.Body so later readers
// (the signature check, a reverse proxy) still see the full body.
func ReadBody(r *http.Request, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, limit+1)) // +1 detects overflow
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("request body exceeds %d bytes", limit)
	}
	r.Body = io.NopCloser(bytes.NewReader(b)) // restore for downstream readers
	return b, nil
}

// Form parses a query-protocol request: the body (form-encoded) merged with the URL query.
func Form(r *http.Request, body []byte) (url.Values, error) {
	v, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	for k, vals := range r.URL.Query() {
		if _, exists := v[k]; !exists { // body parameters take precedence
			v[k] = vals
		}
	}
	return v, nil
}
