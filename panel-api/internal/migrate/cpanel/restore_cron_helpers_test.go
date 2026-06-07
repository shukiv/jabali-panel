package cpanel

import "testing"

func TestIsCronEnvLine(t *testing.T) {
	env := []string{
		`MAILTO=""`,
		`MAILTO="ops@example.com"`,
		`SHELL=/bin/bash`,
		`PATH=/usr/local/bin:/usr/bin:/bin`,
		`HOME=/home/user`,
		`FOO = bar`,
	}
	for _, l := range env {
		if !isCronEnvLine(l) {
			t.Errorf("isCronEnvLine(%q) = false, want true", l)
		}
	}
	jobs := []string{
		`*/5 * * * * php /home/u/public_html/wp-cron.php`,
		`@daily wp cron event run --path=/home/u/public_html`,
		`0 0 * * * curl https://example.com/wp-cron.php`,
		`# a comment`,
	}
	for _, l := range jobs {
		if isCronEnvLine(l) {
			t.Errorf("isCronEnvLine(%q) = true, want false", l)
		}
	}
}

func TestIsWPCronTrigger(t *testing.T) {
	yes := []string{
		`curl https://example.com/wp-cron.php`,
		`curl -s "https://example.com/wp-cron.php?doing_wp_cron"`,
		`wget -q -O - https://example.com/wp-cron.php`,
		`/usr/bin/curl https://www.site.org/wp-cron.php >/dev/null 2>&1`,
	}
	for _, c := range yes {
		if !isWPCronTrigger(c) {
			t.Errorf("isWPCronTrigger(%q) = false, want true", c)
		}
	}
	no := []string{
		`php /home/u/public_html/wp-cron.php`,        // already php — not a curl trigger
		`curl https://example.com/healthz.php`,        // not wp-cron
		`wp cron event run --path=/home/u/public_html`,
	}
	for _, c := range no {
		if isWPCronTrigger(c) {
			t.Errorf("isWPCronTrigger(%q) = true, want false", c)
		}
	}
}
