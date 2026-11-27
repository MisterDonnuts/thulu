package hostrun

import (
	"context"
	"testing"
)

// hiddenEscapees leaves what an agent leaves through Claude Code's Bash tool, with macOS's own
// binaries, whose environment the runner cannot read: a session leader that backgrounds a shell loop
// and a perl with nohup and exits at once. $1 starts the leader: a fork (perl) or posix_spawn
// (python, like Bun).
const hiddenEscapees = `M="$THULU_TEST_MARK"
body='nohup /bin/sh -c "while :; do /bin/sleep 1; done" "helper-$0-loop" >/dev/null 2>&1 &
nohup /usr/bin/perl -e "sleep 600" "helper-$0-perl" >/dev/null 2>&1 &'
case "$SPAWN" in
fork) perl -e 'use POSIX; POSIX::setsid(); exec @ARGV' /bin/sh -c "$body" "$M" ;;
*) python3 -c 'import os, sys
os.posix_spawn("/bin/sh", ["/bin/sh", "-c", sys.argv[1], sys.argv[2]], dict(os.environ), setsid=True)' "$body" "$M" ;;
esac
sleep 1.5
`

func TestRunKillsHiddenEnvEscapees(t *testing.T) {
	for _, spawn := range []string{"fork", "posix_spawn"} {
		t.Run(spawn, func(t *testing.T) {
			f := newKillFixture(t)
			j := f.job(hiddenEscapees)
			j.spec.Env["SPAWN"] = spawn
			if res := f.execute(context.Background(), j, nil); res.ExitCode != 0 || len(res.Warnings) != 0 {
				t.Errorf("finish = %+v, want a clean exit with no warning", res)
			}
		})
	}
}
