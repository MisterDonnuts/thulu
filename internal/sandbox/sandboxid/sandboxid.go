// Must import nothing: internal/dockergc (in the host CLI) uses it without linking the sandbox
// runtime and its Kubernetes client.
package sandboxid

const ContainerLabel = "thulu.sandbox"

const CacheVolumeLabel = "octopus.sandbox-cache"

// The tag `make runtime-image` builds; change both together.
const DefaultRuntimeImage = "octopus-runtime:latest" //i18n:ignore image reference
