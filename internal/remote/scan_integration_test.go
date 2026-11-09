//go:build integration

package remote

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"owysync/internal/ignore"
	"owysync/internal/standtest"
	"owysync/internal/tree"
)

// scantestSetup builds the tree of kb/fixtures/probes/scantest.sh under root as the stand user.
// Both odd directories belong to the user, which works the same: access(2) honours the owner's bits.
const scantestSetup = `r=%s
[ -d "$r" ] && chmod -R u+rwx "$r" </dev/null
rm -rf "$r" </dev/null
mkdir -p "$r/.owysync/tmp" "$r/sub" "$r/node_modules/x" "$r/locked/inner" "$r/rnox" </dev/null
echo hi > "$r/a b"; printf x > "$r/$(printf 'nl\nname')"; echo ru > "$r/sub/файл"
ln -s /etc "$r/sub/link" </dev/null
touch "$r/node_modules/x/ignored" "$r/.owysync/remote" </dev/null
echo secret > "$r/locked/inner/s"; chmod 000 "$r/locked" </dev/null
echo y > "$r/rnox/child"; chmod 0444 "$r/rnox" </dev/null
mkfifo "$r/fifo" </dev/null
echo setup-done
`

const scantestCleanup = `r=%s; chmod -R u+rwx "$r" </dev/null; rm -rf "$r" </dev/null`

func TestScanStand(t *testing.T) {
	for _, target := range standtest.Targets(t) {
		for _, user := range standtest.Users {
			t.Run(target.Name+"/"+user, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				p := standtest.Setup(t, target, user, "/home/"+user+"/proj")
				c, err := Connect(ctx, Options{Host: target.Host, Port: target.Port, User: user, IdentityFile: standtest.KeyPath(), KnownHostsFile: p.KnownHosts})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { c.Close() }) // runs after the tree cleanup below (LIFO)

				root := shQuote("/home/" + user + "/scantest")
				if out, _, err := c.RunScript(ctx, fmt.Sprintf(scantestSetup, root)); err != nil || !bytes.Contains(out, []byte("setup-done")) {
					t.Fatalf("setup: %v %s", err, out)
				}
				t.Cleanup(func() { c.RunScript(context.Background(), fmt.Sprintf(scantestCleanup, root)) })

				v, err := c.Probe(ctx)
				if err != nil {
					t.Fatal(err)
				}
				real, err := c.RealRoot("/home/" + user + "/scantest")
				if err != nil {
					t.Fatal(err)
				}
				ign, err := ignore.Parse("node_modules")
				if err != nil {
					t.Fatal(err)
				}
				res, err := c.Scan(ctx, real, v, ign.PruneArgs(real))
				if err != nil {
					t.Fatal(err)
				}

				got := map[string]tree.Type{}
				for _, e := range res.Entries {
					got[e.Path] = e.Type
				}
				want := map[string]tree.Type{
					"rnox": tree.Dir, "nl\nname": tree.File, "fifo": tree.Other, "locked": tree.Dir,
					"a b": tree.File, "sub": tree.Dir, "sub/link": tree.Symlink, "sub/файл": tree.File,
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("entries:\n got %q\nwant %q", got, want)
				}
				sort.Strings(res.Unreadable)
				if w := []string{"locked", "rnox"}; !reflect.DeepEqual(res.Unreadable, w) {
					t.Errorf("unreadable = %q, want %q", res.Unreadable, w)
				}
			})
		}
	}
}
