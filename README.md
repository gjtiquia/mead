# mead

Fix media capture times so files appear in chronological order in Google Photos.

Camera files have unreliable embedded datetimes. `mead` sequences every file in a
folder by filename and writes the correct timestamp per media type:

- **Photos** (JPG/PNG/HEIC/TIFF/GIF) and **modern video** (MP4/MOV/M4V) → embedded
  EXIF date via `exiftool`
- **Legacy AVI** → filesystem dates + embedded ICRD via `ffmpeg` (stream copy, no re-encode)

Requires `exiftool` and `ffmpeg` on `PATH` (`brew install exiftool ffmpeg`).

## Install

```
go install .
```

## Usage

```
mead <dir> <base_time> [flags]
mead                     # interactive prompts
```

First file gets `base_time` exactly; each next file gets `base_time + i·inc` seconds.

### Flags

Flags may appear anywhere on the command line (before or after `dir`/`base_time`),
and accept either `--flag value` or `--flag=value`.

| Flag | Default | Meaning |
|---|---|---|
| `--inc N` | `1` | seconds added per file in sequence |
| `--tz TZ` | device local | IANA name (`America/Montreal`) or fixed offset (`-04:00`) |
| `--dry-run` | `false` | print the plan + commands, write nothing |
| `-h`, `--help` | | show usage |

Long flags are double-dash only: use `--dry-run`, not `-dry-run`.

### Examples

```
# dry-run first
mead --dry-run ~/shoots/0900-apple "2026:08:03 09:00:00-04:00"

# for real
mead ~/shoots/0900-apple "2026:08:03 09:00:00-04:00"

# 30s apart instead of 1s
mead --inc 30 ~/shoots/1700-bnb "2026-08-03 17:00:00"

# unknown extensions are reported as UNKNOWN warnings and left alone
```

`base_time` accepts `2026:08:03 09:00:00-04:00` (exiftool-style, optional offset) or
`2026-08-03 09:00:00` (wall clock, no offset).

Exit codes: `0` ok (warnings allowed) · `1` runtime error · `2` usage error.

**Writes are irreversible** (`-overwrite_original`). Use `--dry-run` first.
