package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
)

// rangeV4 is one address range with its country code.
type rangeV4 struct {
	start uint32
	end   uint32
	code  [2]byte
}

// Geo holds the country table, the block list, and the proxy list.
// The structure is read-only after LoadGeo returns.
type Geo struct {
	ranges  []rangeV4
	blocked map[string]bool
	proxies []netip.Prefix
}

// LoadGeo builds the country table from the configuration.
func LoadGeo(conf *Config) (*Geo, error) {
	geo := &Geo{blocked: make(map[string]bool, len(conf.BlockedCountries))}
	for _, code := range conf.BlockedCountries {
		geo.blocked[code] = true
	}

	for _, text := range conf.TrustedProxies {
		prefix, err := netip.ParsePrefix(text)
		if err != nil {
			return nil, fmt.Errorf("trusted_proxies %q: %w", text, err)
		}
		geo.proxies = append(geo.proxies, prefix)
	}

	if conf.GeoV4File != "" {
		table, err := loadRanges(conf.GeoV4File)
		if err != nil {
			return nil, err
		}
		geo.ranges = table
		log.Printf("geo: %d ranges loaded from %s", len(table), conf.GeoV4File)
	}
	if len(geo.ranges) == 0 && len(geo.proxies) == 0 && len(geo.blocked) > 0 {
		log.Printf("geo warning: a block list exists, but there is no range " +
			"file and no trusted proxy, so no request can be blocked")
	}
	return geo, nil
}

// loadRanges reads a CSV file with the columns start, end, country code.
// The address columns accept the dotted form and the decimal form.
func loadRanges(path string) ([]rangeV4, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open geo file: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = true

	table := make([]rangeV4, 0, 300000)
	line := 0
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("geo file line %d: %w", line, err)
		}
		line++
		if len(record) < 3 {
			continue
		}
		start, valid := parseV4(strings.TrimSpace(record[0]))
		if !valid {
			continue
		}
		end, valid := parseV4(strings.TrimSpace(record[1]))
		if !valid || end < start {
			continue
		}
		code := strings.ToUpper(strings.TrimSpace(record[2]))
		if len(code) != 2 {
			continue
		}
		table = append(table, rangeV4{
			start: start,
			end:   end,
			code:  [2]byte{code[0], code[1]},
		})
	}

	sort.Slice(table, func(one, two int) bool {
		return table[one].start < table[two].start
	})
	return table, nil
}

// parseV4 accepts 1.2.3.4 and also the decimal form 16909060.
func parseV4(text string) (uint32, bool) {
	if strings.Contains(text, ".") {
		addr, err := netip.ParseAddr(text)
		if err != nil || !addr.Is4() {
			return 0, false
		}
		quad := addr.As4()
		return uint32(quad[0])<<24 | uint32(quad[1])<<16 |
			uint32(quad[2])<<8 | uint32(quad[3]), true
	}
	num, err := strconv.ParseUint(text, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(num), true
}

// Lookup returns the two-letter country code of an address, or an empty
// string when the address is not in the table.
func (geo *Geo) Lookup(addr netip.Addr) string {
	if len(geo.ranges) == 0 {
		return ""
	}
	addr = addr.Unmap()
	if !addr.Is4() {
		// The table holds IPv4 ranges only. An IPv6 client is not blocked.
		return ""
	}
	quad := addr.As4()
	value := uint32(quad[0])<<24 | uint32(quad[1])<<16 |
		uint32(quad[2])<<8 | uint32(quad[3])

	pos := sort.Search(len(geo.ranges), func(idx int) bool {
		return geo.ranges[idx].start > value
	})
	if pos == 0 {
		return ""
	}
	row := geo.ranges[pos-1]
	if value > row.end {
		return ""
	}
	return string(row.code[:])
}

// trusted reports whether the peer is one of the configured proxies.
func (geo *Geo) trusted(addr netip.Addr) bool {
	for _, prefix := range geo.proxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// ClientAddr returns the address of the client. When the peer is a trusted
// proxy, the first entry of X-Forwarded-For is used instead.
func (geo *Geo) ClientAddr(req *http.Request) netip.Addr {
	peer := peerAddr(req)
	if !geo.trusted(peer) {
		return peer
	}
	forwarded := req.Header.Get("X-Forwarded-For")
	if forwarded == "" {
		return peer
	}
	first := strings.TrimSpace(strings.Split(forwarded, ",")[0])
	addr, err := netip.ParseAddr(first)
	if err != nil {
		return peer
	}
	return addr.Unmap()
}

// Country returns the country of the request. A header from a trusted proxy
// has priority over the local table.
func (geo *Geo) Country(req *http.Request) string {
	peer := peerAddr(req)
	if geo.trusted(peer) {
		header := strings.ToUpper(strings.TrimSpace(req.Header.Get("CF-IPCountry")))
		if len(header) == 2 {
			return header
		}
	}
	return geo.Lookup(geo.ClientAddr(req))
}

func peerAddr(req *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}

// GeoBlock is the first middleware in the chain. It runs before the router
// and before every database access.
//
// The result is best effort only. VPN services and old allocation data make
// the country wrong for some clients. This is a traffic filter. It is not a
// security control.
func (app *App) GeoBlock(next http.Handler) http.Handler {
	return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		// The platform health check must never be blocked, because a
		// failed check stops the container.
		if req.URL.Path == "/healthz" {
			next.ServeHTTP(res, req)
			return
		}
		geo := app.GeoTable()
		if len(geo.blocked) == 0 {
			next.ServeHTTP(res, req)
			return
		}
		code := geo.Country(req)
		if code != "" && geo.blocked[code] {
			res.Header().Set("Content-Type", "text/plain; charset=utf-8")
			res.WriteHeader(http.StatusForbidden)
			io.WriteString(res, "Not available in your region.\n")
			return
		}
		next.ServeHTTP(res, req)
	})
}
