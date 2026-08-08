package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoryPath(t *testing.T) {
	t.Setenv("HOME", "/tmp/fake-home")
	t.Setenv("XDG_STATE_HOME", "")
	got, err := historyPath()
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if want := "/tmp/fake-home/.local/state/mead/history"; got != want {
		t.Fatalf("default historyPath = %q, want %q", got, want)
	}

	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state")
	got, err = historyPath()
	if err != nil {
		t.Fatalf("xdg: %v", err)
	}
	if want := "/tmp/xdg-state/mead/history"; got != want {
		t.Fatalf("xdg historyPath = %q, want %q", got, want)
	}
}

func TestAppendHistoryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history")
	for _, e := range []string{"2026:08:03 09:00:00-04:00", "2026-08-04 10:00:00"} {
		if err := appendHistory(path, e); err != nil {
			t.Fatalf("append %q: %v", e, err)
		}
	}
	got, err := loadHistory(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []string{"2026:08:03 09:00:00-04:00", "2026-08-04 10:00:00"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("round-trip = %v, want %v", got, want)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("history perms = %o, want 600", fi.Mode().Perm())
	}
}

func TestAppendHistoryDedupe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history")
	for _, e := range []string{"a", "b", "b", "c", "c", "c"} {
		if err := appendHistory(path, e); err != nil {
			t.Fatalf("append %q: %v", e, err)
		}
	}
	got, _ := loadHistory(path)
	want := []string{"a", "b", "c"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("dedupe = %v, want %v", got, want)
	}
}

func TestAppendHistoryCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history")
	histMax := historyMax
	historyMax = 5
	defer func() { historyMax = histMax }()
	for i := 0; i < 10; i++ {
		if err := appendHistory(path, string(rune('0'+i))); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	got, _ := loadHistory(path)
	if len(got) != 5 {
		t.Fatalf("cap: len = %d, want 5", len(got))
	}
	if want := "56789"; strings.Join(got, "") != want {
		t.Fatalf("cap dropped oldest: %v, want %q", got, want)
	}
}

func TestLoadHistorySkipsBlanks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history")
	if err := os.WriteFile(path, []byte("  a  \n\nb\n\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := loadHistory(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("skips blanks = %v, want [a b]", got)
	}
}

func TestAppendHistoryErrorTolerant(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	if err := appendHistory(filepath.Join(blocker, "x", "history"), "a"); err == nil {
		t.Fatalf("expected error for unwritable path")
	}
	if err := appendHistory("", ""); err != nil {
		t.Fatalf("empty entry should be a no-op, got %v", err)
	}
}
