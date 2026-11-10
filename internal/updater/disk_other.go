//go:build !unix

package updater

func freeBytes(string) int64 { return -1 }
