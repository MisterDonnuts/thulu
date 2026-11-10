package edition

import (
	"sync"
	"time"
)

type Feature string

const (
	AuditExport Feature = "audit_export"
	Kubernetes  Feature = "kubernetes"
	SaaS        Feature = "saas"
)

const (
	Community  = "ce"
	Enterprise = "ee"
)

// CommunitySeats is the team size up to which Thulu asks for nothing (docs/ee-licensing.md).
// Nothing enforces it: the members page only tells an admin when an install has more.
const CommunitySeats = 4

type Status struct {
	Edition   string
	Licensee  string
	Features  []string
	ExpiresAt time.Time
	// Seats is the number of people the license covers; 0 means it does not say.
	Seats int
}

// License is what ee/ installs once it has checked OCTOPUS_LICENSE_KEY. Without ee/, or without a
// valid license, nothing is installed and the core runs as Community.
type License interface {
	Status() Status
	Has(Feature) bool
}

// Activate is set by ee/. The daemon calls it once at startup; it reads the license, installs it
// with SetLicense when it is valid, and returns the line to log.
var Activate func() string

var (
	mu       sync.RWMutex
	license  License
	loadOnce sync.Once
	loaded   string
)

// Load runs Activate once per process, for the daemon and for CLI commands that check a feature,
// and returns the line it logged.
func Load() string {
	loadOnce.Do(func() {
		if Activate != nil {
			loaded = Activate()
		}
	})
	return loaded
}

func SetLicense(l License) {
	mu.Lock()
	defer mu.Unlock()
	license = l
}

func Installed() License { return current() }

func current() License {
	mu.RLock()
	defer mu.RUnlock()
	return license
}

func Has(f Feature) bool {
	l := current()
	return l != nil && l.Has(f)
}

func Current() Status {
	l := current()
	if l == nil {
		return Status{Edition: Community, Features: []string{}}
	}
	return l.Status()
}
