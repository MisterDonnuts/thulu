package runner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeOutputFile(t *testing.T, dir, rel string, size int) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), size), 0o600); err != nil {
		t.Fatal(err)
	}
}

func tarNames(t *testing.T, b []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			names = append(names, h.Name)
		}
	}
}

// Testing runs carry videos: an output dir over the budget used to fail the whole upload, and the
// verdict went with it. Now the control files go first and what does not fit stays out, listed.
func TestPackOutputsKeepsTheVerdictWhenTheDirIsOverTheBudget(t *testing.T) {
	dir := t.TempDir()
	writeOutputFile(t, dir, "videos/tc-1.mp4", 6000)
	writeOutputFile(t, dir, "videos/tc-2.mp4", 900)
	writeOutputFile(t, dir, "report.md", 300)
	writeOutputFile(t, dir, "evidence/cases.json", 200)
	writeOutputFile(t, dir, "verdict.md", 100)
	writeOutputFile(t, dir, "_index.md", 100)

	var buf bytes.Buffer
	skipped, err := packOutputs(dir, &buf, 4*tarEntryCost+700+tarEntryCost*6)
	if err != nil {
		t.Fatal(err)
	}
	names := tarNames(t, buf.Bytes())
	if len(names) < 2 || !slices.Contains(names[:2], "verdict.md") || !slices.Contains(names[:2], "_index.md") {
		t.Fatalf("packed %v, want the control files first", names)
	}
	for _, want := range []string{"report.md", "evidence/cases.json"} {
		if !slices.Contains(names, want) {
			t.Errorf("packed %v, want %s too", names, want)
		}
	}
	if slices.Contains(names, "videos/tc-1.mp4") {
		t.Errorf("packed %v: the video over the budget went in", names)
	}
	if len(skipped) == 0 || skipped[len(skipped)-1] != (SkippedOutput{Path: "videos/tc-1.mp4", Size: 6000}) {
		t.Errorf("skipped %+v, want the large video listed", skipped)
	}

	dst := t.TempDir()
	if extra, err := ExtractOutputs(&buf, dst); err != nil || len(extra) > 0 {
		t.Fatalf("extract: skipped %v, err %v", extra, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "verdict.md")); err != nil {
		t.Errorf("verdict.md did not arrive: %v", err)
	}
}

// The real budget: a file over it is listed without being read, and verdict.md still travels.
func TestPackOutputsSkipsAFileOverMaxOutputBytes(t *testing.T) {
	dir := t.TempDir()
	writeOutputFile(t, dir, "verdict.md", 40)
	big := filepath.Join(dir, "videos", "run.mp4")
	if err := os.MkdirAll(filepath.Dir(big), 0o750); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxOutputBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	var buf bytes.Buffer
	skipped, err := PackOutputs(dir, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || skipped[0].Path != "videos/run.mp4" {
		t.Fatalf("skipped %+v, want the video", skipped)
	}
	if buf.Len() > 1<<20 {
		t.Errorf("the archive is %d bytes: the skipped video went in", buf.Len())
	}
	if names := tarNames(t, buf.Bytes()); !slices.Equal(names, []string{"verdict.md"}) {
		t.Errorf("packed %v, want verdict.md", names)
	}
}

// An archive from a runner that packs past the cap loses only the file that crosses it.
func TestExtractOutputsLeavesOutWhatCrossesTheCap(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, size int64) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.CopyN(tw, zeros{}, size); err != nil {
			t.Fatal(err)
		}
	}
	add("videos/huge.mp4", MaxOutputBytes+1)
	add("verdict.md", 10)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	skipped, err := ExtractOutputs(&buf, dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || skipped[0].Path != "videos/huge.mp4" {
		t.Errorf("skipped %+v, want the video", skipped)
	}
	if _, err := os.Stat(filepath.Join(dst, "verdict.md")); err != nil {
		t.Errorf("verdict.md did not land: %v", err)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
