package contract

import (
	"errors"
	"maps"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Fleet's production browser binding uses the existing site-agents Access policy.
const (
	ProductionBrowserProject = "fleet"
	ProductionBrowserOrigin  = "https://fleet.ufobe.com"
	BrowserAccessBytes       = 4096
	SiteAccessRefresh        = 10 * time.Minute
	SiteAccessEvidenceAge    = SiteAccessRefresh + time.Minute
)

// BrowserBindingProduction is the file configuration. Header values are env
// names here and are resolved only by the trusted process that owns the file.
type BrowserBindingProduction struct {
	Origin         string                 `json:"origin"`
	AccessFile     string                 `json:"accessFile"`
	Headers        map[string]string      `json:"headers"`
	ReadinessProbe BrowserProductionProbe `json:"readinessProbe"`
}

var (
	browserHTTPToken = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
	browserEnvName   = regexp.MustCompile("^[A-Za-z_][A-Za-z0-9_]*$")
)

func browserHeaderAllowed(name string) bool {
	if !browserHTTPToken.MatchString(name) {
		return false
	}
	name = strings.ToLower(name)
	if strings.HasPrefix(name, "sec-") || strings.HasPrefix(name, "proxy-") {
		return false
	}
	switch name {
	case "host", "cookie", "cookie2", "set-cookie", "origin", "referer", "connection", "content-length",
		"keep-alive", "transfer-encoding", "te", "trailer", "upgrade", "expect", "content-type":
		return false
	}
	return true
}

func browserProbeValid(p BrowserProductionProbe) bool {
	if !strings.HasPrefix(p.Path, "/") || strings.HasPrefix(p.Path, "//") || strings.ContainsAny(p.Path, "\\?#") {
		return false
	}
	u, err := url.ParseRequestURI(p.Path)
	if err != nil || u.Host != "" {
		return false
	}
	if !browserHTTPToken.MatchString(p.Method) || strings.ToUpper(p.Method) != p.Method {
		return false
	}
	switch p.Method {
	case "CONNECT", "TRACE", "TRACK":
		return false
	}
	hasIdentity := p.IdentityField != nil && p.ExpectedIdentity != nil && *p.IdentityField != "" && *p.ExpectedIdentity != ""
	if (p.IdentityField != nil || p.ExpectedIdentity != nil) && !hasIdentity {
		return false
	}
	if p.Status == nil {
		return hasIdentity
	}
	return *p.Status >= 200 && *p.Status <= 599
}

func browserOriginValid(origin string) bool {
	return origin != ProductionBrowserOrigin && ValidHTTPSBrowserOrigin(origin)
}

// ValidHTTPSBrowserOrigin accepts only canonical HTTPS origins, including Fleet itself.
func ValidHTTPSBrowserOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || origin != "https://"+u.Host ||
		u.Host != strings.ToLower(u.Host) || strings.HasSuffix(u.Host, ":") {
		return false
	}
	if strings.ContainsAny(u.Host, "%\\") || !browserHostnameValid(u.Hostname()) {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		return err == nil && n != 0 && n != 443 && port == strconv.FormatUint(n, 10)
	}
	return true
}

var browserDNSLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func browserHostnameValid(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Zone() == "" && ip.String() == host
	}
	// WHATWG parses hosts whose last component is numeric as IPv4, including hex and octal.
	host = strings.TrimSuffix(host, ".")
	last := host[strings.LastIndex(host, ".")+1:]
	hex, hasHex := strings.CutPrefix(last, "0x")
	if len(last) > 0 && (strings.Trim(last, "0123456789") == "" || hasHex && strings.Trim(hex, "0123456789abcdef") == "") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if !browserDNSLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// ValidBrowserProduction is checked on both sides of the private host channel.
func ValidBrowserProduction(p *BrowserProductionAccess) bool {
	return p != nil && browserOriginValid(p.Origin) && len(p.Headers) > 0 && ValidSiteBrowserProduction(p)
}

// ValidSiteBrowserProduction checks the private browser transport for an approved site target.
func ValidSiteBrowserProduction(p *BrowserProductionAccess) bool {
	if p == nil || !ValidHTTPSBrowserOrigin(p.Origin) || !browserProbeValid(p.ReadinessProbe) {
		return false
	}
	seen := make(map[string]bool, len(p.Headers))
	for _, h := range p.Headers {
		name := strings.ToLower(h.Name)
		if !browserHeaderAllowed(h.Name) || seen[name] || h.Value == "" ||
			strings.ContainsFunc(h.Value, func(r rune) bool { return r < ' ' || r == 127 }) {
			return false
		}
		seen[name] = true
	}
	return true
}

func (p BrowserBindingProduction) Resolve(env string) (*BrowserProductionAccess, error) {
	invalid := errors.New("browser production binding or credentials invalid")
	grant, err := p.ResolveSite(env)
	if err != nil || !ValidBrowserProduction(grant) {
		return nil, invalid
	}
	return grant, nil
}

// ResolveSite reads literal values for a managed site grant. The coordinator verifies the provider and scope.
func (p BrowserBindingProduction) ResolveSite(env string) (*BrowserProductionAccess, error) {
	invalid := errors.New("browser production binding or credentials invalid")
	values := map[string]string{}
	for _, line := range strings.Split(env, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "export\t") {
			line = strings.TrimLeft(line[len("export"):], " \t")
		}
		if key, value, ok := strings.Cut(line, "="); ok {
			if _, seen := values[key]; !seen {
				if len(value) >= 2 && (value[0] == '\'' || value[0] == '"') && value[len(value)-1] == value[0] {
					value = value[1 : len(value)-1]
				}
				values[key] = value
			}
		}
	}
	grant := &BrowserProductionAccess{Origin: p.Origin, ReadinessProbe: p.ReadinessProbe, Headers: []BrowserSecretHeader{}}
	for _, name := range slices.Sorted(maps.Keys(p.Headers)) {
		key := p.Headers[name]
		if !browserEnvName.MatchString(key) {
			return nil, invalid
		}
		grant.Headers = append(grant.Headers, BrowserSecretHeader{Name: name, Value: values[key]})
	}
	if !ValidSiteBrowserProduction(grant) {
		return nil, invalid
	}
	return grant, nil
}
