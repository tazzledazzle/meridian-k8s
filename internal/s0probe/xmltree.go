package s0probe

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// maxXMLDepth bounds nesting so a hostile or broken backend cannot make the parser build a huge stack.
const maxXMLDepth = 256

// Node is a namespace-free XML element. AWS query-protocol responses are deep
// trees of repeated <item> elements, and the checks only need "does this path
// exist and what does it say", so a tiny tree beats a struct per operation.
type Node struct {
	Name  string
	Chars string // character data directly inside the element
	Kids  []*Node
}

// charsetReader supports the declared encodings an AWS-compatible backend may legally send: UTF-8, US-ASCII (both
// pass through; the decoder rejects invalid UTF-8 itself) and ISO-8859-1/latin1 (every byte is the same code point).
// Anything else is refused with a clear error rather than guessed at. No third-party dependency.
func charsetReader(label string, in io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "utf-8", "utf8", "us-ascii", "ascii":
		return in, nil
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1":
		b, err := io.ReadAll(in)
		if err != nil {
			return nil, err
		}
		rs := make([]rune, len(b))
		for i, c := range b {
			rs[i] = rune(c)
		}
		return strings.NewReader(string(rs)), nil
	}
	return nil, fmt.Errorf("unsupported XML encoding %q (supported: UTF-8, US-ASCII, ISO-8859-1)", label)
}

// ParseXML parses a response body into a tree rooted at the document element.
func ParseXML(b []byte) (*Node, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.CharsetReader = charsetReader
	root := &Node{}
	stack := []*Node{root}
	// Character data is collected in one builder per open element: appending to a string for every token (comments
	// split text into many tokens) would copy the text so far each time, which is quadratic.
	chars := []*strings.Builder{{}}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) > maxXMLDepth {
				return nil, errors.New("XML nested too deeply")
			}
			n := &Node{Name: t.Name.Local}
			top := stack[len(stack)-1]
			top.Kids = append(top.Kids, n)
			stack = append(stack, n)
			chars = append(chars, &strings.Builder{})
		case xml.EndElement:
			if len(stack) > 1 { // the decoder already rejects unbalanced tags; this keeps the root safe regardless
				stack[len(stack)-1].Chars = chars[len(chars)-1].String()
				stack = stack[:len(stack)-1]
				chars = chars[:len(chars)-1]
			}
		case xml.CharData:
			chars[len(chars)-1].Write(t)
		}
	}
	if len(stack) != 1 {
		return nil, io.ErrUnexpectedEOF // an element was never closed
	}
	if len(root.Kids) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	return root.Kids[0], nil
}

// All returns every node at a slash-separated path below n ("a/b/item").
func (n *Node) All(path string) []*Node {
	cur := []*Node{n}
	for _, seg := range strings.Split(path, "/") {
		var next []*Node
		for _, c := range cur {
			for _, k := range c.Kids {
				if k.Name == seg {
					next = append(next, k)
				}
			}
		}
		cur = next
	}
	return cur
}

// Text returns the trimmed text of the first node at path, or "".
func (n *Node) Text(path string) string {
	if got := n.All(path); len(got) > 0 {
		return strings.TrimSpace(got[0].Chars)
	}
	return ""
}

// Texts returns the trimmed text of every node at path.
func (n *Node) Texts(path string) []string {
	var out []string
	for _, g := range n.All(path) {
		out = append(out, strings.TrimSpace(g.Chars))
	}
	return out
}
