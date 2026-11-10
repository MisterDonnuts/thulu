package initwizard

import (
	"net"
	"testing"
)

func TestPortFree_WildcardListenerIsBusy(t *testing.T) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if portFree(t.Context(), port) {
		t.Fatalf("port %d has a listener on every address, portFree says free", port)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if !portFree(t.Context(), port) {
		t.Fatalf("port %d was released, portFree says busy", port)
	}
}
