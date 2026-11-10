package imageref

import (
	"os"
	"strings"
)

const RegistryEnv = "OCTOPUS_IMAGE_REGISTRY"

func Registry() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv(RegistryEnv)), "/")
}

func Resolve(ref string) string { return WithRegistry(Registry(), ref) }

func WithRegistry(registry, ref string) string {
	ref = strings.TrimSpace(ref)
	registry = strings.TrimRight(strings.TrimSpace(registry), "/")
	if registry == "" || ref == "" || HasRegistry(ref) {
		return ref
	}
	return registry + "/" + ref
}

// Follows the distribution reference grammar: the first component is a host if it has '.' or ':' or
// is "localhost".
func HasRegistry(ref string) bool {
	i := strings.IndexByte(ref, '/')
	if i <= 0 {
		return false
	}
	host := ref[:i]
	return host == "localhost" || strings.ContainsAny(host, ".:")
}
