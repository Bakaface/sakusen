package task

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeItems_Rejects(t *testing.T) {
	tests := []struct {
		name    string
		items   []PublishedItem
		wantErr string
	}{
		{"empty list", nil, "no items"},
		{"empty input", []PublishedItem{{Slug: "a", Input: "x"}, {Slug: "b", Input: "  "}}, "item 2: input is empty"},
		{"bad slug", []PublishedItem{{Slug: "-a", Input: "x"}}, `invalid slug "-a"`},
		{"slug too long", []PublishedItem{{Slug: strings.Repeat("a", 65), Input: "x"}}, "invalid slug"},
		{"duplicate slug", []PublishedItem{{Slug: "a", Input: "x"}, {Slug: "a", Input: "y"}}, `duplicate slug "a"`},
		{"duplicate auto slug", []PublishedItem{{Slug: "item-2", Input: "x"}, {Input: "y"}}, `duplicate slug "item-2"`},
		{"unknown need", []PublishedItem{{Slug: "a", Input: "x", Needs: []string{"z"}}}, `needs unknown item "z"`},
		{"self need", []PublishedItem{{Slug: "a", Input: "x", Needs: []string{"a"}}}, `"a" needs itself`},
		{"two-cycle", []PublishedItem{
			{Slug: "a", Input: "x", Needs: []string{"b"}},
			{Slug: "b", Input: "y", Needs: []string{"a"}},
		}, "cycle: a -> b -> a"},
		{"three-cycle", []PublishedItem{
			{Slug: "root", Input: "r"},
			{Slug: "a", Input: "x", Needs: []string{"root", "c"}},
			{Slug: "b", Input: "y", Needs: []string{"a"}},
			{Slug: "c", Input: "z", Needs: []string{"b"}},
		}, "cycle: a -> c -> b -> a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NormalizeItems(tt.items)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestNormalizeItems_FillsSlugsAndDedupesNeeds(t *testing.T) {
	in := []PublishedItem{
		{Input: "first"},
		{Slug: "b", Input: "second", Needs: []string{"item-1", "item-1"}},
		{Input: "third", Needs: []string{"b"}},
	}
	got, err := NormalizeItems(in)
	if err != nil {
		t.Fatalf("NormalizeItems: %v", err)
	}
	want := []PublishedItem{
		{Slug: "item-1", Input: "first"},
		{Slug: "b", Input: "second", Needs: []string{"item-1"}},
		{Slug: "item-3", Input: "third", Needs: []string{"b"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if in[0].Slug != "" || len(in[1].Needs) != 2 {
		t.Fatalf("input was mutated: %+v", in)
	}
}

func TestNormalizeItems_KeepsPublishedOrder(t *testing.T) {
	in := []PublishedItem{{Slug: "z", Input: "1"}, {Slug: "a", Input: "2"}, {Slug: "m", Input: "3"}}
	got, err := NormalizeItems(in)
	if err != nil {
		t.Fatalf("NormalizeItems: %v", err)
	}
	for i, s := range []string{"z", "a", "m"} {
		if got[i].Slug != s {
			t.Fatalf("got[%d].Slug = %q, want %q", i, got[i].Slug, s)
		}
	}
}

func TestTopoOrder(t *testing.T) {
	tests := []struct {
		name  string
		items []PublishedItem
		want  []int
	}{
		{"no needs keeps order", []PublishedItem{{Slug: "a"}, {Slug: "b"}, {Slug: "c"}}, []int{0, 1, 2}},
		{"dependency moves first", []PublishedItem{
			{Slug: "a", Needs: []string{"c"}},
			{Slug: "b"},
			{Slug: "c"},
		}, []int{1, 2, 0}},
		{"chain", []PublishedItem{
			{Slug: "a", Needs: []string{"b"}},
			{Slug: "b", Needs: []string{"c"}},
			{Slug: "c"},
		}, []int{2, 1, 0}},
		{"diamond", []PublishedItem{
			{Slug: "d", Needs: []string{"b", "c"}},
			{Slug: "c", Needs: []string{"a"}},
			{Slug: "b", Needs: []string{"a"}},
			{Slug: "a"},
		}, []int{3, 1, 2, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TopoOrder(tt.items); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("TopoOrder = %v, want %v", got, tt.want)
			}
		})
	}
}
