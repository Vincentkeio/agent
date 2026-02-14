package tcpping

import (
	"context"
	"net"
	"time"
)

type Target struct {
	ID       string `json:"id,omitempty"`
	Province string `json:"province,omitempty"`
	Carrier  string `json:"carrier,omitempty"` // telecom/mobile/unicom
	IPVer    int    `json:"ip_ver,omitempty"`  // 4/6/0
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Label    string `json:"label,omitempty"`
	TimeoutMS int   `json:"timeout_ms,omitempty"`
}

type Sample struct {
	ID       string `json:"id,omitempty"`
	Province string `json:"province,omitempty"`
	Carrier  string `json:"carrier,omitempty"`
	IPVer    int    `json:"ip_ver,omitempty"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Label    string `json:"label,omitempty"`

	OK    bool   `json:"ok"`
	RTTMS int64  `json:"rtt_ms,omitempty"`
	Err   string `json:"err,omitempty"`
}

func Ping(ctx context.Context, t Target) Sample {
	s := Sample{
		ID: t.ID, Province: t.Province, Carrier: t.Carrier, IPVer: t.IPVer,
		Host: t.Host, Port: t.Port, Label: t.Label,
	}

	timeout := time.Duration(t.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}

	network := "tcp"
	if t.IPVer == 4 {
		network = "tcp4"
	} else if t.IPVer == 6 {
		network = "tcp6"
	}

	addr := net.JoinHostPort(t.Host, itoa(t.Port))

	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		s.OK = false
		s.Err = shortErr(err)
		return s
	}
	_ = conn.Close()
	s.OK = true
	s.RTTMS = time.Since(start).Milliseconds()
	return s
}

func itoa(i int) string {
	// tiny int->string without fmt
	if i == 0 {
		return "0"
	}
	sign := false
	if i < 0 {
		sign = true
		i = -i
	}
	var b [16]byte
	n := len(b)
	for i > 0 && n > 0 {
		n--
		b[n] = byte('0' + (i % 10))
		i /= 10
	}
	if sign && n > 0 {
		n--
		b[n] = '-'
	}
	return string(b[n:])
}

func shortErr(err error) string {
	// keep it short for DB/UI
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return "timeout"
	}
	msg := err.Error()
	// common patterns
	if contains(msg, "refused") {
		return "refused"
	}
	if contains(msg, "no route") {
		return "noroute"
	}
	if contains(msg, "network is unreachable") {
		return "unreach"
	}
	if contains(msg, "i/o timeout") {
		return "timeout"
	}
	if contains(msg, "too many open files") {
		return "fdlimit"
	}
	return "error"
}

func contains(s, sub string) bool {
	// strings.Contains without importing strings (keeps deps tiny)
	n := len(sub)
	if n == 0 {
		return true
	}
	for i := 0; i+n <= len(s); i++ {
		if s[i:i+n] == sub {
			return true
		}
	}
	return false
}
