// Imports nothing, so any command can report its version without linking the daemon.
package buildinfo

// Stamped via -ldflags "-X ...buildinfo.Version=vX.Y.Z"; renaming it breaks the stamp.
var Version = "dev"

// What Thulu sends to GitHub and other APIs that ask clients to identify themselves.
func UserAgent() string { return "thulu/" + Version } //i18n:ignore HTTP user agent
