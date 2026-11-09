package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// streamReader makes sftp.File.ReadFrom pipeline the writes of a stream of unknown length:
// a negative Size asks it for full concurrency.
type streamReader struct{ io.Reader }

func (streamReader) Size() int64 { return -1 }

// PutPart stores the bulk part that write produces as .owysync/tmp/bulk.<rand>.tar (spec §9.1, step 2)
// and returns its path relative to the root. The data is streamed: write runs in its own goroutine,
// and PutPart returns only after write has returned.
func (w *Writer) PutPart(write func(io.Writer) error) (string, error) {
	rel := ".owysync/tmp/bulk." + randName() + ".tar"
	f, err := w.s.OpenFile(w.abs(rel), os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return "", fmt.Errorf("creating bulk part %s: %w", rel, err)
	}
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		pw.CloseWithError(write(pw))
	}()
	_, err = f.ReadFrom(streamReader{pr})
	pr.CloseWithError(errors.New("bulk part upload stopped")) // unblocks write if the upload failed
	<-done
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		w.s.Remove(w.abs(rel))
		return "", fmt.Errorf("uploading bulk part %s: %w", rel, err)
	}
	return rel, nil
}

const extractScript = `echo owysync-tar-begin; echo owysync-tar-begin >&2
LC_ALL=C; export LC_ALL
umask 0
cd @ROOT@ </dev/null && tar -xof @PART@ </dev/null && echo owysync-tar-ok
rm -f @PART@ </dev/null
echo owysync-tar-done
`

// Extract unpacks a part stored by PutPart under the root and removes it (spec §9.1, step 3).
// umask 0 makes the modes exactly those of the headers; -o keeps the owner.
func (w *Writer) Extract(ctx context.Context, part string) error {
	script := strings.NewReplacer("@ROOT@", shQuote(w.root), "@PART@", shQuote(w.abs(part))).Replace(extractScript)
	body, err := w.run(ctx, script, "tar")
	if err == nil && !bytes.Contains(body, []byte("owysync-tar-ok\n")) {
		err = errors.New("bulk extraction: tar failed")
	}
	return err
}

const removeScript = `echo owysync-del-begin; echo owysync-del-begin >&2
LC_ALL=C; export LC_ALL
cd @ROOT@ </dev/null || exit 1
f=@FILES@; d=@DIRS@
[ -s "$f" ] && xargs -0 rm -f < "$f"
[ -s "$d" ] && xargs -0 sh -c 'rmdir "$@" 2>/dev/null </dev/null || for p; do
  [ -d "$p" ] || continue
  e=$(rmdir "$p" 2>&1 </dev/null) && continue
  case $e in *"not empty"*) printf "%s\0" "$p";; *) printf "%s\n" "$e" >&2;; esac
done' sh < "$d"
rm -f "$f" "$d" </dev/null
echo owysync-del-done
`

// RemoveList deletes files and symlinks, then directories, by lists uploaded as files
// (spec §9.1, "Удаление"). dirs must be ordered bottom-up. A missing path is a success.
// kept lists the directories that are not empty: they hold ignored content (spec §9).
func (w *Writer) RemoveList(ctx context.Context, files, dirs []string) (kept []string, err error) {
	list := func(paths []string) (string, error) {
		var b bytes.Buffer
		for _, p := range paths {
			b.WriteString("./" + p + "\x00") // ./ keeps a leading - from being an option
		}
		rel := ".owysync/tmp/del." + randName()
		f, err := w.s.OpenFile(w.abs(rel), os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err != nil {
			return "", fmt.Errorf("creating delete list %s: %w", rel, err)
		}
		_, err = f.ReadFrom(&b)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return "", fmt.Errorf("uploading delete list %s: %w", rel, err)
		}
		return w.abs(rel), nil
	}
	fl, err := list(files)
	if err != nil {
		return nil, err
	}
	dl, err := list(dirs)
	if err != nil {
		return nil, err
	}
	script := strings.NewReplacer("@ROOT@", shQuote(w.root), "@FILES@", shQuote(fl), "@DIRS@", shQuote(dl)).Replace(removeScript)
	body, err := w.run(ctx, script, "del")
	if err != nil {
		return nil, err
	}
	for _, rec := range strings.Split(string(body), "\x00") {
		if p, ok := strings.CutPrefix(rec, "./"); ok {
			kept = append(kept, p)
		}
	}
	return kept, nil
}

// run runs a bulk script that prints owysync-<name>-begin and -done markers and returns stdout
// between them. Any stderr output after the marker is an error (spec §9.1, "Результат"), except
// GNU tar's warning about mtimes in the future: a clock skew is not an error for us.
func (w *Writer) run(ctx context.Context, script, name string) ([]byte, error) {
	stdout, stderr, err := w.c.RunScript(ctx, script)
	if err != nil {
		return nil, err
	}
	begin, done := "owysync-"+name+"-begin", "owysync-"+name+"-done\n"
	body, ok := afterMarker(stdout, begin)
	errBody, ok2 := afterMarker(stderr, begin)
	if !ok || !ok2 {
		return nil, fmt.Errorf("bulk %s: sh -s printed no start marker: the remote has a restricted shell or a non-standard login command", name)
	}
	var msgs []string
	for _, line := range strings.Split(string(errBody), "\n") {
		if line != "" && !strings.Contains(line, "in the future") {
			msgs = append(msgs, line)
		}
	}
	if len(msgs) > 0 {
		return nil, fmt.Errorf("bulk %s on the remote failed:\n  %s", name, strings.Join(msgs, "\n  "))
	}
	body, ok = bytes.CutSuffix(body, []byte(done))
	if !ok {
		return nil, fmt.Errorf("bulk %s: output was cut off: no completion marker", name)
	}
	return body, nil
}
