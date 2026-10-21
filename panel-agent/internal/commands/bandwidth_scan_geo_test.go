package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// goaccessGeoFixture is goaccess 1.8.1 `-o json --geoip-database=…` output,
// trimmed to the keys the scan reads (shape captured from a real log).
const goaccessGeoFixture = `{"general": {"total_requests": 1443, "valid_requests": 903, "bandwidth": 4631606},
"geolocation": {"metadata": {}, "data": [
 {"hits": {"count": 489}, "bytes": {"count": 2658828}, "data": "NA North America", "items": [
  {"hits": {"count": 489, "percent": "54.15"}, "visitors": {"count": 6}, "bytes": {"count": 2658828}, "data": "US United States"}]},
 {"hits": {"count": 236}, "bytes": {"count": 1156458}, "data": "EU Europe", "items": [
  {"hits": {"count": 233}, "bytes": {"count": 1144054}, "data": "BE Belgium"},
  {"hits": {"count": 2}, "bytes": {"count": 6650}, "data": "DE Germany"}]},
 {"hits": {"count": 5}, "bytes": {"count": 500}, "data": "Unknown", "items": [
  {"hits": {"count": 5}, "bytes": {"count": 500}, "data": "Unknown"}]}
]}}`

func TestParseGoaccessScan_TotalsAndCountries(t *testing.T) {
	st, err := parseGoaccessScan([]byte(goaccessGeoFixture))
	if err != nil {
		t.Fatal(err)
	}
	if st.BytesTotal != 4631606 || st.RequestsTotal != 1443 {
		t.Fatalf("totals = %d bytes / %d requests, want 4631606 / 1443", st.BytesTotal, st.RequestsTotal)
	}
	want := []bandwidthCountryStats{
		{Code: "US", RequestsTotal: 489, BytesTotal: 2658828},
		{Code: "BE", RequestsTotal: 233, BytesTotal: 1144054},
		{Code: "DE", RequestsTotal: 2, BytesTotal: 6650},
		{Code: "ZZ", RequestsTotal: 5, BytesTotal: 500},
	}
	if !slices.Equal(st.Countries, want) {
		t.Fatalf("countries = %+v\nwant        %+v", st.Countries, want)
	}
}

// Without the geolocation panel (no GeoIP database) there are no countries,
// and the totals are read as before.
func TestParseGoaccessScan_NoGeolocation(t *testing.T) {
	st, err := parseGoaccessScan([]byte(`{"general": {"total_requests": "12", "bandwidth": "3400"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if st.RequestsTotal != 12 || st.BytesTotal != 3400 || st.Countries != nil {
		t.Fatalf("got %+v, want 12 requests, 3400 bytes, no countries", st)
	}
}

// More countries than maxScanCountries: the busiest are kept, the rest are
// added to ZZ with the unknown addresses, and no traffic is lost.
func TestCountryStats_CapsAndFoldsIntoZZ(t *testing.T) {
	var items []goaccessGeoItem
	var wantReq uint64
	for i := 0; i < 40; i++ {
		code := string(rune('A'+i/26)) + string(rune('A'+i%26))
		items = append(items, geoItem(code+" Somewhere", uint64(100+i), 10))
		wantReq += uint64(100 + i)
	}
	items = append(items, geoItem("Unknown", 7, 1))
	wantReq += 7

	got := countryStats(items)
	if len(got) != maxScanCountries {
		t.Fatalf("kept %d countries, want %d", len(got), maxScanCountries)
	}
	last := got[len(got)-1]
	if last.Code != unknownCountry {
		t.Fatalf("last entry is %q, want %q", last.Code, unknownCountry)
	}
	var sum uint64
	for i, c := range got {
		sum += c.RequestsTotal
		if i > 0 && i < len(got)-1 && c.RequestsTotal > got[i-1].RequestsTotal {
			t.Fatalf("not sorted by requests: %+v", got)
		}
	}
	if sum != wantReq {
		t.Fatalf("requests sum to %d after folding, want %d", sum, wantReq)
	}
	// The 29 busiest (139 down to 111) are kept by name.
	if got[0].RequestsTotal != 139 || got[maxScanCountries-2].RequestsTotal != 111 {
		t.Fatalf("kept the wrong countries: first %+v, last named %+v", got[0], got[maxScanCountries-2])
	}
}

func TestGeoCountryCode(t *testing.T) {
	for label, want := range map[string]string{
		"DE Germany":       "DE",
		"US United States": "US",
		"Unknown":          "ZZ",
		"":                 "ZZ",
		"de germany":       "ZZ",
		"EU":               "ZZ",
		"A1 Anonymous":     "ZZ",
	} {
		if got := geoCountryCode(label); got != want {
			t.Errorf("geoCountryCode(%q) = %q, want %q", label, got, want)
		}
	}
}

// The scan passes the GeoIP database only when it exists, and a goaccess
// that refuses it still yields the day's totals.
func TestBandwidthScanDay_GeoIPDatabase(t *testing.T) {
	for _, tc := range []struct {
		name        string
		dbExists    bool
		refuseGeoIP bool
		wantGeoArg  bool
		wantCountry bool
	}{
		{name: "database present", dbExists: true, wantGeoArg: true, wantCountry: true},
		{name: "no database", dbExists: false, wantGeoArg: false, wantCountry: false},
		{name: "goaccess without GeoIP support", dbExists: true, refuseGeoIP: true, wantGeoArg: true, wantCountry: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			mustWriteFile(t, filepath.Join(dir, "example.com-access.log.1"), []byte("x\n"))
			db := filepath.Join(t.TempDir(), "GeoLite2-City.mmdb")
			if tc.dbExists {
				mustWriteFile(t, db, []byte("mmdb"))
			}
			prevDB := geoIPCityDB
			geoIPCityDB = db
			t.Cleanup(func() { geoIPCityDB = prevDB })

			sawGeoArg := false
			prev := execCommandContext
			execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				geo := slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "--geoip-database=") })
				sawGeoArg = sawGeoArg || geo
				if geo && tc.refuseGeoIP {
					return exec.CommandContext(ctx, "sh", "-c", "echo 'unrecognized option' >&2; exit 1")
				}
				out := `{"general": {"total_requests": 9, "bandwidth": 900}}`
				if geo {
					out = goaccessGeoFixture
				}
				return exec.CommandContext(ctx, "printf", "%s", out)
			}
			t.Cleanup(func() { execCommandContext = prev })

			params, _ := json.Marshal(bandwidthScanDayParams{LogDir: dir})
			got, err := bandwidthScanDayHandler(context.Background(), params)
			if err != nil {
				t.Fatal(err)
			}
			resp := got.(bandwidthScanDayResponse)
			if len(resp.Stats) != 1 {
				t.Fatalf("stats = %+v (skipped %v), want one domain", resp.Stats, resp.Skipped)
			}
			st := resp.Stats[0]
			if sawGeoArg != tc.wantGeoArg {
				t.Errorf("--geoip-database passed = %v, want %v", sawGeoArg, tc.wantGeoArg)
			}
			if (len(st.Countries) > 0) != tc.wantCountry {
				t.Errorf("countries = %+v, want present=%v", st.Countries, tc.wantCountry)
			}
			if st.Domain != "example.com" || st.RequestsTotal == 0 {
				t.Errorf("stats = %+v, want example.com with its totals", st)
			}
		})
	}
}

func geoItem(label string, hits, bytes uint64) goaccessGeoItem {
	var it goaccessGeoItem
	it.Data = label
	it.Hits.Count = json.Number(fmt.Sprint(hits))
	it.Bytes.Count = json.Number(fmt.Sprint(bytes))
	return it
}

// The panel's side of the bandwidth.scan_day contract: the reply the panel's
// bandwidth ticker decodes is what this handler returns, every field kept.
func TestBandwidthScanDay_PanelContract(t *testing.T) {
	raw, err := os.ReadFile("../../../panel-api/internal/agent/testdata/bandwidth_scan_day_response.json")
	if err != nil {
		t.Fatal(err)
	}
	var resp bandwidthScanDayResponse
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(resp)
	var got, want any
	_ = json.Unmarshal(again, &got)
	_ = json.Unmarshal(raw, &want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the agent drops or renames fields:\nwant %s\ngot  %s", raw, again)
	}
}
