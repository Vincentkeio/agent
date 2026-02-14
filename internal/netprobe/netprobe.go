package netprobe

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"
)

type Result struct {
	Done       bool   `json:"done"`
	IPv4OK     bool   `json:"ipv4_ok"`
	PublicIPv4 string `json:"public_ipv4,omitempty"`
	IPv6OK     bool   `json:"ipv6_ok"`
	PublicIPv6 string `json:"public_ipv6,omitempty"`
	ProbeTS    int64  `json:"probe_ts"`
}

type ipifyResp struct {
	IP string `json:"ip"`
}

// Probe does a one-time connectivity + public IP check via ipify endpoints.
// - IPv4: https://api.ipify.org?format=json
// - IPv6: https://api6.ipify.org?format=json
func Probe(timeout time.Duration, insecureSkipVerify bool) Result {
	now := time.Now().Unix()
	r := Result{Done: true, ProbeTS: now}

	ip4, ok4 := fetchIP("tcp4", "https://api.ipify.org?format=json", timeout, insecureSkipVerify)
	r.IPv4OK = ok4
	r.PublicIPv4 = ip4

	ip6, ok6 := fetchIP("tcp6", "https://api6.ipify.org?format=json", timeout, insecureSkipVerify)
	r.IPv6OK = ok6
	r.PublicIPv6 = ip6

	return r
}

func fetchIP(network, url string, timeout time.Duration, insecureSkipVerify bool) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	dialer := &net.Dialer{Timeout: timeout}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecureSkipVerify},
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr) // force tcp4/tcp6
		},
	}
	client := &http.Client{Transport: tr}

	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}
	var out ipifyResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", false
	}
	if out.IP == "" {
		return "", false
	}
	// Basic sanity check: must parse as IP
	if net.ParseIP(out.IP) == nil {
		return "", false
	}
	// Extra: ensure family matches
	if network == "tcp4" && net.ParseIP(out.IP).To4() == nil {
		return "", false
	}
	if network == "tcp6" && net.ParseIP(out.IP).To4() != nil {
		return "", false
	}
	return out.IP, true
}

var ErrAuth = errors.New("auth failed")
