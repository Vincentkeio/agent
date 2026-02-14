package ws

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var (
	ErrBadHandshake = errors.New("websocket handshake failed")
)

type Conn struct {
	c  net.Conn
	br *bufio.Reader
	mu sync.Mutex
}

// Dial establishes a ws:// or wss:// client connection with a minimal RFC6455 implementation.
// Supports: Text frames, Ping/Pong, Close. Client->server frames are masked.
func Dial(ctx context.Context, rawURL string, insecureSkipVerify bool) (*Conn, *http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, err
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return nil, nil, fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}

	host := u.Host
	if !strings.Contains(host, ":") {
		if u.Scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	var d net.Dialer
	if deadline, ok := ctx.Deadline(); ok {
		d.Timeout = time.Until(deadline)
	} else {
		d.Timeout = 8 * time.Second
	}
	rawConn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, nil, err
	}

	var conn net.Conn = rawConn
	if u.Scheme == "wss" {
		tlsConn := tls.Client(rawConn, &tls.Config{
			ServerName:         stripPort(u.Host),
			InsecureSkipVerify: insecureSkipVerify,
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = rawConn.Close()
			return nil, nil, err
		}
		conn = tlsConn
	}

	keyRaw := make([]byte, 16)
	_, _ = rand.Read(keyRaw)
	key := base64.StdEncoding.EncodeToString(keyRaw)

	path := u.RequestURI()
	if path == "" {
		path = "/"
	}

	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nUser-Agent: kokoro-agent/0.1\r\n\r\n",
		path, stripPort(u.Host), key)

	if _, err := conn.Write([]byte(req)); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if resp.StatusCode != 101 {
		_ = conn.Close()
		return nil, resp, ErrBadHandshake
	}

	accept := resp.Header.Get("Sec-WebSocket-Accept")
	if accept == "" {
		_ = conn.Close()
		return nil, resp, ErrBadHandshake
	}
	want := computeAccept(key)
	if accept != want {
		_ = conn.Close()
		return nil, resp, ErrBadHandshake
	}

	return &Conn{c: conn, br: br}, resp, nil
}

func stripPort(host string) string {
	if i := strings.LastIndex(host, ":"); i > -1 && strings.Count(host, ":") == 1 {
		return host[:i]
	}
	// IPv6 host may include many colons; leave as-is
	return host
}

func computeAccept(key string) string {
	const magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	h := sha1.Sum([]byte(key + magic))
	return base64.StdEncoding.EncodeToString(h[:])
}

func (w *Conn) Close() error {
	return w.c.Close()
}

func (w *Conn) SetDeadline(t time.Time) error {
	return w.c.SetDeadline(t)
}

func (w *Conn) WriteText(payload []byte) error {
	return w.writeFrame(0x1, payload)
}

func (w *Conn) WritePing(payload []byte) error {
	return w.writeFrame(0x9, payload)
}

func (w *Conn) WritePong(payload []byte) error {
	return w.writeFrame(0xA, payload)
}

func (w *Conn) WriteClose(code uint16, reason string) error {
	b := make([]byte, 2+len(reason))
	b[0] = byte(code >> 8)
	b[1] = byte(code)
	copy(b[2:], []byte(reason))
	return w.writeFrame(0x8, b)
}

func (w *Conn) writeFrame(opcode byte, payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// client must mask
	maskKey := make([]byte, 4)
	_, _ = rand.Read(maskKey)

	header := make([]byte, 0, 14)
	finOpcode := byte(0x80) | (opcode & 0x0f)
	header = append(header, finOpcode)

	n := len(payload)
	switch {
	case n <= 125:
		header = append(header, byte(0x80|byte(n)))
	case n <= 65535:
		header = append(header, byte(0x80|126), byte(n>>8), byte(n))
	default:
		// 64-bit length
		header = append(header, byte(0x80|127))
		for i := 7; i >= 0; i-- {
			header = append(header, byte(uint64(n)>>(8*i)))
		}
	}
	header = append(header, maskKey...)

	masked := make([]byte, n)
	for i := 0; i < n; i++ {
		masked[i] = payload[i] ^ maskKey[i%4]
	}

	if _, err := w.c.Write(header); err != nil {
		return err
	}
	_, err := w.c.Write(masked)
	return err
}

// ReadMessage reads next data frame; it auto-replies to Ping with Pong.
// Returns opcode, payload.
func (w *Conn) ReadMessage() (byte, []byte, error) {
	for {
		op, payload, err := w.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case 0x9: // ping
			_ = w.WritePong(payload)
			continue
		case 0xA: // pong
			continue
		case 0x8: // close
			// reply close and exit
			_ = w.WriteClose(1000, "bye")
			return op, payload, io.EOF
		default:
			return op, payload, nil
		}
	}
}

func (w *Conn) readFrame() (byte, []byte, error) {
	b1, err := w.br.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	b2, err := w.br.ReadByte()
	if err != nil {
		return 0, nil, err
	}

	fin := (b1 & 0x80) != 0
	_ = fin // we only support FIN frames (no fragmentation)
	opcode := b1 & 0x0f

	masked := (b2 & 0x80) != 0
	length := int64(b2 & 0x7f)

	if length == 126 {
		var ext [2]byte
		if _, err := io.ReadFull(w.br, ext[:]); err != nil {
			return 0, nil, err
		}
		length = int64(ext[0])<<8 | int64(ext[1])
	} else if length == 127 {
		var ext [8]byte
		if _, err := io.ReadFull(w.br, ext[:]); err != nil {
			return 0, nil, err
		}
		var v uint64
		for i := 0; i < 8; i++ {
			v = (v << 8) | uint64(ext[i])
		}
		length = int64(v)
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(w.br, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}

	if length < 0 || length > 32*1024*1024 {
		return 0, nil, fmt.Errorf("frame too large: %d", length)
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(w.br, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := int64(0); i < length; i++ {
			payload[i] ^= maskKey[i%4]
		}
	}

	if !fin {
		// reject fragmented frames
		return 0, nil, errors.New("fragmented frames not supported")
	}

	return opcode, payload, nil
}
