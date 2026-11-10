package netguard

import (
	"log"
	"net"
	"net/netip"
	"os"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const localRefresh = time.Minute

var interfaceAddrs = net.InterfaceAddrs

var now = time.Now

var local struct {
	sync.Mutex
	ifaces     []netip.Prefix
	listedAt   time.Time
	discovered map[string][]netip.Prefix
}

func LocalAddrs() []netip.Prefix {
	local.Lock()
	if local.listedAt.IsZero() || now().Sub(local.listedAt) >= localRefresh {
		local.ifaces = listInterfaces(local.ifaces)
		local.listedAt = now()
	}
	all := slices.Clone(local.ifaces)
	for _, d := range local.discovered {
		all = append(all, d...)
	}
	local.Unlock()

	raw := os.Getenv("OCTOPUS_PUBLIC_IPS")
	public, bad := parsePrefixes(raw)
	if len(bad) > 0 {
		if _, seen := warned.LoadOrStore("public="+raw, true); !seen {
			log.Printf("[netguard] %s", i18n.Log("netguard.log.bad_public_ip_entry", "index", bad[0]))
		}
	}
	all = append(all, public...)
	return dedupePrefixes(all)
}

// On failure keep the previous listing: forgetting an address would let requests reach the host
// again.
func listInterfaces(prev []netip.Prefix) []netip.Prefix {
	addrs, err := interfaceAddrs()
	if err != nil {
		if _, seen := warned.LoadOrStore("ifaces", true); !seen {
			log.Printf("[netguard] %s", i18n.Log("netguard.log.interfaces_failed", "err", err))
		}
		return prev
	}
	out := make([]netip.Prefix, 0, len(addrs))
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if addr, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, hostPrefix(addr))
		}
	}
	return out
}

func SetDiscovered(source string, addrs []netip.Addr) {
	prefixes := make([]netip.Prefix, 0, len(addrs))
	for _, a := range addrs {
		if a.IsValid() {
			prefixes = append(prefixes, hostPrefix(a))
		}
	}
	local.Lock()
	defer local.Unlock()
	if local.discovered == nil {
		local.discovered = map[string][]netip.Prefix{}
	}
	if len(prefixes) == 0 {
		delete(local.discovered, source)
		return
	}
	local.discovered[source] = prefixes
}

func dedupePrefixes(in []netip.Prefix) []netip.Prefix {
	sort.Slice(in, func(i, j int) bool {
		if c := in[i].Addr().Compare(in[j].Addr()); c != 0 {
			return c < 0
		}
		return in[i].Bits() < in[j].Bits()
	})
	return slices.Compact(in)
}
