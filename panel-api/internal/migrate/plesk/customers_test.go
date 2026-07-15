package plesk

import (
	"context"
	"testing"
)

func TestParsePleskSizeMB(t *testing.T) {
	cases := map[string]uint32{
		"unlimited":  0,
		"-1":         0,
		"":           0,
		"0":          0,
		"500M":       500,
		"500 MB":     500,
		"1G":         1024,
		"1 GB":       1024,
		"2T":         2 * 1024 * 1024,
		"1048576":    1,    // bytes → MB
		"1073741824": 1024, // 1 GiB in bytes → MB
		"2048K":      2,    // KB → MB
	}
	for in, want := range cases {
		if got := parsePleskSizeMB(in); got != want {
			t.Errorf("parsePleskSizeMB(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParsePleskCount(t *testing.T) {
	cases := map[string]uint32{"unlimited": 0, "-1": 0, "": 0, "10": 10, "x": 0}
	for in, want := range cases {
		if got := parsePleskCount(in); got != want {
			t.Errorf("parsePleskCount(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestPlanToHostingPackage_FieldMap(t *testing.T) {
	p := PleskPlan{
		Name: "Business", DiskMB: 10240, BandwidthMB: 102400,
		MaxDomains: 25, MaxMailboxes: 100, MaxDatabases: 50,
	}
	pkg := PlanToHostingPackage(p)
	if pkg.Name != "Business" {
		t.Errorf("name = %q", pkg.Name)
	}
	if pkg.DiskQuotaMB != 10240 || pkg.BandwidthQuotaMB != 102400 {
		t.Errorf("size fields wrong: %+v", pkg)
	}
	if pkg.MaxDomains != 25 || pkg.MaxEmailAccounts != 100 || pkg.MaxDatabases != 50 {
		t.Errorf("count fields wrong: %+v", pkg)
	}
	// jabali-safe defaults: app allowances off, ID unset (assigned at create).
	if pkg.MaxDockerApps != 0 || pkg.MaxPythonApps != 0 {
		t.Errorf("app allowances must default to 0: %+v", pkg)
	}
	if pkg.ID != "" {
		t.Errorf("ID must be assigned at create time, not by the mapper: %q", pkg.ID)
	}
}

func TestCustomerToUser_NoPasswordSanitizedUsername(t *testing.T) {
	u := CustomerToUser(PleskCustomer{
		Login: "John.Doe@Example.com", ContactName: "John Doe",
		Email: "jd@example.com", Suspended: true,
	})
	if u.Username != "johndoe" {
		// '@Example.com' stripped; '.' is not in jabali's username charset
		// so it is stripped too → "johndoe".
		t.Errorf("username = %q, want johndoe", u.Username)
	}
	if u.Email != "jd@example.com" || u.DisplayName != "John Doe" || !u.Suspended {
		t.Errorf("draft = %+v", u)
	}
	// Security: the draft type has no password field at all — nothing to
	// leak. (Compile-time guarantee; this test documents the intent.)
}

func TestSanitizeUsername(t *testing.T) {
	cases := map[string]string{
		"john.doe@example.com": "johndoe",   // '.' not in charset → stripped
		"Weird User!":          "weirduser", // space + '!' stripped, lowered
		"ok_name-1":            "ok_name-1",
	}
	for in, want := range cases {
		if got := sanitizeUsername(in); got != want {
			t.Errorf("sanitizeUsername(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiscoverTenants_ParsesPlansAndCustomers(t *testing.T) {
	d := New()
	s := fixtureSession(map[string]string{
		"SELECT name FROM Templates": "Unlimited\nBusiness\n",
		// real service_plan --info: column-aligned (no colon).
		"service_plan --info 'Unlimited'": "Disk space                Unlimited\nTraffic                   Unlimited\nDomains                   Unlimited\n",
		"service_plan --info 'Business'":  "Disk space                10240M\nTraffic                   100G\nDomains                   25\nMailboxes                 100\nDatabases                 50\n",
		"SELECT login FROM clients":       "jdoe\nacme\n",
		"customer --info 'jdoe'":          "Contact name:  John Doe\nEmail:  jd@example.com\nStatus:  active\n",
		"customer --info 'acme'":          "Company:  ACME Ltd\nEmail:  ops@acme.com\nSuspended:  true\nReseller:  bigres\n",
	})
	ts, err := d.DiscoverTenants(context.Background(), s)
	if err != nil {
		t.Fatalf("DiscoverTenants: %v", err)
	}
	if len(ts.Plans) != 2 || len(ts.Customers) != 2 {
		t.Fatalf("got %d plans / %d customers, want 2/2", len(ts.Plans), len(ts.Customers))
	}
	// Business plan field mapping.
	var biz PleskPlan
	for _, p := range ts.Plans {
		if p.Name == "Business" {
			biz = p
		}
	}
	if biz.DiskMB != 10240 || biz.BandwidthMB != 100*1024 || biz.MaxDomains != 25 || biz.MaxMailboxes != 100 || biz.MaxDatabases != 50 {
		t.Errorf("Business plan mapped wrong: %+v", biz)
	}
	// acme customer: suspended + reseller-owned.
	var acme PleskCustomer
	for _, c := range ts.Customers {
		if c.Login == "acme" {
			acme = c
		}
	}
	if !acme.Suspended || acme.Reseller != "bigres" || acme.Company != "ACME Ltd" {
		t.Errorf("acme customer parsed wrong: %+v", acme)
	}
}

func TestAuthoritativeDomains(t *testing.T) {
	d := New()
	s := fixtureSession(map[string]string{
		// keyed on the SELECT (quote-free substring of the escaped command).
		"SELECT name FROM domains WHERE name=": "lychee-fruits.co.il\naddon.example.net\n",
	})
	doms, err := d.AuthoritativeDomains(context.Background(), s, "lychee-fruits.co.il")
	if err != nil {
		t.Fatalf("AuthoritativeDomains: %v", err)
	}
	if len(doms) != 2 || doms[0] != "lychee-fruits.co.il" || doms[1] != "addon.example.net" {
		t.Errorf("domains = %v", doms)
	}
}
