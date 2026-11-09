# OwySync User Guide

[Русская версия](user-guide.ru.md)

OwySync syncs one directory in one direction, from a **Windows machine (local)** to a **Unix server over SSH (remote)**. It is built for the "source code → dev server" workflow: trees of 10k–200k files, many small edits, and changes that need to arrive fast. It ships as a single `owysync.exe`: no Cygwin, Perl or rsync, and nothing to install on the server.

- [How it works](#how-it-works)
- [Requirements](#requirements)
- [Installation](#installation)
- [Quick start](#quick-start)
- [Commands](#commands)
- [Configuration: `.owysync/config`](#configuration-owysyncconfig)
- [SSH setup](#ssh-setup)
- [Exclusions: `.owysync/ignore`](#exclusions-owysyncignore)
- [What gets synced and how](#what-gets-synced-and-how)
- [Output](#output)
- [Errors and recovery](#errors-and-recovery)
- [Limitations](#limitations)
- [Troubleshooting](#troubleshooting)

## How it works

1. On start, OwySync runs the checks (see [`owysync check`](#owysync-check)), then does a **full resync**. It walks both trees, compares them and makes the remote match the local directory. The local side is the only source of truth: anything on the remote that is not in the local directory and not ignored **is deleted**.
2. It then switches to **live mode**: it watches the local directory and sends each change to the server within about a second.
3. If the connection drops, OwySync reconnects and runs a full resync again.

Files are compared by **type, size and modification time** (to the second). Contents and permissions are not compared.

## Requirements

**Local side**

- Windows 10/11, amd64 or arm64.
- The synced directory must be on a local disk (NTFS/ReFS). Network drives and `\\wsl$\...` are not supported, because Windows does not report changes there reliably.

**Remote side**

- Linux (GNU coreutils or BusyBox) or FreeBSD. macOS is not supported.
- SSH access with a real shell and the SFTP subsystem. Restricted shells (`rbash`, `git-shell`, `ForceCommand`) and SFTP-only accounts do not work.
- Standard utilities: `sh`, `find` (with `-mindepth` and `-print0`), `xargs -0`, `stat` (GNU/BusyBox `stat -c` or BSD `stat -f`), `tar`, `rm`, `rmdir`. A stock Linux or FreeBSD install has all of them.
- The file system under the remote directory must be case-sensitive (see [Limitations](#limitations)).

## Installation

Put `owysync.exe` anywhere on your `PATH`. There is no installer. Check it with:

```bat
owysync version
```

Building from source needs Go (see `go.mod` for the version):

```bat
go build -ldflags "-X main.version=0.1.0" -o owysync.exe ./cmd/owysync
```

## Quick start

1. Make sure you can log in with plain `ssh` without a password prompt (see [SSH setup](#ssh-setup)):

   ```bat
   ssh user@dev.example.com
   ```

2. Initialize the project directory:

   ```bat
   cd C:\work\myproject
   owysync init
   ```

   Init asks for the host, the user and the absolute remote path. It writes `.owysync\config` and `.owysync\ignore`, connects to the server and prepares the remote directory.

3. **Edit `.owysync\ignore` before the first sync.** The first sync deletes everything on the remote that is not in the local directory and not ignored: `node_modules`, uploads, logs, generated files.

4. Look at what the first sync is going to do:

   ```bat
   owysync sync --once --dry-run
   ```

5. Start syncing:

   ```bat
   owysync sync
   ```

   Stop with Ctrl+C.

## Commands

The command always comes first. Every command except `help` and `version` takes an optional path to the local directory. Without it, the current directory is used. A relative path is resolved from the current directory. The directory must exist. `.owysync\` is looked up only in that directory, never in its parents. Flags can go before or after the path.

| Command | What it does |
| --- | --- |
| `owysync [path]` | Full resync, then live mode until Ctrl+C |
| `owysync --once [path]` | Full resync only. Exit code 0 means success |
| `owysync sync --once --dry-run [path]` | Prints the planned operations and a summary line. Changes nothing |
| `owysync init [path]` | Interactive setup |
| `owysync init --remote [path]` | Only the remote part of setup, for an existing local config |
| `owysync check [path]` | Checks the setup without writing anything |
| `owysync version` | Prints the version |
| `owysync help` | Prints the version and a short help. Same as `owysync` without arguments or `owysync --help` |
| `owysync sync -v [path]` | Also prints every operation of a full resync |

`--dry-run` works only together with `--once`.

**Ctrl+C** stops running operations and exits right away. One press is enough. Temporary files left on the server are cleaned up on the next start.

### `owysync init`

1. Fails if the directory already has `.owysync\`. In that case edit `.owysync\config` directly and verify it with `owysync check`.
2. Asks for:
   - the host (`host` or `host:port`);
   - the user (default: your Windows user name);
   - the absolute remote path.

   Ctrl+C during the dialog writes nothing.
3. Writes `.owysync\config` (the entered values, plus the other keys commented out with their defaults) and the `.owysync\ignore` template.
4. Connects and runs checks 1–6 of `owysync check`. If that fails, the config is kept. Fix it by hand (for example, set `identity_file` or `port`), verify with `owysync check` and finish with `owysync init --remote`.
5. Prepares the remote directory. Every question defaults to "no":

   | Remote directory | What init does |
   | --- | --- |
   | Does not exist | Offers to create it, with parent directories |
   | Exists, no OwySync marker | Shows how many objects it holds and up to 10 top-level names, warns that the first sync deletes everything not in the local directory and not ignored, and asks whether to initialize it |
   | Exists, already has the marker | Warns that another local directory probably syncs into it (two sources would overwrite each other) and asks whether to connect to it. Writes nothing |

   Initializing creates `.owysync/` with an empty `remote` marker file and a `tmp/` directory inside the remote directory.

Init needs a console. It does not run with redirected input, and it has no non-interactive mode.

### `owysync check`

Writes nothing on either side. Runs the checks in order, prints `ok` or `FAIL: reason` for each, and stops at the first failure:

1. `.owysync\config` and `.owysync\ignore` exist and parse.
2. A TCP connection to `host:port`.
3. The host key is in `known_hosts` and matches.
4. Authentication (ssh-agent, then `identity_file`).
5. The SFTP subsystem works.
6. A shell is available, the `stat` variant is detected (printed) and `tar` is present.
7. `remote_root` exists and is a directory.
8. The `.owysync/remote` marker exists.

It does not check write access, since that would need a write. Exit code 0 means all checks passed. A sync runs the same checks before it starts and prints them only if one fails.

## Configuration: `.owysync/config`

Lines of the form `key = value`. Lines starting with `#` are comments. Each key may appear only once. An unknown key or an empty value is an error.

| Key | Required | Default | Meaning |
| --- | --- | --- | --- |
| `host` | yes | — | Server name or address. `host:port` and `[ipv6]:port` are accepted. Do not set the port in both `host` and `port` |
| `port` | | `22` | SSH port |
| `user` | yes | — | Remote user |
| `identity_file` | | — | Full path to a private key, e.g. `C:\Users\you\.ssh\id_ed25519`. Without it, only ssh-agent keys are used |
| `remote_root` | yes | — | Absolute remote path (starts with `/`). It may be a symlink to a directory |
| `file_mode` | | `0644` | Permissions (octal) for new files |
| `dir_mode` | | `0755` | Permissions (octal) for new directories |
| `debounce_ms` | | `100` | How long the local directory must stay quiet before a batch of changes is sent, in milliseconds |
| `mtime_window` | | `0` | Tolerance in seconds when comparing modification times. Use `2` if the local directory is on FAT |
| `concurrency` | | `64` | Number of SFTP operations in flight at once |
| `bulk_threshold` | | `100` | Uploads or deletes per batch at which bulk mode starts (see below). `0` turns bulk mode off |
| `sound` | | `on` | `off` turns off the chime after a sync |

Example:

```ini
host = dev.example.com
user = alice
remote_root = /home/alice/www/myproject
identity_file = C:\Users\alice\.ssh\id_ed25519
```

`config` and `ignore` are read at start. If you edit them while OwySync runs, it warns you, and the changes take effect after a restart.

## SSH setup

OwySync has its own SSH client. It does **not** read `~/.ssh/config` and does not support passwords or Pageant.

- **Authentication.** OwySync first tries the keys in the Windows OpenSSH agent (the `ssh-agent` service), then `identity_file`. A passphrase-protected `identity_file` cannot be used directly: add that key to the agent instead.

  Start the agent once (in an elevated PowerShell) and add the key:

  ```powershell
  Get-Service ssh-agent | Set-Service -StartupType Automatic
  Start-Service ssh-agent
  ssh-add $env:USERPROFILE\.ssh\id_ed25519
  ```

- **Host key.** The server key is checked against `%USERPROFILE%\.ssh\known_hosts`. OwySync never accepts an unknown key by itself. Connect once with plain ssh and confirm the key:

  ```bat
  ssh -p 22 alice@dev.example.com
  ```

- **Keepalive.** A keepalive request goes out every 15 seconds. Three missed replies in a row count as a dropped connection, which catches dead connections after a laptop wakes up.

## Exclusions: `.owysync/ignore`

Ignored paths are neither uploaded nor deleted on the remote. An ignored directory is skipped with everything in it, on both sides.

**This is not `.gitignore`.** The format comes from dklab RealSync and is simpler on purpose, so the effect of any mask is easy to predict.

**File format**

- UTF-8 text, CRLF line endings are fine.
- One mask per line. Leading and trailing spaces are dropped.
- Empty lines and lines starting with `#` are skipped.
- `\` is treated as `/`, so you can write Windows-style paths.

**Two kinds of masks**

- A **name mask** has no `/` and no `**`. It matches **any component** of a path at any depth: `node_modules` excludes `node_modules`, `a/node_modules` and everything inside them.
- A **path mask** has a `/` anywhere or a `**`. It matches a path **relative to the root**, and everything under it. A leading `/` only makes the mask a path mask: `/config.php` means "only in the root".

A trailing `/` (or `\`) is dropped **before** the kind of mask is decided, so it does not make a mask a path mask and does not limit it to directories. `node_modules/` is the same name mask as `node_modules` and still matches at any depth. `var/cache/` is the same path mask as `var/cache` because of the inner `/`.

**Special characters**

| Character | Meaning |
| --- | --- |
| `*` | Any characters except `/`, including none. Also matches a leading dot: `*.log` excludes `.hidden.log` |
| `**/` | Zero or more directories: `**/tmp` matches `tmp` and `a/b/tmp`; `a/**/b` matches `a/b` and `a/x/y/b` |
| `**` elsewhere | Any characters including `/`: `a/**` is everything inside `a` |
| anything else | Matched literally and **case-sensitively**. `[`, `]`, `!` (not at the start), `{` are plain characters |

**Examples**

| Mask | Excludes | Does not exclude |
| --- | --- | --- |
| `node_modules` | `node_modules`, `web/node_modules/x.js` | `node_modules2` |
| `*.log` | `app.log`, `var/log/app.log`, `.x.log` | `app.log.1` |
| `~*` | `~$doc.docx`, `a/~lock` | `a~` |
| `/config.php` | `config.php` | `app/config.php` |
| `config.php` | `config.php`, `app/config.php` | `config.php.bak` |
| `var/cache` | `var/cache`, `var/cache/a/b` | `a/var/cache`, `var/cache2` |
| `var/cache/*` | everything inside `var/cache` | the `var/cache` directory itself |
| `/*.log` | `app.log` in the root | `var/app.log` |
| `**/tmp/*.pid` | `tmp/a.pid`, `x/y/tmp/a.pid` | `tmp/sub/a.pid` |
| `[id].tsx` | `[id].tsx`, `pages/[id].tsx` | `i.tsx` |

**Differences from `.gitignore`**

- No negation with `!`. A line starting with `!` is an error.
- No `?` and no `[...]` ranges. A `?` in a mask is an error: write `*.swp` and `*.swo` instead of `*.sw?`.
- A `/` in the middle anchors the mask to the root (as in git), and so does `**`: `**/x` is the same as `x`.
- A trailing `/` does not limit the mask to directories. As in git, it also does not anchor the mask: `build/` matches `build` at any depth.
- No nested ignore files: there is only `.owysync/ignore`. The project `.gitignore` is not read.

A mask that is empty (for example, `/`) or contains a `.` or `..` component is also an error. Errors name the line number, and nothing syncs until they are fixed.

**The `.owysync/` directory** exists on both sides and never takes part in syncing: it can't be ignored, deleted or synced. If a file watcher or web server on the server looks at the project directory, exclude `.owysync/` there.

The template from `owysync init` excludes version control directories (`.git`, `.svn`, `.hg`, `CVS`), IDE directories, and temporary files of editors and Windows. It also has commented-out lines for `node_modules`, `vendor` and `*.log`.

## What gets synced and how

- **Files and directories.** Missing ones are created, changed files are uploaded, and remote objects that are not in the local directory are deleted. If a type changed (file ↔ directory), the remote object is deleted and created again.
- **Modification times.** After an upload, the remote file gets the local file's mtime, so the clock difference between machines does not matter. Directory mtimes are not synced.
- **Permissions.** Permissions of existing remote files are kept. New files get `file_mode` and new directories get `dir_mode`.
- **Atomic replacement.** A file is written to a temporary file and then renamed over the target, so readers on the server never see a half-written file. This needs the OpenSSH `posix-rename` extension. Without it the file is missing for a moment. The tree as a whole is not updated atomically.
- **Local symlinks and junctions** are skipped with a warning and treated as ignored.
- **Remote symlinks** that are not in the local directory are deleted as links. Their targets are never touched.
- **Sockets, FIFOs and devices on the remote** (for example a php-fpm socket inside the project) are never deleted. OwySync prints a warning and suggests adding the path to `ignore`.
- **Directories with ignored contents.** When a directory deleted locally still holds ignored files on the remote, the directory stays with those files, and OwySync prints `kept directory ...`. This is not an error.
- **Unreadable remote directories.** A remote directory OwySync can't read is left alone with everything in it, and the rest of the tree is synced. The resync is reported as failed and names the directory. Add it to `ignore` if that is intended.
- **Renames** are sent as a delete plus a create. Case-only renames (`Foo` → `foo`) work the same way, because path comparison is case-sensitive.
- **Bulk mode.** When a resync plan or a live batch has more than `bulk_threshold` uploads or deletes (a first upload, `git checkout`, a large build), OwySync packs files into tar archives of up to 64 MB, uploads them and unpacks them on the server. This needs up to about 128 MB free in `.owysync/tmp` on the server. During bulk mode, files are written in place, not atomically.

## Output

Normal output goes to stdout:

```text
[10:25:27] resync: started
[10:25:29] resync: done in 2.4s: 100000 local files, 0 operations
[10:46:44] + src/new.php
[10:47:03] ~ src/changed.php
[10:47:19] - src/removed.php
```

- `owysync sync` starts every line, including warnings and errors, with the local time `[HH:MM:SS]`. The `--dry-run` plan has no timestamps.
- `+` means created, `~` means updated, `-` means deleted.
- Live mode prints every operation. A full resync prints only start and summary lines; add `-v` to see each of its operations.
- `--dry-run` prints the operations it would perform and a closing `dry run: N operations, M bytes to upload; nothing changed`.

Warnings and errors go to stderr, prefixed with `warning:` or `error:`.

A short chime plays after every sync that changed the server without errors; a sync with nothing to do stays silent. Set `sound = off` in the config to turn it off.

**Exit codes:** `0` means success; `1` means a failed check, a failed resync (`--once`), an interruption or an error; `2` means invalid command-line arguments.

## Errors and recovery

- **A single file fails** (locked by an editor, no permission): OwySync retries up to 3 times with growing delays, then prints a warning. In live mode the path is retried every 30 seconds. With `--once`, the exit code is non-zero.
- **The connection drops** (network error or three missed keepalives): OwySync reconnects with growing delays from 1 to 30 seconds, then runs a full resync.
- **Someone else changed the remote** behind OwySync's back: the next resync fixes it. If live mode runs into such a change itself (for example, a directory removed on the server), it starts a full resync in the background.
- **Config, authentication or marker errors**: OwySync exits immediately with a message.
- **Protection against a wrong `remote_root`**: OwySync refuses to sync into a directory that has no `.owysync/remote` marker, so a typo in the path can't wipe an unrelated directory.
- **One copy per directory**: `.owysync/lock` stops a second OwySync from running on the same local directory. The lock is released when the process ends, even after a crash.

## Limitations

- **Comparison by size and mtime is a heuristic.** If something on the server rewrites a file with the same size within the same second, OwySync won't notice. A file changed on the server usually gets a different mtime and is uploaded again on the next resync.
- **Case-insensitive file systems on the remote are not supported** (FAT/exFAT, CIFS mounts). A rename like `Foo` → `foo` could delete the file just uploaded.
- **Local side:** Windows only, local disks only.
- **Remote side:** Linux and FreeBSD with a shell. macOS and SFTP-only accounts are not supported.
- **SSH:** no `~/.ssh/config`, no passwords, no Pageant.
- **Every start is a full resync.** There is no saved index. With 100k files this takes a few seconds.
- **Paths are compared byte for byte.** No Unicode normalization is done. Windows names with invalid UTF-16 are skipped with a warning.
- **One direction only.** Changes made on the server are never copied back. They are overwritten or deleted.

## Troubleshooting

| Message or symptom | What to do |
| --- | --- |
| `the directory is not initialized, run owysync init` | Run `owysync init`, or pass the right path |
| `host key: FAIL` | Connect once with `ssh -p <port> <user>@<host>` and accept the key |
| `authentication: FAIL` | Add the key to ssh-agent (`ssh-add`) or set `identity_file`. A passphrase-protected key must go through the agent |
| `shell: FAIL` | The account has no usable shell, or lacks `find`/`stat`/`tar`. A restricted shell or SFTP-only account won't work |
| `marker .owysync/remote: FAIL` | Wrong `remote_root`, or the remote directory was never initialized. Fix the path, or run `owysync init --remote` |
| `? is not supported, use *` | Rewrite the mask without `?` (for example `*.swp`, `*.swo`) |
| `not synced (no access): ...` | The remote user can't read those directories. Fix their permissions or add them to `ignore` |
| `kept directory ...: it holds ignored files` | Informational. Remove the directory on the server yourself if you don't need it |
| `warning: ... the changes take effect after a restart` | You edited `config` or `ignore`. Restart OwySync |
| Changes are not picked up | Make sure the directory is on a local disk, not a network drive or `\\wsl$` |
| The server keeps getting a file uploaded again | Something on the server rewrites it, or the local file system has coarse timestamps. On FAT, set `mtime_window = 2` |
