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
	// replyQ, when set, rewrites the question echoed in the reply.
	replyQ func(q dnsmessage.Question) dnsmessage.Question
	// foreignID answers with an ID that is never the query's.
	foreignID bool
	// strayFirst sends a reply with a foreign ID before the real one (UDP).
	strayFirst bool
	// udpOnly refuses TCP.
	udpOnly bool

	mu    sync.Mutex
	asked []string // "<name> <type> <udp|tcp>"
}

func startFakeDNS(t *testing.T, answer func(q dnsmessage.Question, tcp bool) (dnsmessage.Header, []dnsmessage.Resource), opts ...func(*fakeDNS)) *fakeDNS {
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
	for _, o := range opts {
		o(f)
	}
	if f.udpOnly {
		_ = ln.Close()
	}
	t.Cleanup(func() { _ = pc.Close(); _ = ln.Close() })

	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			out := f.reply(buf[:n], false)
			if out == nil {
				continue
			}
			if f.strayFirst {
				stray := slices.Clone(out)
				stray[0] ^= 0xff // another ID
				_, _ = pc.WriteTo(stray, from)
			}
			_, _ = pc.WriteTo(out, from)
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
	rh.ID = h.ID
	if f.foreignID {
		rh.ID = h.ID ^ 0xffff
	}
	rh.Response = true
	rh.RecursionDesired = h.RecursionDesired
	rh.RecursionAvailable = true
	b := dnsmessage.NewBuilder(nil, rh)
	b.EnableCompression()
	_ = b.StartQuestions()
	if f.replyQ != nil {
		_ = b.Question(f.replyQ(q))
	} else {
		_ = b.Question(q)
	}
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

func aaaaRR(owner string, ip [16]byte) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: name(owner), Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET, TTL: 60},
		Body:   &dnsmessage.AAAAResource{AAAA: ip},
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
		v6 := [16]byte{0x20, 0x01, 0x0d, 0xb8, 15: 7}
		return dnsmessage.Header{}, []dnsmessage.Resource{aaaaRR(q.Name.String(), v6)}
	})
	useResolvers(t, f.addr)

	addrs, queried := lookupFake(t, "localhost")
	if !queried || !slices.Equal(addrs, []string{"203.0.113.7", "2001:db8::7"}) {
		t.Fatalf("got %v queried=%v, want the public answer [203.0.113.7 2001:db8::7] (not /etc/hosts)", addrs, queried)
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
			name: "an AAAA failure doesn't hide the A records",
			answer: func(q dnsmessage.Question, _ bool) (dnsmessage.Header, []dnsmessage.Resource) {
				if q.Type == dnsmessage.TypeA {
					return dnsmessage.Header{}, []dnsmessage.Resource{aRR("www.example.test.", [4]byte{198, 51, 100, 9})}
				}
				return dnsmessage.Header{RCode: dnsmessage.RCodeServerFailure}, nil
			},
			want:    []string{"198.51.100.9"},
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

// A reply that isn't for our query (another ID, another question) is never
// used: with nothing else answering, the lookup is inconclusive.
func TestLookupHostExternalResult_IgnoresForeignReplies(t *testing.T) {
	answer := func(q dnsmessage.Question, _ bool) (dnsmessage.Header, []dnsmessage.Resource) {
		return dnsmessage.Header{}, []dnsmessage.Resource{aRR(q.Name.String(), [4]byte{192, 0, 2, 66})}
	}
	t.Run("another ID", func(t *testing.T) {
		f := startFakeDNS(t, answer, func(f *fakeDNS) { f.foreignID = true })
		useResolvers(t, f.addr)
		if addrs, queried := lookupFake(t, "www.example.test"); queried || len(addrs) != 0 {
			t.Fatalf("got %v queried=%v, want a reply with another ID ignored", addrs, queried)
		}
	})
	t.Run("another question", func(t *testing.T) {
		f := startFakeDNS(t, answer, func(f *fakeDNS) {
			f.replyQ = func(q dnsmessage.Question) dnsmessage.Question {
				q.Name = name("evil.example.test.")
				return q
			}
		})
		useResolvers(t, f.addr)
		if addrs, queried := lookupFake(t, "www.example.test"); queried || len(addrs) != 0 {
			t.Fatalf("got %v queried=%v, want a reply for another name ignored", addrs, queried)
		}
	})
}

// A stray datagram (another ID) before the real reply is skipped, not taken
// as the answer or as a failure.
func TestLookupHostExternalResult_SkipsStrayDatagram(t *testing.T) {
	f := startFakeDNS(t, func(q dnsmessage.Question, _ bool) (dnsmessage.Header, []dnsmessage.Resource) {
		if q.Type == dnsmessage.TypeA {
			return dnsmessage.Header{}, []dnsmessage.Resource{aRR(q.Name.String(), [4]byte{198, 51, 100, 8})}
		}
		return dnsmessage.Header{}, nil
	}, func(f *fakeDNS) { f.strayFirst, f.udpOnly = true, true })
	useResolvers(t, f.addr)
	if addrs, queried := lookupFake(t, "www.example.test"); !queried || !slices.Equal(addrs, []string{"198.51.100.8"}) {
		t.Fatalf("got %v queried=%v, want the real reply after the stray one", addrs, queried)
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
