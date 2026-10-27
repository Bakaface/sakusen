package db

import (
	"reflect"
	"testing"

	"github.com/Bakaface/sakusen/internal/task"
)

func TestPublishedItemsRoundTrip(t *testing.T) {
	db := openTestDBForWaits(t)
	pid := mustProject(t, db, "/p")
	tk := mustCreateTaskForWaits(t, db, pid, "t")

	if _, ok, err := db.GetPublishedItems(tk.ID, "plan"); err != nil || ok {
		t.Fatalf("GetPublishedItems before publish = ok %v, err %v; want ok=false", ok, err)
	}

	first := []task.PublishedItem{{Slug: "a", Title: "A", Input: "do a"}, {Slug: "b", Input: "do b", Needs: []string{"a"}}}
	if err := db.PublishItems(tk.ID, "plan", first); err != nil {
		t.Fatalf("PublishItems: %v", err)
	}
	got, ok, err := db.GetPublishedItems(tk.ID, "plan")
	if err != nil || !ok {
		t.Fatalf("GetPublishedItems = ok %v, err %v", ok, err)
	}
	if !reflect.DeepEqual(got, first) {
		t.Errorf("got %+v, want %+v", got, first)
	}

	second := []task.PublishedItem{{Slug: "c", Input: "do c"}}
	if err := db.PublishItems(tk.ID, "plan", second); err != nil {
		t.Fatalf("re-PublishItems: %v", err)
	}
	got, _, err = db.GetPublishedItems(tk.ID, "plan")
	if err != nil {
		t.Fatalf("GetPublishedItems: %v", err)
	}
	if !reflect.DeepEqual(got, second) {
		t.Errorf("after re-publish got %+v, want %+v", got, second)
	}

	other := []task.PublishedItem{{Slug: "x", Input: "do x"}}
	if err := db.PublishItems(tk.ID, "review", other); err != nil {
		t.Fatalf("PublishItems review: %v", err)
	}
	all, err := db.GetAllPublishedItems(tk.ID)
	if err != nil {
		t.Fatalf("GetAllPublishedItems: %v", err)
	}
	want := map[string][]task.PublishedItem{"plan": second, "review": other}
	if !reflect.DeepEqual(all, want) {
		t.Errorf("GetAllPublishedItems = %+v, want %+v", all, want)
	}

	for i := 0; i < 2; i++ {
		if err := db.ClearPublishedItems(tk.ID, "plan"); err != nil {
			t.Fatalf("ClearPublishedItems #%d: %v", i+1, err)
		}
	}
	if _, ok, err := db.GetPublishedItems(tk.ID, "plan"); err != nil || ok {
		t.Errorf("GetPublishedItems after clear = ok %v, err %v; want ok=false", ok, err)
	}
	if _, ok, _ := db.GetPublishedItems(tk.ID, "review"); !ok {
		t.Error("clearing plan also cleared review")
	}
}

func TestPublishedItemsCascadeOnTaskDelete(t *testing.T) {
	db := openTestDBForWaits(t)
	pid := mustProject(t, db, "/p")
	tk := mustCreateTaskForWaits(t, db, pid, "t")
	if err := db.PublishItems(tk.ID, "plan", []task.PublishedItem{{Slug: "a", Input: "x"}}); err != nil {
		t.Fatalf("PublishItems: %v", err)
	}
	if err := db.DeleteTask(tk.ID); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	var n int
	if err := db.sqlDB.QueryRow(`SELECT COUNT(*) FROM task_published_items WHERE task_id = ?`, tk.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("%d published rows survived task delete", n)
	}
}
