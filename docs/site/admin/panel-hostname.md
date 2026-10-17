# Panel Hostname

The FQDN the panel serves itself on. Set under Server Settings → General → **Panel Hostname**. The hostname drives several derived states:

- The Let's Encrypt certificate the panel runs on (see [Panel Certificate](./panel-certificate.md)).
- Stalwart's `HELO`, the `Message-ID` host, and the default `From:` for system mail.
- Bulwark's autoconfig / autodiscover endpoints (`autoconfig.<panel-hostname>`, `autodiscover.<panel-hostname>`).
- The displayed nameserver records (`ns1.<panel-hostname>`, `ns2.<panel-hostname>`) recommended in the [DNS Zones](./dns-zones.md) page.

## Initial choice

The installer prompts for the hostname at install time. The default is the current `hostname` command output. Pick the public FQDN that DNS will actually resolve to the panel's IP — once chosen, changing it later triggers cert reissuance and downstream config rewrites.

## Changing the hostname

1. Server Settings → General → Panel Hostname → enter the new FQDN. `jabali settings set hostname=<fqdn>` and claiming a free hostname run the same steps.
2. The agent:
   - Updates `/etc/hostname` and runs `hostnamectl set-hostname`.
   - Updates `/etc/hosts` the way the installer does: removes every `127.0.1.1` line and maps the new name to the server's public IPv4, unless a line already maps that exact name (an address you set yourself, for example behind NAT, is kept). The old name's lines stay, because the mail hostname does not follow a panel rename.
   - Sets `hostname` under `[server]` in `/etc/jabali/config.toml`, so `jabali update` keeps the new name in the login (Kratos) config, and the CLI uses it for SSO links and for the placeholder email of a user created without `--email`.
   - Re-points the `:443` landing vhost to the new name.
   - Re-renders the panel nginx vhost with the new `server_name`.
   - Calls `ssl.panel.issue` to obtain a Let's Encrypt cert for the new hostname.
   - Updates Stalwart's primary domain (M6.4).
   - Re-renders Bulwark's autoconfig endpoints.
   - Reloads nginx, panel API, Bulwark.
3. The page returns once the cert is issued (typically under 60 seconds).

## DNS prerequisites

The new hostname must resolve to one of the server's IPs before reissuance — Let's Encrypt's HTTP-01 challenge requires it. The page warns when the configured hostname does not resolve to a known panel IP, and refuses to start the change until the warning is acknowledged.

## RFC 6761 reserved TLD guard

Hostnames under reserved TLDs (`.local`, `.localhost`, `.test`, `.example`, `.invalid`, etc.) are accepted for development but skip Let's Encrypt issuance. The panel falls back to a self-signed certificate, and Stalwart is configured to refuse outbound mail under a reserved-TLD hostname (RFC 6761 compliance; prevents accidental leakage during testing). This guard was added after a Stalwart `Domain/set` crash-loop on `.local` test hostnames.

## Audit

Each change writes a structured audit row with the old and new hostnames.
