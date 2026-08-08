package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

var errAbort = errors.New("aborted")

type editor struct {
	r       *bufio.Reader
	w       io.Writer
	prompt  string
	history []string
	histIdx int
	fresh   string
	buf     []rune
	cur     int
}

func editLine(r io.Reader, w io.Writer, prompt, def string, history []string) (string, error) {
	e := &editor{
		r:       bufio.NewReader(r),
		w:       w,
		prompt:  prompt,
		history: history,
		histIdx: len(history),
		fresh:   def,
		buf:     []rune(def),
		cur:     utf8.RuneCountInString(def),
	}
	e.redraw()
	for {
		ru, _, err := e.r.ReadRune()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", errAbort
			}
			return "", err
		}
		switch {
		case ru == '\r' || ru == '\n':
			e.clear()
			return string(e.buf), nil
		case ru == 0x03:
			return "", errAbort
		case ru == 0x7f || ru == 0x08:
			e.backspace()
		case ru == 0x1b:
			e.escape()
		case ru < 0x20:
			// ignore other control chars (e.g. ctrl-D)
		default:
			e.insert(ru)
		}
	}
}

func promptTTY(fd int, r io.Reader, w io.Writer, prompt, def string, history []string) (string, error) {
	old, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer term.Restore(fd, old)
	return editLine(r, w, prompt, def, history)
}

func (e *editor) redraw() {
	left := len(e.buf) - e.cur
	move := ""
	if left > 0 {
		move = fmt.Sprintf("\x1b[%dD", left)
	}
	fmt.Fprintf(e.w, "\r\x1b[2K%s%s%s", e.prompt, string(e.buf), move)
}

func (e *editor) clear() {
	fmt.Fprintf(e.w, "\r\x1b[2K%s%s\r\n", e.prompt, string(e.buf))
}

func (e *editor) insert(ru rune) {
	e.buf = append(e.buf[:e.cur], append([]rune{ru}, e.buf[e.cur:]...)...)
	e.cur++
	e.redraw()
}

func (e *editor) backspace() {
	if e.cur == 0 {
		return
	}
	e.buf = append(e.buf[:e.cur-1], e.buf[e.cur:]...)
	e.cur--
	e.redraw()
}

func (e *editor) escape() {
	b1, err := e.r.ReadByte()
	if err != nil {
		return
	}
	if b1 != '[' {
		return
	}
	b2, err := e.r.ReadByte()
	if err != nil {
		return
	}
	switch b2 {
	case 'A':
		e.up()
	case 'B':
		e.down()
	case 'C':
		if e.cur < len(e.buf) {
			e.cur++
			e.redraw()
		}
	case 'D':
		if e.cur > 0 {
			e.cur--
			e.redraw()
		}
	default:
		for last := b2; last < 0x40 || last > 0x7e; {
			b, err := e.r.ReadByte()
			if err != nil {
				return
			}
			last = b
		}
	}
}

func (e *editor) up() {
	if e.histIdx == 0 {
		return
	}
	if e.histIdx == len(e.history) {
		e.fresh = string(e.buf)
	}
	e.histIdx--
	e.buf = []rune(e.history[e.histIdx])
	e.cur = len(e.buf)
	e.redraw()
}

func (e *editor) down() {
	if e.histIdx >= len(e.history) {
		return
	}
	e.histIdx++
	if e.histIdx == len(e.history) {
		e.buf = []rune(e.fresh)
	} else {
		e.buf = []rune(e.history[e.histIdx])
	}
	e.cur = len(e.buf)
	e.redraw()
}

func promptBaseTime(sc *bufio.Scanner, label, def string, history []string) (string, bool) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return prompt(sc, label, def)
	}
	s, err := promptTTY(fd, os.Stdin, os.Stderr, label+": ", def, history)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(s), true
}
