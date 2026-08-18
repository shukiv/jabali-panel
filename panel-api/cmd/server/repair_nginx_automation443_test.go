package main

import (
	"strings"
	"testing"
)

// hostnameVhost is a trimmed jabali-default.conf-style fixture: the default_server
// block (whose location / uses the catch-all include) plus the GH#135 hostname
// block (whose location / uses the try_files landing). ensureAutomation443Include
// must inject into the hostname block only.
const hostnameVhost = `server {
    listen 443 ssl default_server;
    server_name _;
    location / {
        include /etc/nginx/jabali-catchall.conf;
    }
}

server {
    listen 443 ssl;
    server_name panel.example.com;
    root  /var/www/panel.example.com;
    include /etc/nginx/sites-available/includes/phpmyadmin.conf;
    location = /webmail  { return 301 https://mail.panel.example.com/; }

    location / {
        try_files $uri $uri/ =404;
    }

    location ~ \.php$ {
        try_files $uri =404;
    }
}
`

func TestEnsureAutomation443Include_InjectsAtHostnameLanding(t *testing.T) {
	out, changed := ensureAutomation443Include(hostnameVhost)
	if !changed {
		t.Fatal("expected changed=true for a vhost missing the include")
	}
	if !strings.Contains(out, automation443IncludeMarker) {
		t.Fatalf("include marker not injected:\n%s", out)
	}
	// The include must land immediately before the hostname block's landing
	// location (server scope), not inside the default_server catch-all block.
	incPos := strings.Index(out, auto443IncludeLine)
	anchorPos := strings.Index(out, auto443Anchor)
	if incPos < 0 || anchorPos < 0 || incPos > anchorPos {
		t.Fatalf("include not placed just before the hostname landing location (inc=%d anchor=%d):\n%s", incPos, anchorPos, out)
	}
	// It must sit AFTER the default_server catch-all include (i.e. not in the
	// first server block).
	catchallPos := strings.Index(out, "include /etc/nginx/jabali-catchall.conf;")
	if incPos < catchallPos {
		t.Fatalf("include wrongly injected into the default_server block (inc=%d catchall=%d)", incPos, catchallPos)
	}
}

func TestEnsureAutomation443Include_Idempotent(t *testing.T) {
	once, _ := ensureAutomation443Include(hostnameVhost)
	twice, changed := ensureAutomation443Include(once)
	if changed {
		t.Fatal("expected changed=false when the include is already present")
	}
	if once != twice {
		t.Fatal("second pass must not modify an already-healed config")
	}
}

func TestEnsureAutomation443Include_NoAnchorNoChange(t *testing.T) {
	// A vhost the healer doesn't recognize (no try_files landing) must be left
	// untouched rather than guessing an insertion point.
	unknown := "server {\n    server_name x;\n    location / { return 444; }\n}\n"
	out, changed := ensureAutomation443Include(unknown)
	if changed || out != unknown {
		t.Fatalf("unrecognized vhost must be left unchanged, changed=%v", changed)
	}
}
