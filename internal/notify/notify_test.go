package notify

import (
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
)

type fakeBackend struct {
	titles, bodies []string
}

func (f *fakeBackend) Notify(title, body string, _ Urgency) error {
	f.titles = append(f.titles, title)
	f.bodies = append(f.bodies, body)
	return nil
}

func TestMergeBlockedGating(t *testing.T) {
	cases := []struct {
		name     string
		cfg      config.NotificationsConfig
		wantSent bool
	}{
		{"disabled", config.NotificationsConfig{}, false},
		{"flag only", config.NotificationsConfig{OnMergeBlocked: true}, false},
		{"enabled only", config.NotificationsConfig{Enabled: true}, false},
		{"enabled and flag", config.NotificationsConfig{Enabled: true, OnMergeBlocked: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := &fakeBackend{}
			n := &Notifier{cfg: &tc.cfg, backend: backend}
			if err := n.MergeBlocked(7, "docs edit", []string{"?? scratch.txt"}); err != nil {
				t.Fatal(err)
			}
			if got := len(backend.titles) == 1; got != tc.wantSent {
				t.Fatalf("sent = %v, want %v", got, tc.wantSent)
			}
		})
	}
}

func TestMergeBlockedBodyListsFirstPaths(t *testing.T) {
	backend := &fakeBackend{}
	n := &Notifier{cfg: &config.NotificationsConfig{Enabled: true, OnMergeBlocked: true}, backend: backend}
	paths := []string{"?? a", "M b", "M c", "M d", "M e"}
	if err := n.MergeBlocked(7, "docs edit", paths); err != nil {
		t.Fatal(err)
	}
	body := backend.bodies[0]
	for _, want := range []string{"#7: docs edit", "?? a", "M c", "and 2 more"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q missing %q", body, want)
		}
	}
	if strings.Contains(body, "M d") {
		t.Errorf("body %q lists more than the first paths", body)
	}
}
