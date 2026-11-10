// Command deploy-bundle writes octopus_<version>_deploy.tar.gz: the deploy
// tree a release installs (compose files, Caddyfile, backup.sh, the chart),
// taken from the same embedded set the binary materializes (deploy.Files).
//
//	go run ./cmd/deploy-bundle -o dist/octopus_0.3.0_deploy.tar.gz
//
// The archive is reproducible: entries sorted, fixed timestamps and owners,
// so two builds of one commit give the same bytes and the sha256 in the
// release manifest can be checked by rebuilding. Paths are relative to deploy/
// ("docker-compose.yml", "helm/octopus/Chart.yaml"), with no directory
// entries, links or absolute paths: what `thulu apply` extracts is plain
// files under deploy/ and nothing else.
package main

import (
	"archive/tar"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/deploy"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

func main() {
	configureLocale()
	out := flag.String("o", "", i18n.Text("deploybundle.cli.flag_out"))
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, i18n.Text("deploybundle.cli.usage"))
		os.Exit(2)
	}
	f, err := os.Create(*out) // #nosec G304 -- the workflow names the output
	if err == nil {
		err = write(f, deploy.Files)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.Text("deploybundle.cli.error", "err", apperr.Text(err)))
		os.Exit(1)
	}
}

func configureLocale() {
	for _, env := range []string{"OCTOPUS_LOCALE", "LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(env); v != "" {
			i18n.SetDaemonLocale(i18n.Negotiate(v))
			return
		}
	}
}

var epoch = time.Unix(0, 0).UTC()

func write(w io.Writer, files fs.FS) error {
	var names []string
	if err := fs.WalkDir(files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			names = append(names, name)
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Strings(names)

	gz, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	gz.ModTime = epoch
	tw := tar.NewWriter(gz)
	for _, name := range names {
		body, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		mode := int64(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     name,
			Mode:     mode,
			Size:     int64(len(body)),
			ModTime:  epoch,
			Format:   tar.FormatPAX,
		}); err != nil {
			return err
		}
		if _, err := tw.Write(body); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
