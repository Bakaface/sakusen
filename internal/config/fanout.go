package config

import "fmt"

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

// IsFanoutSource reports whether any fanout step in the workflow names step in
// its `from:`.
func (wf *WorkflowConfig) IsFanoutSource(step string) bool {
	for i := range wf.Steps {
		if wf.Steps[i].IsFanout() && wf.Steps[i].Fanout.From == step {
			return true
		}
	}
	return false
}
