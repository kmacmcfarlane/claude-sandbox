package clipboard

import (
	"bufio"
	"io"
	"net"
	"strings"
	"time"
)

// Client is one request to a helper: the request is sent at dial, then the
// admission line and the result line are read separately, so the shim can
// write OSC 52 in between (CS-LNCH-199).
type Client struct {
	c     net.Conn
	br    *bufio.Reader
	start time.Time
	total time.Duration
}

// Dial connects within ShimConnect and sends the request and its payload.
func Dial(sock string, req Request, data []byte, total time.Duration) (*Client, error) {
	start := time.Now()
	c, err := net.DialTimeout("unix", sock, ShimConnect)
	if err != nil {
		return nil, err
	}
	c.SetDeadline(start.Add(total))
	if _, err := io.WriteString(c, FormatRequest(req)); err != nil {
		c.Close()
		return nil, err
	}
	if _, err := c.Write(data); err != nil {
		c.Close()
		return nil, err
	}
	return &Client{c: c, br: bufio.NewReader(c), start: start, total: total}, nil
}

// Admission reads the admission line within ShimAdmission.
func (cl *Client) Admission() (string, error) {
	cl.c.SetReadDeadline(minTime(time.Now().Add(ShimAdmission), cl.start.Add(cl.total)))
	line, err := cl.br.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// Result reads the result line within what is left of the total.
func (cl *Client) Result() (string, error) {
	cl.c.SetReadDeadline(cl.start.Add(cl.total))
	line, err := cl.br.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// Close closes the connection.
func (cl *Client) Close() { cl.c.Close() }

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Exchange is one whole round trip. adm is "" when the helper was
// unreachable or did not admit in time (err set).
func Exchange(sock string, req Request, data []byte, total time.Duration) (adm, res string, err error) {
	cl, err := Dial(sock, req, data, total)
	if err != nil {
		return "", "", err
	}
	defer cl.Close()
	adm, err = cl.Admission()
	if err != nil || adm != AnswerAccepted {
		return adm, "", err
	}
	res, err = cl.Result()
	return adm, res, err
}

// Ping asks a helper whether it serves (the attach/join health check,
// CS-LNCH-197): no token, no backend.
func Ping(sock string, timeout time.Duration) bool {
	adm, res, err := Exchange(sock, Request{Op: OpPing}, nil, timeout)
	return err == nil && adm == AnswerAccepted && res == AnswerOK
}
