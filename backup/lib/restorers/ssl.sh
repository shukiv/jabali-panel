#!/usr/bin/env bash
# Restorer: SSL certificates

restore_ssl() {
    local extract_dir="$1" username="$2" user_id="$3" force="${4:-0}"
    local staging="${extract_dir}/tmp/jabali-backup/${username}/ssl"

    [[ ! -d "$staging" ]] && return 0

    local count=0
    for domain_dir in "${staging}"/*/; do
        [[ ! -d "$domain_dir" ]] && continue
        local domain_name
        domain_name=$(basename "$domain_dir")

        local domain_id
        domain_id=$(_db_query "SELECT id FROM domains WHERE domain='$(mysql_escape "$domain_name")' LIMIT 1")
        [[ -z "$domain_id" ]] && { log_warn "restore/ssl: Domain $domain_name not found"; continue; }

        for service_dir in "${domain_dir}"/*/; do
            [[ ! -d "$service_dir" ]] && continue
            local service
            service=$(basename "$service_dir")
            local meta_file="${service_dir}/meta.json"
            [[ ! -f "$meta_file" ]] && continue

            # Parse metadata
            local cert_type status issuer expires auto_renew
            cert_type=$(grep -oP '"type"\s*:\s*"\K[^"]+' "$meta_file" 2>/dev/null || echo "lets_encrypt")
            status=$(grep -oP '"status"\s*:\s*"\K[^"]+' "$meta_file" 2>/dev/null || echo "active")
            issuer=$(grep -oP '"issuer"\s*:\s*"\K[^"]+' "$meta_file" 2>/dev/null || true)
            expires=$(grep -oP '"expires_at"\s*:\s*"\K[^"]+' "$meta_file" 2>/dev/null || true)
            auto_renew=$(grep -oP '"auto_renew"\s*:\s*\K[0-9]+' "$meta_file" 2>/dev/null || echo "1")

            # Read cert files
            local cert_pem="" ca_bundle="" privkey=""
            [[ -f "${service_dir}/cert.pem" ]] && cert_pem=$(cat "${service_dir}/cert.pem")
            [[ -f "${service_dir}/ca-bundle.pem" ]] && ca_bundle=$(cat "${service_dir}/ca-bundle.pem")

            # Re-encrypt private key
            local privkey_enc=""
            if [[ -f "${service_dir}/privkey.pem" ]] && [[ -n "$CFG_APP_KEY" ]]; then
                privkey_enc=$(JABALI_APP_KEY="$CFG_APP_KEY" php "$LIB_DIR/jabali-encrypt.php" "$(cat "${service_dir}/privkey.pem")" 2>/dev/null) || true
            fi

            local existing_cert
            existing_cert=$(_db_query "SELECT id FROM ssl_certificates WHERE domain_id=$domain_id AND service='$(mysql_escape "$service")' LIMIT 1")

            if [[ -n "$existing_cert" ]]; then
                if [[ "$force" -eq 1 ]]; then
                    _db_write "UPDATE ssl_certificates SET type='$(mysql_escape "$cert_type")', status='$(mysql_escape "$status")', issuer='$(mysql_escape "$issuer")', certificate='$(mysql_escape "$cert_pem")', ca_bundle='$(mysql_escape "$ca_bundle")', expires_at='$(mysql_escape "$expires")', auto_renew=$auto_renew, updated_at=NOW() WHERE id=$existing_cert"
                    [[ -n "$privkey_enc" ]] && _db_write "UPDATE ssl_certificates SET private_key='$(mysql_escape "$privkey_enc")' WHERE id=$existing_cert"
                    log_info "restore/ssl: Updated $domain_name/$service cert"
                else
                    log_info "restore/ssl: $domain_name/$service cert exists, skipping"
                fi
            else
                local pk_col="" pk_val=""
                if [[ -n "$privkey_enc" ]]; then
                    pk_col=", private_key"
                    pk_val=", '$(mysql_escape "$privkey_enc")'"
                fi
                _db_write "INSERT INTO ssl_certificates (domain_id, service, type, status, issuer, certificate, ca_bundle, expires_at, auto_renew, created_at, updated_at${pk_col}) VALUES ($domain_id, '$(mysql_escape "$service")', '$(mysql_escape "$cert_type")', '$(mysql_escape "$status")', '$(mysql_escape "$issuer")', '$(mysql_escape "$cert_pem")', '$(mysql_escape "$ca_bundle")', '$(mysql_escape "$expires")', $auto_renew, NOW(), NOW()${pk_val})"
                log_info "restore/ssl: Created $domain_name/$service cert"
            fi
            count=$((count + 1))
        done
    done

    log_info "restore/ssl: Restored $count certificate(s)"
}
