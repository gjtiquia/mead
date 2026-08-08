package main

import (
	"bytes"
	"errors"
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

func TestEditLineBasic(t *testing.T) {
	tests := []struct {
		name string
		in   string
		def  string
		want string
	}{
		{"type_and_enter", "2026-08-03 09:00:00\r", "", "2026-08-03 09:00:00"},
		{"enter_keeps_default", "\r", "2026:08:03 09:00:00-04:00", "2026:08:03 09:00:00-04:00"},
		{"append_to_default", " 09:30:00\r", "2026-08-03", "2026-08-03 09:30:00"},
		{"backspace", "abc\x7f\r", "", "ab"},
		{"left_arrow_insert", "abc\x1b[DX\r", "", "abXc"},
		{"right_arrow", "abc\x1b[D\x1b[D\x1b[CY\r", "", "abYc"},
		{"ignore_ctrl_d", "\x04a\r", "", "a"},
		{"ctrl_d_after_text_ignored", "ab\x04\r", "", "ab"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			got, err := editLine(bytes.NewReader([]byte(tc.in)), &out, "x: ", tc.def, nil)
			if err != nil {
				t.Fatalf("editLine err: %v", err)
			}
			if got != tc.want {
				t.Fatalf("editLine = %q, want %q", got, tc.want)
			}
			if !strings.Contains(out.String(), "x: "+tc.def) {
				t.Fatalf("output missing prompt+default: %q", out.String())
			}
		})
	}
}

func TestEditLineAbort(t *testing.T) {
	var out bytes.Buffer
	if _, err := editLine(bytes.NewReader([]byte("abc\x03")), &out, "x: ", "", nil); !errors.Is(err, errAbort) {
		t.Fatalf("ctrl-C err = %v, want errAbort", err)
	}
	if _, err := editLine(bytes.NewReader([]byte("abc")), &out, "x: ", "", nil); !errors.Is(err, errAbort) {
		t.Fatalf("EOF err = %v, want abort", err)
	}
}

func TestEditLineHistory(t *testing.T) {
	hist := []string{"2026:08:01 09:00:00-04:00", "2026:08:02 09:00:00-04:00"}
	tests := []struct {
		name string
		in   string
		def  string
		want string
	}{
		{"up_to_newest", "\x1b[A\r", "fresh", "2026:08:02 09:00:00-04:00"},
		{"up_up_to_oldest", "\x1b[A\x1b[A\r", "fresh", "2026:08:01 09:00:00-04:00"},
		{"down_past_newest_is_empty", "\x1b[A\x1b[B\r", "", ""},
		{"down_past_newest_restores_fresh", "\x1b[A\x1b[B\r", "fresh", "fresh"},
		{"stash_preserved_while_scrolling", "D\x1b[A\x1b[B\r", "fresh", "freshD"},
		{"up_at_oldest_stays", "\x1b[A\x1b[A\x1b[A\r", "fresh", "2026:08:01 09:00:00-04:00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			got, err := editLine(bytes.NewReader([]byte(tc.in)), &out, "x: ", tc.def, hist)
			if err != nil {
				t.Fatalf("editLine err: %v", err)
			}
			if got != tc.want {
				t.Fatalf("editLine = %q, want %q", got, tc.want)
			}
		})
	}
}
