package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/task"
)

func TestPublishItemsParse(t *testing.T) {
	items, err := parsePublishItems(strings.NewReader(`[{"input":"do a","slug":"a","title":"A"},{"input":"do b","needs":["a"]}]`))
	if err != nil {
		t.Fatal(err)
	}
	want := []task.PublishedItem{
		{Slug: "a", Title: "A", Input: "do a"},
		{Input: "do b", Needs: []string{"a"}},
	}
	if !reflect.DeepEqual(items, want) {
		t.Errorf("items = %+v, want %+v", items, want)
	}

	for name, in := range map[string]string{
		"invalid JSON": `[{"input": }`,
		"non-array":    `{"input":"x"}`,
		"empty input":  ``,
	} {
		if _, err := parsePublishItems(strings.NewReader(in)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestPublishItemsResolveTarget(t *testing.T) {
	env := func(vals map[string]string) func(string) string {
		return func(k string) string { return vals[k] }
	}
	full := env(map[string]string{"SAKUSEN_TASK_ID": "42", "SAKUSEN_STEP": "plan"})

	if id, step, err := resolvePublishTarget(0, "", full); err != nil || id != 42 || step != "plan" {
		t.Errorf("env defaults: got (%d, %q, %v)", id, step, err)
	}
	if id, step, err := resolvePublishTarget(7, "design", full); err != nil || id != 7 || step != "design" {
		t.Errorf("flags should win over env: got (%d, %q, %v)", id, step, err)
	}

	for name, tc := range map[string]struct {
		task int64
		step string
		env  map[string]string
		want string
	}{
		"no task":       {0, "plan", nil, "--task is required"},
		"no step":       {1, "", nil, "--step is required"},
		"bad env task":  {0, "plan", map[string]string{"SAKUSEN_TASK_ID": "x"}, "invalid SAKUSEN_TASK_ID"},
		"negative task": {-1, "plan", nil, "positive integer"},
	} {
		if _, _, err := resolvePublishTarget(tc.task, tc.step, env(tc.env)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}
