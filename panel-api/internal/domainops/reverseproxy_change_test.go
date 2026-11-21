package domainops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakePortSetter struct {
	err   error
	calls []int
}

func (f *fakePortSetter) SetReverseProxyPort(_ context.Context, _ string, port int) error {
	f.calls = append(f.calls, port)
	return f.err
}

func proxyDomain() *models.Domain {
	return &models.Domain{ID: "d1", Name: "app.example", ReverseProxyPort: 3000}
}

// GH #2544: a reverse-proxy domain moves to another port, through the same
// gates as a port chosen at create.
func TestChangeReverseProxyPort(t *testing.T) {
	ctx := context.Background()

	t.Run("moves to a free port", func(t *testing.T) {
		set, ag := &fakePortSetter{}, &fakeProbe{resp: `{"bound":false}`}
		dom := proxyDomain()
		if err := ChangeReverseProxyPort(ctx, set, ag, dom, 5000); err != nil {
			t.Fatalf("change: %v", err)
		}
		if len(set.calls) != 1 || set.calls[0] != 5000 || dom.ReverseProxyPort != 5000 {
			t.Fatalf("calls=%v port=%d, want one set to 5000", set.calls, dom.ReverseProxyPort)
		}
		if len(ag.calls) != 1 {
			t.Fatalf("want the system-uid probe, got %v", ag.calls)
		}
	})

	t.Run("the current port is a no-op", func(t *testing.T) {
		set, ag := &fakePortSetter{}, &fakeProbe{resp: `{"bound":false}`}
		if err := ChangeReverseProxyPort(ctx, set, ag, proxyDomain(), 3000); err != nil {
			t.Fatalf("change: %v", err)
		}
		if len(set.calls) != 0 || len(ag.calls) != 0 {
			t.Fatalf("no-op wrote or probed: set=%v probe=%v", set.calls, ag.calls)
		}
	})

	t.Run("a docroot domain refuses", func(t *testing.T) {
		set := &fakePortSetter{}
		dom := proxyDomain()
		dom.ReverseProxyPort = 0
		if err := ChangeReverseProxyPort(ctx, set, nil, dom, 5000); !errors.Is(err, ErrNotReverseProxy) {
			t.Fatalf("err = %v, want ErrNotReverseProxy", err)
		}
		if len(set.calls) != 0 {
			t.Fatalf("wrote on a refusal: %v", set.calls)
		}
	})

	t.Run("a port the policy forbids refuses before anything", func(t *testing.T) {
		for _, port := range []int{0, 80, 8443, 3306, 30001, 40050, 70000} {
			set, ag := &fakePortSetter{}, &fakeProbe{resp: `{"bound":false}`}
			err := ChangeReverseProxyPort(ctx, set, ag, proxyDomain(), port)
			if !errors.Is(err, ErrReverseProxyPortInvalid) {
				t.Fatalf("port %d: err = %v, want ErrReverseProxyPortInvalid", port, err)
			}
			if len(set.calls) != 0 || len(ag.calls) != 0 {
				t.Fatalf("port %d: wrote or probed on a refusal", port)
			}
		}
	})

	t.Run("a system service on the port refuses", func(t *testing.T) {
		set, ag := &fakePortSetter{}, &fakeProbe{resp: `{"bound":true,"uid":0}`}
		err := ChangeReverseProxyPort(ctx, set, ag, proxyDomain(), 5000)
		if !errors.Is(err, ErrReverseProxyPortSystemBound) {
			t.Fatalf("err = %v, want ErrReverseProxyPortSystemBound", err)
		}
		if len(set.calls) != 0 {
			t.Fatalf("wrote on a refusal: %v", set.calls)
		}
	})

	t.Run("another domain's port refuses and the domain keeps its port", func(t *testing.T) {
		set := &fakePortSetter{err: repository.ErrPortInUse}
		dom := proxyDomain()
		err := ChangeReverseProxyPort(ctx, set, &fakeProbe{resp: `{"bound":false}`}, dom, 5000)
		if !errors.Is(err, ErrReverseProxyPortInUse) {
			t.Fatalf("err = %v, want ErrReverseProxyPortInUse", err)
		}
		if dom.ReverseProxyPort != 3000 {
			t.Fatalf("port = %d after a refusal, want 3000", dom.ReverseProxyPort)
		}
	})

	t.Run("the row turning out not to be a proxy refuses", func(t *testing.T) {
		set := &fakePortSetter{err: repository.ErrNotReverseProxy}
		err := ChangeReverseProxyPort(ctx, set, nil, proxyDomain(), 5000)
		if !errors.Is(err, ErrNotReverseProxy) {
			t.Fatalf("err = %v, want ErrNotReverseProxy", err)
		}
	})
}
