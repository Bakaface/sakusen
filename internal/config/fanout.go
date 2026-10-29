package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// validateFanoutTargets checks every fanout step's child workflow once all
// workflow tiers are assembled: it must exist and contain no human gate, since
// fanout children run unattended.
func validateFanoutTargets(cfg *Config) error {
	for i := range cfg.Workflows {
		wf := &cfg.Workflows[i]
		for j := range wf.Steps {
			step := &wf.Steps[j]
			if !step.IsFanout() {
				continue
			}
			if step.Fanout.IsNodes() {
				if err := validateFanoutNodeTargets(cfg, wf, step); err != nil {
					return err
				}
				continue
			}
			child := findWorkflowByName(cfg, step.Fanout.Workflow)
			if child == nil {
				return fmt.Errorf("workflow %q step %q: fanout workflow %q is not defined", wf.Name, step.Name, step.Fanout.Workflow)
			}
			for k := range child.Steps {
				if child.Steps[k].Human {
					return fmt.Errorf("workflow %q step %q: fanout workflow %q step %q has human: true (fanout children cannot wait on a human gate)",
						wf.Name, step.Name, child.Name, child.Steps[k].Name)
				}
			}
		}
	}
	return nil
}

// validateFanoutNodeTargets checks each node's effective child workflow. A
// node another node needs must merge on completion, since its dependents
// start from the result it lands.
func validateFanoutNodeTargets(cfg *Config, wf *WorkflowConfig, step *StepConfig) error {
	f := step.Fanout
	needed := make(map[string]bool)
	for _, n := range f.Nodes {
		for _, need := range n.Needs {
			needed[need] = true
		}
	}
	for k, n := range f.Nodes {
		name := f.EffectiveNodeWorkflow(k)
		if name == "" {
			return fmt.Errorf("workflow %q step %q: fanout node %q has no workflow: set it on the node or the fanout step", wf.Name, step.Name, n.Name)
		}
		child := findWorkflowByName(cfg, name)
		if child == nil {
			return fmt.Errorf("workflow %q step %q: fanout node %q workflow %q is not defined", wf.Name, step.Name, n.Name, name)
		}
		for s := range child.Steps {
			if child.Steps[s].Human {
				return fmt.Errorf("workflow %q step %q: fanout node %q workflow %q step %q has human: true (fanout children cannot wait on a human gate)",
					wf.Name, step.Name, n.Name, child.Name, child.Steps[s].Name)
			}
		}
		if needed[n.Name] {
			// "" is an unresolved tier (single-file validation); the merged load decides.
			if oc := cfg.EffectiveOnComplete(child.Name); oc != "merge" && oc != "" {
				return fmt.Errorf("workflow %q step %q: fanout node %q is needed by another node, so its workflow %q must have on_complete: merge (got %q)",
					wf.Name, step.Name, n.Name, child.Name, oc)
			}
		}
	}
	return nil
}

// FanoutConsumer returns the name of the first fanout step in the workflow
// whose `from:` is step, or "" when none is.
func (wf *WorkflowConfig) FanoutConsumer(step string) string {
	for i := range wf.Steps {
		if wf.Steps[i].IsFanout() && wf.Steps[i].Fanout.From != "" && wf.Steps[i].Fanout.From == step {
			return wf.Steps[i].Name
		}
	}
	return ""
}

// IsFanoutSource reports whether any fanout step in the workflow names step in
// its `from:`.
func (wf *WorkflowConfig) IsFanoutSource(step string) bool {
	return wf.FanoutConsumer(step) != ""
}

// fanoutNodeNameRe mirrors the item slug rule in internal/task (itemSlugRe),
// since a node name becomes its child's item slug.
var fanoutNodeNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// stepRefRe matches a {{steps.<name>[.context]}} placeholder the way the
// workflow template engine does.
var stepRefRe = regexp.MustCompile(`\{\{steps\.([a-zA-Z0-9_.-]+)\}\}`)

// validateFanoutNodes checks a `nodes:` fanout at index i: well-formed unique
// node names, an acyclic needs graph within the fanout, and node inputs that
// only read contexts guaranteed to exist when the node starts.
func (wf *WorkflowConfig) validateFanoutNodes(step *StepConfig, i int) error {
	f := step.Fanout
	if f.Input != "" {
		return fmt.Errorf("step %q: fanout input %q cannot be set with `nodes:` (set input on each node)", step.Name, f.Input)
	}
	if f.AllowEmpty {
		return fmt.Errorf("step %q: fanout allow_empty cannot be set with `nodes:` (declared nodes are never empty)", step.Name)
	}
	index := make(map[string]int, len(f.Nodes))
	for j, n := range f.Nodes {
		if n.Name == "" {
			return fmt.Errorf("step %q: fanout node %d is missing a name", step.Name, j)
		}
		if !fanoutNodeNameRe.MatchString(n.Name) {
			return fmt.Errorf("step %q: fanout node name %q is invalid (must match %s)", step.Name, n.Name, fanoutNodeNameRe)
		}
		if _, dup := index[n.Name]; dup {
			return fmt.Errorf("step %q: duplicate fanout node %q", step.Name, n.Name)
		}
		index[n.Name] = j
	}
	for _, n := range f.Nodes {
		if strings.TrimSpace(n.Input) == "" {
			return fmt.Errorf("step %q: fanout node %q needs `input:`", step.Name, n.Name)
		}
		if n.Retries != nil && *n.Retries < 0 {
			return fmt.Errorf("step %q: fanout node %q retries must be >= 0 (got %d)", step.Name, n.Name, *n.Retries)
		}
		for _, need := range n.Needs {
			if _, ok := index[need]; !ok {
				return fmt.Errorf("step %q: fanout node %q needs unknown node %q (needs may only name nodes of this fanout)", step.Name, n.Name, need)
			}
		}
	}
	if cycle := fanoutNodeCycle(f.Nodes, index); cycle != nil {
		return fmt.Errorf("step %q: fanout node %q is on a needs cycle: %s", step.Name, cycle[0], strings.Join(cycle, " -> "))
	}
	if err := validateFanoutCommon(step); err != nil {
		return err
	}
	if n, err := strconv.Atoi(f.Require); err == nil && n > len(f.Nodes) {
		return fmt.Errorf("step %q: fanout require %d exceeds the node count (%d)", step.Name, n, len(f.Nodes))
	}

	// Contexts that exist before the fanout starts: every name an earlier
	// top-level step, group branch or fanout node claims.
	earlier := make(map[string]bool)
	for k := range wf.Steps[:i] {
		prev := &wf.Steps[k]
		earlier[prev.Name] = true
		if prev.IsParallel() {
			for _, b := range prev.Parallel.Branches {
				earlier[b.Name] = true
			}
		}
		if prev.IsFanout() {
			for _, pn := range prev.Fanout.Nodes {
				earlier[pn.Name] = true
			}
		}
	}
	for j, n := range f.Nodes {
		ancestors := fanoutNodeAncestors(f.Nodes, index, j)
		for _, m := range stepRefRe.FindAllStringSubmatch(n.Input, -1) {
			ref := strings.TrimSuffix(m[1], ".context")
			if ancestors[ref] || earlier[ref] {
				continue
			}
			return fmt.Errorf("step %q: fanout node %q input references {{steps.%s}}, which is neither a transitive need of the node nor an earlier step",
				step.Name, n.Name, m[1])
		}
	}
	return nil
}

// fanoutNodeCycle returns the first needs cycle as a closed path of node
// names (e.g. a, b, a), or nil when the graph is acyclic.
func fanoutNodeCycle(nodes []FanoutNodeConfig, index map[string]int) []string {
	const (
		unvisited = iota
		visiting
		visited
	)
	state := make([]int, len(nodes))
	var stack []string
	var visit func(j int) []string
	visit = func(j int) []string {
		state[j] = visiting
		stack = append(stack, nodes[j].Name)
		for _, need := range nodes[j].Needs {
			k := index[need]
			switch state[k] {
			case visiting:
				for s, name := range stack {
					if name == need {
						return append(append([]string{}, stack[s:]...), need)
					}
				}
			case unvisited:
				if c := visit(k); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[j] = visited
		return nil
	}
	for j := range nodes {
		if state[j] == unvisited {
			if c := visit(j); c != nil {
				return c
			}
		}
	}
	return nil
}

// fanoutNodeAncestors returns the names node j transitively needs.
func fanoutNodeAncestors(nodes []FanoutNodeConfig, index map[string]int, j int) map[string]bool {
	out := make(map[string]bool)
	queue := append([]string{}, nodes[j].Needs...)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if out[name] {
			continue
		}
		out[name] = true
		queue = append(queue, nodes[index[name]].Needs...)
	}
	return out
}
