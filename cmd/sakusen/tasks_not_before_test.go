package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/daemon"
)

func capturePrintTaskDetail(t *testing.T, info *daemon.TaskInfo) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	printTaskDetail(info)
	os.Stdout = orig
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestPrintTaskDetail_NotBefore(t *testing.T) {
	at := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	out := capturePrintTaskDetail(t, &daemon.TaskInfo{ID: 1, Title: "t", Status: "pending", NotBefore: &at})
	if want := "Not before:  " + at.Local().Format(time.RFC3339); !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
	out = capturePrintTaskDetail(t, &daemon.TaskInfo{ID: 1, Title: "t", Status: "pending"})
	if strings.Contains(out, "Not before") {
		t.Errorf("Not before shown without a value:\n%s", out)
	}
}
