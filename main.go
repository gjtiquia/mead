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
	layoutHeader      = "2006-01-02 15:04:05 -0700"
)

var (
	errUsage = errors.New("usage error")
	errFatal = errors.New("fatal error")
)

func main() {
	os.Exit(runMain(os.Args[1:]))
}

func runMain(args []string) int {
	fs := pflag.NewFlagSet("mead", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { usage(os.Stderr) }
	inc := fs.Int("inc", 1, "seconds added per file in sequence")
	tz := fs.String("tz", "", "IANA name or fixed offset")
	dryRun := fs.Bool("dry-run", false, "preview without writing")
	help := fs.BoolP("help", "h", false, "show usage")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *help {
		usage(os.Stdout)
		return 0
	}

	opts := Options{Inc: *inc, TZ: *tz, DryRun: *dryRun}
	switch rest := fs.Args(); len(rest) {
	case 0:
		if err := promptInteractive(&opts); err != nil {
			fmt.Fprintln(os.Stderr, "mead:", err)
			return 2
		}
	case 2:
		opts.Dir = rest[0]
		opts.BaseTime = rest[1]
		if info, err := os.Stat(opts.Dir); err != nil || !info.IsDir() {
			fmt.Fprintf(os.Stderr, "mead: bad dir: %s\n", opts.Dir)
			fs.Usage()
			return 2
		}
	default:
		fmt.Fprintln(os.Stderr, "mead: usage: mead <dir> <base_time> [flags]")
		fs.Usage()
		return 2
	}

	if err := run(opts, os.Stdout); err != nil {
		if !errors.Is(err, errUsage) && !errors.Is(err, errFatal) {
			fmt.Fprintf(os.Stderr, "mead: %v\n", err)
		}
		if errors.Is(err, errUsage) {
			return 2
		}
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "mead — media date-fixer")
	fmt.Fprintln(w, "usage: mead <dir> <base_time> [flags]")
	fmt.Fprintln(w, "       mead   (interactive prompts)")
	fmt.Fprintln(w, "flags:")
	fmt.Fprintln(w, "  --inc N     seconds per file in sequence (default 1)")
	fmt.Fprintln(w, "  --tz TZ     IANA name or fixed offset, e.g. America/Montreal or -04:00 (default device local)")
	fmt.Fprintln(w, "  --dry-run   preview without writing")
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
	catNone Category = iota
	catPhoto
	catVideoModern
	catVideoAVI
)

type fileTask struct {
	path string
	name string
	cat  Category
}

func run(opts Options, stdout io.Writer) error {
	etool, err := resolveTool("exiftool", "EXIFTOOL_PATH")
	if err != nil {
		return fmt.Errorf("missing required dependency: exiftool\ninstall with:  brew install exiftool ffmpeg")
	}
	ftool, err := resolveTool("ffmpeg", "FFMPEG_PATH")
	if err != nil {
		return fmt.Errorf("missing required dependency: ffmpeg\ninstall with:  brew install exiftool ffmpeg")
	}

	loc, err := resolveTZ(opts.TZ)
	if err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	base, err := parseBaseTime(opts.BaseTime, loc)
	if err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if opts.Inc < 0 {
		return fmt.Errorf("%w: --inc must be >= 0", errUsage)
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

	etVer, _ := runCmd(etool, []string{"-ver"})
	ftVer, _ := runCmd(ftool, []string{"-version"})
	etVerS := strings.TrimSpace(string(etVer))
	ftVerS := strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(ftVer)), "\n", 2)[0])

	seq := sequence(base, len(known), opts.Inc)

	fmt.Fprintf(stdout, "mead — %s\n", opts.Dir)
	fmt.Fprintf(stdout, "  base %s · inc %ds · N=%d files · dry-run=%s\n",
		base.Format(layoutHeader), opts.Inc, len(known), yn(opts.DryRun))
	if etVerS != "" || ftVerS != "" {
		fmt.Fprintf(stdout, "  exiftool %s · ffmpeg %s\n", etVerS, ftVerS)
	}
	if opts.DryRun {
		for i, ft := range known {
			for _, c := range planFile(etool, ftool, ft, seq[i]) {
				fmt.Fprintf(stdout, "  $ %s\n", c)
			}
		}
		fmt.Fprintln(stdout)
	}

	changed, errored := 0, 0
	for i, ft := range known {
		t := seq[i]
		var ferr error
		if !opts.DryRun {
			switch ft.cat {
			case catPhoto, catVideoModern:
				ferr = writePhotoModern(etool, ft.path, t)
			case catVideoAVI:
				ferr = writeAVI(etool, ftool, ft.path, t)
			}
		}
		if ferr != nil {
			fmt.Fprintf(stdout, "  ERROR    %s  %v\n", ft.name, ferr)
			errored++
			continue
		}
		fmt.Fprintf(stdout, "  CHANGED  %s  %s  (%s)\n", ft.name, t.Format(layoutDash), desc(ft.cat))
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
	dir, ok := prompt(sc, "dir", ".")
	if !ok {
		return fmt.Errorf("aborted")
	}
	opts.Dir = dir
	if fi, err := os.Stat(opts.Dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("bad dir: %s", opts.Dir)
	}
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
	tz, _ := prompt(sc, "timezone", localTZName())
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

func resolveTool(name, envOverride string) (string, error) {
	if p := os.Getenv(envOverride); p != "" {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return exec.LookPath(name)
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
		if len(rest) != 4 {
			return nil, fmt.Errorf("bad --tz offset %q", s)
		}
		for _, r := range rest {
			if r < '0' || r > '9' {
				return nil, fmt.Errorf("bad --tz offset %q", s)
			}
		}
		h, _ := strconv.Atoi(rest[:2])
		m, _ := strconv.Atoi(rest[2:])
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
	for _, l := range []string{layoutColonOffset, layoutDashOffset} {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	for _, l := range []string{layoutColon, layoutDash} {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("bad base_time %q (use 'YYYY:MM:DD HH:MM:SS[±HH:MM]' or 'YYYY-MM-DD HH:MM:SS')", s)
}

func sequence(base time.Time, n, inc int) []time.Time {
	seq := make([]time.Time, n)
	for i := 0; i < n; i++ {
		seq[i] = base.Add(time.Duration(i*inc) * time.Second)
	}
	return seq
}

func classifyExt(name string) (Category, bool) {
	switch ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), ".")); ext {
	case "jpg", "jpeg", "png", "heic", "tif", "tiff", "gif":
		return catPhoto, true
	case "mp4", "m4v", "mov":
		return catVideoModern, true
	case "avi":
		return catVideoAVI, true
	}
	return catNone, false
}

func photoCmd(exif, file string, t time.Time) []string {
	return []string{exif, "-overwrite_original", "-AllDates=" + t.Format(layoutColonOffset), file}
}

func aviCmds(exif, ffmpeg, file string, t time.Time) [][]string {
	ts := t.Format(layoutColonOffset)
	wall := t.Format(layoutDash)
	tmp := tmpAVI(file)
	return [][]string{
		{exif, "-overwrite_original", "-FileModifyDate=" + ts, "-FileCreateDate=" + ts, file},
		{ffmpeg, "-y", "-i", file, "-c", "copy", "-metadata", "date=" + wall, tmp},
		{"mv", tmp, file},
		{exif, "-overwrite_original", "-FileModifyDate=" + ts, "-FileCreateDate=" + ts, file},
	}
}

func tmpAVI(file string) string {
	return filepath.Join(filepath.Dir(file), fmt.Sprintf("mead-%d.avi", os.Getpid()))
}

func writePhotoModern(exif, file string, t time.Time) error {
	argv := photoCmd(exif, file, t)
	_, err := runCmd(argv[0], argv[1:])
	return err
}

func writeAVI(exif, ffmpeg, file string, t time.Time) error {
	for i, argv := range aviCmds(exif, ffmpeg, file, t) {
		if _, err := runCmd(argv[0], argv[1:]); err != nil {
			if i == 1 {
				os.Remove(argv[len(argv)-1])
			}
			return err
		}
	}
	return nil
}

func planFile(exif, ffmpeg string, ft fileTask, t time.Time) []string {
	switch ft.cat {
	case catPhoto, catVideoModern:
		return []string{cmdLine(photoCmd(exif, ft.path, t))}
	case catVideoAVI:
		var cmds []string
		for _, argv := range aviCmds(exif, ffmpeg, ft.path, t) {
			cmds = append(cmds, cmdLine(argv))
		}
		return cmds
	}
	return nil
}

func cmdLine(argv []string) string { return strings.Join(argv, " ") }

func desc(cat Category) string {
	switch cat {
	case catPhoto:
		return "DateTimeOriginal"
	case catVideoModern:
		return "AllDates"
	case catVideoAVI:
		return "fs + ICRD"
	}
	return ""
}

func yn(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func localTZName() string {
	if tz := os.Getenv("TZ"); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		for _, pref := range []string{"/var/db/timezone/zoneinfo/", "/usr/share/zoneinfo/"} {
			if strings.HasPrefix(link, pref) {
				return strings.TrimPrefix(link, pref)
			}
		}
	}
	return "Local"
}

func runCmd(name string, args []string) ([]byte, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return out, err
}
