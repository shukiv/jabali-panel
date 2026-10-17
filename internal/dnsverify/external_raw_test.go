package dnsverify

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// fakeDNS is a tiny DNS server on 127.0.0.1 (UDP and TCP on one port). It
// records every question and answers from answer().
type fakeDNS struct {
	addr   string
	answer func(q dnsmessage.Question, tcp bool) (dnsmessage.Header, []dnsmessage.Resource)

	mu    sync.Mutex
	asked []string // "<name> <type> <udp|tcp>"
}

func startFakeDNS(t *testing.T, answer func(q dnsmessage.Question, tcp bool) (dnsmessage.Header, []dnsmessage.Resource)) *fakeDNS {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", pc.LocalAddr().String())
	if err != nil {
		_ = pc.Close()
		t.Skipf("tcp port taken: %v", err)
	}
	f := &fakeDNS{addr: pc.LocalAddr().String(), answer: answer}
	t.Cleanup(func() { _ = pc.Close(); _ = ln.Close() })

	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if out := f.reply(buf[:n], false); out != nil {
				_, _ = pc.WriteTo(out, from)
			}
		}
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					var n uint16
					if err := binary.Read(r, binary.BigEndian, &n); err != nil {
						return
					}
					msg := make([]byte, n)
					if _, err := io.ReadFull(r, msg); err != nil {
						return
					}
					out := f.reply(msg, true)
					if out == nil {
						return
					}
					_ = binary.Write(c, binary.BigEndian, uint16(len(out)))
					_, _ = c.Write(out)
				}
			}(c)
		}
	}()
	return f
}

func (f *fakeDNS) reply(req []byte, tcp bool) []byte {
	var p dnsmessage.Parser
	h, err := p.Start(req)
	if err != nil {
		return nil
	}
	q, err := p.Question()
	if err != nil {
		return nil
	}
	proto := "udp"
	if tcp {
		proto = "tcp"
	}
	f.mu.Lock()
	f.asked = append(f.asked, q.Name.String()+" "+q.Type.String()+" "+proto)
	f.mu.Unlock()

	rh, answers := f.answer(q, tcp)
	if rh.ID == 0 {
		rh.ID = h.ID
	}
	rh.Response = true
	rh.RecursionDesired = h.RecursionDesired
	rh.RecursionAvailable = true
	b := dnsmessage.NewBuilder(nil, rh)
	b.EnableCompression()
	_ = b.StartQuestions()
	_ = b.Question(q)
	_ = b.StartAnswers()
	for _, rr := range answers {
		switch body := rr.Body.(type) {
		case *dnsmessage.AResource:
			_ = b.AResource(rr.Header, *body)
		case *dnsmessage.AAAAResource:
			_ = b.AAAAResource(rr.Header, *body)
		case *dnsmessage.CNAMEResource:
			_ = b.CNAMEResource(rr.Header, *body)
		}
	}
	out, err := b.Finish()
	if err != nil {
		return nil
	}
	return out
}

func (f *fakeDNS) questions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

func useResolvers(t *testing.T, addrs ...string) {
	t.Helper()
	old := externalResolvers
	externalResolvers = addrs
	t.Cleanup(func() { externalResolvers = old })
}

func name(s string) dnsmessage.Name { return dnsmessage.MustNewName(s) }

func aRR(owner string, ip [4]byte) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: name(owner), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
		Body:   &dnsmessage.AResource{A: ip},
	}
}

func cnameRR(owner, target string) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: name(owner), Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET, TTL: 60},
		Body:   &dnsmessage.CNAMEResource{CNAME: name(target)},
	}
}

func lookupFake(t *testing.T, host string) ([]string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return LookupHostExternalResult(ctx, host)
}

// GH #2166. A name in /etc/hosts was answered from /etc/hosts: Go's resolver
// reads the hosts file before it dials the server, so "public DNS" saw
// Debian's 127.0.1.1 line (or install.sh's own public-IP lines) instead of
// what Let's Encrypt sees. localhost is in every /etc/hosts; the public
// answer here is the fake server's.
func TestLookupHostExternalResult_IgnoresEtcHosts(t *testing.T) {
	hosts, err := os.ReadFile("/etc/hosts")
	if err != nil || !strings.Contains(string(hosts), "localhost") {
		t.Skip("needs localhost in /etc/hosts")
	}
	f := startFakeDNS(t, func(q dnsmessage.Question, _ bool) (dnsmessage.Header, []dnsmessage.Resource) {
		if q.Type == dnsmessage.TypeA {
			return dnsmessage.Header{}, []dnsmessage.Resource{aRR(q.Name.String(), [4]byte{203, 0, 113, 7})}
		}
		return dnsmessage.Header{}, nil // AAAA: no data
	})
	useResolvers(t, f.addr)

	addrs, queried := lookupFake(t, "localhost")
	if !queried || !slices.Equal(addrs, []string{"203.0.113.7"}) {
		t.Fatalf("got %v queried=%v, want the public answer [203.0.113.7] (not /etc/hosts)", addrs, queried)
	}
	if !slices.Contains(f.questions(), "localhost. TypeA udp") {
		t.Fatalf("the public resolver was never asked: %v", f.questions())
	}
}

func TestLookupHostExternalResult_Answers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  func(q dnsmessage.Question, tcp bool) (dnsmessage.Header, []dnsmessage.Resource)
		want    []string
		queried bool
	}{
		{
			name: "NXDOMAIN is a definitive empty answer",
			answer: func(dnsmessage.Question, bool) (dnsmessage.Header, []dnsmessage.Resource) {
				return dnsmessage.Header{RCode: dnsmessage.RCodeNameError}, nil
			},
			queried: true,
		},
		{
			name: "no A or AAAA is a definitive empty answer",
			answer: func(dnsmessage.Question, bool) (dnsmessage.Header, []dnsmessage.Resource) {
				return dnsmessage.Header{}, nil
			},
			queried: true,
		},
		{
			name: "a CNAME chain is followed, unrelated records are not used",
			answer: func(q dnsmessage.Question, _ bool) (dnsmessage.Header, []dnsmessage.Resource) {
				if q.Type != dnsmessage.TypeA {
					return dnsmessage.Header{}, []dnsmessage.Resource{cnameRR("www.example.test.", "edge.example.test.")}
				}
				return dnsmessage.Header{}, []dnsmessage.Resource{
					cnameRR("www.example.test.", "edge.example.test."),
					aRR("edge.example.test.", [4]byte{198, 51, 100, 4}),
					aRR("other.example.test.", [4]byte{192, 0, 2, 99}),
				}
			},
			want:    []string{"198.51.100.4"},
			queried: true,
		},
		{
			name: "a truncated UDP answer is asked again over TCP",
			answer: func(q dnsmessage.Question, tcp bool) (dnsmessage.Header, []dnsmessage.Resource) {
				if !tcp {
					return dnsmessage.Header{Truncated: true}, nil
				}
				if q.Type == dnsmessage.TypeA {
					return dnsmessage.Header{}, []dnsmessage.Resource{aRR("www.example.test.", [4]byte{198, 51, 100, 5})}
				}
				return dnsmessage.Header{}, nil
			},
			want:    []string{"198.51.100.5"},
			queried: true,
		},
		{
			name: "SERVFAIL everywhere is inconclusive",
			answer: func(dnsmessage.Question, bool) (dnsmessage.Header, []dnsmessage.Resource) {
				return dnsmessage.Header{RCode: dnsmessage.RCodeServerFailure}, nil
			},
			queried: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startFakeDNS(t, tc.answer)
			useResolvers(t, f.addr)
			addrs, queried := lookupFake(t, "www.example.test")
			if queried != tc.queried || !slices.Equal(addrs, tc.want) {
				t.Fatalf("got %v queried=%v, want %v queried=%v (asked %v)", addrs, queried, tc.want, tc.queried, f.questions())
			}
		})
	}
}

// The next resolver is asked when one fails; nothing listening anywhere is
// inconclusive, not "no record".
func TestLookupHostExternalResult_ResolverFallback(t *testing.T) {
	bad := startFakeDNS(t, func(dnsmessage.Question, bool) (dnsmessage.Header, []dnsmessage.Resource) {
		return dnsmessage.Header{RCode: dnsmessage.RCodeRefused}, nil
	})
	good := startFakeDNS(t, func(q dnsmessage.Question, _ bool) (dnsmessage.Header, []dnsmessage.Resource) {
		if q.Type == dnsmessage.TypeA {
			return dnsmessage.Header{}, []dnsmessage.Resource{aRR(q.Name.String(), [4]byte{198, 51, 100, 6})}
		}
		return dnsmessage.Header{}, nil
	})
	useResolvers(t, bad.addr, good.addr)
	if addrs, queried := lookupFake(t, "www.example.test"); !queried || !slices.Equal(addrs, []string{"198.51.100.6"}) {
		t.Fatalf("got %v queried=%v, want the second resolver's answer", addrs, queried)
	}

	// A port with nothing on it: refused over UDP and TCP.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := pc.LocalAddr().String()
	_ = pc.Close()
	useResolvers(t, closed)
	if addrs, queried := lookupFake(t, "www.example.test"); queried || len(addrs) != 0 {
		t.Fatalf("got %v queried=%v, want nothing and inconclusive", addrs, queried)
	}
}
