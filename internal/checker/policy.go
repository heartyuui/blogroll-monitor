package checker

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

const maxResolvedAddresses = 16

var blockedPrefixes = mustPrefixes(
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.88.99.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"224.0.0.0/4",
	"240.0.0.0/4",
	"::/128",
	"::1/128",
	"64:ff9b::/96",
	"100::/64",
	"2001:db8::/32",
	"2002::/16",
	"fc00::/7",
	"fe80::/10",
	"ff00::/8",
)

type PolicyError struct {
	Code string
}

func (e *PolicyError) Error() string { return e.Code }

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type SystemResolver struct {
	Resolver *net.Resolver
}

func (r SystemResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	resolver := r.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return resolver.LookupNetIP(ctx, network, host)
}

type Destination struct {
	URL               *url.URL
	Hostname          string
	RegistrableDomain string
	Addresses         []netip.Addr
}

type Policy struct {
	Resolver          Resolver
	LookupTimeout     time.Duration
	AllowTestLoopback bool
}

func (p Policy) Resolve(ctx context.Context, rawURL string) (Destination, error) {
	if len(rawURL) == 0 || len(rawURL) > 2048 {
		return Destination{}, &PolicyError{Code: "INVALID_URL"}
	}
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return Destination{}, &PolicyError{Code: "INVALID_URL"}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Destination{}, &PolicyError{Code: "UNSUPPORTED_SCHEME"}
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return Destination{}, &PolicyError{Code: "INVALID_URL"}
	}
	requestedPort := parsed.Port()
	if requestedPort != "" && !(p.AllowTestLoopback && isLoopbackHostname(parsed.Hostname())) {
		return Destination{}, &PolicyError{Code: "INVALID_URL"}
	}

	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" || len(host) > 253 {
		return Destination{}, &PolicyError{Code: "INVALID_HOSTNAME"}
	}
	if address, parseErr := netip.ParseAddr(host); parseErr == nil {
		host = address.Unmap().String()
	} else {
		host, err = idna.Lookup.ToASCII(host)
		if err != nil || (isForbiddenHostname(host) && !(p.AllowTestLoopback && host == "localhost")) {
			return Destination{}, &PolicyError{Code: "INVALID_HOSTNAME"}
		}
	}

	lookupContext, cancel := context.WithTimeout(ctx, p.LookupTimeout)
	defer cancel()
	addresses, err := p.resolveAddresses(lookupContext, host)
	if err != nil {
		if lookupContext.Err() != nil {
			return Destination{}, &PolicyError{Code: "DNS_TIMEOUT"}
		}
		return Destination{}, &PolicyError{Code: "DNS_RESOLUTION_FAILED"}
	}
	if len(addresses) == 0 || len(addresses) > maxResolvedAddresses {
		return Destination{}, &PolicyError{Code: "DNS_RESOLUTION_FAILED"}
	}

	unique := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		normalized := address.Unmap()
		if !p.isAllowedAddress(normalized) {
			return Destination{}, &PolicyError{Code: "SSRF_BLOCKED"}
		}
		unique[normalized] = struct{}{}
	}
	addresses = addresses[:0]
	for address := range unique {
		addresses = append(addresses, address)
	}
	sort.Slice(addresses, func(i, j int) bool {
		return addresses[i].Compare(addresses[j]) < 0
	})

	if requestedPort != "" {
		parsed.Host = net.JoinHostPort(host, requestedPort)
	} else if address, parseErr := netip.ParseAddr(host); parseErr == nil && address.Is6() {
		parsed.Host = "[" + host + "]"
	} else {
		parsed.Host = host
	}
	registrableDomain := host
	if p.AllowTestLoopback && host == "localhost" {
		registrableDomain = host
	} else if _, parseErr := netip.ParseAddr(host); parseErr != nil {
		registrableDomain, err = publicsuffix.EffectiveTLDPlusOne(host)
		if err != nil {
			return Destination{}, &PolicyError{Code: "INVALID_HOSTNAME"}
		}
	}
	return Destination{
		URL:               parsed,
		Hostname:          host,
		RegistrableDomain: registrableDomain,
		Addresses:         addresses,
	}, nil
}

func (p Policy) resolveAddresses(ctx context.Context, host string) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{address}, nil
	}
	resolver := p.Resolver
	if resolver == nil {
		resolver = SystemResolver{}
	}
	return resolver.LookupNetIP(ctx, "ip", host)
}

func (p Policy) isAllowedAddress(address netip.Addr) bool {
	if p.AllowTestLoopback && address.IsLoopback() {
		return true
	}
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func isForbiddenHostname(host string) bool {
	if host == "localhost" || host == "metadata" || host == "metadata.google.internal" || !strings.Contains(host, ".") {
		return true
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".home.arpa"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

func isLoopbackHostname(host string) bool {
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	address, err := netip.ParseAddr(host)
	return err == nil && address.Unmap().IsLoopback()
}

func mustPrefixes(values ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			panic(fmt.Sprintf("invalid blocked prefix %q", value))
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}
