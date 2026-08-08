package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"
)

const (
	layoutColonOffset = "2006:01:02 15:04:05-07:00"
	layoutColon       = "2006:01:02 15:04:05"
	layoutDashOffset  = "2006-01-02 15:04:05-07:00"
	layoutDash        = "2006-01-02 15:04:05"
)

type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

var errFatal = errors.New("fatal error")

func main() {
	os.Exit(runMain(os.Args[1:]))
}

func runMain(args []string) int {
	fs := pflag.NewFlagSet("mead", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		w := fs.Output()
		fmt.Fprintln(w, "mead — media date-fixer")
		fmt.Fprintln(w, "usage: mead <base_time> [dir] [flags]")
		fmt.Fprintln(w, "       mead   (interactive prompts)")
		fs.PrintDefaults()
	}
	inc := fs.Int("inc", 1, "seconds added per file in sequence")
	tz := fs.String("tz", "", "IANA name or fixed offset, e.g. America/Montreal or -04:00 (default device local)")
	dryRun := fs.Bool("dry-run", false, "preview without writing")
	help := fs.BoolP("help", "h", false, "show usage")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *help {
		fs.SetOutput(os.Stdout)
		fs.Usage()
		return 0
	}

	opts := Options{Dir: ".", Inc: *inc, TZ: *tz, DryRun: *dryRun}
	switch rest := fs.Args(); len(rest) {
	case 0:
		if err := promptInteractive(&opts); err != nil {
			fmt.Fprintln(os.Stderr, "mead:", err)
			return 2
		}
	case 1:
		opts.BaseTime = rest[0]
	case 2:
		opts.BaseTime = rest[0]
		opts.Dir = rest[1]
		if info, err := os.Stat(opts.Dir); err != nil || !info.IsDir() {
			fmt.Fprintf(os.Stderr, "mead: bad dir: %s\n", opts.Dir)
			fs.Usage()
			return 2
		}
	default:
		fmt.Fprintln(os.Stderr, "mead: usage: mead <base_time> [dir] [flags]")
		fs.Usage()
		return 2
	}

	if err := run(opts, os.Stdout); err != nil {
		var ue *usageError
		switch {
		case errors.As(err, &ue):
			fmt.Fprintf(os.Stderr, "mead: %v\n", err)
			return 2
		case errors.Is(err, errFatal):
			return 1
		default:
			fmt.Fprintf(os.Stderr, "mead: %v\n", err)
			return 1
		}
	}
	return 0
}

type Options struct {
	Dir      string
	BaseTime string
	Inc      int
	TZ       string
	DryRun   bool
}

type Category int

const (
	categoryNone Category = iota
	categoryPhoto
	categoryVideoModern
	categoryVideoAVI
)

type fileTask struct {
	path string
	name string
	cat  Category
}

func run(opts Options, stdout io.Writer) error {
	etool, err := exec.LookPath("exiftool")
	if err != nil {
		return fmt.Errorf("missing required dependency: exiftool\ninstall with:  brew install exiftool ffmpeg")
	}
	ftool, err := exec.LookPath("ffmpeg")
	if err != nil {
		return fmt.Errorf("missing required dependency: ffmpeg\ninstall with:  brew install exiftool ffmpeg")
	}

	loc, err := resolveTZ(opts.TZ)
	if err != nil {
		return &usageError{err}
	}
	base, err := parseBaseTime(opts.BaseTime, loc)
	if err != nil {
		return &usageError{err}
	}
	if opts.Inc < 0 {
		return &usageError{errors.New("--inc must be >= 0")}
	}

	entries, err := os.ReadDir(opts.Dir)
	if err != nil {
		return err
	}
	var known []fileTask
	var unknown []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		cat, ok := classifyExt(name)
		if !ok {
			unknown = append(unknown, name)
			continue
		}
		known = append(known, fileTask{path: filepath.Join(opts.Dir, name), name: name, cat: cat})
	}
	sort.Slice(known, func(i, j int) bool { return known[i].name < known[j].name })
	sort.Strings(unknown)

	if len(known) == 0 && len(unknown) == 0 {
		fmt.Fprintf(stdout, "mead: no supported media files in %s\n", opts.Dir)
		return nil
	}

	changed, errored := 0, 0
	for i, ft := range known {
		t := base.Add(time.Duration(i*opts.Inc) * time.Second)
		if opts.DryRun {
			for _, c := range planFile(etool, ftool, ft, t) {
				fmt.Fprintf(stdout, "  $ %s\n", c)
			}
			continue
		}
		var ferr error
		switch ft.cat {
		case categoryPhoto, categoryVideoModern:
			argv := exifCmd(etool, ft.path, t, true)
			_, ferr = runCmd(argv[0], argv[1:])
		case categoryVideoAVI:
			ferr = writeAVI(etool, ftool, ft.path, t)
		}
		if ferr != nil {
			fmt.Fprintf(stdout, "  ERROR    %s  %v\n", ft.name, ferr)
			errored++
			continue
		}
		fmt.Fprintf(stdout, "  CHANGED  %s  %s \n", ft.name, t.Format(layoutDash))
		changed++
	}
	for _, u := range unknown {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(u), "."))
		fmt.Fprintf(stdout, "  UNKNOWN  %s  (.%s not in whitelist, skipped)\n", u, ext)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "  %d changed · 0 skipped · %d unknown · %d errors\n", changed, len(unknown), errored)

	if errored > 0 {
		return errFatal
	}
	return nil
}

func promptInteractive(opts *Options) error {
	sc := bufio.NewScanner(os.Stdin)
	for {
		b, ok := prompt(sc, "base_time", "")
		if !ok {
			return fmt.Errorf("aborted")
		}
		if b == "" {
			fmt.Fprintln(os.Stderr, "mead: base_time required")
			continue
		}
		loc, _ := resolveTZ(opts.TZ)
		if _, err := parseBaseTime(b, loc); err != nil {
			fmt.Fprintf(os.Stderr, "mead: %v\n", err)
			continue
		}
		opts.BaseTime = b
		break
	}
	s, _ := prompt(sc, "seconds per file", "1")
	if n, err := strconv.Atoi(s); err == nil {
		opts.Inc = n
	} else {
		opts.Inc = 1
	}
	_, off := time.Now().Zone()
	sign := "+"
	if off < 0 {
		sign = "-"
		off = -off
	}
	tz, _ := prompt(sc, "timezone", fmt.Sprintf("%s%02d%02d", sign, off/3600, off%3600/60))
	opts.TZ = tz
	dr, _ := prompt(sc, "dry-run? [y/N]", "N")
	opts.DryRun = strings.HasPrefix(strings.ToLower(dr), "y")
	return nil
}

func prompt(sc *bufio.Scanner, label, def string) (string, bool) {
	if def == "" {
		fmt.Fprintf(os.Stderr, "%s: ", label)
	} else {
		fmt.Fprintf(os.Stderr, "%s [%s]: ", label, def)
	}
	if !sc.Scan() {
		return def, false
	}
	line := strings.TrimSpace(sc.Text())
	if line == "" {
		return def, true
	}
	return line, true
}

func resolveTZ(s string) (*time.Location, error) {
	if s == "" {
		return time.Local, nil
	}
	if s == "Z" || s == "UTC" {
		return time.UTC, nil
	}
	if s[0] == '+' || s[0] == '-' {
		rest := strings.ReplaceAll(s[1:], ":", "")
		n, err := strconv.Atoi(rest)
		if len(rest) != 4 || err != nil || n < 0 {
			return nil, fmt.Errorf("bad --tz offset %q", s)
		}
		h, m := n/100, n%100
		if h > 23 || m > 59 {
			return nil, fmt.Errorf("bad --tz offset %q", s)
		}
		sign := 1
		if s[0] == '-' {
			sign = -1
		}
		return time.FixedZone(s, sign*(h*3600+m*60)), nil
	}
	loc, err := time.LoadLocation(s)
	if err != nil {
		return nil, fmt.Errorf("bad --tz %q: %w", s, err)
	}
	return loc, nil
}

func parseBaseTime(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty base_time")
	}
	for _, l := range []string{layoutColonOffset, layoutDashOffset, layoutColon, layoutDash} {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("bad base_time %q (use 'YYYY:MM:DD HH:MM:SS[±HH:MM]' or 'YYYY-MM-DD HH:MM:SS')", s)
}

func classifyExt(name string) (Category, bool) {
	switch ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), ".")); ext {
	case "jpg", "jpeg", "png", "heic", "tif", "tiff", "gif":
		return categoryPhoto, true
	case "mp4", "m4v", "mov":
		return categoryVideoModern, true
	case "avi":
		return categoryVideoAVI, true
	}
	return categoryNone, false
}

func exifCmd(exif, file string, t time.Time, embedded bool) []string {
	ts := t.Format(layoutColonOffset)
	if embedded {
		return []string{exif, "-overwrite_original", "-AllDates=" + ts, "-FileCreateDate=" + ts, file}
	}
	return []string{exif, "-overwrite_original", "-FileCreateDate=" + ts, file}
}

func aviCmds(exif, ffmpeg, file string, t time.Time) [][]string {
	// exiftool can't write RIFF/AVI containers, so ffmpeg embeds the ICRD
	// (embedded capture date) first; exiftool then sets the filesystem dates.
	wall := t.Format(layoutDash)
	tmp := filepath.Join(filepath.Dir(file), fmt.Sprintf("mead-%d.avi", os.Getpid()))
	return [][]string{
		{ffmpeg, "-y", "-i", file, "-c", "copy", "-metadata", "date=" + wall, tmp},
		{"mv", tmp, file},
		exifCmd(exif, file, t, false),
	}
}

func writeAVI(exif, ffmpeg, file string, t time.Time) error {
	for i, argv := range aviCmds(exif, ffmpeg, file, t) {
		if _, err := runCmd(argv[0], argv[1:]); err != nil {
			if i == 0 {
				os.Remove(argv[len(argv)-1])
			}
			return err
		}
	}
	return nil
}

func planFile(exif, ffmpeg string, ft fileTask, t time.Time) []string {
	switch ft.cat {
	case categoryPhoto, categoryVideoModern:
		return []string{cmdLine(exifCmd(exif, ft.path, t, true))}
	case categoryVideoAVI:
		var cmds []string
		for _, argv := range aviCmds(exif, ffmpeg, ft.path, t) {
			cmds = append(cmds, cmdLine(argv))
		}
		return cmds
	}
	return nil
}

func cmdLine(argv []string) string { return strings.Join(argv, " ") }

func runCmd(name string, args []string) ([]byte, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return out, err
}
