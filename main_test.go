package main

import (
	"bufio"
	"bytes"
	"errors"
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
	dir := t.TempDir()
	corpusPhoto(t, dir, "IMG_0001.JPG")
	goodBase := "2026:08:03 09:00:00-04:00"
	missingDir := filepath.Join(dir, "does-not-exist")

	tests := []struct {
		name string
		args []string
		want int
	}{
		{"help_short", []string{"-h"}, 0},
		{"help_long", []string{"--help"}, 0},
		{"bad_base_time", []string{"not-a-time"}, 2},
		{"too_many_args", []string{goodBase, "extra"}, 2},
		{"bad_inc", []string{goodBase, "--inc", "-1"}, 2},
		{"bad_tz", []string{goodBase, "--tz", "Bogus/Zone"}, 2},
		{"bad_dir", []string{goodBase, missingDir}, 2},
		{"dir_defaults_to_dot", []string{goodBase}, 0},
		{"dir_override", []string{goodBase, "."}, 0},
		{"flags_after_positionals", []string{goodBase, "--dry-run"}, 0},
		{"flag_between_positionals", []string{"--inc", "30", goodBase}, 0},
		{"flag_equals_form", []string{goodBase, "--inc=30"}, 0},
		{"double_dash_separator", []string{"--", goodBase}, 0},
		{"unknown_flag", []string{goodBase, "--bogus"}, 2},
		{"single_dash_long_flag", []string{goodBase, "-dry-run"}, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(dir)
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
		t.Fatalf("no avi-only shoot in corpus")
	}
	t.Run("dry_run_no_mutation", func(t *testing.T) {
		for _, s := range shoots {
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
				if cat, _ := classifyExt(e.Name()); cat == categoryVideoAVI {
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
			t.Fatalf("no mixed shoot in corpus")
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
			case categoryVideoAVI:
				f, err := probeField(p, "FileModifyDate")
				if err != nil {
					t.Fatalf("probe %s: %v", n, err)
				}
				got, _ = parseExifTime(f["FileModifyDate"], loc)
			case categoryPhoto:
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
					case categoryVideoAVI:
						f, _ := probeField(p, "FileModifyDate")
						got, _ = parseExifTime(f["FileModifyDate"], loc)
					case categoryPhoto:
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
			t.Fatalf("no mixed shoot in corpus")
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

	t.Run("error_continues", func(t *testing.T) {
		dir := t.TempDir()
		garbage := writeFile(t, dir, "A.JPG", "not an image")
		corpusPhoto(t, dir, "B.JPG")

		buf := &bytes.Buffer{}
		err := run(Options{Dir: dir, BaseTime: "2026:08:03 09:00:00-04:00"}, buf)
		if err == nil {
			t.Fatalf("run with a failing file should error:\n%s", buf.String())
		}
		out := buf.String()
		if !strings.Contains(out, "ERROR    A.JPG") {
			t.Fatalf("report missing ERROR for A.JPG:\n%s", out)
		}
		if !strings.Contains(out, "CHANGED  B.JPG") {
			t.Fatalf("report missing CHANGED for B.JPG (loop must continue past error):\n%s", out)
		}
		fields, err := probeField(filepath.Join(dir, "B.JPG"), "DateTimeOriginal")
		if err != nil {
			t.Fatalf("probe B.JPG: %v", err)
		}
		loc, _ := resolveTZ("-04:00")
		got, e := parseExifTime(fields["DateTimeOriginal"], loc)
		if e != nil {
			t.Fatalf("parse DateTimeOriginal %q: %v", fields["DateTimeOriginal"], e)
		}
		want, _ := parseBaseTime("2026:08:03 09:00:00-04:00", loc)
		if !got.Equal(want) {
			t.Fatalf("B.JPG date = %v, want %v", got, want)
		}
		if _, err := os.Stat(garbage); err != nil {
			t.Fatalf("garbage A.JPG removed: %v", err)
		}
	})
}

type shoot struct {
	name string
	dir  string
}

func corpusSrc(t *testing.T) string {
	t.Helper()
	requireTools(t)
	src := filepath.Join("..", "2026-08-montreal-import", "2026-08-03")
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		t.Fatalf("corpus not present at %s", src)
	}
	return src
}

func discoverShoots(t *testing.T, src string) []shoot {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("cannot read corpus dir: %v", err)
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
		t.Fatalf("no shoots in corpus %s", src)
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
		case categoryVideoAVI:
			hasAVI = true
		case categoryPhoto, categoryVideoModern:
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
	if t, e := time.Parse(layoutColonOffset, s); e == nil {
		return t, nil
	}
	return time.ParseInLocation(layoutColon, s, loc)
}

func TestPhotoCmd(t *testing.T) {
	loc, _ := resolveTZ("-04:00")
	base, _ := parseBaseTime("2026:08:03 09:00:00-04:00", loc)
	for _, tc := range []struct {
		name string
		ext  string
	}{
		{"jpg", "IMG_0001.JPG"},
		{"mp4", "VID_0001.mp4"},
		{"mov", "VID_0001.mov"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv := photoCmd("/usr/bin/exiftool", "/tmp/dir/"+tc.ext, base)
			if len(argv) != 4 {
				t.Fatalf("argv len = %d, want 4: %v", len(argv), argv)
			}
			if argv[0] != "/usr/bin/exiftool" {
				t.Fatalf("argv0 = %q", argv[0])
			}
			if argv[1] != "-overwrite_original" {
				t.Fatalf("argv1 = %q, want -overwrite_original", argv[1])
			}
			if argv[2] != "-AllDates="+base.Format(layoutColonOffset) {
				t.Fatalf("argv2 = %q", argv[2])
			}
			if argv[3] != "/tmp/dir/"+tc.ext {
				t.Fatalf("argv3 = %q", argv[3])
			}
		})
	}
}

func TestAVICmds(t *testing.T) {
	loc, _ := resolveTZ("-04:00")
	base, _ := parseBaseTime("2026:08:03 09:00:00-04:00", loc)
	exif := "/usr/bin/exiftool"
	ff := "/usr/bin/ffmpeg"
	file := "/tmp/dir/SL740083.AVI"
	cmds := aviCmds(exif, ff, file, base)
	if len(cmds) != 4 {
		t.Fatalf("want 4 commands, got %d", len(cmds))
	}
	// 1: exiftool fs dates
	if cmds[0][0] != exif || !strings.HasPrefix(cmds[0][2], "-FileModifyDate=") || !strings.HasPrefix(cmds[0][3], "-FileCreateDate=") {
		t.Fatalf("cmd1 = %v", cmds[0])
	}
	if cmds[0][4] != file {
		t.Fatalf("cmd1 file arg = %q", cmds[0][4])
	}
	// 2: ffmpeg -y -i file -c copy -metadata date=<wall> tmp
	a := cmds[1]
	if a[0] != ff {
		t.Fatalf("cmd2 argv0 = %v", a)
	}
	if !strings.Contains(strings.Join(a, " "), "-c copy") {
		t.Fatalf("cmd2 not stream copy: %v", a)
	}
	var dateVal string
	for i := 0; i < len(a); i++ {
		if strings.HasPrefix(a[i], "date=") {
			dateVal = strings.TrimPrefix(a[i], "date=")
		}
	}
	if dateVal != base.Format(layoutDash) {
		t.Fatalf("cmd2 date= = %q, want %q", dateVal, base.Format(layoutDash))
	}
	tmp := a[len(a)-1]
	if filepath.Dir(tmp) != filepath.Dir(file) || !strings.HasSuffix(tmp, ".avi") {
		t.Fatalf("cmd2 tmp path = %q", tmp)
	}
	// 3: mv tmp file
	if cmds[2][0] != "mv" || cmds[2][1] != tmp || cmds[2][2] != file {
		t.Fatalf("cmd3 = %v", cmds[2])
	}
	// 4: exiftool fs dates again
	if cmds[3][0] != exif || !strings.HasPrefix(cmds[3][2], "-FileModifyDate=") {
		t.Fatalf("cmd4 = %v", cmds[3])
	}
	if cmds[3][4] != file {
		t.Fatalf("cmd4 file arg = %q", cmds[3][4])
	}
}

func TestPlanFile(t *testing.T) {
	loc, _ := resolveTZ("-04:00")
	base, _ := parseBaseTime("2026:08:03 09:00:00-04:00", loc)
	exif := "/usr/bin/exiftool"
	ff := "/usr/bin/ffmpeg"

	file := "/tmp/dir/F.JPG"
	lines := planFile(exif, ff, fileTask{path: file, name: "F.JPG", cat: categoryPhoto}, base)
	if len(lines) != 1 || lines[0] != cmdLine(photoCmd(exif, file, base)) {
		t.Fatalf("photo plan = %v", lines)
	}

	aviFile := "/tmp/dir/V.AVI"
	want := aviCmds(exif, ff, aviFile, base)
	lines = planFile(exif, ff, fileTask{path: aviFile, name: "V.AVI", cat: categoryVideoAVI}, base)
	if len(lines) != len(want) {
		t.Fatalf("avi plan len = %d, want %d", len(lines), len(want))
	}
	for i := range want {
		if lines[i] != cmdLine(want[i]) {
			t.Fatalf("avi plan[%d] = %q, want %q", i, lines[i], cmdLine(want[i]))
		}
	}

	if got := planFile(exif, ff, fileTask{path: "/tmp/dir/X", name: "X", cat: categoryNone}, base); got != nil {
		t.Fatalf("none plan = %v, want nil", got)
	}
}

func TestCmdLine(t *testing.T) {
	if got := cmdLine([]string{"exiftool", "-overwrite_original", "a b"}); got != "exiftool -overwrite_original a b" {
		t.Fatalf("cmdLine = %q", got)
	}
}

func TestNoSupportedFiles(t *testing.T) {
	requireTools(t)
	dir := t.TempDir()
	buf := &bytes.Buffer{}
	if err := run(Options{Dir: dir, BaseTime: "2026:08:03 09:00:00-04:00"}, buf); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(buf.String(), "no supported media files") {
		t.Fatalf("expected no-supported message:\n%s", buf.String())
	}
}

func TestUsageErrorReturned(t *testing.T) {
	buf := &bytes.Buffer{}
	err := run(Options{Dir: ".", TZ: "Bogus/Zone"}, buf)
	var ue *usageError
	if !errors.As(err, &ue) {
		t.Fatalf("run with bad tz err = %v, want *usageError", err)
	}
}

func TestDryRunOutputNoChanged(t *testing.T) {
	dir := t.TempDir()
	corpusPhoto(t, dir, "A.JPG")
	buf := &bytes.Buffer{}
	if err := run(Options{Dir: dir, BaseTime: "2026:08:03 09:00:00-04:00", DryRun: true}, buf); err != nil {
		t.Fatalf("dry-run err: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "exiftool") || !strings.Contains(out, "$ ") {
		t.Fatalf("dry-run missing plan lines:\n%s", out)
	}
	if strings.Contains(out, "CHANGED") {
		t.Fatalf("dry-run reported CHANGED:\n%s", out)
	}
}

func TestReportSummaryLine(t *testing.T) {
	t.Run("changed_and_unknown", func(t *testing.T) {
		dir := t.TempDir()
		corpusPhoto(t, dir, "A.JPG")
		writeFile(t, dir, "README.txt", "hello")
		buf := &bytes.Buffer{}
		if err := run(Options{Dir: dir, BaseTime: "2026:08:03 09:00:00-04:00"}, buf); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "  1 changed · 0 skipped · 1 unknown · 0 errors\n"
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("summary line wrong:\n%s\nwant %q", buf.String(), want)
		}
	})

	t.Run("errors", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "BROKEN.JPG", "not an image")
		buf := &bytes.Buffer{}
		if err := run(Options{Dir: dir, BaseTime: "2026:08:03 09:00:00-04:00"}, buf); err == nil {
			t.Fatalf("expected error, got nil")
		}
		want := "  0 changed · 0 skipped · 0 unknown · 1 errors\n"
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("summary line wrong:\n%s\nwant %q", buf.String(), want)
		}
	})
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

func TestClassifyExt(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Category
		ok   bool
	}{
		{"IMG_0001.JPG", categoryPhoto, true},
		{"photo.jpeg", categoryPhoto, true},
		{"x.png", categoryPhoto, true},
		{"x.heic", categoryPhoto, true},
		{"x.TIFF", categoryPhoto, true},
		{"x.gif", categoryPhoto, true},
		{"vid.mp4", categoryVideoModern, true},
		{"v.MOV", categoryVideoModern, true},
		{"v.m4v", categoryVideoModern, true},
		{"SL740083.AVI", categoryVideoAVI, true},
		{"x.avi", categoryVideoAVI, true},
		{"README.TXT", categoryNone, false},
		{".DS_Store", categoryNone, false},
		{"noext", categoryNone, false},
		{"x.raw", categoryNone, false},
	} {
		got, ok := classifyExt(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("classifyExt(%q) = (%v,%v), want (%v,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func requireTools(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("exiftool"); err != nil {
		t.Fatalf("mead: missing required dependency: exiftool\ninstall with:  brew install exiftool ffmpeg")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Fatalf("mead: missing required dependency: ffmpeg\ninstall with:  brew install exiftool ffmpeg")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Fatalf("mead: missing required dependency: ffprobe\ninstall with:  brew install exiftool ffmpeg")
	}
}

func corpusMedia(t *testing.T, match func(Category) bool) (srcDir, name string) {
	t.Helper()
	src := corpusSrc(t)
	shoots, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	for _, s := range shoots {
		if !s.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(src, s.Name()))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if cat, ok := classifyExt(e.Name()); ok && match(cat) {
				return filepath.Join(src, s.Name()), e.Name()
			}
		}
	}
	t.Fatalf("no matching media file in corpus %s", src)
	return "", ""
}

func corpusPhoto(t *testing.T, dir, name string) string {
	t.Helper()
	srcDir, srcName := corpusMedia(t, func(c Category) bool { return c == categoryPhoto })
	dst := filepath.Join(dir, name)
	copyFile(t, filepath.Join(srcDir, srcName), dst)
	return dst
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open %s: %v", src, err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("create %s: %v", dst, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatalf("copy to %s: %v", dst, err)
	}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}
