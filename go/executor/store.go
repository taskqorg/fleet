package main

// This host's artifact store: registered with the coordinator through the outbox (the coordinator role's S3 key from the
// host-only key file); reachability and capacity go with each poll. Configuration: FLEET_STORE_CONFIG, a JSON file
// {id, zone, bucket, endpoint, localEndpoint, keyFile, capacityBytes, backingPath, requiresLogon}.

import (
	"fleet/contract"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func storeConfig() M {
	p := os.Getenv("FLEET_STORE_CONFIG")
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	v, err := dec(string(b))
	if err != nil {
		return nil
	}
	m, _ := v.(M)
	return m
}

func unquote(v string) string { return strings.Trim(strings.Trim(strings.TrimSpace(v), "'"), `"`) }

// readKey accepts JSON {accessKeyId, secretAccessKey}, env lines, `garage key info` output, or two lines.
func readKey(path string) (string, string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	text := string(b)
	if v, err := dec(text); err == nil {
		id, s := jStr(v, "accessKeyId"), jStr(v, "secretAccessKey")
		return id, s, jIsStr(v, "accessKeyId") && jIsStr(v, "secretAccessKey")
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
func effectiveCapacity(cfg M) *int64 {
	c, ok := capacity(cfg)
	if !ok {
		return nil
	}
	v := int64(min(c, uint64(math.MaxInt64)))
	return &v
}

func capacity(cfg M) (uint64, bool) {
	c, hasC := jU64(cfg, "capacityBytes")
	var f uint64
	hasF := false
	if p := jStr(cfg, "backingPath"); p != "" {
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
func storeReachable(cfg M) bool {
	u := jStr(cfg, "localEndpoint")
	if u == "" {
		return false
	}
	c := &http.Client{Timeout: 2 * time.Second, CheckRedirect: noRedirect}
	r, err := c.Get(u)
	if err != nil {
		return false
	}
	r.Body.Close()
	return true
}

func storeRegistration(cfg M, host string) *contract.StoresRequest {
	key, secret, ok := readKey(jStr(cfg, "keyFile"))
	if !ok {
		return nil
	}
	opt := func(k string) *string {
		if s, ok := jAt(cfg, k).(string); ok {
			return &s
		}
		return nil
	}
	logon := jBoolOr(false, cfg, "requiresLogon")
	return &contract.StoresRequest{HostId: host, Store: contract.StoreRegistration{Id: jStr(cfg, "id"), Zone: opt("zone"), Bucket: opt("bucket"),
		Endpoint: opt("endpoint"), LocalEndpoint: opt("localEndpoint"), AccessKeyId: &key, SecretAccessKey: &secret,
		CapacityBytes: effectiveCapacity(cfg), RequiresLogon: &logon}}
}

func storeReport(cfg M) []contract.StoreReport {
	return []contract.StoreReport{{Id: jStr(cfg, "id"), Reachable: storeReachable(cfg), CapacityBytes: effectiveCapacity(cfg)}}
}
