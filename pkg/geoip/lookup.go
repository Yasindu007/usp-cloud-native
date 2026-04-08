// Package geoip provides IP-to-country geolocation.
//
// Phase 3 implementation: stub that returns "XX" for all IPs.
//
// Phase 4 implementation plan:
//
//	Replace Lookup() with a real MaxMind GeoLite2 Country database lookup.
//	MaxMind provides a free GeoLite2 database (Creative Commons license)
//	updated twice weekly. The database file (GeoLite2-Country.mmdb) is
//	downloaded and mounted into the container at deployment time.
//
//	Phase 4 integration:
//	  1. Add github.com/oschwald/maxminddb-golang to go.mod
//	  2. Load the .mmdb file at startup via geoip.LoadDatabase(path)
//	  3. Replace the stub Lookup with a real db.Lookup(net.ParseIP(ip), &record)
//
//	Why MaxMind GeoLite2 and not an API service?
//	  - No network call on the redirect hot path (file-based, local lookup)
//	  - Sub-millisecond lookup time (binary search in memory-mapped file)
//	  - No per-request cost (important at 10k RPS)
//	  - Works offline / in air-gapped environments
//
// Country code meaning:
//
//	"XX" = unknown / unresolvable (private ranges, loopback, VPNs, Tor)
//	"ZZ" = reserved for test use
//	All others = ISO 3166-1 alpha-2 (e.g., "US", "GB", "DE")
package geoip

// Lookup returns the ISO 3166-1 alpha-2 country code for an IP address.
// Phase 3 stub: always returns "XX" (unknown).
// Phase 4: replaced with MaxMind GeoLite2 database lookup.
//
// The ip parameter is the raw client IP address (not the hash).
// This is the only place in the system where the raw IP is used after
// being received — it is NOT stored, only looked up and immediately discarded.
func Lookup(ip string) string {
	// Phase 3 stub: all IPs map to "XX" (unknown country).
	// This is deliberately conservative — incorrect country attribution is
	// worse than "unknown" because it corrupts analytics data.
	//
	// Common private/special IP ranges that would always be "XX" even with
	// a real DB: 127.0.0.1, 10.x.x.x, 172.16-31.x.x, 192.168.x.x,
	// ::1, fd00::/8 (ULA), 100.64.x.x (carrier-grade NAT).
	// In a local development environment, all requests come from these ranges.
	_ = ip
	return "XX"
}
