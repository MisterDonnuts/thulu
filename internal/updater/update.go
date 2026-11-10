package updater

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/dockergc"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/release"
)

type Options struct {
	Root         string
	Version      string
	Check        bool
	Output       string
	To           string
	Restore      string
	Rollback     bool
	Branch       string
	Keep, Unkeep string
	ListKept     bool

	AckManualSteps  bool
	BackupConfirmed bool
	NoFetchCLI      bool
	Delegated       bool
	Registry        string
	ChartRegistry   string
	// Never print it or pass it on a command line.
	APIKey      string
	Operator    string
	Yes         bool
	Values      string
	ReuseValues bool

	Prompter *initwizard.Prompter
	Runner   initwizard.Runner
	Out      io.Writer
	Docker   dockergc.API

	Deps Deps
}

type Deps struct {
	Keys         release.Keys
	Source       release.Source
	Images       ImageOps
	VerifyImage  func(ctx context.Context, ref string) error
	FetchChart   func(ctx context.Context, ref string) ([]byte, error)
	HTTP         *http.Client
	SelfVersion  string
	Exec         func(ctx context.Context, bin string, args []string) error
	ReadyTimeout time.Duration
	Now          func() time.Time
	Backup       func(ctx context.Context, in *Install, out io.Writer) (string, BackupMethod, error)
}

func (d *Deps) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

func (d *Deps) http() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (d *Deps) readyTimeout() time.Duration {
	if d.ReadyTimeout > 0 {
		return d.ReadyTimeout
	}
	return readyTimeout
}

const readyTimeout = 8 * time.Minute

func Run(ctx context.Context, opts Options) error {
	if opts.Branch != "" {
		return apperr.Invalid("updater.errors.branch_removed")
	}
	in, err := Detect(ctx, opts.Root)
	if err != nil {
		return err
	}
	return runUpdate(ctx, opts, in)
}

func runUpdate(ctx context.Context, opts Options, in *Install) error {
	u, err := newRun(opts, in)
	if err != nil {
		return err
	}
	switch {
	case opts.ListKept:
		return u.listKept()
	case opts.Keep != "" || opts.Unkeep != "":
		return u.pin(opts.Keep, opts.Unkeep)
	case opts.Rollback:
		prev, err := u.previousVersion()
		if err != nil {
			return err
		}
		u.opts.To = prev
		return u.to(ctx)
	case opts.To != "":
		return u.to(ctx)
	case opts.Check:
		return u.check(ctx, true)
	}
	return u.apply(ctx, true)
}

func Apply(ctx context.Context, opts Options) error {
	in, err := Detect(ctx, opts.Root)
	if err != nil {
		return err
	}
	return runApply(ctx, opts, in)
}

func runApply(ctx context.Context, opts Options, in *Install) error {
	u, err := newRun(opts, in)
	if err != nil {
		return err
	}
	if opts.Check {
		return u.check(ctx, false)
	}
	return u.apply(ctx, false)
}

type updateRun struct {
	opts   Options
	deps   *Deps
	in     *Install
	out    io.Writer
	getenv func(string) string
}

func newRun(opts Options, in *Install) (*updateRun, error) {
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	u := &updateRun{opts: opts, deps: &opts.Deps, in: in, out: opts.Out, getenv: in.Getenv()}
	if u.opts.APIKey == "" {
		u.opts.APIKey = strings.TrimSpace(u.getenv("OCTOPUS_API_KEY"))
	}
	in.APIKey = u.opts.APIKey
	if u.deps.Keys == nil {
		keys, err := release.TrustedKeys()
		if err != nil {
			return nil, err
		}
		u.deps.Keys = keys
	}
	if u.deps.Images == nil {
		u.deps.Images = cliImages{runner: opts.Runner}
	}
	if u.opts.Registry == "" {
		u.opts.Registry = strings.TrimRight(strings.TrimSpace(u.getenv(initwizard.ImageRegistryEnv)), "/")
	}
	if u.opts.ChartRegistry == "" {
		u.opts.ChartRegistry = strings.TrimSpace(u.getenv("OCTOPUS_CHART_REGISTRY"))
	}
	if u.opts.Operator == "" {
		u.opts.Operator = defaultOperator()
	}
	return u, nil
}

func (u *updateRun) say(key string, args ...any) { printer{w: u.out}.say(key, args...) }

type printer struct{ w io.Writer }

func (p printer) say(key string, args ...any) {
	if p.w != nil {
		fmt.Fprintln(p.w, i18n.Text(key, args...))
	}
}

func (u *updateRun) confirm(key string) (bool, error) {
	if u.opts.Yes {
		return true, nil
	}
	if u.opts.Prompter == nil {
		return false, apperr.Precondition("updater.errors.no_terminal")
	}
	fmt.Fprintln(u.out)
	return u.opts.Prompter.YesNo(i18n.Text(key), false)
}

func defaultOperator() string {
	name := os.Getenv("USER")
	if cur, err := user.Current(); err == nil && cur.Username != "" {
		name = cur.Username
	}
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	switch {
	case name != "" && host != "":
		return name + "@" + host
	case name != "":
		return name
	}
	return "unknown"
}

func describeRunning(in *Install) string {
	switch in.RunningVersion {
	case "":
		return i18n.Text("updater.labels.daemon_not_answering")
	case "dev":
		return i18n.Text("updater.labels.dev_build")
	default:
		return in.RunningVersion
	}
}
