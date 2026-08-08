# mead

> mead as in [med]ia

Fix media capture times so files appear in chronological order in Google Photos.

Camera files have unreliable embedded datetimes. `mead` sequences every file in the
current folder (or a given `dir`) by filename and writes the correct timestamp per
media type:

- **Photos** (JPG/PNG/HEIC/TIFF/GIF) and **modern video** (MP4/MOV/M4V) → embedded
  EXIF date via `exiftool`, plus the filesystem creation date so macOS Finder's
  "Created" (Get Info) shows the capture time
- **Legacy AVI** → filesystem creation date + embedded ICRD via `ffmpeg` (stream copy, no re-encode)

Requires `exiftool` and `ffmpeg` on `PATH` (`brew install exiftool ffmpeg`). Writing the
filesystem creation date also needs `setfile` (ships with Xcode Command Line Tools,
`xcode-select --install`).

## Install

```
go install .
```

## Usage

```
mead <base_time> [dir]
mead                    # interactive prompts
```

Operates on the current folder by default; pass an optional `dir` to target another
folder without `cd`-ing. First file gets `base_time` exactly; each next file gets
`base_time + i·inc` seconds.

In interactive mode the `base_time` prompt pre-fills your last value (edit in place).
`↑`/`↓` scroll history (bash-style); the latest history is shown by default and `↓`
from there clears the line. `←`/`→` move the cursor, `Ctrl-C` aborts. History is
stored at `~/.local/state/mead/history` (or `$XDG_STATE_HOME/mead/history`).

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
# dry-run first (run inside the shoot folder)
cd ~/shoots/0900-apple && mead --dry-run "2026:08:03 09:00:00-04:00"

# or point at the folder directly
mead "2026:08:03 09:00:00-04:00" ~/shoots/0900-apple

# 30s apart instead of 1s
cd ~/shoots/1700-bnb && mead --inc 30 "2026-08-03 17:00:00"

# unknown extensions are reported as UNKNOWN warnings and left alone
```

`base_time` accepts `2026:08:03 09:00:00-04:00` (exiftool-style, optional offset) or
`2026-08-03 09:00:00` (wall clock, no offset).

Exit codes: `0` ok (warnings allowed) · `1` runtime error · `2` usage error.

**Writes are irreversible** (`-overwrite_original`). Use `--dry-run` first.

## ai-usage disclosure

(inspired by [ghostty's ai usage policy](https://github.com/ghostty-org/ghostty/blob/main/AI_POLICY.md))

ai is heavily used for generating code in this project

other than the obvious utility of the project, other things that im exploring while "vibecoding" out this project
- the usability of Deepseek v4 Flash (0731) via Opencode Go as a "daily driver" model
- Opencode built-in subagents capabilities
    - ie. this prompt: go ahead with the plan. use subagents to implement, another subagent to review, loop till done. you, the main agent is just an orchestrator agent.
- Golang's capabilities for building quick CLI tools
    - whats included out of the box (stdlib)
    - whats idiomatic Go and best practices


