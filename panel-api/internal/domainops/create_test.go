package domainops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// createStore is an in-memory CreateDomainStore.
type createStore struct {
	byName  map[string]*models.Domain
	created []*models.Domain
	shared  []string
}

func newCreateStore(existing ...*models.Domain) *createStore {
	s := &createStore{byName: map[string]*models.Domain{}}
	for _, d := range existing {
		s.byName[d.Name] = d
	}
	return s
}

func (s *createStore) FindByName(_ context.Context, name string) (*models.Domain, error) {
	if d, ok := s.byName[name]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}

func (s *createStore) FindStrictSubdomains(_ context.Context, name string) ([]models.Domain, error) {
	var out []models.Domain
	for n, d := range s.byName {
		if strings.HasSuffix(n, "."+name) {
			out = append(out, *d)
		}
	}
	return out, nil
}

func (s *createStore) CountByUserID(context.Context, string) (int64, error) { return 0, nil }

func (s *createStore) ListPreviewEnabled(context.Context) ([]models.Domain, error) {
	var out []models.Domain
	for _, d := range s.byName {
		if d.TempURLEnabled {
			out = append(out, *d)
		}
	}
	return out, nil
}

func (s *createStore) Create(_ context.Context, d *models.Domain) error {
	if _, ok := s.byName[d.Name]; ok {
		return repository.ErrConflict
	}
	s.byName[d.Name] = d
	s.created = append(s.created, d)
	return nil
}

func (s *createStore) SetSharedCertificate(_ context.Context, id string, certID *string, mode string) error {
	s.shared = append(s.shared, id+"|"+*certID+"|"+mode)
	return nil
}

type createOwners map[string]*models.User

func (o createOwners) FindByID(_ context.Context, id string) (*models.User, error) {
	if u, ok := o[id]; ok {
		return u, nil
	}
	return nil, repository.ErrNotFound
}

// hookLog records the post-create hooks in the order they ran.
type hookLog struct {
	calls   []string
	mailErr error
}

func (l *hookLog) hooks() CreateHooks {
	return CreateHooks{
		Schedule:  func(string) { l.calls = append(l.calls, "schedule") },
		InlineSSL: func(context.Context, *models.Domain) { l.calls = append(l.calls, "inline-ssl") },
		EnableMail: func(context.Context, *models.Domain) ([]string, error) {
			l.calls = append(l.calls, "enable-mail")
			return []string{"autoconfig CNAME exists"}, l.mailErr
		},
	}
}

func TestCreate(t *testing.T) {
	ctx := context.Background()
	uname := "alice"
	owner := &models.User{ID: "u1", Username: &uname}
	deps := func(s *createStore) CreateDeps {
		return CreateDeps{Domains: s, Users: createOwners{"u1": owner}}
	}
	base := CreateInput{OwnerID: "u1", Name: "shop.example.com"}

	t.Run("a full-service domain runs inline SSL, then mail, then the schedule", func(t *testing.T) {
		s, l := newCreateStore(), &hookLog{}
		res, err := Create(ctx, deps(s), l.hooks(), base)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if got := strings.Join(l.calls, ","); got != "inline-ssl,enable-mail,schedule" {
			t.Fatalf("hook order: %s", got)
		}
		d := res.Domain
		if len(s.created) != 1 || s.created[0] != d {
			t.Fatalf("want the returned domain stored, got %v", s.created)
		}
		if d.DocRoot != "/home/alice/domains/shop.example.com/public_html" || d.MailProvider != models.MailProviderJabali ||
			!d.EmailEnabled || d.SSLMode != models.SSLModeLE {
			t.Fatalf("unexpected stored domain: %+v", d)
		}
		if len(res.MailWarnings) != 1 || res.MailErr != nil {
			t.Fatalf("want the mail warnings surfaced, got %v %v", res.MailWarnings, res.MailErr)
		}
	})

	t.Run("an attached shared certificate replaces the inline SSL attempt", func(t *testing.T) {
		s, l := newCreateStore(), &hookLog{}
		d := deps(s)
		wild := `["*.example.com"]`
		d.SharedCerts = &fakeCertLister{certs: []models.SharedCertificate{{ID: "c1", SANs: &wild}}}
		res, err := Create(ctx, d, l.hooks(), base)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if got := strings.Join(l.calls, ","); got != "schedule,enable-mail,schedule" {
			t.Fatalf("hook order: %s", got)
		}
		if res.SharedCert == nil || res.SharedCertErr != nil || res.Domain.SSLMode != models.SSLModeShared {
			t.Fatalf("want c1 attached, got %v %v %s", res.SharedCert, res.SharedCertErr, res.Domain.SSLMode)
		}
	})

	t.Run("a failed shared-certificate lookup is soft and inline SSL still runs", func(t *testing.T) {
		s, l := newCreateStore(), &hookLog{}
		d := deps(s)
		d.SharedCerts = &fakeCertLister{err: errors.New("db down")}
		res, err := Create(ctx, d, l.hooks(), base)
		if err != nil || !errors.Is(res.SharedCertErr, ErrSharedCertLookup) {
			t.Fatalf("want a soft lookup error, got %v %v", err, res)
		}
		if l.calls[0] != "inline-ssl" {
			t.Fatalf("hook order: %v", l.calls)
		}
	})

	t.Run("a mail-only domain gets no inline SSL; a domain without Jabali mail gets no enable", func(t *testing.T) {
		s, l := newCreateStore(), &hookLog{}
		in := base
		in.WebDisabled = true
		if _, err := Create(ctx, deps(s), l.hooks(), in); err != nil {
			t.Fatalf("create: %v", err)
		}
		if got := strings.Join(l.calls, ","); got != "enable-mail,schedule" {
			t.Fatalf("mail-only hook order: %s", got)
		}

		s, l = newCreateStore(), &hookLog{}
		in = base
		in.MailProvider = models.MailProviderNone
		if _, err := Create(ctx, deps(s), l.hooks(), in); err != nil {
			t.Fatalf("create: %v", err)
		}
		if got := strings.Join(l.calls, ","); got != "inline-ssl,schedule" {
			t.Fatalf("no-mail hook order: %s", got)
		}
	})

	t.Run("a failed mail enable is soft: the row keeps email_enabled for the reconciler", func(t *testing.T) {
		s, l := newCreateStore(), &hookLog{mailErr: errors.New("stalwart down")}
		res, err := Create(ctx, deps(s), l.hooks(), base)
		if err != nil || res.MailErr == nil || !res.Domain.EmailEnabled || len(s.created) != 1 {
			t.Fatalf("want a stored domain and a soft mail error, got %v %+v", err, res)
		}
		if l.calls[len(l.calls)-1] != "schedule" {
			t.Fatalf("the schedule must still run after a failed enable: %v", l.calls)
		}
	})

	t.Run("nil hooks are skipped", func(t *testing.T) {
		if _, err := Create(ctx, deps(newCreateStore()), CreateHooks{}, base); err != nil {
			t.Fatalf("create: %v", err)
		}
	})

	t.Run("a preview-slug conflict releases the reserved port and stores nothing", func(t *testing.T) {
		s := newCreateStore(&models.Domain{ID: "d0", Name: "shop-example.com", UserID: "u1", TempURLEnabled: true})
		ports := newFakePorts()
		d := deps(s)
		d.Ports = ports
		in := base
		in.ReverseProxy, in.TempURLEnabled = true, true
		_, err := Create(ctx, d, CreateHooks{}, in)
		var slug *PreviewSlugConflictError
		if !errors.As(err, &slug) || slug.Other != "shop-example.com" {
			t.Fatalf("want a slug conflict with shop-example.com, got %v", err)
		}
		if len(ports.held) != 0 || len(ports.releaseKinds) != 1 || len(s.created) != 0 {
			t.Fatalf("want the port released and nothing stored, got held=%v released=%v created=%d",
				ports.held, ports.releaseKinds, len(s.created))
		}
	})

	t.Run("a tenant is held to the domain's own tree; the error names both", func(t *testing.T) {
		in := base
		in.DocRoot = "/home/alice/other"
		_, err := Create(ctx, deps(newCreateStore()), CreateHooks{}, in)
		var dr *DocRootError
		if !errors.As(err, &dr) || !errors.Is(err, ErrDocRootOutsideDomain) || dr.Username != "alice" || dr.DomainName != "shop.example.com" {
			t.Fatalf("want a DocRootError(outside domain, alice, shop.example.com), got %v", err)
		}
		in.ActorIsAdmin = true
		if _, err := Create(ctx, deps(newCreateStore()), CreateHooks{}, in); err != nil {
			t.Fatalf("an admin may use anywhere under the owner's home: %v", err)
		}
	})

	t.Run("an admin actor skips the cross-tenant guard; a tenant does not", func(t *testing.T) {
		victim := &models.Domain{ID: "dv", Name: "example.com", UserID: "u-victim"}
		if _, err := Create(ctx, deps(newCreateStore(victim)), CreateHooks{}, base); !errors.Is(err, ErrDomainConflictsTenant) {
			t.Fatalf("tenant: want ErrDomainConflictsTenant, got %v", err)
		}
		in := base
		in.ActorIsAdmin = true
		if _, err := Create(ctx, deps(newCreateStore(victim)), CreateHooks{}, in); err != nil {
			t.Fatalf("admin: %v", err)
		}
	})

	t.Run("a web template is admin-only and fails closed without a store", func(t *testing.T) {
		in := base
		in.WebTemplateID = "t1"
		if _, err := Create(ctx, deps(newCreateStore()), CreateHooks{}, in); !errors.Is(err, ErrWebTemplateAdminOnly) {
			t.Fatalf("tenant: want ErrWebTemplateAdminOnly, got %v", err)
		}
		in.ActorIsAdmin = true
		if _, err := Create(ctx, deps(newCreateStore()), CreateHooks{}, in); !errors.Is(err, ErrWebTemplatesUnavailable) {
			t.Fatalf("admin without a store: want ErrWebTemplatesUnavailable, got %v", err)
		}
	})

	t.Run("missing required stores are a wiring error", func(t *testing.T) {
		if _, err := Create(ctx, CreateDeps{Users: createOwners{}}, CreateHooks{}, base); !errors.Is(err, ErrCreateDeps) {
			t.Fatalf("want ErrCreateDeps, got %v", err)
		}
	})
}
