#!/usr/bin/env bash
# jabali-panel-cert.sh — certbot deploy-hook for the panel certs.
#
# install.sh drops this at /etc/letsencrypt/renewal-hooks/deploy/
# during install_jabali_panel_cert_hook. Certbot invokes it on every
# successful renewal AND the panel-agent invokes it directly after
# first-issue in ssl.panel.issue.
#
# Post ADR-0105 the panel has TWO independent certs:
#   kind=hostname  → /etc/jabali/tls/panel.{crt,key};
#                    reload nginx, restart jabali-panel + jabali-bulwark.
#   kind=mail      → /etc/jabali/tls/panel-mail.{crt,key};
#                    reload nginx, restart jabali-stalwart.
#
# Inputs:
#   $RENEWED_LINEAGE        — LE lineage dir (certbot or ssl.panel.issue).
#   $JABALI_PANEL_CERT_KIND — "hostname" (default) or "mail". On a real
#                             certbot renewal the var is absent, so the
#                             kind is derived from the lineage basename
#                             (mail.* → mail) to keep unattended
#                             renewals correct.
#
# Idempotent + best-effort reloads. set -euo pipefail.
set -euo pipefail

cn="$(hostname -f 2>/dev/null || hostname)"
src="${RENEWED_LINEAGE:-/etc/letsencrypt/live/${cn}}"

if [[ ! -d "$src" ]]; then
  echo "jabali-panel-cert.sh: no LE lineage at $src — nothing to deploy" >&2
  exit 0
fi
if [[ ! -f "$src/fullchain.pem" || ! -f "$src/privkey.pem" ]]; then
  echo "jabali-panel-cert.sh: lineage at $src missing fullchain.pem or privkey.pem" >&2
  exit 1
fi

# Resolve kind: explicit env wins; otherwise infer from the lineage
# name so certbot's unattended timer routes mail.* correctly.
kind="${JABALI_PANEL_CERT_KIND:-}"
if [[ -z "$kind" ]]; then
  case "$(basename "$src")" in
    mail.*)
      # mail-domain lineages also start with `mail.` -- disambiguate
      # via the env var ssl.mail.issue sets. Absent the var, fall back
      # to legacy 'mail' kind so existing panel-mail renewals don't
      # silently flip to the per-domain branch.
      if [[ -n "${JABALI_MAIL_DOMAIN_ID:-}" ]]; then
        kind="mail-domain"
      else
        kind="mail"
      fi
      ;;
    *)      kind="hostname" ;;
  esac
fi

dst_dir="/etc/jabali/tls"
install -d -m 0755 -o root -g root "$dst_dir"

case "$kind" in
  mail)
    install -m 0640 -o root -g jabali "$src/fullchain.pem" "$dst_dir/panel-mail.crt"
    install -m 0640 -o root -g jabali "$src/privkey.pem"   "$dst_dir/panel-mail.key"
    # nginx serves mail.<hostname> :443; Stalwart reads the mail cert
    # for SMTP/IMAPS. jabali-panel / jabali-bulwark use the hostname
    # cert and are deliberately NOT bounced here.
    systemctl reload nginx            || echo "jabali-panel-cert.sh: nginx reload failed (continuing)" >&2
    systemctl restart jabali-stalwart || echo "jabali-panel-cert.sh: jabali-stalwart restart failed (continuing)" >&2
    # Push the renewed PEM into Stalwart's Certificate object so
    # IMAPS / 465 / 587 serve the LE cert instead of Stalwart's rcgen
    # self-signed fallback. Idempotent; safe to call multiple times.
    # The script waits for Stalwart to come up after the restart above.
    if [[ -x /usr/local/bin/jabali-stalwart-push-cert ]]; then
      /usr/local/bin/jabali-stalwart-push-cert || \
        echo "jabali-panel-cert.sh: jabali-stalwart-push-cert non-zero (continuing)" >&2
    fi
    ;;
  mail-domain)
    # M6.6: per-tenant-domain mail cert. JABALI_MAIL_DOMAIN_ID is
    # the panel's domain row ULID; ssl.mail.issue sets it so the
    # cert lands in Stalwart's x:Certificate registry under a name
    # the reconciler can find by domain id. The cert itself stays
    # under the lineage dir (/etc/letsencrypt/live/mail.<d>/); we
    # do NOT copy it to /etc/jabali/tls -- only the panel-hostname
    # mail cert lives there. Stalwart reads each per-domain cert
    # directly from its lineage path via the push-cert subprocess.
    if [[ -z "${JABALI_MAIL_DOMAIN_ID:-}" ]]; then
      echo "jabali-panel-cert.sh: mail-domain kind requires JABALI_MAIL_DOMAIN_ID" >&2
      exit 1
    fi
    if [[ -x /usr/local/bin/jabali-stalwart-push-cert ]]; then
      JABALI_STALWART_CERT_NAME="domain-${JABALI_MAIL_DOMAIN_ID}" \
      JABALI_STALWART_CERT_PATH="$src/fullchain.pem" \
      JABALI_STALWART_KEY_PATH="$src/privkey.pem" \
        /usr/local/bin/jabali-stalwart-push-cert || \
        echo "jabali-panel-cert.sh: mail-domain push-cert non-zero (continuing)" >&2
      # Stalwart caches its TLS certs in memory at startup and does NOT
      # hot-reload the x:Certificate registry when push-cert writes a
      # new entry — without a restart it keeps presenting the previously
      # loaded cert (the panel default) for the new SNI on :465/:993.
      # The kind=mail branch already restarts for the same reason; the
      # per-domain branch must too or GH #132 stays broken (cert pushed
      # but never served).
      systemctl restart jabali-stalwart || \
        echo "jabali-panel-cert.sh: mail-domain jabali-stalwart restart failed (continuing)" >&2
    fi
    ;;

  *)
    install -m 0640 -o root -g jabali "$src/fullchain.pem" "$dst_dir/panel.crt"
    install -m 0640 -o root -g jabali "$src/privkey.pem"   "$dst_dir/panel.key"
    systemctl reload nginx           || echo "jabali-panel-cert.sh: nginx reload failed (continuing)" >&2
    # --no-block on jabali-panel: this hook is invoked synchronously by
    # the panel-agent ssl.panel.issue command, whose CALLER is
    # jabali-panel's own panel-cert reconciler. A blocking
    # `systemctl restart jabali-panel` SIGTERMs the panel mid-RPC, so
    # the reconciler never reaches MarkIssued and the row stays
    # pending_acme forever — every tick re-dispatches, re-runs this
    # hook, re-kills the panel (observed on mx: clean SIGTERM every
    # ~10 min, NRestarts=0, row never leaving pending_acme). --no-block
    # queues the restart so the agent reply propagates and the
    # reconciler commits status=issued BEFORE systemd fires it; the
    # panel then restarts into an already-issued row and does not
    # re-dispatch. jabali-bulwark is not the caller — synchronous.
    systemctl restart --no-block jabali-panel || echo "jabali-panel-cert.sh: jabali-panel restart failed (continuing)" >&2
    systemctl restart jabali-bulwark || echo "jabali-panel-cert.sh: jabali-bulwark restart failed (continuing)" >&2
    ;;
esac

exit 0
