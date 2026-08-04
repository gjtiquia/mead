package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestRunMainExitCodes(t *testing.T) {
	stubsDir, err := filepath.Abs("testdata/stubs")
	if err != nil {
		t.Fatalf("abs stubs: %v", err)
	}
	basePATH := os.Getenv("PATH")
	withStubs := stubsDir + string(os.PathListSeparator) + basePATH

	dir := t.TempDir()
	writeFile(t, dir, "IMG_0001.JPG")
	goodBase := "2026:08:03 09:00:00-04:00"
	missingDir := filepath.Join(dir, "does-not-exist")

	newLog := func(t *testing.T) string {
		f, err := os.CreateTemp("", "mead-stub-log-*")
		if err != nil {
			t.Fatalf("temp log: %v", err)
		}
		p := f.Name()
		f.Close()
		t.Setenv("MEAD_STUB_LOG", p)
		return p
	}

	tests := []struct {
		name string
		args []string
		path string
		want int
	}{
		{"help_short", []string{"-h"}, basePATH, 0},
		{"help_long", []string{"--help"}, basePATH, 0},
		{"missing_base_time", []string{dir}, basePATH, 2},
		{"too_many_args", []string{dir, goodBase, "extra"}, basePATH, 2},
		{"bad_inc", []string{dir, goodBase, "--inc", "-1"}, withStubs, 2},
		{"bad_tz", []string{dir, goodBase, "--tz", "Bogus/Zone"}, withStubs, 2},
		{"missing_dir", []string{missingDir, goodBase}, basePATH, 2},
		{"passing", []string{dir, goodBase}, withStubs, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PATH", tc.path)
			newLog(t)
			got := runMain(tc.args)
			if got != tc.want {
				t.Fatalf("runMain(%v) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}

func TestMead_Corpus(t *testing.T) {
	src := corpusSrc(t)
	shoots := discoverShoots(t, src)

	var aviOnly, mixed *shoot
	for i := range shoots {
		ao, mx := classifyShoot(t, shoots[i])
		if ao && aviOnly == nil {
			sh := shoots[i]
			aviOnly = &sh
		}
		if mx && mixed == nil {
			sh := shoots[i]
			mixed = &sh
		}
	}
	if aviOnly == nil {
		t.Skip("no avi-only shoot in corpus")
	}
	t.Run("dry_run_no_mutation", func(t *testing.T) {
		loc, _ := resolveTZ("-04:00")
		_ = loc
		for _, s := range shoots {
			s := s
			t.Run(s.name, func(t *testing.T) {
				tmp := copyShootToTmp(t, s)
				type snap struct {
					name  string
					mtime time.Time
					size  int64
				}
				entries, _ := os.ReadDir(tmp)
				var snaps []snap
				for _, e := range entries {
					if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
						continue
					}
					if _, ok := classifyExt(e.Name()); !ok {
						continue
					}
					fi, _ := e.Info()
					snaps = append(snaps, snap{e.Name(), fi.ModTime(), fi.Size()})
				}
				buf := &bytes.Buffer{}
				if err := run(Options{Dir: tmp, BaseTime: "2026:08:03 09:00:00-04:00", DryRun: true}, buf); err != nil {
					t.Fatalf("dry-run err: %v", err)
				}
				for _, sn := range snaps {
					fi, err := os.Stat(filepath.Join(tmp, sn.name))
					if err != nil {
						t.Fatalf("stat %s: %v", sn.name, err)
					}
					if !fi.ModTime().Equal(sn.mtime) {
						t.Fatalf("%s mtime changed in dry-run", sn.name)
					}
					if fi.Size() != sn.size {
						t.Fatalf("%s size changed in dry-run", sn.name)
					}
				}
				out := buf.String()
				if strings.Contains(out, "UNKNOWN") {
					t.Fatalf("dry-run report has UNKNOWN: %s", out)
				}
			})
		}
	})

	t.Run("avi_only", func(t *testing.T) {
		tmp := copyShootToTmp(t, *aviOnly)
		loc, _ := resolveTZ("-04:00")
		base, _ := parseBaseTime("2026:08:03 09:00:00-04:00", loc)
		inc := 1

		entries, _ := os.ReadDir(tmp)
		var avis []string
		for _, e := range entries {
			if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				if cat, _ := classifyExt(e.Name()); cat == catVideoAVI {
					avis = append(avis, e.Name())
				}
			}
		}
		sort.Strings(avis)
		dur0 := map[string]string{}
		for _, n := range avis {
			d, err := probeDuration(filepath.Join(tmp, n))
			if err != nil {
				t.Fatalf("ffprobe %s: %v", n, err)
			}
			dur0[n] = d
		}

		buf := &bytes.Buffer{}
		if err := run(Options{Dir: tmp, BaseTime: "2026:08:03 09:00:00-04:00", Inc: inc}, buf); err != nil {
			t.Fatalf("run: %v\n%s", err, buf.String())
		}
		for i, n := range avis {
			p := filepath.Join(tmp, n)
			fields, err := probeField(p, "FileModifyDate", "FileCreateDate", "DateCreated")
			if err != nil {
				t.Fatalf("probe %s: %v", n, err)
			}
			want := base.Add(time.Duration(i*inc) * time.Second)
			if v, ok := fields["FileModifyDate"]; !ok {
				t.Fatalf("%s: no FileModifyDate", n)
			} else {
				got, e := parseExifTime(v, loc)
				if e != nil {
					t.Fatalf("%s: parse FileModifyDate %q: %v", n, v, e)
				}
				if !got.Equal(want) {
					t.Fatalf("%s FileModifyDate = %v, want %v", n, got, want)
				}
			}
			if v, ok := fields["FileCreateDate"]; ok {
				got, e := parseExifTime(v, loc)
				if e == nil && !got.Equal(want) {
					t.Fatalf("%s FileCreateDate = %v, want %v", n, got, want)
				}
			}
			if v, ok := fields["DateCreated"]; ok {
				got, e := parseExifTime(v, loc)
				if e == nil && !got.Equal(want) {
					t.Fatalf("%s DateCreated(ICRD) = %v, want %v", n, got, want)
				}
			}
			d, err := probeDuration(p)
			if err != nil {
				t.Fatalf("ffprobe2 %s: %v", n, err)
			}
			if d != dur0[n] {
				t.Fatalf("%s duration changed: %s -> %s", n, dur0[n], d)
			}
		}
	})

	t.Run("mixed", func(t *testing.T) {
		if mixed == nil {
			t.Skip("no mixed shoot in corpus")
		}
		tmp := copyShootToTmp(t, *mixed)
		loc, _ := resolveTZ("-04:00")
		base, _ := parseBaseTime("2026:08:03 09:00:00-04:00", loc)
		inc := 1

		entries, _ := os.ReadDir(tmp)
		var files []string
		for _, e := range entries {
			if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				if _, ok := classifyExt(e.Name()); ok {
					files = append(files, e.Name())
				}
			}
		}
		sort.Strings(files)
		buf := &bytes.Buffer{}
		if err := run(Options{Dir: tmp, BaseTime: "2026:08:03 09:00:00-04:00", Inc: inc}, buf); err != nil {
			t.Fatalf("run: %v\n%s", err, buf.String())
		}
		for i, n := range files {
			p := filepath.Join(tmp, n)
			cat, _ := classifyExt(n)
			want := base.Add(time.Duration(i*inc) * time.Second)
			var got time.Time
			switch cat {
			case catVideoAVI:
				f, err := probeField(p, "FileModifyDate")
				if err != nil {
					t.Fatalf("probe %s: %v", n, err)
				}
				got, _ = parseExifTime(f["FileModifyDate"], loc)
			case catPhoto:
				f, err := probeField(p, "DateTimeOriginal")
				if err != nil {
					t.Fatalf("probe %s: %v", n, err)
				}
				got, _ = parseExifTime(f["DateTimeOriginal"], loc)
			}
			if !got.Equal(want) {
				t.Fatalf("%s date = %v, want %v", n, got, want)
			}
		}
	})

	t.Run("remaining_smoke", func(t *testing.T) {
		used := map[string]bool{}
		if aviOnly != nil {
			used[aviOnly.name] = true
		}
		if mixed != nil {
			used[mixed.name] = true
		}
		loc, _ := resolveTZ("-04:00")
		for _, s := range shoots {
			if used[s.name] {
				continue
			}
			s := s
			t.Run(s.name, func(t *testing.T) {
				tmp := copyShootToTmp(t, s)
				buf := &bytes.Buffer{}
				if err := run(Options{Dir: tmp, BaseTime: "2026:08:03 09:00:00-04:00"}, buf); err != nil {
					t.Fatalf("run %s: %v\n%s", s.name, err, buf.String())
				}
				entries, _ := os.ReadDir(tmp)
				var files []string
				for _, e := range entries {
					if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
						if _, ok := classifyExt(e.Name()); ok {
							files = append(files, e.Name())
						}
					}
				}
				sort.Strings(files)
				var prev time.Time
				for i, n := range files {
					p := filepath.Join(tmp, n)
					cat, _ := classifyExt(n)
					var got time.Time
					switch cat {
					case catVideoAVI:
						f, _ := probeField(p, "FileModifyDate")
						got, _ = parseExifTime(f["FileModifyDate"], loc)
					case catPhoto:
						f, _ := probeField(p, "DateTimeOriginal")
						got, _ = parseExifTime(f["DateTimeOriginal"], loc)
					}
					if i == 0 {
						prev = got
						continue
					}
					if !got.After(prev) && !got.Equal(prev) {
						t.Fatalf("%s: sequence not monotonic at i=%d (%v <= %v)", n, i, got, prev)
					}
					prev = got
				}
			})
		}
	})

	t.Run("unknown_extension", func(t *testing.T) {
		if mixed == nil {
			t.Skip("no mixed shoot")
		}
		tmp := copyShootToTmp(t, *mixed)
		readmePath := filepath.Join(tmp, "README.txt")
		if err := os.WriteFile(readmePath, []byte("hello"), 0o644); err != nil {
			t.Fatalf("write readme: %v", err)
		}
		rfi, _ := os.Stat(readmePath)
		rsize := rfi.Size()
		rmt := rfi.ModTime()

		buf := &bytes.Buffer{}
		err := run(Options{Dir: tmp, BaseTime: "2026:08:03 09:00:00-04:00"}, buf)
		if err != nil {
			t.Fatalf("run with unknown should exit 0, got err: %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, "UNKNOWN  README.txt") {
			t.Fatalf("report missing UNKNOWN README.txt:\n%s", out)
		}
		rfi2, _ := os.Stat(readmePath)
		if rfi2.Size() != rsize || !rfi2.ModTime().Equal(rmt) {
			t.Fatalf("README.txt mutated on disk")
		}
	})
}

type shoot struct {
	name string
	dir  string
}

func corpusSrc(t *testing.T) string {
	t.Helper()
	src := filepath.Join("..", "2026-08-montreal-import", "2026-08-03")
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		t.Skipf("montreal corpus not present at %s", src)
	}
	if _, err := exec.LookPath("exiftool"); err != nil {
		t.Skipf("mead: missing required dependency: exiftool\ninstall with:  brew install exiftool ffmpeg")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skipf("mead: missing required dependency: ffmpeg\ninstall with:  brew install exiftool ffmpeg")
	}
	return src
}

func discoverShoots(t *testing.T, src string) []shoot {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Skipf("cannot read corpus dir: %v", err)
	}
	var shoots []shoot
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		shoots = append(shoots, shoot{name: e.Name(), dir: filepath.Join(src, e.Name())})
	}
	sort.Slice(shoots, func(i, j int) bool { return shoots[i].name < shoots[j].name })
	if len(shoots) == 0 {
		t.Skip("no shoots in corpus")
	}
	return shoots
}

func classifyShoot(t *testing.T, s shoot) (aviOnly, mixed bool) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatalf("read shoot %s: %v", s.name, err)
	}
	var hasAVI, hasOther bool
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		cat, _ := classifyExt(e.Name())
		switch cat {
		case catVideoAVI:
			hasAVI = true
		case catPhoto, catVideoModern:
			hasOther = true
		}
	}
	return hasAVI && !hasOther, hasAVI && hasOther
}

func copyShootToTmp(t *testing.T, s shoot) string {
	t.Helper()
	tmp := t.TempDir()
	err := filepath.WalkDir(s.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(s.dir, p)
		dst := filepath.Join(tmp, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if _, ok := classifyExt(d.Name()); !ok {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(dst)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		if err != nil {
			return err
		}
		st, _ := in.Stat()
		if st != nil {
			os.Chtimes(dst, time.Now(), st.ModTime())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("copy %s: %v", s.name, err)
	}
	return tmp
}

func probeField(file string, fields ...string) (map[string]string, error) {
	args := []string{"-s"}
	for _, f := range fields {
		args = append(args, "-"+f)
	}
	args = append(args, file)
	out, err := runCmd("exiftool", args)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		m[key] = val
	}
	return m, nil
}

func probeDuration(file string) (string, error) {
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", file).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func parseExifTime(s string, loc *time.Location) (time.Time, error) {
	if t, e := time.Parse("2006:01:02 15:04:05-07:00", s); e == nil {
		return t, nil
	}
	return time.ParseInLocation("2006:01:02 15:04:05", s, loc)
}

func TestStubbedPhotoArgs(t *testing.T) {
	logPath := stubEnv(t)
	dir := t.TempDir()
	writeFile(t, dir, "IMG_0001.JPG")

	buf := &bytes.Buffer{}
	err := run(Options{Dir: dir, BaseTime: "2026:08:03 09:00:00-04:00"}, buf)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	log := readStubLog(t, logPath)
	if len(log) != 1 {
		t.Fatalf("want 1 stub call, got %d: %v", len(log), log)
	}
	args := log[0]
	if len(args) != 4 {
		t.Fatalf("argv len = %d, want 4: %v", len(args), args)
	}
	if args[0] == "" {
		t.Fatalf("argv0 empty")
	}
	if args[1] != "-overwrite_original" {
		t.Fatalf("arg1 = %q, want -overwrite_original", args[1])
	}
	if !strings.HasPrefix(args[2], "-AllDates=") {
		t.Fatalf("arg2 = %q", args[2])
	}
	ts := strings.TrimPrefix(args[2], "-AllDates=")
	if _, e := time.Parse("2006:01:02 15:04:05-07:00", ts); e != nil {
		t.Fatalf("AllDates value %q not offset-timestamp: %v", ts, e)
	}
	if args[3] != filepath.Join(dir, "IMG_0001.JPG") {
		t.Fatalf("arg3 = %q", args[3])
	}
	if !strings.Contains(buf.String(), "CHANGED  IMG_0001.JPG") {
		t.Fatalf("report missing CHANGED line:\n%s", buf.String())
	}
}

func TestStubbedModernVideoArgs(t *testing.T) {
	for _, ext := range []string{"mp4", "mov"} {
		t.Run(ext, func(t *testing.T) {
			logPath := stubEnv(t)
			dir := t.TempDir()
			name := "VID_0001." + ext
			writeFile(t, dir, name)
			buf := &bytes.Buffer{}
			if err := run(Options{Dir: dir, BaseTime: "2026:08:03 09:00:00-04:00"}, buf); err != nil {
				t.Fatalf("run: %v", err)
			}
			log := readStubLog(t, logPath)
			if len(log) != 1 {
				t.Fatalf("want 1 call, got %d", len(log))
			}
			if log[0][1] != "-overwrite_original" || !strings.HasPrefix(log[0][2], "-AllDates=") {
				t.Fatalf("bad argv: %v", log[0])
			}
			if log[0][3] != filepath.Join(dir, name) {
				t.Fatalf("file arg mismatch: %q", log[0][3])
			}
		})
	}
}

func TestStubbedAVIArgs(t *testing.T) {
	logPath := stubEnv(t)
	dir := t.TempDir()
	writeFile(t, dir, "SL740083.AVI")

	buf := &bytes.Buffer{}
	if err := run(Options{Dir: dir, BaseTime: "2026:08:03 09:00:00-04:00"}, buf); err != nil {
		t.Fatalf("run: %v", err)
	}
	log := readStubLog(t, logPath)
	if len(log) != 3 {
		t.Fatalf("want 3 stub calls, got %d: %v", len(log), log)
	}
	// Call 1: exiftool fs dates
	if !strings.HasPrefix(log[0][2], "-FileModifyDate=") || !strings.HasPrefix(log[0][3], "-FileCreateDate=") {
		t.Fatalf("call1 not fs dates: %v", log[0])
	}
	if log[0][4] != filepath.Join(dir, "SL740083.AVI") {
		t.Fatalf("call1 file arg mismatch: %q", log[0][4])
	}
	// Call 2: ffmpeg -c copy -metadata date=<wall>
	if !strings.Contains(strings.Join(log[1], " "), "-c copy") {
		t.Fatalf("call2 not stream copy: %v", log[1])
	}
	var dateVal, outPath string
	a := log[1]
	for i := 0; i < len(a); i++ {
		if strings.HasPrefix(a[i], "date=") {
			dateVal = strings.TrimPrefix(a[i], "date=")
		}
	}
	outPath = a[len(a)-1]
	if dateVal == "" {
		t.Fatalf("no date= in ffmpeg args: %v", a)
	}
	if _, e := time.Parse("2006-01-02 15:04:05", dateVal); e != nil {
		t.Fatalf("ffmpeg date= has offset or bad format: %q (%v)", dateVal, e)
	}
	if !strings.HasSuffix(outPath, ".avi") {
		t.Fatalf("ffmpeg output not .avi: %q", outPath)
	}
	if filepath.Dir(outPath) != dir {
		t.Fatalf("temp file not in same dir: %q (dir %s)", outPath, dir)
	}
	// Call 3: exiftool fs dates again
	if !strings.HasPrefix(log[2][2], "-FileModifyDate=") {
		t.Fatalf("call3 not fs dates: %v", log[2])
	}
	if !strings.Contains(buf.String(), "CHANGED  SL740083.AVI") {
		t.Fatalf("report missing CHANGED:\n%s", buf.String())
	}
}

func TestStubbedDryRunNoCalls(t *testing.T) {
	logPath := stubEnv(t)
	dir := t.TempDir()
	p := writeFile(t, dir, "IMG_0001.JPG")
	fi, _ := os.Stat(p)
	mtime := fi.ModTime()
	size := fi.Size()

	buf := &bytes.Buffer{}
	if err := run(Options{Dir: dir, BaseTime: "2026:08:03 09:00:00-04:00", DryRun: true}, buf); err != nil {
		t.Fatalf("run: %v", err)
	}
	if log := readStubLog(t, logPath); len(log) != 0 {
		t.Fatalf("dry-run logged calls: %v", log)
	}
	fi2, _ := os.Stat(p)
	if !fi2.ModTime().Equal(mtime) || fi2.Size() != size {
		t.Fatalf("file mutated in dry-run")
	}
	if !strings.Contains(buf.String(), "CHANGED  IMG_0001.JPG") {
		t.Fatalf("dry-run report missing plan:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "dry-run=yes") {
		t.Fatalf("report header missing dry-run=yes:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "exiftool -overwrite_original -AllDates=") {
		t.Fatalf("dry-run missing command preview:\n%s", buf.String())
	}
}

func TestStubbedUnknownSkipped(t *testing.T) {
	logPath := stubEnv(t)
	dir := t.TempDir()
	writeFile(t, dir, "README.txt")
	p := filepath.Join(dir, "README.txt")

	buf := &bytes.Buffer{}
	err := run(Options{Dir: dir, BaseTime: "2026:08:03 09:00:00-04:00"}, buf)
	if err != nil {
		t.Fatalf("run with unknown should exit 0, got err: %v", err)
	}
	if log := readStubLog(t, logPath); len(log) != 0 {
		t.Fatalf("unknown triggered calls: %v", log)
	}
	if !strings.Contains(buf.String(), "UNKNOWN  README.txt") {
		t.Fatalf("report missing UNKNOWN:\n%s", buf.String())
	}
	_ = p
}

func TestStubbedPerFileErrorContinues(t *testing.T) {
	logPath := stubEnv(t)
	t.Setenv("MEAD_STUB_FAIL_FIRST", "1")
	dir := t.TempDir()
	writeFile(t, dir, "A.JPG")
	writeFile(t, dir, "B.JPG")

	buf := &bytes.Buffer{}
	err := run(Options{Dir: dir, BaseTime: "2026:08:03 09:00:00-04:00"}, buf)
	if err == nil {
		t.Fatalf("run with failure should error")
	}
	log := readStubLog(t, logPath)
	if len(log) < 2 {
		t.Fatalf("want >=2 attempted calls, got %d: %v", len(log), log)
	}
	if !strings.Contains(buf.String(), "ERROR") {
		t.Fatalf("report missing ERROR:\n%s", buf.String())
	}
	aAttempted := false
	bAttempted := false
	for _, args := range log {
		if len(args) > 0 && strings.HasSuffix(args[len(args)-1], "A.JPG") {
			aAttempted = true
		}
		if len(args) > 0 && strings.HasSuffix(args[len(args)-1], "B.JPG") {
			bAttempted = true
		}
	}
	if !aAttempted || !bAttempted {
		t.Fatalf("not all files attempted: A=%v B=%v", aAttempted, bAttempted)
	}
}

func TestStubbedNoSupportedFiles(t *testing.T) {
	stubEnv(t)
	dir := t.TempDir()
	buf := &bytes.Buffer{}
	if err := run(Options{Dir: dir, BaseTime: "2026:08:03 09:00:00-04:00"}, buf); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(buf.String(), "no supported media files") {
		t.Fatalf("expected no-supported message:\n%s", buf.String())
	}
}

func TestStubbedEnvOverrides(t *testing.T) {
	stubsDir, err := filepath.Abs("testdata/stubs")
	if err != nil {
		t.Fatalf("abs stubs: %v", err)
	}
	exiftoolStub := filepath.Join(stubsDir, "exiftool")
	ffmpegStub := filepath.Join(stubsDir, "ffmpeg")

	t.Run("exiftool_override", func(t *testing.T) {
		t.Setenv("EXIFTOOL_PATH", exiftoolStub)
		t.Setenv("FFMPEG_PATH", ffmpegStub)
		f, err := os.CreateTemp("", "mead-stub-log-*")
		if err != nil {
			t.Fatalf("temp log: %v", err)
		}
		logPath := f.Name()
		f.Close()
		t.Setenv("MEAD_STUB_LOG", logPath)
		dir := t.TempDir()
		writeFile(t, dir, "IMG_0001.JPG")
		buf := &bytes.Buffer{}
		if err := run(Options{Dir: dir, BaseTime: "2026:08:03 09:00:00-04:00"}, buf); err != nil {
			t.Fatalf("run: %v", err)
		}
		log := readStubLog(t, logPath)
		if len(log) != 1 {
			t.Fatalf("want 1 call, got %d: %v", len(log), log)
		}
		if log[0][0] != exiftoolStub {
			t.Fatalf("argv0 = %q, want %q (EXIFTOOL_PATH not used)", log[0][0], exiftoolStub)
		}
	})

	t.Run("ffmpeg_override", func(t *testing.T) {
		t.Setenv("EXIFTOOL_PATH", exiftoolStub)
		t.Setenv("FFMPEG_PATH", ffmpegStub)
		f, err := os.CreateTemp("", "mead-stub-log-*")
		if err != nil {
			t.Fatalf("temp log: %v", err)
		}
		logPath := f.Name()
		f.Close()
		t.Setenv("MEAD_STUB_LOG", logPath)
		dir := t.TempDir()
		writeFile(t, dir, "X.AVI")
		buf := &bytes.Buffer{}
		if err := run(Options{Dir: dir, BaseTime: "2026:08:03 09:00:00-04:00"}, buf); err != nil {
			t.Fatalf("run: %v", err)
		}
		log := readStubLog(t, logPath)
		var ffmpegCall []string
		for _, c := range log {
			if len(c) > 0 && strings.HasSuffix(c[0], "ffmpeg") {
				ffmpegCall = c
				break
			}
		}
		if ffmpegCall == nil {
			t.Fatalf("no ffmpeg call in log: %v", log)
		}
		if ffmpegCall[0] != ffmpegStub {
			t.Fatalf("ffmpeg argv0 = %q, want %q (FFMPEG_PATH not used)", ffmpegCall[0], ffmpegStub)
		}
	})
}

func TestReportSummaryLine(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   []string
		fail    bool
		changed int
		unknown int
		errored int
	}{
		{"changed_and_unknown", []string{"A.JPG", "README.txt"}, false, 1, 1, 0},
		{"errors", []string{"A.JPG"}, true, 0, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := stubEnv(t)
			if tc.fail {
				t.Setenv("MEAD_STUB_FAIL", "1")
			}
			dir := t.TempDir()
			for _, f := range tc.files {
				writeFile(t, dir, f)
			}
			buf := &bytes.Buffer{}
			err := run(Options{Dir: dir, BaseTime: "2026:08:03 09:00:00-04:00"}, buf)
			if tc.errored > 0 && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if tc.errored == 0 && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := fmt.Sprintf("  %d changed · 0 skipped · %d unknown · %d errors\n",
				tc.changed, tc.unknown, tc.errored)
			if !strings.Contains(buf.String(), want) {
				t.Fatalf("summary line wrong:\n%s\nwant %q", buf.String(), want)
			}
			_ = logPath
		})
	}
}

func TestParseBaseTime(t *testing.T) {
	montreal, _ := time.LoadLocation("America/Montreal")
	tokyo, _ := time.LoadLocation("Asia/Tokyo")

	tests := []struct {
		name    string
		s       string
		loc     *time.Location
		want    time.Time
		wantErr bool
	}{
		{"exiftool-colon-with-offset", "2026:08:03 09:00:00-04:00", montreal,
			time.Date(2026, 8, 3, 9, 0, 0, 0, time.FixedZone("-04:00", -4*3600)), false},
		{"dashed-no-offset", "2026-08-03 09:00:00", montreal,
			time.Date(2026, 8, 3, 9, 0, 0, 0, montreal), false},
		{"dashed-no-offset-with-tz", "2026-08-03 09:00:00", tokyo,
			time.Date(2026, 8, 3, 9, 0, 0, 0, tokyo), false},
		{"colon-no-offset-with-tz", "2026:08:03 09:00:00", tokyo,
			time.Date(2026, 8, 3, 9, 0, 0, 0, tokyo), false},
		{"junk", "not a time", montreal, time.Time{}, true},
		{"wrong-separator", "2026/08/03 09:00:00", montreal, time.Time{}, true},
		{"two-digit-year", "26:08:03 09:00:00", montreal, time.Time{}, true},
		{"empty", "", montreal, time.Time{}, true},
		{"trailing-offset-sign-only", "2026-08-03 09:00:00-", montreal, time.Time{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseBaseTime(tc.s, tc.loc)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("got %v (%s), want %v (%s)", got, got.Location(), tc.want, tc.want.Location())
			}
		})
	}
}

func TestResolveTZ(t *testing.T) {
	loc, err := resolveTZ("")
	if err != nil || loc != time.Local {
		t.Fatalf("empty -> Local, got %v %v", loc, err)
	}
	loc, err = resolveTZ("America/Montreal")
	if err != nil {
		t.Fatalf("IANA: %v", err)
	}
	if got := time.Date(2026, 8, 3, 9, 0, 0, 0, loc).Format("-07:00"); got != "-04:00" {
		t.Fatalf("IANA offset = %s, want -04:00", got)
	}
	loc, err = resolveTZ("-04:00")
	if err != nil {
		t.Fatalf("fixed offset: %v", err)
	}
	if got := time.Date(2026, 8, 3, 9, 0, 0, 0, loc).Format("-07:00"); got != "-04:00" {
		t.Fatalf("fixed offset = %s, want -04:00", got)
	}
	loc, err = resolveTZ("+05:30")
	if err != nil {
		t.Fatalf("fixed offset +05:30: %v", err)
	}
	if got := time.Date(2026, 8, 3, 9, 0, 0, 0, loc).Format("-07:00"); got != "+05:30" {
		t.Fatalf("fixed +05:30 = %s", got)
	}
	if _, err := resolveTZ("not/a/zone"); err == nil {
		t.Fatalf("malformed gave no error")
	}
	if _, err := resolveTZ("-99:00"); err == nil {
		t.Fatalf("out-of-range offset gave no error")
	}
}

func TestSequence(t *testing.T) {
	loc, _ := time.LoadLocation("America/Montreal")
	base := time.Date(2026, 8, 3, 9, 0, 0, 0, loc)
	for _, tc := range []struct{ n, inc int }{
		{0, 1}, {1, 1}, {5, 1}, {3, 60}, {4, 30},
	} {
		seq := sequence(base, tc.n, tc.inc)
		if len(seq) != tc.n {
			t.Fatalf("n=%d inc=%d: len=%d", tc.n, tc.inc, len(seq))
		}
		if tc.n == 0 {
			continue
		}
		if !seq[0].Equal(base) {
			t.Fatalf("first != base: %v", seq[0])
		}
		for i := 1; i < tc.n; i++ {
			if got := seq[i].Sub(seq[0]); got != time.Duration(i*tc.inc)*time.Second {
				t.Fatalf("n=%d inc=%d i=%d gap=%v want=%v", tc.n, tc.inc, i, got, time.Duration(i*tc.inc)*time.Second)
			}
		}
	}
}

func TestClassifyExt(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Category
		ok   bool
	}{
		{"IMG_0001.JPG", catPhoto, true},
		{"photo.jpeg", catPhoto, true},
		{"x.png", catPhoto, true},
		{"x.heic", catPhoto, true},
		{"x.TIFF", catPhoto, true},
		{"x.gif", catPhoto, true},
		{"vid.mp4", catVideoModern, true},
		{"v.MOV", catVideoModern, true},
		{"v.m4v", catVideoModern, true},
		{"SL740083.AVI", catVideoAVI, true},
		{"x.avi", catVideoAVI, true},
		{"README.TXT", catNone, false},
		{".DS_Store", catNone, false},
		{"noext", catNone, false},
		{"x.raw", catNone, false},
	} {
		got, ok := classifyExt(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("classifyExt(%q) = (%v,%v), want (%v,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestLocalTZName(t *testing.T) {
	t.Run("env_tz", func(t *testing.T) {
		t.Setenv("TZ", "America/Toronto")
		if got := localTZName(); got != "America/Toronto" {
			t.Fatalf("env TZ: got %q, want America/Toronto", got)
		}
	})
	t.Run("env_tz_invalid_falls_through", func(t *testing.T) {
		t.Setenv("TZ", "Bogus/Zone")
		got := localTZName()
		if got == "" {
			t.Fatalf("localTZName returned empty for invalid TZ")
		}
	})
	t.Run("no_env_nonempty_no_panic", func(t *testing.T) {
		t.Setenv("TZ", "")
		got := localTZName()
		if got == "" {
			t.Fatalf("localTZName returned empty")
		}
	})
}

// Stub convention (documented):
// testdata/stubs/{exiftool,ffmpeg} honor:
//   MEAD_STUB_LOG          - file path; each call appends one line of NUL-separated argv
//                            (argv0 then args), terminated by a newline. Version probes
//                            (-ver / -version) print a fake version and are NOT logged.
//   MEAD_STUB_FAIL         - if set, every stub call exits 1 (after logging).
//   MEAD_STUB_FAIL_FIRST   - if set, only the first stub call (globally) exits 1.
//   MEAD_STUB_EXIFTOOL_FAIL / MEAD_STUB_FFMPEG_FAIL - per-tool variants of _FAIL.

func readStubLog(t *testing.T, p string) [][]string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read stub log: %v", err)
	}
	data = bytes.TrimRight(data, "\n")
	if len(data) == 0 {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	out := make([][]string, 0, len(lines))
	for _, l := range lines {
		parts := strings.Split(l, "\x00")
		clean := parts[:0]
		for _, p := range parts {
			if p != "" {
				clean = append(clean, p)
			}
		}
		out = append(out, clean)
	}
	return out
}

func stubEnv(t *testing.T) (logPath string) {
	t.Helper()
	stubsDir, err := filepath.Abs("testdata/stubs")
	if err != nil {
		t.Fatalf("abs stubs: %v", err)
	}
	t.Setenv("PATH", stubsDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	f, err := os.CreateTemp("", "mead-stub-log-*")
	if err != nil {
		t.Fatalf("temp stub log: %v", err)
	}
	logPath = f.Name()
	f.Close()
	t.Setenv("MEAD_STUB_LOG", logPath)
	return logPath
}

func writeFile(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("stub"), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}
