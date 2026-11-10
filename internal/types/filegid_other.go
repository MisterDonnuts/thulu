//go:build !unix

package types

func fileGID(string) (uint32, bool) { return 0, false }
