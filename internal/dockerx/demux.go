package dockerx

import (
	"bufio"
	"io"
	"strings"

	"github.com/docker/docker/pkg/stdcopy"
)

// Each complete frame is exactly one w.Write call; streaming (SSE) endpoints rely on it for
// per-frame flushing.
func Demux(w io.Writer, r io.Reader) error {
	br := bufio.NewReaderSize(r, 32*1024)
	hdr, err := br.Peek(8)
	if err != nil || !isFrameHeader(hdr) {
		_, cerr := io.Copy(w, br)
		return cerr
	}
	_, err = stdcopy.StdCopy(w, w, br)
	return err
}

func DemuxAll(r io.Reader) string {
	var b strings.Builder
	if err := Demux(&b, r); err != nil {
		return b.String()
	}
	return b.String()
}

func isFrameHeader(h []byte) bool {
	return len(h) == 8 && h[0] <= 2 && h[1] == 0 && h[2] == 0 && h[3] == 0
}
