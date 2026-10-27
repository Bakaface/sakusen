package task

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// PublishedItem is one unit of work a step publishes for a fanout step to
// spawn as a child task.
type PublishedItem struct {
	Slug  string   `json:"slug"`
	Title string   `json:"title,omitempty"`
	Input string   `json:"input"`
	Needs []string `json:"needs,omitempty"`
}

// FanoutOrigin identifies the parent task, fanout step and item a child task
// was spawned for.
type FanoutOrigin struct {
	ParentID int64
	Step     string
	Item     string
}

var itemSlugRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// NormalizeItems validates a published item list and returns a normalized
// copy: missing slugs become item-<n> (1-based) and repeated needs entries are
// dropped. It rejects an empty list, empty inputs, malformed or duplicate
// slugs, unknown or self needs, and dependency cycles.
func NormalizeItems(items []PublishedItem) ([]PublishedItem, error) {
	if len(items) == 0 {
		return nil, errors.New("no items published")
	}
	out := make([]PublishedItem, len(items))
	index := make(map[string]int, len(items))
	for i, it := range items {
		if strings.TrimSpace(it.Input) == "" {
			return nil, fmt.Errorf("item %d: input is empty", i+1)
		}
		if it.Slug == "" {
			it.Slug = fmt.Sprintf("item-%d", i+1)
		}
		if !itemSlugRe.MatchString(it.Slug) {
			return nil, fmt.Errorf("item %d: invalid slug %q (letters, digits, '.', '_', '-'; must start with a letter or digit; max 64 chars)", i+1, it.Slug)
		}
		if prev, dup := index[it.Slug]; dup {
			return nil, fmt.Errorf("item %d: duplicate slug %q (also item %d)", i+1, it.Slug, prev+1)
		}
		index[it.Slug] = i
		out[i] = it
	}
	for i := range out {
		var needs []string
		seen := make(map[string]bool, len(out[i].Needs))
		for _, n := range out[i].Needs {
			if seen[n] {
				continue
			}
			seen[n] = true
			if n == out[i].Slug {
				return nil, fmt.Errorf("item %q needs itself", n)
			}
			if _, ok := index[n]; !ok {
				return nil, fmt.Errorf("item %q needs unknown item %q", out[i].Slug, n)
			}
			needs = append(needs, n)
		}
		out[i].Needs = needs
	}
	if cycle := findItemCycle(out, index); cycle != nil {
		return nil, fmt.Errorf("cycle: %s", strings.Join(cycle, " -> "))
	}
	return out, nil
}

// findItemCycle returns the slugs along a dependency cycle, closed with its
// first slug repeated, or nil when the graph is acyclic.
func findItemCycle(items []PublishedItem, index map[string]int) []string {
	const (
		unvisited = iota
		onStack
		done
	)
	state := make([]int, len(items))
	var stack []string
	var visit func(i int) []string
	visit = func(i int) []string {
		state[i] = onStack
		stack = append(stack, items[i].Slug)
		for _, n := range items[i].Needs {
			j := index[n]
			switch state[j] {
			case onStack:
				for k, s := range stack {
					if s == n {
						return append(append([]string{}, stack[k:]...), n)
					}
				}
			case unvisited:
				if c := visit(j); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[i] = done
		return nil
	}
	for i := range items {
		if state[i] == unvisited {
			if c := visit(i); c != nil {
				return c
			}
		}
	}
	return nil
}

// TopoOrder returns the indices of normalized items with every item after the
// items it needs; among items whose needs are satisfied, published order wins.
func TopoOrder(items []PublishedItem) []int {
	index := make(map[string]int, len(items))
	for i, it := range items {
		index[it.Slug] = i
	}
	placed := make([]bool, len(items))
	order := make([]int, 0, len(items))
	for len(order) < len(items) {
		progressed := false
		for i, it := range items {
			if placed[i] {
				continue
			}
			ready := true
			for _, n := range it.Needs {
				if j, ok := index[n]; ok && !placed[j] {
					ready = false
					break
				}
			}
			if ready {
				placed[i] = true
				order = append(order, i)
				progressed = true
				break
			}
		}
		if !progressed {
			break
		}
	}
	return order
}
