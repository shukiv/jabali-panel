#!/usr/bin/env bash
# Collector: SSL certificates

collect_ssl() {
    local user_id="$1" staging_dir="$2"
    local ssl_dir="${staging_dir}/ssl"

    local certs
    certs=$(discover_ssl_certs "$user_id")
    [[ -z "$certs" ]] && return 0

    mkdir -p "$ssl_dir"

    local count=0
    while IFS=$'\t' read -r cert_id domain service cert_type status issuer certificate ca_bundle expires auto_renew; do
        [[ -z "$cert_id" ]] && continue

        local cert_dir="${ssl_dir}/${domain}/${service}"
        mkdir -p "$cert_dir"

        # Save certificate
        if [[ -n "$certificate" ]] && [[ "$certificate" != "NULL" ]]; then
            echo "$certificate" > "${cert_dir}/cert.pem"
        fi

        # Save CA bundle
        if [[ -n "$ca_bundle" ]] && [[ "$ca_bundle" != "NULL" ]]; then
            echo "$ca_bundle" > "${cert_dir}/ca-bundle.pem"
        fi

        # Try to decrypt private key
        local priv_key_enc
        priv_key_enc=$(_db_query "SELECT private_key FROM ssl_certificates WHERE id = $cert_id" 2>/dev/null)
        if [[ -n "$priv_key_enc" ]] && [[ -n "$CFG_APP_KEY" ]]; then
            local priv_key_dec
            priv_key_dec=$(JABALI_APP_KEY="$CFG_APP_KEY" php "$LIB_DIR/jabali-decrypt.php" "$priv_key_enc" 2>/dev/null)
            if [[ -n "$priv_key_dec" ]]; then
                echo "$priv_key_dec" > "${cert_dir}/privkey.pem"
                chmod 600 "${cert_dir}/privkey.pem"
            fi
        fi

        # Metadata
        cat > "${cert_dir}/meta.json" <<EOJSON
{
  "domain": "${domain}",
  "service": "${service}",
  "type": "${cert_type}",
  "status": "${status}",
  "issuer": "${issuer}",
  "expires_at": "${expires}",
  "auto_renew": ${auto_renew:-0}
}
EOJSON

        # Also copy actual PEM files from /etc/letsencrypt/live/ (resolve symlinks)
        local le_dir="/etc/letsencrypt/live/${domain}"
        if [[ -d "$le_dir" ]]; then
            local le_out="${cert_dir}/letsencrypt"
            mkdir -p "$le_out"
            for pem in privkey.pem cert.pem chain.pem fullchain.pem; do
                [[ -f "${le_dir}/${pem}" ]] && cp -L "${le_dir}/${pem}" "${le_out}/${pem}" 2>/dev/null
            done
            chmod 600 "${le_out}/privkey.pem" 2>/dev/null || true
            log_info "ssl: Copied Let's Encrypt PEM files for ${domain}"
        fi

        count=$((count + 1))
        log_info "ssl: Exported ${domain}/${service} cert (expires: ${expires})"
    done <<< "$certs"

    log_info "ssl: Exported $count certificates"
}
