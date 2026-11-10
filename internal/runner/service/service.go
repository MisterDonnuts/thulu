// Neither definition redirects the runner's output: the runner rotates its own log (hostrun.Log),
// and launchd/systemd would only append.
package service

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/fsx"
)

const Label = "dev.octopus.runner"

const unitName = "octopus-runner.service"

var (
	goos    = runtime.GOOS
	command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		//nolint:gosec // G204: launchctl, systemctl or loginctl with this package's own verbs, label and file
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		if err != nil {
			return out, apperr.Wrap(err, "service.errors.command",
				"command", name+" "+strings.Join(args, " "), "output", strings.TrimSpace(string(out)))
		}
		return out, nil
	}
)

type Spec struct {
	Binary string
	Config string
	Log    string
	Path   string
}

type Status struct {
	File      string
	Installed bool
	Loaded    bool
	Running   bool
	LastExit  string
	NoLinger  bool
}

func Supported() bool { return goos == "darwin" || goos == "linux" }

func LogPath(config string) string { return filepath.Join(filepath.Dir(config), "runner.log") }

func SpecFor(config string) (Spec, error) {
	exe, err := os.Executable()
	if err != nil {
		return Spec{}, apperr.Wrap(err, "service.errors.executable")
	}
	// `go run` builds into a temp dir deleted on exit; a service pointing there fails silently on
	// next login.
	if strings.Contains(exe, string(filepath.Separator)+"go-build") {
		return Spec{}, apperr.Precondition("service.errors.temporary_binary", "path", exe)
	}
	abs, err := filepath.Abs(config)
	if err != nil {
		return Spec{}, apperr.Wrap(err, "service.errors.config_path", "path", config)
	}
	return Spec{
		Binary: stableBinary(exe),
		Config: abs,
		Log:    LogPath(abs),
		Path:   os.Getenv("PATH"),
	}, nil
}

// Prefer the PATH name (e.g. a package manager symlink): the resolved versioned target is deleted
// on upgrade.
func stableBinary(exe string) string {
	found, err := exec.LookPath("octopus")
	if err != nil {
		return exe
	}
	if abs, aerr := filepath.Abs(found); aerr == nil {
		found = abs
	}
	a, aerr := os.Stat(found)
	b, berr := os.Stat(exe)
	if aerr != nil || berr != nil || !os.SameFile(a, b) {
		return exe
	}
	return found
}

// launchd starts a job a moment after `bootstrap` returns; querying at once reports a runner about
// to run as not running.
var startWait = 3 * time.Second

func Install(ctx context.Context, s Spec) (Status, error) {
	var st Status
	var err error
	switch goos {
	case "darwin":
		st, err = installLaunchd(ctx, s)
	case "linux":
		st, err = installSystemd(ctx, s)
	default:
		return Status{}, apperr.Precondition("service.errors.unsupported_os", "os", goos)
	}
	for deadline := time.Now().Add(startWait); err == nil && !st.Running && time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			return st, nil
		case <-time.After(200 * time.Millisecond):
		}
		if again, qerr := Query(ctx); qerr == nil {
			st = again
		}
	}
	return st, err
}

func Uninstall(ctx context.Context) (bool, error) {
	switch goos {
	case "darwin":
		return uninstallLaunchd(ctx)
	case "linux":
		return uninstallSystemd(ctx)
	}
	return false, apperr.Precondition("service.errors.unsupported_os", "os", goos)
}

func Query(ctx context.Context) (Status, error) {
	switch goos {
	case "darwin":
		path, err := plistPath()
		if err != nil {
			return Status{}, err
		}
		st := Status{File: path, Installed: exists(path)}
		out, loaded := launchdPrint(ctx)
		st.Loaded = loaded
		st.Running = loaded && bytes.Contains(out, []byte("state = running"))
		//i18n:ignore a key of launchctl's output
		if code := launchdField(out, "last exit code"); !strings.HasPrefix(code, "(") {
			st.LastExit = code
		}
		return st, nil
	case "linux":
		path, err := unitPath()
		if err != nil {
			return Status{}, err
		}
		st := Status{File: path, Installed: exists(path)}
		if !st.Installed {
			return st, nil
		}
		out, err := command(ctx, "systemctl", "--user", "show", unitName,
			"-p", "LoadState", "-p", "ActiveState", "-p", "ExecMainStatus", "-p", "ExecMainExitTimestampMonotonic")
		if err != nil {
			return st, err
		}
		props := systemdProps(out)
		st.Loaded = props["LoadState"] == "loaded"
		st.Running = props["ActiveState"] == "active"
		if props["ExecMainExitTimestampMonotonic"] != "0" {
			st.LastExit = props["ExecMainStatus"]
		}
		//i18n:ignore a logind property name
		lout, lerr := command(ctx, "loginctl", "show-user", strconv.Itoa(os.Getuid()), "-p", "Linger")
		st.NoLinger = lerr != nil || systemdProps(lout)["Linger"] != "yes" //i18n:ignore a logind property name
		return st, nil
	}
	return Status{}, apperr.Precondition("service.errors.unsupported_os", "os", goos)
}

// Restart only on failure: a clean exit means the runner was told to stop.
// ThrottleInterval caps restarts of a runner the daemon refuses at twice a minute.
//
//i18n:ignore a launchd property list, not a message
var launchdPlist = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>{{.Label}}</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{.Binary}}</string>
    <string>runner</string>
    <string>run</string>
    <string>-config</string>
    <string>{{.Config}}</string>
    <string>-log</string>
    <string>{{.Log}}</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key><string>{{.Path}}</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key><false/>
  </dict>
  <key>ThrottleInterval</key><integer>30</integer>
</dict>
</plist>
`))

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", apperr.Wrap(err, "service.errors.home")
	}
	//i18n:ignore macOS directory names
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), nil
}

// The GUI domain is where the agent can read the login keychain holding claude's session.
func launchdDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func launchdPrint(ctx context.Context) (out []byte, loaded bool) {
	out, err := command(ctx, "launchctl", "print", launchdDomain()+"/"+Label)
	return out, err == nil
}

func launchdField(out []byte, key string) string {
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), " = "); ok && k == key {
			return v
		}
	}
	return ""
}

func bootoutIfLoaded(ctx context.Context) (loaded bool, err error) {
	if _, loaded := launchdPrint(ctx); !loaded {
		return false, nil
	}
	if _, err := command(ctx, "launchctl", "bootout", launchdDomain()+"/"+Label); err != nil {
		return true, err
	}
	return true, nil
}

func uninstallLaunchd(ctx context.Context) (bool, error) {
	path, err := plistPath()
	if err != nil {
		return false, err
	}
	// launchd keeps an agent whose file was deleted loaded until logout.
	loaded, err := bootoutIfLoaded(ctx)
	if err != nil {
		return false, err
	}
	had, err := removeIfThere(path)
	return loaded || had, err
}

func installLaunchd(ctx context.Context, s Spec) (Status, error) {
	path, err := plistPath()
	if err != nil {
		return Status{}, err
	}
	var buf bytes.Buffer
	data := struct {
		Spec
		Label string
	}{Spec: escaped(s), Label: Label}
	if err := launchdPlist.Execute(&buf, data); err != nil {
		return Status{}, apperr.Wrap(err, "service.errors.render")
	}
	if err := writeFile(path, buf.Bytes()); err != nil {
		return Status{}, err
	}
	// bootstrap refuses a label that is already loaded.
	if _, err := bootoutIfLoaded(ctx); err != nil {
		return Status{}, err
	}
	if _, err := command(ctx, "launchctl", "bootstrap", launchdDomain(), path); err != nil {
		return Status{}, err
	}
	return Query(ctx)
}

//i18n:ignore a systemd unit file, not a message
var systemdUnit = template.Must(template.New("unit").Parse(`[Unit]
Description=Thulu runner

[Service]
ExecStart="{{.Binary}}" runner run -config "{{.Config}}" -log "{{.Log}}"
Environment="PATH={{.Path}}"
Restart=on-failure
RestartSec=30

[Install]
WantedBy=default.target
`))

func unitPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", apperr.Wrap(err, "service.errors.home")
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", unitName), nil
}

func installSystemd(ctx context.Context, s Spec) (Status, error) {
	path, err := unitPath()
	if err != nil {
		return Status{}, err
	}
	var buf bytes.Buffer
	if err := systemdUnit.Execute(&buf, unitEscaped(s)); err != nil {
		return Status{}, apperr.Wrap(err, "service.errors.render")
	}
	if err := writeFile(path, buf.Bytes()); err != nil {
		return Status{}, err
	}
	// restart, not start: a rerun must pick up the new unit.
	for _, args := range [][]string{{"daemon-reload"}, {"enable", unitName}, {"restart", unitName}} {
		if _, err := command(ctx, "systemctl", append([]string{"--user"}, args...)...); err != nil {
			return Status{}, err
		}
	}
	return Query(ctx)
}

func uninstallSystemd(ctx context.Context) (bool, error) {
	path, err := unitPath()
	if err != nil {
		return false, err
	}
	if !exists(path) {
		return false, nil
	}
	if _, err = command(ctx, "systemctl", "--user", "disable", "--now", unitName); err != nil {
		return false, err
	}
	if _, err = removeIfThere(path); err != nil {
		return false, err
	}
	_, err = command(ctx, "systemctl", "--user", "daemon-reload")
	return true, err
}

func systemdProps(out []byte) map[string]string {
	props := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[k] = v
		}
	}
	return props
}

func escaped(s Spec) Spec {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return Spec{Binary: r.Replace(s.Binary), Config: r.Replace(s.Config), Log: r.Replace(s.Log), Path: r.Replace(s.Path)}
}

// systemd expands % specifiers in every setting and $VARS in ExecStart, and treats \ and " as
// escapes in a quoted word.
func unitEscaped(s Spec) Spec {
	quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%")
	word := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$")
	return Spec{Binary: word.Replace(s.Binary), Config: word.Replace(s.Config),
		Log: word.Replace(s.Log), Path: quoted.Replace(s.Path)}
}

func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return apperr.Wrap(err, "service.errors.write", "path", path)
	}
	if err := fsx.WriteFile(path, b, 0o600); err != nil {
		return apperr.Wrap(err, "service.errors.write", "path", path)
	}
	return nil
}

func removeIfThere(path string) (bool, error) {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, apperr.Wrap(err, "service.errors.write", "path", path)
	}
	return true, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
