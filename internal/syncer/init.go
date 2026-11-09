package syncer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"owysync/internal/config"
	"owysync/internal/remote"
)

// ignoreTemplate is the initial .owysync/ignore (spec §4.3).
const ignoreTemplate = `# OwySync: paths that are neither synchronized nor deleted on the remote.
# One mask per line. This is NOT .gitignore: see the documentation for the rules.
#   name      - matches at any depth:     node_modules   *.log
#   /path     - path from the root:       /config.php    var/cache
#   *         - any characters except /
#   **        - any characters including /

# Version control systems
.git
.svn
.hg
CVS

# IDEs and caches
.idea
nbproject
.cache

# Temporary files of editors and the OS
~*
*~
*.tmp
*.swp
*___jb_tmp___
*___jb_old___
*.pyc
Thumbs.db
desktop.ini

# Common candidates - uncomment what you need:
# node_modules
# vendor
# *.log
`

// configText renders the initial .owysync/config: the entered values, the other keys
// commented out with their defaults (spec §4.1, step 3).
func configText(host, user, remoteRoot string) string {
	return fmt.Sprintf(`# OwySync configuration: lines of the form `+"`key = value`"+`, # starts a comment.
host = %s
user = %s
remote_root = %s

# Uncomment a line to change the default.
# port = 22
# identity_file = C:\Users\you\.ssh\id_ed25519
# file_mode = 0644
# dir_mode = 0755
# debounce_ms = 100
# mtime_window = 0
# concurrency = 64
# bulk_threshold = 100
`, host, user, remoteRoot)
}

// prompter asks questions on out and reads answers from in; Ctrl+C (ctx) interrupts a read.
type prompter struct {
	ctx context.Context
	in  *bufio.Reader
	out io.Writer
}

// line prints prompt and returns the trimmed answer.
func (p *prompter) line(prompt string) (string, error) {
	fmt.Fprint(p.out, prompt)
	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() { // a read cannot be cancelled: on Ctrl+C the goroutine is left to the process exit
		s, err := p.in.ReadString('\n')
		ch <- result{strings.TrimSpace(s), err}
	}()
	select {
	case r := <-ch:
		if r.err != nil && r.s == "" {
			return "", errors.New("input ended before the answer")
		}
		return r.s, nil
	case <-p.ctx.Done():
		return "", p.ctx.Err()
	}
}

// ask repeats the question until the answer (or the default for an empty one) passes validate.
func (p *prompter) ask(question, def string, validate func(string) error) (string, error) {
	prompt := question + ": "
	if def != "" {
		prompt = fmt.Sprintf("%s [%s]: ", question, def)
	}
	for {
		s, err := p.line(prompt)
		if err != nil {
			return "", err
		}
		if s == "" {
			s = def
		}
		if s == "" {
			fmt.Fprintln(p.out, "  a value is required")
			continue
		}
		if err := validate(s); err != nil {
			fmt.Fprintln(p.out, "  "+err.Error())
			continue
		}
		return s, nil
	}
}

// confirm asks a yes/no question; the default answer is no (spec §4.1, step 5).
func (p *prompter) confirm(question string) (bool, error) {
	s, err := p.line(question + " [y/N]: ")
	if err != nil {
		return false, err
	}
	s = strings.ToLower(s)
	return s == "y" || s == "yes", nil
}

// validateKey checks one config value by parsing a minimal config that contains it.
func validateKey(key, val string) error {
	kv := map[string]string{"host": "h", "user": "u", "remote_root": "/r"}
	kv[key] = val
	_, err := config.Parse(fmt.Sprintf("host = %s\nuser = %s\nremote_root = %s\n", kv["host"], kv["user"], kv["remote_root"]))
	if err != nil {
		_, msg, _ := strings.Cut(err.Error(), ": ") // drop "line N"
		return errors.New(msg)
	}
	return nil
}

// defaultUser is the current Windows user name without the domain.
func defaultUser() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	name := u.Username
	if i := strings.LastIndexAny(name, `\`); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// Init is `owysync init` (spec §4.1); with remoteOnly it is `owysync init --remote`: steps 4-6 for an
// existing config. The dialog uses in and out. After a failure that the user must handle itself
// (the connection, a declined question) it returns ErrFailed; the reason is already printed.
func Init(ctx context.Context, localDir string, remoteOnly bool, in io.Reader, out io.Writer) error {
	p := &prompter{ctx: ctx, in: bufio.NewReader(in), out: out}
	dir := filepath.Join(localDir, config.Dir)
	cfgFile := config.Path(localDir)

	if !remoteOnly {
		// 1. Local check.
		if _, err := os.Stat(dir); err == nil {
			return fmt.Errorf("directory %s is already initialized; edit %s, verify with `owysync check`", localDir, cfgFile)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// 2. Dialog. Nothing is written until it ends.
		host, err := p.ask("Remote host (host or host:port)", "", func(v string) error { return validateKey("host", v) })
		if err != nil {
			return err
		}
		usr, err := p.ask("Remote user", defaultUser(), func(v string) error { return validateKey("user", v) })
		if err != nil {
			return err
		}
		root, err := p.ask("Absolute remote path", "", func(v string) error { return validateKey("remote_root", v) })
		if err != nil {
			return err
		}
		// 3. Write local.
		if err := createOwysyncDir(dir, configText(host, usr, root)); err != nil {
			return err
		}
		fmt.Fprintf(out, "Saved %s and %s\n", cfgFile, filepath.Join(dir, "ignore"))
	}

	// 4. Connection: checks 1-6.
	env, err := checkConnection(ctx, localDir, out)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !remoteOnly {
			fmt.Fprintf(out, "The config is saved in %s. Fix the problem above (for example set identity_file or port in it), verify with `owysync check \"%s\"` (checks 1-6 must be ok), then run `owysync init --remote \"%s\"`.\n", cfgFile, localDir, localDir)
		} else {
			fmt.Fprintf(out, "Fix the problem above, then run `owysync init --remote \"%s\"` again.\n", localDir)
		}
		return ErrFailed
	}
	defer env.Close()

	// 5. Remote directory.
	done, err := initRemote(env, p)
	if err != nil {
		return err
	}
	if !done {
		fmt.Fprintf(out, "Nothing was changed on the remote. Run `owysync init --remote \"%s\"` to answer again.\n", localDir)
		return ErrFailed
	}

	// 6. Summary.
	fmt.Fprintf(out, "\nDone. Before the first sync, review %s: the first sync deletes everything on the remote that is neither in the local directory nor ignored (for example node_modules, uploads, logs).\n", filepath.Join(dir, "ignore"))
	fmt.Fprintf(out, "Then look at the plan: owysync --once --dry-run \"%s\"\n", localDir)
	return nil
}

// createOwysyncDir creates .owysync with config and ignore; on failure nothing stays behind.
func createOwysyncDir(dir, cfg string) (err error) {
	if err = os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()
	for name, text := range map[string]string{"config": cfg, "ignore": ignoreTemplate} {
		if err = os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", filepath.Join(dir, name), err)
		}
	}
	return nil
}

// initRemote is step 5 of spec §4.1. done is false when the user declined a question.
func initRemote(env *Env, p *prompter) (done bool, err error) {
	cfg, c, out := env.Cfg, env.Client, p.out
	root, err := c.RealRoot(cfg.RemoteRoot)
	if errors.Is(err, remote.ErrRootMissing) {
		ok, err := p.confirm(fmt.Sprintf("Directory %s does not exist on the remote. Create it?", cfg.RemoteRoot))
		if err != nil || !ok {
			return false, err
		}
		if err := c.MkdirAll(cfg.RemoteRoot, cfg.DirMode); err != nil {
			return false, err
		}
		if root, err = c.RealRoot(cfg.RemoteRoot); err != nil {
			return false, err
		}
		return true, c.InitRoot(root, cfg.DirMode, cfg.FileMode)
	}
	if err != nil {
		return false, err
	}

	has, err := c.HasMarker(root)
	if err != nil {
		return false, err
	}
	if has {
		fmt.Fprintf(out, "%s already has the .owysync/remote marker: another local directory probably syncs into it, and two sources will overwrite each other.\n", root)
		ok, err := p.confirm("Connect to it?")
		if ok {
			fmt.Fprintln(out, "Nothing was written on the remote.")
		}
		return ok, err
	}

	names, err := c.TopLevel(root)
	if err != nil {
		return false, err
	}
	fmt.Fprintf(out, "%s exists and has no marker. It contains %d objects at the top level", root, len(names))
	if len(names) > 0 {
		shown := names[:min(len(names), 10)]
		for i := range shown {
			shown[i] = display(shown[i])
		}
		fmt.Fprintf(out, ": %s", strings.Join(shown, ", "))
		if len(names) > len(shown) {
			fmt.Fprint(out, ", ...")
		}
	}
	fmt.Fprintln(out, ".")
	fmt.Fprintln(out, "The first sync will delete everything in it that is not in the local directory and not ignored.")
	ok, err := p.confirm("Initialize it?")
	if err != nil || !ok {
		return false, err
	}
	return true, c.InitRoot(root, cfg.DirMode, cfg.FileMode)
}
