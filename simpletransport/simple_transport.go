package simpletransport

import (
	"bufio"
	"compress/gzip"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tsenart/tb"
)

// SimpleTransport is an HTTP RoundTripper that doesn't pool connections. Most of this is ripped from http.Transport.
type SimpleTransport struct {
	ReadTimeout       time.Duration
	ConnectionTimeout time.Duration

	// RequestTimeout isn't exact. In the worst case, the actual timeout can come at RequestTimeout * 2.
	RequestTimeout time.Duration
	totalTokens    int64
}

// ThrottleTransport is an HTTP RoundTripper that uses a SimpleTransport but throttles the requests.
type ThrottleTransport struct {
	SimpleTransport
	throttler *tb.Throttler
}

// ThrottleOptions are the options to create a new throttle transport
type ThrottleOptions struct {
	ThrottleRate      time.Duration
	ReadTimeout       time.Duration
	RequestTimeout    time.Duration
	ConnectionTimeout time.Duration
	TotalTokens       int64
}

// NewThrottleTransport setups and returns a ThrottleTransport
func NewThrottleTransport(opt *ThrottleOptions) *ThrottleTransport {
	if opt.ThrottleRate < time.Second {
		panic("Throttle rate cannot be less than a second")
	}

	s := SimpleTransport{
		ReadTimeout:       opt.ReadTimeout,
		RequestTimeout:    opt.RequestTimeout,
		ConnectionTimeout: opt.ConnectionTimeout,
		totalTokens:       opt.TotalTokens,
	}
	return &ThrottleTransport{
		throttler:       tb.NewThrottler(opt.ThrottleRate),
		SimpleTransport: s,
	}

}

func (t *ThrottleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.throttler.Wait("request", 1, t.totalTokens)
	return t.SimpleTransport.RoundTrip(req)
}

func (t *SimpleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	switch {
	case req.URL == nil:
		return nil, errors.New("http: nil Request.URL")
	case req.Header == nil:
		return nil, errors.New("http: nil Request.Header")
	case req.URL.Scheme != "http" && req.URL.Scheme != "https":
		return nil, errors.New("http: unsupported protocol scheme")
	case req.URL.Host == "":
		return nil, errors.New("http: no Host in request URL")
	}

	conn, err := t.dial(req)
	if err != nil {
		return nil, err
	}

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	readDone := make(chan responseAndError, 1)
	writeDone := make(chan error, 1)

	// Always request GZIP.
	req.Header.Set("Accept-Encoding", "gzip")

	// Write the request.
	go func() {
		err := req.Write(writer)

		if err == nil {
			writer.Flush()
		}

		writeDone <- err
	}()

	// And read the response.
	go func() {
		resp, err := http.ReadResponse(reader, req)
		if err != nil {
			readDone <- responseAndError{nil, err}
			return
		}

		resp.Body = &connCloser{resp.Body, conn}

		if resp.Header.Get("Content-Encoding") == "gzip" {
			resp.Header.Del("Content-Encoding")
			resp.Header.Del("Content-Length")
			resp.ContentLength = -1

			reader, err := gzip.NewReader(resp.Body)
			if err != nil {
				resp.Body.Close()
				readDone <- responseAndError{nil, err}
				return
			}

			resp.Body = &readerAndCloser{reader, resp.Body}

		}

		readDone <- responseAndError{resp, nil}
	}()

	if err = <-writeDone; err != nil {
		return nil, err
	}

	r := <-readDone

	if r.err != nil {
		return nil, r.err
	}

	return r.res, nil
}

func (t *SimpleTransport) dial(req *http.Request) (net.Conn, error) {
	targetAddr := canonicalAddr(req.URL)

	c, err := net.DialTimeout("tcp", targetAddr, t.ConnectionTimeout)
	if err != nil {
		return c, err
	}

	if t.RequestTimeout > 0 && t.ReadTimeout == 0 {
		t.ReadTimeout = t.RequestTimeout
	}

	if t.ReadTimeout > 0 {
		c = newDeadlineConn(c, t.ReadTimeout)

		if t.RequestTimeout > 0 {
			c = newTimeoutConn(c, t.RequestTimeout)
		}
	}

	if req.URL.Scheme == "https" {
		c = tls.Client(c, &tls.Config{ServerName: req.URL.Host})

		if err = c.(*tls.Conn).Handshake(); err != nil {
			return nil, err
		}

		if err = c.(*tls.Conn).VerifyHostname(req.URL.Host); err != nil {
			return nil, err
		}
	}

	return c, nil
}

// canonicalAddr returns url.Host but always with a ":port" suffix
func canonicalAddr(url *url.URL) string {
	addr := url.Host
	if !hasPort(addr) {
		if url.Scheme == "http" {
			return addr + ":80"
		}
		return addr + ":443"
	}

	return addr
}

func hasPort(s string) bool {
	return strings.LastIndex(s, ":") > strings.LastIndex(s, "]")
}

type readerAndCloser struct {
	io.Reader
	io.Closer
}

type responseAndError struct {
	res *http.Response
	err error
}

type connCloser struct {
	io.ReadCloser
	conn net.Conn
}

func (c *connCloser) Close() error {
	c.conn.Close()
	return c.ReadCloser.Close()
}

// A connection wrapper that times out after a period of time with no data sent.
type deadlineConn struct {
	net.Conn
	deadline time.Duration
}

func newDeadlineConn(conn net.Conn, deadline time.Duration) *deadlineConn {
	c := &deadlineConn{Conn: conn, deadline: deadline}
	conn.SetReadDeadline(time.Now().Add(deadline))
	return c
}

func (c *deadlineConn) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)
	if err != nil {
		return
	}

	c.Conn.SetReadDeadline(time.Now().Add(c.deadline))
	return
}

// A connection wrapper that times out after an absolute amount of time.
// Must wrap a deadlineConn or a hung connection may not trigger an error.
type timeoutConn struct {
	net.Conn
	timeout time.Time
}

func newTimeoutConn(conn net.Conn, timeout time.Duration) *timeoutConn {
	return &timeoutConn{Conn: conn, timeout: time.Now().Add(timeout)}
}

func (c *timeoutConn) Read(b []byte) (int, error) {
	if time.Now().After(c.timeout) {
		return 0, errors.New("connection timeout")
	}

	return c.Conn.Read(b)
}
