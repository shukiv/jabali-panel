#!/usr/bin/env bash
# jabalihosted.com service deploy (JAB-213). Idempotent; run as root on the
# service box (hostsclick / 182.54.236.100). Implements the go-live ordering
# from plans/jab213-free-hostname-service.md — the sequence is load-bearing:
#
#   0. disk precondition   (DNS on a full disk dies at 3am)
#   1. pdns + FULL zone    (incl. _psl TXT — the PSL PR attestation MUST
#                           survive the NS cutover; SPF/DMARC — a PSL-listed
#                           base is a spoofing magnet)
#   2. service binary + unit + nginx vhost
#   3. glue + NS switch at the registrar   (MANUAL/API step, printed at end)
#   4. public _psl verify → LE cert for api vhost → live
#
# Secrets: /etc/jabalihosted/svc.env must exist (0600) with PDNS_API_KEY +
# SMTP_* before the service starts; this script generates PDNS_API_KEY if
# the file is absent and leaves SMTP_* placeholders to fill in.
set -euo pipefail

BASE=jabalihosted.com
SVC_IP=182.54.236.100
PSL_PR_URL="https://github.com/publicsuffix/list/pull/3127"
NEED_FREE_GB=10

say() { echo "==> $*"; }
die() { echo "FATAL: $*" >&2; exit 1; }

# ---- 0. disk gate -----------------------------------------------------------
free_gb=$(df --output=avail -BG / | tail -1 | tr -dc 0-9)
if (( free_gb < NEED_FREE_GB )); then
  die "only ${free_gb}GB free on / — clean the box below ${NEED_FREE_GB}GB and rerun (fleet DNS does not go on a full disk)"
fi

# ---- 1. pdns + zone ---------------------------------------------------------
say "installing PowerDNS (Debian native) + sqlite backend"
apt-get install -y -qq pdns-server pdns-backend-sqlite3 >/dev/null

install -d -m 0755 /var/lib/powerdns
if [[ ! -s /var/lib/powerdns/pdns.sqlite3 ]]; then
  sqlite3 /var/lib/powerdns/pdns.sqlite3 < /usr/share/pdns-backend-sqlite3/schema/schema.sqlite3.sql
  chown pdns:pdns /var/lib/powerdns/pdns.sqlite3
fi

if [[ ! -f /etc/jabalihosted/svc.env ]]; then
  install -d -m 0755 /etc/jabalihosted
  PDNS_KEY=$(head -c 32 /dev/urandom | sha256sum | cut -c1-48)
  cat > /etc/jabalihosted/svc.env <<EOF
PDNS_API_KEY=${PDNS_KEY}
SMTP_ADDR=mail.reeva.me:587
SMTP_HOST=mail.reeva.me
SMTP_FROM=FILL-ME-IN
SMTP_PASSWORD=FILL-ME-IN
EOF
  chmod 0600 /etc/jabalihosted/svc.env
  say "wrote /etc/jabalihosted/svc.env — FILL IN SMTP_FROM/SMTP_PASSWORD"
fi
# shellcheck disable=SC1091
source /etc/jabalihosted/svc.env

cat > /etc/powerdns/pdns.d/jabalihosted.conf <<EOF
launch=gsqlite3
gsqlite3-database=/var/lib/powerdns/pdns.sqlite3
api=yes
api-key=${PDNS_API_KEY}
webserver=yes
webserver-address=127.0.0.1
webserver-port=8081
local-address=0.0.0.0:53, [::]:53
EOF
systemctl enable --now pdns
systemctl restart pdns

say "bootstrapping zone ${BASE} (idempotent)"
PD="curl -s -H X-API-Key:${PDNS_API_KEY} http://127.0.0.1:8081/api/v1/servers/localhost"
if ! $PD/zones/${BASE}. >/dev/null 2>&1 || $PD/zones/${BASE}. | grep -q "Not Found"; then
  curl -s -X POST -H "X-API-Key:${PDNS_API_KEY}" -H "Content-Type: application/json" \
    http://127.0.0.1:8081/api/v1/servers/localhost/zones -d "{
      \"name\": \"${BASE}.\", \"kind\": \"Native\",
      \"nameservers\": [\"ns1.${BASE}.\", \"ns2.${BASE}.\"]
    }" >/dev/null
fi

# Full base rrsets. _psl MUST be here BEFORE any NS cutover (PSL attestation).
curl -s -X PATCH -H "X-API-Key:${PDNS_API_KEY}" -H "Content-Type: application/json" \
  http://127.0.0.1:8081/api/v1/servers/localhost/zones/${BASE}. -d "{
  \"rrsets\": [
    {\"name\": \"ns1.${BASE}.\", \"type\": \"A\", \"ttl\": 3600, \"changetype\": \"REPLACE\",
     \"records\": [{\"content\": \"${SVC_IP}\"}]},
    {\"name\": \"ns2.${BASE}.\", \"type\": \"A\", \"ttl\": 3600, \"changetype\": \"REPLACE\",
     \"records\": [{\"content\": \"${SVC_IP}\"}]},
    {\"name\": \"api.${BASE}.\", \"type\": \"A\", \"ttl\": 300, \"changetype\": \"REPLACE\",
     \"records\": [{\"content\": \"${SVC_IP}\"}]},
    {\"name\": \"_psl.${BASE}.\", \"type\": \"TXT\", \"ttl\": 3600, \"changetype\": \"REPLACE\",
     \"records\": [{\"content\": \"\\\"${PSL_PR_URL}\\\"\"}]},
    {\"name\": \"${BASE}.\", \"type\": \"TXT\", \"ttl\": 3600, \"changetype\": \"REPLACE\",
     \"records\": [{\"content\": \"\\\"v=spf1 -all\\\"\"}]},
    {\"name\": \"_dmarc.${BASE}.\", \"type\": \"TXT\", \"ttl\": 3600, \"changetype\": \"REPLACE\",
     \"records\": [{\"content\": \"\\\"v=DMARC1; p=reject; rua=mailto:abuse@jabali-panel.com\\\"\"}]}
  ]}" >/dev/null
say "zone ready (ns1/ns2/api/_psl/SPF/DMARC)"

# ---- 2. service -------------------------------------------------------------
id -u jabalihosted >/dev/null 2>&1 || useradd --system --home /var/lib/jabalihosted --shell /usr/sbin/nologin jabalihosted
install -d -m 0750 -o jabalihosted -g jabalihosted /var/lib/jabalihosted

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
[[ -x "${HERE}/jabalihosted-svc" ]] || die "build first: go build -o hostedsvc/deploy/jabalihosted-svc ./hostedsvc/cmd/jabalihosted-svc"
install -m 0755 "${HERE}/jabalihosted-svc" /usr/local/bin/jabalihosted-svc
install -m 0644 "${HERE}/jabalihosted-svc.service" /etc/systemd/system/jabalihosted-svc.service
# The unit runs as the service user; it needs the env file readable.
chown root:jabalihosted /etc/jabalihosted/svc.env
chmod 0640 /etc/jabalihosted/svc.env
systemctl daemon-reload
systemctl enable --now jabalihosted-svc
install -m 0644 "${HERE}/nginx-api.conf" /etc/nginx/sites-available/jabalihosted-api.conf
ln -sf ../sites-available/jabalihosted-api.conf /etc/nginx/sites-enabled/jabalihosted-api.conf

say "service up on loopback; nginx vhost staged (cert comes after delegation)"

# ---- 3+4. manual gate -------------------------------------------------------
cat <<EOF

NEXT (in this order — do not reorder):
 1. Fill SMTP_FROM/SMTP_PASSWORD in /etc/jabalihosted/svc.env if still
    placeholders, then: systemctl restart jabalihosted-svc
 2. Registrar (Namecheap API):
      domains.ns.create  ns1.${BASE} -> ${SVC_IP}
      domains.ns.create  ns2.${BASE} -> ${SVC_IP}
      domains.dns.setCustom ${BASE} = ns1.${BASE},ns2.${BASE}
 3. VERIFY the PSL attestation survived the cutover:
      dig +short TXT _psl.${BASE} @8.8.8.8   → must print ${PSL_PR_URL}
 4. Only then issue the API cert:
      certbot certonly --webroot -w /var/www/html -d api.${BASE}
      nginx -t && systemctl reload nginx
 5. Nightly off-box dump (add to root crontab):
      sqlite3 /var/lib/jabalihosted/svc.db ".backup /root/svc-db-backup/svc-\$(date +%u).db"
      + rsync that dir off-box. Lost DB = every issued token dead.
 6. Daily reap of moved-away labels (add to root crontab). Deletes the
    dangling DNS a box leaves when it re-claims at a new IP. Dry-run once
    first to see the worklist, then wire the real run:
      17 4 * * *  /usr/local/bin/jabalihosted-svc reap >> /var/log/jabalihosted-reap.log 2>&1
EOF
