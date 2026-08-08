package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

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
	if _, err := editLine(bytes.NewReader([]byte("a\x1b")), &out, "x: ", "", nil); !errors.Is(err, errAbort) {
		t.Fatalf("bare ESC at EOF err = %v, want abort", err)
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

func TestEditLineEdges(t *testing.T) {
	tests := []struct {
		name string
		in   string
		def  string
		want string
	}{
		{"backspace_at_start", "a\x1b[D\x7f\r", "", "a"},
		{"backspace_on_empty", "\x7f\r", "", ""},
		{"left_at_start", "\x1b[DX\r", "", "X"},
		{"right_at_end", "a\x1b[CX\r", "", "aX"},
		{"home_end_ignored", "ab\x1b[H\x1b[Fc\r", "", "abc"},
		{"home_tilde_ignored", "ab\x1b[1~c\r", "", "abc"},
		{"end_tilde_ignored", "ab\x1b[4~c\r", "", "abc"},
		{"delete_ignored", "ab\x1b[3~c\r", "", "abc"},
		{"alt_key_swallows_next", "ab\x1bc\r", "", "ab"},
		{"ctrl_chars_ignored", "\x01\x05\x15\x04abc\r", "", "abc"},
		{"multibyte_insert", "éX\r", "", "éX"},
		{"multibyte_backspace", "é\x7f\r", "", ""},
		{"multibyte_mixed", "héllo\x7f\r", "", "héll"},
		{"multibyte_left_arrow", "héllo\x1b[D\x1b[DX\r", "", "hélXlo"},
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
		})
	}
}

func TestEditLineHistoryEdges(t *testing.T) {
	tests := []struct {
		name string
		in   string
		def  string
		hist []string
		want string
	}{
		{"empty_history_up", "\x1b[A\r", "def", nil, "def"},
		{"empty_history_down", "\x1b[B\r", "def", nil, "def"},
		{"down_at_fresh_noop", "\x1b[B\r", "def", []string{"a", "b"}, "def"},
		{"recalled_entry_edits_discarded", "X\x1b[A\x1b[A\x1b[B\r", "c", []string{"a", "b"}, "b"},
		{"utf8_history_entry", "\x1b[A\r", "def", []string{"hél"}, "hél"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			got, err := editLine(bytes.NewReader([]byte(tc.in)), &out, "x: ", tc.def, tc.hist)
			if err != nil {
				t.Fatalf("editLine err: %v", err)
			}
			if got != tc.want {
				t.Fatalf("editLine = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEditLineRedraw(t *testing.T) {
	var out bytes.Buffer
	if got, err := editLine(bytes.NewReader([]byte("ab\x1b[DX\r")), &out, "p: ", "", nil); err != nil {
		t.Fatalf("editLine err: %v", err)
	} else if got != "aXb" {
		t.Fatalf("editLine = %q, want aXb", got)
	}
	o := out.String()
	if !strings.Contains(o, "\r\x1b[2Kp: aXb\n") {
		t.Fatalf("missing final clear line: %q", o)
	}
	if !strings.Contains(o, "\x1b[1D") {
		t.Fatalf("missing cursor-left move for mid-line insert: %q", o)
	}

	out.Reset()
	if got, err := editLine(bytes.NewReader([]byte("\r")), &out, "p: ", "def", nil); err != nil {
		t.Fatalf("editLine err: %v", err)
	} else if got != "def" {
		t.Fatalf("editLine = %q, want def", got)
	}
	o = out.String()
	if !strings.Contains(o, "\r\x1b[2Kp: def\n") {
		t.Fatalf("missing default clear line: %q", o)
	}
}
