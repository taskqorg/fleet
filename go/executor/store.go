package main

// This host's artifact store: registered with the coordinator through the outbox (the coordinator role's S3 key from the
// host-only key file); reachability and capacity go with each poll. Configuration: FLEET_STORE_CONFIG, a JSON file
// {id, zone, bucket, endpoint, localEndpoint, keyFile, capacityBytes, backingPath, requiresLogon}.

import (
	"math"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"fleet/contract"
)

// storeFile: FLEET_STORE_CONFIG; a field of another type counts as absent.
type storeFile struct {
	Id            string  `json:"id"`
	Zone          *string `json:"zone"`
	Bucket        *string `json:"bucket"`
	Endpoint      *string `json:"endpoint"`
	LocalEndpoint *string `json:"localEndpoint"`
	KeyFile       string  `json:"keyFile"`
	CapacityBytes *uint64 `json:"capacityBytes"`
	BackingPath   string  `json:"backingPath"`
	RequiresLogon bool    `json:"requiresLogon"`
}

// storeConfig: nil without a config file that holds a JSON object.
func storeConfig() *storeFile {
	p := os.Getenv("FLEET_STORE_CONFIG")
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	v, err := dec(string(b))
	if _, ok := v.(M); err != nil || !ok {
		return nil
	}
	cfg := typed[storeFile](v)
	return &cfg
}

// endpoint: the store's public endpoint; "" without a config.
func (cfg *storeFile) endpoint() string {
	if cfg == nil {
		return ""
	}
	return str0(cfg.Endpoint)
}

func unquote(v string) string { return strings.Trim(strings.Trim(strings.TrimSpace(v), "'"), `"`) }

// readKey accepts JSON {accessKeyId, secretAccessKey}, env lines, `garage key info` output, or two lines.
func readKey(path string) (string, string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	text := string(b)
	if _, err := dec(text); err == nil {
		k := typedRaw[struct { // JSON that is not an object holds no key
			AccessKeyId     *string `json:"accessKeyId"`
			SecretAccessKey *string `json:"secretAccessKey"`
		}](b)
		return str0(k.AccessKeyId), str0(k.SecretAccessKey), k.AccessKeyId != nil && k.SecretAccessKey != nil
	}
	var id, secret *string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "export ")
		if v, ok := strings.CutPrefix(line, "Key ID:"); ok {
			v = strings.TrimSpace(v)
			id = &v
			continue
		}
		if v, ok := strings.CutPrefix(line, "Secret key:"); ok {
			v = strings.TrimSpace(v)
			secret = &v
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			v = unquote(v)
			if k == "AWS_ACCESS_KEY_ID" {
				id = &v
			} else if k == "AWS_SECRET_ACCESS_KEY" {
				secret = &v
			}
		}
	}
	if id == nil {
		var lines []string
		for _, l := range strings.Split(text, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
		if len(lines) == 2 {
			return lines[0], lines[1], true
		}
	}
	if id == nil || secret == nil {
		return "", "", false
	}
	return *id, *secret, true
}

func freeBytes(path string) (uint64, bool) {
	var s unix.Statfs_t
	if unix.Statfs(path, &s) != nil {
		return 0, false
	}
	return uint64(s.Bavail) * uint64(s.Bsize), true
}

// effectiveCapacity: the configured capacity bounded by the free space behind the store; nil when neither is known.
func effectiveCapacity(cfg *storeFile) *int64 {
	c, ok := capacity(cfg)
	if !ok {
		return nil
	}
	v := int64(min(c, uint64(math.MaxInt64)))
	return &v
}

func capacity(cfg *storeFile) (uint64, bool) {
	c, hasC := orU64(cfg.CapacityBytes, 0), cfg.CapacityBytes != nil
	var f uint64
	hasF := false
	if p := cfg.BackingPath; p != "" {
		f, hasF = freeBytes(p)
	}
	switch {
	case hasC && hasF:
		return min(c, f), true
	case hasC:
		return c, true
	case hasF:
		return f, true
	}
	return 0, false
}

// storeReachable: any HTTP answer from the loopback endpoint means the store process is up.
func storeReachable(cfg *storeFile) bool {
	u := str0(cfg.LocalEndpoint)
	if u == "" {
		return false
	}
	r, err := httpClient(2 * time.Second).Get(u)
	if err != nil {
		return false
	}
	r.Body.Close()
	return true
}

func storeRegistration(cfg *storeFile, host string) *contract.StoresRequest {
	key, secret, ok := readKey(cfg.KeyFile)
	if !ok {
		return nil
	}
	logon := cfg.RequiresLogon
	return &contract.StoresRequest{HostId: host, Store: contract.StoreRegistration{Id: cfg.Id, Zone: cfg.Zone, Bucket: cfg.Bucket,
		Endpoint: cfg.Endpoint, LocalEndpoint: cfg.LocalEndpoint, AccessKeyId: &key, SecretAccessKey: &secret,
		CapacityBytes: effectiveCapacity(cfg), RequiresLogon: &logon}}
}

func storeReport(cfg *storeFile) []contract.StoreReport {
	return []contract.StoreReport{{Id: cfg.Id, Reachable: storeReachable(cfg), CapacityBytes: effectiveCapacity(cfg)}}
}
