package dockerx

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/go-units"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const (
	DefaultLogMaxSize = "10m"
	DefaultLogMaxFile = "3"

	EnvLogMaxSize = "OCTOPUS_CONTAINER_LOG_MAX_SIZE"
	EnvLogMaxFile = "OCTOPUS_CONTAINER_LOG_MAX_FILE"
)

type InfoAPI interface {
	Info(ctx context.Context) (system.Info, error)
}

var (
	driverMu    sync.Mutex
	driverCache = map[InfoAPI]string{}
)

// Type stays empty so the operator's default driver applies. max-size/max-file are only set for
// json-file and local:
// any other driver fails the container create. A failed driver lookup is not cached.
func ContainerLogConfig(ctx context.Context, api InfoAPI) container.LogConfig {
	if api == nil {
		return container.LogConfig{}
	}
	driverMu.Lock()
	driver, ok := driverCache[api]
	driverMu.Unlock()
	if !ok {
		info, err := api.Info(ctx)
		if err != nil {
			log.Printf("[docker] %s", i18n.Log("dockerx.log.driver_lookup_failed", "err", err))
			return container.LogConfig{}
		}
		driver = info.LoggingDriver
		driverMu.Lock()
		driverCache[api] = driver
		driverMu.Unlock()
	}
	return LogConfigFor(driver, os.Getenv)
}

func LogConfigFor(driver string, getenv func(string) string) container.LogConfig {
	switch driver {
	case "json-file", "local":
	default:
		return container.LogConfig{}
	}
	return container.LogConfig{Config: map[string]string{
		"max-size": logOption(getenv, EnvLogMaxSize, DefaultLogMaxSize, validLogSize),
		"max-file": logOption(getenv, EnvLogMaxFile, DefaultLogMaxFile, validLogFileCount),
	}}
}

func logOption(getenv func(string) string, key, def string, valid func(string) bool) string {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return def
	}
	if !valid(v) {
		log.Printf("[docker] %s", i18n.Log("dockerx.log.invalid_option", "key", key, "value", v, "default", def))
		return def
	}
	return v
}

func validLogSize(v string) bool {
	if v == "-1" {
		return true
	}
	n, err := units.RAMInBytes(v)
	return err == nil && n > 0
}

func validLogFileCount(v string) bool {
	n, err := strconv.Atoi(v)
	return err == nil && n > 0
}
