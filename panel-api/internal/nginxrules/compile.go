// Package nginxrules compiles the structured NginxRule entries on a
// Domain into nginx directive strings that sit inside the server {}
// block. See the validator in internal/api for input constraints.
//
// Unknown rule types are silently skipped so an older panel build
// against a newer DB row never hard-fails nginx -t; the tradeoff is
// you won't see an error, just a missing directive.
package nginxrules

import (
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func Compile(d *models.Domain) string {
	if d == nil {
		return ""
	}
	rules := reverseProxyRules(d)
	if len(rules) == 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range rules {
		switch r.Type {
		case "custom_header":
			alwaysSuffix := ""
			if r.Always != nil && *r.Always {
				alwaysSuffix = " always"
			}
			fmt.Fprintf(&b, "    add_header %s %s%s;\n",
				r.Name, quoteNginxString(r.Value), alwaysSuffix)

		case "rewrite":
			flag := r.Flag
			if flag == "" {
				flag = "last"
			}
			fmt.Fprintf(&b, "    rewrite %s %s %s;\n",
				r.Pattern, quoteNginxString(r.Replacement), flag)

		case "proxy_pass":
			// Prepend ^~ so this location wins over regex matchers
			// (the per-vhost static-asset block at the bottom of the
			// template that 30d-caches *.css/*.js/etc.). Without it
			// /assets/foo.css goes to try_files in the empty docroot
			// instead of the upstream -- Gitea + every other proxied
			// app's bundled assets came back 404.
			//
			// Upload-safe defaults (GH#172): proxied apps (ownCloud,
			// dailytxt, ...) were failing file uploads because the
			// template carried none of the streaming/keepalive
			// directives. We bake the safe ones into EVERY proxy block:
			//   - proxy_http_version 1.1 + Connection "" -> upstream
			//     keepalive (instead of HTTP/1.0 close-per-request)
			//   - proxy_request_buffering off -> stream large bodies to
			//     the upstream instead of spooling to a temp file
			//   - default 300s connect/send/read timeouts so long
			//     uploads / slow apps don't 504
			// The request-body SIZE cap comes from the http{}-scope
			// client_max_body_size set by Server Settings -> Nginx
			// (default 50m); admins raise it there for big-upload apps.
			// proxy_buffering stays ON (default) -- disabling it is an
			// opt-in concern, not a safe default.
			fmt.Fprintf(&b,
				"    location ^~ %s {\n"+
					"        proxy_pass %s;\n"+
					"        proxy_http_version 1.1;\n"+
					"        proxy_set_header Host $host;\n"+
					"        proxy_set_header X-Real-IP $remote_addr;\n"+
					"        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n"+
					"        proxy_set_header X-Forwarded-Proto $scheme;\n"+
					"        proxy_request_buffering off;\n",
				quoteNginxLocation(r.Path), r.Target)
			if r.Websocket != nil && *r.Websocket {
				b.WriteString("        proxy_set_header Upgrade $http_upgrade;\n")
				b.WriteString("        proxy_set_header Connection \"upgrade\";\n")
			} else {
				// Empty Connection header lets nginx keep the upstream
				// connection alive across requests (HTTP/1.1 keepalive).
				b.WriteString("        proxy_set_header Connection \"\";\n")
			}
			readTimeout := r.ReadTimeout
			if readTimeout == "" {
				readTimeout = "300s"
			}
			b.WriteString("        proxy_connect_timeout 300s;\n")
			b.WriteString("        proxy_send_timeout 300s;\n")
			fmt.Fprintf(&b, "        proxy_read_timeout %s;\n", readTimeout)
			b.WriteString("    }\n")

		case "ip_access":
			b.WriteString("    location ")
			b.WriteString(quoteNginxLocation(r.Path))
			b.WriteString(" {\n")
			switch r.Mode {
			case "deny_list":
				for _, ip := range r.IPs {
					fmt.Fprintf(&b, "        deny %s;\n", ip)
				}
				b.WriteString("        allow all;\n")
			default: // allow_list
				for _, ip := range r.IPs {
					fmt.Fprintf(&b, "        allow %s;\n", ip)
				}
				b.WriteString("        deny all;\n")
			}
			b.WriteString("    }\n")

		case "php_setting":
			// Injected via fastcgi_param so it reaches PHP-FPM.
			fmt.Fprintf(&b,
				"    fastcgi_param PHP_VALUE %s;\n",
				quoteNginxString(r.Name+"="+r.Value))

		case "static_alias":
			// Serve a filesystem dir (framework STATIC_ROOT) directly from nginx.
			// ^~ so it beats the template's regex asset-cache block; the alias +
			// target are constrained to /home/ by the validator (no traversal /
			// system paths). Long-cache immutable static assets.
			fmt.Fprintf(&b,
				"    location ^~ %s {\n"+
					"        alias %s;\n"+
					"        access_log off;\n"+
					"        expires 30d;\n"+
					"        add_header Cache-Control \"public\";\n"+
					"    }\n",
				quoteNginxLocation(r.Path), r.Target)

		case "media_alias":
			// Serve user-uploaded media (Django MEDIA_ROOT) directly from nginx
			// (GH #878). Same constraints as static_alias, but a SHORT cache with
			// revalidation: unlike hashed/immutable static assets, an upload can
			// be replaced at the same URL, so a 30-day immutable cache would serve
			// stale files.
			fmt.Fprintf(&b,
				"    location ^~ %s {\n"+
					"        alias %s;\n"+
					"        access_log off;\n"+
					"        expires 1h;\n"+
					"        add_header Cache-Control \"public, must-revalidate\";\n"+
					"    }\n",
				quoteNginxLocation(r.Path), r.Target)

		case "max_upload_size":
			fmt.Fprintf(&b, "    client_max_body_size %s;\n", r.Size)
		}
	}
	return b.String()
}

// reverseProxyRules returns the domain's persisted NginxRules with the
// GH #1175 reverse-proxy entry synthesised in-memory when the domain
// carries a reverse_proxy_port. The synthesised rule is NEVER persisted:
// the NginxRules array is tenant-editable and goes through the JAB-65
// SSRF validator on every Rule Builder edit, so a stored loopback
// proxy_pass would either break subsequent tenant edits or become the
// exact seam an attacker probes to keep a 127.0.0.1 target while swapping
// the port. Re-deriving it here from the panel-owned column on every
// compile means it survives cert renewal and reconciler ticks by
// construction, and the tenant can neither delete nor repoint it.
//
// Defensive skip: if the tenant already has a root proxy_pass rule
// (path "/"), synthesising a second would emit a duplicate `location /`
// and fail nginx -t. The tenant's explicit rule wins; the create handler
// rejects the combination up front so this is belt-and-suspenders.
func reverseProxyRules(d *models.Domain) []models.NginxRule {
	if d.ReverseProxyPort == 0 {
		return d.NginxRules
	}
	for _, r := range d.NginxRules {
		if r.Type == "proxy_pass" && rootPath(r.Path) {
			return d.NginxRules
		}
	}
	ws := true
	synthetic := models.NginxRule{
		Type:      "proxy_pass",
		Path:      "/",
		Target:    fmt.Sprintf("http://127.0.0.1:%d", d.ReverseProxyPort),
		Websocket: &ws,
	}
	// Copy so append never mutates the domain's backing array.
	out := make([]models.NginxRule, 0, len(d.NginxRules)+1)
	out = append(out, d.NginxRules...)
	return append(out, synthetic)
}

// rootPath reports whether an nginx location path targets the site root.
func rootPath(p string) bool {
	p = strings.TrimSpace(p)
	return p == "/" || p == ""
}

func quoteNginxString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func quoteNginxLocation(s string) string {
	if strings.ContainsAny(s, " \t\"'\\") {
		return quoteNginxString(s)
	}
	return s
}
