// Copyright ©2018 Dan Kortschak. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The mbg program extracts a contact graph from an mbox file, constructing
// edges between addresses that appear together in From:, To:, Cc: and Bcc:
// lists.
package main

import (
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/mail"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/blabber/mbox"

	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/encoding"
	"gonum.org/v1/gonum/graph/encoding/dot"
	"gonum.org/v1/gonum/graph/formats/gexf12"
	"gonum.org/v1/gonum/graph/multi"
)

func main() {
	format := flag.String("format", "dot", "output format (dot or gexf)")
	excl := flag.String("exclude", "", "regex for email addresses to exclude")
	drop := flag.String("drop-from", "", "regex for emails to drop on From:")
	verbose := flag.Bool("verbose", false, "verbosely log warnings")
	flag.Parse()

	var exclude *regexp.Regexp
	var err error
	if *excl != "" {
		exclude, err = regexp.Compile(*excl)
		if err != nil {
			log.Fatalf("failed to parse exclude pattern: %v", *excl)
		}
	}
	var dropFrom *regexp.Regexp
	if *drop != "" {
		dropFrom, err = regexp.Compile(*drop)
		if err != nil {
			log.Fatalf("failed to parse drop-from pattern: %v", *drop)
		}
	}

	ms := mbox.NewScanner(os.Stdin)
	bufsize := 1 << 30
	buf := make([]byte, bufsize)
	ms.Buffer(buf, bufsize)

	g := addrGraph{multi.NewUndirectedGraph(), make(map[string]int64)}

messages:
	for ms.Next() {
		m := ms.Message()
		addrs, err := extractAddrs(nil, m.Header, "from", exclude, dropFrom)
		if err != nil {
			if err == dropMessage {
				continue messages
			}
			if *verbose {
				log.Printf("failed to extract from: address list: %v", err)
			}
		}
		for _, tag := range []string{"to", "cc", "bcc"} {
			addrs, err = extractAddrs(addrs, m.Header, tag, exclude, nil)
			if err != nil && *verbose {
				log.Printf("failed to extract %v: address list: %v", tag, err)
			}
		}
		date, err := m.Header.Date()
		if err != nil && *verbose {
			log.Printf("failed to extract date: %v", err)
		}
		if len(addrs) < 2 {
			continue
		}
		sort.Strings(addrs)
		for i, a := range addrs[1:] {
			if addrs[i] == a {
				addrs[i] = ""
			}
		}
		for i := 0; i < len(addrs); {
			if addrs[i] == "" {
				addrs[i], addrs = addrs[len(addrs)-1], addrs[:len(addrs)-1]
			} else {
				i++
			}
		}
		if len(addrs) < 2 {
			if date.IsZero() && *verbose {
				log.Print("not enough addresses")
			} else if *verbose {
				log.Printf("not enough addresses for message at %v", date)
			}
			continue
		}
		mid := m.Header.Get("message-id")

		for i, p := range addrs {
			for _, q := range addrs[i+1:] {
				g.SetLine(g.message(p, q, date, mid))
			}
		}
	}
	err = ms.Err()
	if err != nil {
		log.Fatalf("error during mbox parse: %v", err)
	}

	switch *format {
	case "dot":
		b, err := dot.Marshal(g, "", "", "  ", false)
		if err != nil {
			log.Fatalf("failed to format DOT: %v", err)
		}
		fmt.Printf("%s\n", b)
	case "gexf":
		marshalGexf(os.Stdout, g)
		if err != nil {
			log.Fatal("failed to format GEXF: %v", err)
		}
	default:
		log.Fatalf("invalid format: %q", *format)
	}
}

const dateTime = "2006-01-02T15:04:05"

var dropMessage = errors.New("drop message")

func extractAddrs(dst []string, h mail.Header, tag string, exclude, drop *regexp.Regexp) ([]string, error) {
	addrs, err := h.AddressList(tag)
	if err != nil {
		if err == mail.ErrHeaderNotPresent {
			err = nil
		}
		return dst, err
	}
	for _, a := range addrs {
		addr := strings.ToLower(a.Address)
		if drop != nil && drop.MatchString(addr) {
			return nil, dropMessage
		}
		if exclude != nil && exclude.MatchString(addr) {
			continue
		}
		dst = append(dst, addr)
	}
	return dst, nil
}

// addrGraph is a multigraph based on string IDs.
type addrGraph struct {
	*multi.UndirectedGraph

	id map[string]int64
}

// addrGraph will report edge weights based on line connections
// between nodes.
var _ graph.Weighted = addrGraph{}

// person returns the graph node for a given address. If
// the address does not already exist in the graph, it is
// created and inserted into the graph.
func (g addrGraph) person(addr string) graph.Node {
	id, ok := g.id[addr]
	if ok {
		return g.Node(id)
	}
	p := person{Node: g.UndirectedGraph.NewNode(), addr: addr}
	g.AddNode(p)
	g.id[addr] = p.ID()
	return p
}

// message returns a graph line representing the message
// containing addressed individuals represented by the nodes
// x and y, on the given date and with the given message ID.
func (g addrGraph) message(x, y string, date time.Time, mid string) graph.Line {
	return message{Line: g.NewLine(g.person(x), g.person(y)), date: date, mid: mid}
}

func (g addrGraph) Edge(xid, yid int64) graph.Edge {
	return g.WeightedEdge(xid, yid)
}

func (g addrGraph) WeightedEdge(xid, yid int64) graph.WeightedEdge {
	e := g.LinesBetween(xid, yid)
	if e == nil {
		return nil
	}
	return edge{multi.Edge{F: g.Node(xid), T: g.Node(yid), Lines: e}}
}

func (g addrGraph) Weight(xid, yid int64) (float64, bool) {
	e := g.LinesBetween(xid, yid)
	if e == nil {
		return 0, false
	}
	return float64(e.Len()), true
}

type person struct {
	graph.Node
	addr string
}

func (n person) DOTID() string { return fmt.Sprintf("%q", n.addr) }

type message struct {
	graph.Line
	date time.Time
	mid  string
}

func (l message) Attributes() []encoding.Attribute {
	return []encoding.Attribute{
		{Key: "date", Value: fmt.Sprint(l.date)},
		{Key: "message-id", Value: l.mid}}
}

type edge struct {
	multi.Edge
}

func (e edge) Weight() float64 { return float64(e.Edge.Len()) }

func (e edge) Attributes() []encoding.Attribute {
	var sd, ed time.Time
	for e.Next() {
		d := e.Line().(message).date
		if d.IsZero() {
			continue
		}
		if sd.IsZero() || d.Before(sd) {
			sd = d
		}
		if ed.IsZero() || d.After(ed) {
			ed = d
		}
	}
	e.Reset()
	return []encoding.Attribute{
		{Key: "weight", Value: fmt.Sprint(e.Weight())},
		{Key: "sd", Value: fmt.Sprint(sd)},
		{Key: "start", Value: fmt.Sprint(sd.Unix())},
		{Key: "ed", Value: fmt.Sprint(ed)},
		{Key: "end", Value: fmt.Sprint(ed.Unix())},
	}
}

func marshalGexf(dst io.Writer, g addrGraph) error {
	c := gexf12.Content{
		Graph: gexf12.Graph{
			TimeFormat:      "dateTime",
			DefaultEdgeType: "undirected",
			Mode:            "dynamic",
			Attributes: []gexf12.Attributes{{
				Class: "edge",
				Mode:  "dynamic",
				Attributes: []gexf12.Attribute{{
					ID:    "mid",
					Title: "message-ID",
					Type:  "string",
				}},
			}},
		},
		Version: "1.2",
	}

	nodes := g.Nodes()
	c.Graph.Nodes.Count = nodes.Len()
	c.Graph.Nodes.Nodes = make([]gexf12.Node, 0, nodes.Len())
	for nodes.Next() {
		n := nodes.Node()
		c.Graph.Nodes.Nodes = append(c.Graph.Nodes.Nodes, gexf12.Node{
			ID:    fmt.Sprint(n.ID()),
			Label: n.(person).addr,
		})
	}

	edges := g.Edges()
	for edges.Next() {
		e := edges.Edge().(multi.Edge)
		for e.Next() {
			m := e.Line().(message)
			l := gexf12.Edge{
				ID:     fmt.Sprint(m.ID()),
				Source: fmt.Sprint(m.From().ID()),
				Target: fmt.Sprint(m.To().ID()),
			}
			var date string
			if !m.date.IsZero() {
				date = m.date.Format(dateTime)
				l.Start = date
				l.End = date
			}
			if m.mid != "" {
				att := gexf12.AttValue{
					For:   "mid",
					Value: m.mid,
				}
				if !m.date.IsZero() {
					att.Start = date
					att.End = date
				}
				l.AttValues = &gexf12.AttValues{AttValues: []gexf12.AttValue{att}}
			}
			c.Graph.Edges.Edges = append(c.Graph.Edges.Edges, l)
		}
	}
	c.Graph.Edges.Count = len(c.Graph.Edges.Edges)

	fmt.Println(xml.Header)
	enc := xml.NewEncoder(dst)
	enc.Indent("", "\t")
	return enc.Encode(c)
}
