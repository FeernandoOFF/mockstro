package tui

import (
	"sort"
	"strings"
)

// mockRow is one line of the mock target explorer: a folder or a target.
type mockRow struct {
	depth  int
	folder string // folder key ("" for targets)
	label  string
	target *mockTarget
}

type mockNode struct {
	name    string
	kids    map[string]*mockNode
	targets []*mockTarget
}

func (n *mockNode) child(name string) *mockNode {
	if n.kids == nil {
		n.kids = map[string]*mockNode{}
	}
	c := n.kids[name]
	if c == nil {
		c = &mockNode{name: name}
		n.kids[name] = c
	}
	return c
}

// buildRows lays targets out as scenarios / profiles / flows groups; flows nest by folder
// under flows/, and a folder holding only one folder is merged into it ("a/b").
func buildRows(targets []*mockTarget, open map[string]bool, expandAll bool) []mockRow {
	root := &mockNode{}
	for _, t := range targets {
		n := root.child(t.kind + "s")
		if t.kind == "flow" {
			dirs := strings.Split(t.rel, "/")
			for _, d := range dirs[:len(dirs)-1] {
				n = n.child(d)
			}
		}
		n.targets = append(n.targets, t)
	}
	var rows []mockRow
	var walk func(n *mockNode, key string, depth int)
	walk = func(n *mockNode, key string, depth int) {
		for _, name := range sortedKids(n) {
			c, label := n.kids[name], name
			for len(c.targets) == 0 && len(c.kids) == 1 && depth > 0 {
				only := sortedKids(c)[0]
				c, label = c.kids[only], label+"/"+only
			}
			k := key + "/" + label
			rows = append(rows, mockRow{depth: depth, folder: k, label: label})
			if isOpen(open, k, depth, expandAll) {
				for _, t := range c.targets {
					rows = append(rows, mockRow{depth: depth + 1, label: t.label, target: t})
				}
				walk(c, k, depth+1)
			}
		}
	}
	walk(root, "", 0)
	return rows
}

// isOpen: top-level groups start open, sub-folders closed; a filter opens everything.
func isOpen(open map[string]bool, key string, depth int, expandAll bool) bool {
	if expandAll {
		return true
	}
	if v, ok := open[key]; ok {
		return v
	}
	return depth == 0
}

func sortedKids(n *mockNode) []string {
	order := map[string]int{"scenarios": 0, "profiles": 1, "flows": 2}
	names := make([]string, 0, len(n.kids))
	for k := range n.kids {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		oi, iok := order[names[i]]
		oj, jok := order[names[j]]
		if iok && jok {
			return oi < oj
		}
		return names[i] < names[j]
	})
	return names
}
