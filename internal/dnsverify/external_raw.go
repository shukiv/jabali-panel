package dnsverify

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// errTruncated marks a UDP answer with the TC bit: the caller asks again
// over TCP.
var errTruncated = errors.New("dns answer truncated")

// lookupHostDirect asks one DNS server for host's A and AAAA records with
// raw queries over proto ("udp" or "tcp"). A net.Resolver can't do this,
// even with a custom Dial: it answers a name found in /etc/hosts from the
// file without asking the server, and applies the resolv.conf search list
// (GH #2166). The answer here is the server's alone.
//
// NXDOMAIN returns a *net.DNSError with IsNotFound; a name with no A or AAAA
// returns no addresses and a nil error. Any other error (no connection,
// timeout, SERVFAIL, REFUSED, a truncated UDP answer, a reply that doesn't
// match the query) means this server and transport gave no usable answer.
func lookupHostDirect(ctx context.Context, server, proto, host string) ([]string, error) {
	fqdn := strings.TrimSpace(host)
	if fqdn == "" || fqdn == "." {
		return nil, errors.New("empty host")
	}
	if !strings.HasSuffix(fqdn, ".") {
		fqdn += "."
	}
	qname, err := dnsmessage.NewName(fqdn)
	if err != nil {
		return nil, err
	}
	var addrs []string
	for _, qtype := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		got, err := queryDirect(ctx, server, proto, qname, qtype)
		if err != nil {
			var derr *net.DNSError
			if errors.As(err, &derr) {
				derr.Name = host
			}
			return nil, err
		}
		addrs = append(addrs, got...)
	}
	return addrs, nil
}

// queryDirect sends one question to server and returns the addresses of
// qname, following the CNAME chain in the answer section.
func queryDirect(ctx context.Context, server, proto string, qname dnsmessage.Name, qtype dnsmessage.Type) ([]string, error) {
	var idb [2]byte
	if _, err := rand.Read(idb[:]); err != nil {
		return nil, err
	}
	id := binary.BigEndian.Uint16(idb[:])
	q := dnsmessage.Question{Name: qname, Type: qtype, Class: dnsmessage.ClassINET}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, RecursionDesired: true})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(q); err != nil {
		return nil, err
	}
	msg, err := b.Finish()
	if err != nil {
		return nil, err
	}

	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, proto, server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	if proto == "tcp" {
		resp, err := exchangeTCP(conn, msg)
		if err != nil {
			return nil, err
		}
		return parseAnswer(resp, id, q, server)
	}
	if _, err := conn.Write(msg); err != nil {
		return nil, err
	}
	buf := make([]byte, 65535)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		addrs, err := parseAnswer(buf[:n], id, q, server)
		if errors.Is(err, errMismatch) {
			continue // a stray or late datagram: keep waiting for ours
		}
		return addrs, err
	}
}

func exchangeTCP(conn net.Conn, msg []byte) ([]byte, error) {
	framed := make([]byte, 2+len(msg))
	binary.BigEndian.PutUint16(framed, uint16(len(msg)))
	copy(framed[2:], msg)
	if _, err := conn.Write(framed); err != nil {
		return nil, err
	}
	var n [2]byte
	if _, err := io.ReadFull(conn, n[:]); err != nil {
		return nil, err
	}
	resp := make([]byte, binary.BigEndian.Uint16(n[:]))
	if _, err := io.ReadFull(conn, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

var errMismatch = errors.New("dns reply does not match the query")

func parseAnswer(resp []byte, id uint16, q dnsmessage.Question, server string) ([]string, error) {
	var p dnsmessage.Parser
	h, err := p.Start(resp)
	if err != nil {
		return nil, err
	}
	if !h.Response || h.ID != id {
		return nil, errMismatch
	}
	qs, err := p.AllQuestions()
	if err != nil {
		return nil, err
	}
	if len(qs) != 1 || qs[0].Type != q.Type || qs[0].Class != q.Class ||
		!strings.EqualFold(qs[0].Name.String(), q.Name.String()) {
		return nil, errMismatch
	}
	if h.Truncated {
		return nil, errTruncated
	}
	switch h.RCode {
	case dnsmessage.RCodeSuccess:
	case dnsmessage.RCodeNameError:
		return nil, &net.DNSError{Err: "no such host", Server: server, IsNotFound: true}
	default:
		return nil, fmt.Errorf("dns server %s answered %s", server, h.RCode)
	}
	answers, err := p.AllAnswers()
	if err != nil {
		return nil, err
	}

	// The names that stand for qname: itself and every CNAME target reached
	// from it. A record for any other name in the reply is not used.
	names := map[string]bool{strings.ToLower(q.Name.String()): true}
	for range answers {
		grew := false
		for _, rr := range answers {
			c, ok := rr.Body.(*dnsmessage.CNAMEResource)
			if !ok || !names[strings.ToLower(rr.Header.Name.String())] {
				continue
			}
			if t := strings.ToLower(c.CNAME.String()); !names[t] {
				names[t] = true
				grew = true
			}
		}
		if !grew {
			break
		}
	}

	var addrs []string
	for _, rr := range answers {
		if !names[strings.ToLower(rr.Header.Name.String())] {
			continue
		}
		switch body := rr.Body.(type) {
		case *dnsmessage.AResource:
			if q.Type == dnsmessage.TypeA {
				addrs = append(addrs, net.IP(body.A[:]).String())
			}
		case *dnsmessage.AAAAResource:
			if q.Type == dnsmessage.TypeAAAA {
				addrs = append(addrs, net.IP(body.AAAA[:]).String())
			}
		}
	}
	return addrs, nil
}
