package commands

import "testing"

// The acquis is the applied truth for bot detection: a stale panel binary can
// drop the jabali-appsec header, but install.sh's plural-guard keeps the
// composed acquis. botModeFromAcquisBody recovers the mode (and threshold)
// from it so the agent's readback never lies "off" while crowdsec challenges.
func TestBotModeFromAcquisBody(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"singular acquis is off", "appsec_config: crowdsecurity/jabali-appsec\nsource: appsec\n", "off"},
		{"empty is off", "", "off"},
		{
			"plural balanced",
			"appsec_configs:\n - crowdsecurity/jabali-appsec\n - crowdsecurity/appsec-bot-challenge-scoring\n - crowdsecurity/appsec-bot-challenge-scoring-balanced\n",
			"balanced",
		},
		{
			"plural permissive",
			"appsec_configs:\n - crowdsecurity/jabali-appsec\n - crowdsecurity/appsec-bot-challenge-scoring\n - crowdsecurity/appsec-bot-challenge-scoring-permissive\n",
			"permissive",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := botModeFromAcquisBody(c.body); got != c.want {
				t.Fatalf("botModeFromAcquisBody = %q, want %q", got, c.want)
			}
		})
	}
}

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		v        string
		maj, min int
		want     bool
	}{
		{"v1.8.0-debian-pragmatic-amd64-cc76dbbc", 1, 8, true},
		{"1.7.8", 1, 8, false},
		{"v1.2.2", 1, 2, true},
		{"1.1.6", 1, 2, false},
		{"v2.0.0", 1, 8, true},
		{"garbage", 1, 8, false}, // unparseable → fail-closed
		{"", 1, 8, false},
	}
	for _, c := range cases {
		if got := versionAtLeast(c.v, c.maj, c.min); got != c.want {
			t.Errorf("versionAtLeast(%q, %d, %d) = %v, want %v", c.v, c.maj, c.min, got, c.want)
		}
	}
}

func TestFilterThreshold(t *testing.T) {
	leaves := []string{
		"crowdsecurity/appsec-bot-challenge-exclude-api",
		"crowdsecurity/appsec-bot-challenge-scoring",
		"crowdsecurity/appsec-bot-challenge-scoring-balanced",
		"crowdsecurity/appsec-bot-challenge-scoring-permissive",
	}
	bal := filterThreshold(leaves, "balanced")
	for _, n := range bal {
		if n == "crowdsecurity/appsec-bot-challenge-scoring-permissive" {
			t.Fatal("balanced kept the permissive scoring config")
		}
	}
	perm := filterThreshold(leaves, "permissive")
	for _, n := range perm {
		if n == "crowdsecurity/appsec-bot-challenge-scoring-balanced" {
			t.Fatal("permissive kept the balanced scoring config")
		}
	}
}
