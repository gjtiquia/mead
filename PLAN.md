# PLAN.md — mead (media date-fixer)

Status: **ready for implementation** · Platform: macOS (darwin/arm64) · Lang: Go (stdlib only)

## 1. Context / Problem

A vintage camera produces files with completely unreliable embedded datetimes (files
report e.g. `2007:01:01`). The owner approximates capture times with a manual workflow
and wants media to appear in correct chronological order in **Google Photos** after upload.

The catch: media types in a shoot folder are mixed (e.g. `SL740083.AVI` + JPGs), and
Google Photos reads **different fields per format**:

- **Photos (JPG/PNG/HEIC/...)** → embedded EXIF `DateTimeOriginal`.
- **Modern videos (MP4/MOV/M4V)** → embedded container creation time (`exiftool -AllDates` writes these fine).
- **Legacy AVI** → has no standardized capture-time field Google Photos trusts; GP falls
  back to the **file modification date**. exiftool **cannot write to RIFF AVI files at all**
  (verified: `Error: Can't currently write RIFF AVI files`), so AVI dates must be set via
  filesystem dates, with an embedded `DateCreated` (ICRD) written by **ffmpeg** as insurance.

`mead` is a single-binary CLI that sequences **all** media in a folder by filename and
applies the correct write strategy per type, so everything lands in Google Photos in
chronological order.

## 2. Goal

Provide `mead`, a Go CLI that:

1. Checks required dependencies (`exiftool`, `ffmpeg`) on startup and early-returns if missing.
2. Scans a directory, classifies files against a **whitelist**, and **flags unknown formats**
   as warnings (so the whitelist can evolve).
3. Assigns each supported file a monotonic timestamp: `t = base_time + i * inc` seconds,
   sorted by filename, one chronological pass across mixed types.
4. Writes the timestamp using the correct strategy per file type (see §8).
5. Reports a summary of changes; `--dry-run` previews everything without touching files.
6. Works interactively (no args → prompts) or non-interactively (args provided).

## 3. Non-goals (for v1)

- No transcoding/re-encoding video (AVI rewrites are ffmpeg **stream copy only**).
- No renaming/moving files.
- No re-encoding to MP4 to force Google Photos ordering (documented fallback, §16).
- No geotagging, dedup, or media-toolset commands yet (future, §17).
- No GUI.

## 4. Project setup

- **Location:** `/Users/gjtiquia/Documents/self/mead` (exists, empty).
- **Module:** `go.mod` with `module mead`, `go 1.25`.
- **Layout (v1):** single `main.go`. Refactor into `cmd/` + `internal/` only when it grows.
- **Build/install:** `go install .` → binary `mead` at `~/go/bin/mead`
  (`GOPATH=~/go`, `~/go/bin` confirmed on `$PATH`). No shell-out dependency on the Go side.
- **Stdlib only:** `flag`, `os/exec`, `os`, `path/filepath`, `sort`, `time`,
  `bufio`/`fmt` for prompts, `strings`, `strconv`.

## 5. CLI interface

```
mead                                # interactive: prompt for every value, then run
mead <dir> <base_time> [flags]      # non-interactive
mead -h / --help                    # usage
```

Flags (non-interactive):

| Flag | Default | Meaning |
|---|---|---|
| `--inc N` | `1` | seconds added per file in sequence |
| `--tz TZ` | device local | IANA name (`America/Montreal`) or fixed offset (`-04:00`) |
| `--dry-run` | `false` | compute + print plan, execute nothing |

Positional (required in non-interactive mode):

- `<dir>` — directory to process (must exist; may be `.`).
- `<base_time>` — first file's base time, one of:
  - exiftool-style: `2026:08:03 09:00:00-04:00` (colon date, optional signed offset)
  - plain: `2026-08-03 09:00:00` (dash date, no offset → device-local wall clock)

**Exit codes:** `0` success (warnings allowed) · `1` runtime/fatal error (missing dep, write
failure) · `2` usage error (bad/missing args, bad `base_time`, bad `--tz`).

## 6. Startup dependency check (always, first thing)

1. `exec.LookPath("exiftool")` and `exec.LookPath("ffmpeg")`.
2. Honor overrides: env `EXIFTOOL_PATH`, `FFMPEG_PATH` used first if set.
3. If any missing → print to stderr, e.g.:

   ```
   mead: missing required dependency: exiftool
   install with:  brew install exiftool ffmpeg
   ```
   then `os.Exit(1)` **before** any directory scanning or writes.
4. If present, probe versions (`exiftool -ver`, `ffmpeg -version`) and note them in the
   verbose/report output. No hard version floor in v1 (warn-only if floor added later).

## 7. Directory scan & whitelist

- List files under `<dir>` (including the dir itself when given `.`).
  **Top-level only** in v1 (subdirs skipped; note as future option).
- Skip hidden files/dirs (name starts with `.`) — avoids `.DS_Store` noise.
- Whitelist (matched **case-insensitively**, extension):

| Category | Extensions | Write strategy |
|---|---|---|
| photo | `jpg jpeg png heic tif tiff gif` | exiftool `-AllDates` |
| video-modern | `mp4 m4v mov` | exiftool `-AllDates` |
| video-avi | `avi` | exiftool FS dates + ffmpeg embed ICRD (always) |

- Any non-hidden file whose extension is not in the whitelist → **UNKNOWN**: skipped, and
  reported in the summary as warnings (one line per file: `UNKNOWN <path> (.ext not in whitelist)`).
  These warnings are the mechanism to "evolve" the whitelist.
- If zero supported files found → print `mead: no supported media files in <dir>`, exit `0`.

## 8. Per-type write strategies

All writes use `-overwrite_original` (no `_original` backups).

### 8.1 Photo / video-modern
```
exiftool -overwrite_original -AllDates="<t>" "<file>"
```
`<t>` is the absolute instant formatted as `YYYY:MM:DD HH:MM:SS±HH:MM`
(offset included so exiftool stores the correct instant). Verified working for JPG/MP4/MOV.

### 8.2 AVI (two steps, order matters)
```
exiftool -overwrite_original -FileModifyDate="<t>" -FileCreateDate="<t>" "<file>"
ffmpeg -y -i "<file>" -c copy -metadata date="<wallclock>" tmp && mv tmp "<file>"
exiftool -overwrite_original -FileModifyDate="<t>" -FileCreateDate="<t>" "<file>"
```
- `<t>` = absolute instant (with offset); `<wallclock>` = the offset-less local string
  (`YYYY-MM-DD HH:MM:SS`) — ICRD has no TZ semantics.
- ffmpeg rewrite **resets mtime to now** → the final exiftool call restores it.
- `-c copy` = stream copy: XviD/ADPCM preserved, duration intact (verified 22.85 s on test file).
- exiftool sets `FileCreateDate` natively on macOS (no `SetFile` needed).

## 9. Sequencing algorithm

1. Collect supported files, sort by **filename byte order** (matches exiftool `-FileOrder Filename`).
2. Parse `base_time` → epoch instant (see §10).
3. For `i = 1..N` (mirrors the existing `$filesequence` workflow where first file = base + 1·inc):
   `t_i = base_epoch + (i * inc)` seconds.
4. Feed each `t_i` through the §8 strategy for that file.

## 10. Timezone handling

- Default TZ = device local (`time.Local`).
- `--tz` override: IANA name → `time.LoadLocation`; fixed offset `-04:00` → `time.FixedZone`.
- `base_time` parsing:
  - With signed offset (`...-04:00`): treat as absolute instant at that fixed offset
    (matches exiftool semantics).
  - Without offset: interpret as wall clock in the effective TZ (device or `--tz`).
- AVI `<wallclock>` for ICRD: derive from the instant formatted in the effective TZ.
- macOS epoch arithmetic verified: `date -j -f "%Y:%m:%d %H:%M:%S %z" "2026:08:03 09:00:00 -0400" "+%s"` → `1785762000`; `+90s` → `2026:08:03 09:01:30 -0400`.

## 11. Interactive mode (no args)

Sequential prompts with defaults in brackets; Ctrl-C aborts:

```
dir [.]:                          (validate exists)
base_time (required):             (re-prompt until valid)
seconds per file [1]:
timezone [device / TZ]:           (empty = device)
dry-run? [y/N]:
```
Then run the same pipeline as non-interactive. Print the full report regardless.

## 12. Report output

After the run (or on `--dry-run`, the preview):

```
mead — <dir>
  base 2026-08-03 09:00:00 -0400 · inc 1s · N=12 files · dry-run=yes

  CHANGED  SL740083.AVI     2026-08-03 09:00:01  (fs + ICRD)
  CHANGED  IMG_0001.JPG     2026-08-03 09:00:02  (DateTimeOriginal)
  ...
  UNKNOWN  README.TXT       (.txt not in whitelist, skipped)

  12 changed · 0 skipped · 1 unknown · 0 errors
```

`--dry-run` prints the exact commands it *would* run (one per file) plus the table, and
touches nothing (no exiftool write, no ffmpeg rewrite, no mtime change).

## 13. Error handling

- Missing deps → §6, exit 1.
- Bad usage/base_time/`--tz` → stderr message + `-h`, exit 2.
- Per-file failure (exiftool/ffmpeg non-zero, missing file, temp/mv failure) → record error
  in report, **continue** with remaining files, exit 1 at end if any errors.
- AVI temp-file approach: write `tmp.<pid>.avi` in same dir, verify ffmpeg success, then `mv`
  over the original; never delete the original until `mv` succeeds.

## 14. Implementation steps

1. **Write this `PLAN.md`** to `/Users/gjtiquia/Documents/self/mead/PLAN.md`.
2. `go mod init mead` in the dir.
3. `main.go` skeleton: flag parsing, `-h`, exit codes, interactive-vs-args branch.
4. Dep check (§6) wired in as the first thing in the run path.
5. Whitelist + scan + unknown-flagging (§7).
6. `base_time`/`--tz` parsing + sequencing (§9, §10) with unit-testable pure functions.
7. Writers (§8) — exiftool and ffmpeg subprocess calls (arg slices, captured stderr, `ExitError`).
8. Report + `--dry-run` (§11, §12).
9. Interactive prompts (§11).
10. `go vet ./...`, `gofmt`, `go build ./...`.
11. `go install .` → confirm `mead` on PATH.

## 15. Testing / verification

- **Unit tests** (pure funcs): base_time parsing (both formats + offset), `--tz` resolution,
  sequencing arithmetic, extension/whitelist classification.
- **Integration tests** with **stubbed tools**: fake `exiftool`/`ffmpeg` shell scripts on a
  temp `PATH` prepend; assert arg lists + exit codes; no real exiftool/ffmpeg needed.
- **Real-file verification (must do before trusting it):**
  1. Copy real samples (1 JPG + 1 AVI) into a scratch dir.
  2. `mead --dry-run` → confirm table matches expectations, nothing changed.
  3. Run for real on the copies → verify with
     `exiftool -s -DateTimeOriginal -FileModifyDate -FileCreateDate -DateCreated <file>`
     and `stat -f "%Sm %SB"`.
  4. Confirm AVI still plays / duration unchanged (`ffprobe -show_entries format=duration`).
  5. Run `mead` on the real `0900-apple` folder in dry-run first, then real.

## 16. Risks / known limitations

- **Google Photos AVI behavior is unverified**: filesystem mtime is the primary lever; embedded
  ICRD is insurance. If GP still mis-orders AVIs after upload, the documented fallback is
  remuxing AVI→MP4 with `-metadata creation_time=` (format change; out of scope for v1).
- Irreversible writes (`-overwrite_original`) — `--dry-run` is the safety net; camera times
  are approximations by definition.
- v1 scans top-level files only; recursive scanning is a future option.
- exiftool/ffmpeg must stay installed on any machine that runs `mead` (binary is not static
  for these deps — by design, see §4/§6).

## 17. Future evolution

- Whitelist growth driven by UNKNOWN warnings (§7).
- More commands when it becomes a media toolset (verify, report-only, batch rename, MP4 remux).
- `cmd/` + `internal/` package split; version injected via `-ldflags`.
- Cross-compile (`GOOS/GOARCH`) and a `Makefile`/install step.
