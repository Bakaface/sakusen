package main

import (
	"slices"
	"testing"
)

func TestAttachCommandFor(t *testing.T) {
	tests := []struct {
		name       string
		insideTmux bool
		want       []string
	}{
		{"inside tmux switches client", true, []string{"tmux", "switch-client", "-t", "=sakusen-proj-5"}},
		{"outside tmux attaches", false, []string{"tmux", "attach-session", "-t", "=sakusen-proj-5"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := attachCommandFor("sakusen-proj-5", tt.insideTmux).Args
			if !slices.Equal(got, tt.want) {
				t.Errorf("attachCommandFor(..., %v).Args = %v, want %v", tt.insideTmux, got, tt.want)
			}
		})
	}
}
