#!/usr/bin/env bash
# Collector: Email domains, mailboxes, forwards, autoresponders from Stalwart

collect_email() {
    local user_id="$1" staging_dir="$2"
    local email_dir="${staging_dir}/email"

    # Email domains
    local edomains
    edomains=$(discover_email_domains "$user_id")
    [[ -z "$edomains" ]] && return 0

    mkdir -p "${email_dir}/domains" "${email_dir}/mailboxes"

    # Export email domain config
    while IFS=$'\t' read -r ed_id domain dkim_sel catch_all_en catch_all_addr max_mbox max_quota; do
        [[ -z "$ed_id" ]] && continue
        # Try to get DKIM keys (encrypted)
        local dkim_priv dkim_pub
        dkim_priv=$(_db_query "SELECT dkim_private_key FROM email_domains WHERE id = $ed_id" 2>/dev/null)
        dkim_pub=$(_db_query "SELECT dkim_public_key FROM email_domains WHERE id = $ed_id" 2>/dev/null)

        # Decrypt DKIM private key if possible
        local dkim_decrypted=""
        if [[ -n "$dkim_priv" ]] && [[ -n "$CFG_APP_KEY" ]]; then
            dkim_decrypted=$(JABALI_APP_KEY="$CFG_APP_KEY" php "$LIB_DIR/jabali-decrypt.php" "$dkim_priv" 2>/dev/null) || true
        fi

        cat > "${email_dir}/domains/${domain}.json" <<EOJSON
{
  "domain": "${domain}",
  "dkim_selector": "${dkim_sel}",
  "dkim_public_key": "${dkim_pub}",
  "dkim_private_key_decrypted": $(if [[ -n "$dkim_decrypted" ]]; then echo "true"; else echo "false"; fi),
  "catch_all_enabled": ${catch_all_en:-0},
  "catch_all_address": "${catch_all_addr}",
  "max_mailboxes": ${max_mbox:-0},
  "max_quota_bytes": ${max_quota:-0}
}
EOJSON
        # Save decrypted DKIM key separately
        if [[ -n "$dkim_decrypted" ]]; then
            echo "$dkim_decrypted" > "${email_dir}/domains/${domain}.dkim.key"
        fi

        log_info "email: Exported domain $domain"
    done <<< "$edomains"

    # Mailboxes
    local mailboxes
    mailboxes=$(discover_mailboxes "$user_id")
    local mail_paths=()
    while IFS=$'\t' read -r mb_id local_part domain quota imap pop3 smtp sys_uid sys_gid; do
        [[ -z "$mb_id" ]] && continue
        cat > "${email_dir}/mailboxes/${local_part}@${domain}.json" <<EOJSON
{
  "local_part": "${local_part}",
  "domain": "${domain}",
  "quota_bytes": ${quota:-0},
  "imap_enabled": ${imap:-1},
  "pop3_enabled": ${pop3:-1},
  "smtp_enabled": ${smtp:-1},
  "system_uid": ${sys_uid:-0},
  "system_gid": ${sys_gid:-0}
}
EOJSON
        # Collect mail storage path
        local maildir="${CFG_STALWART_DATA}/${domain}/${local_part}"
        if [[ -d "$maildir" ]]; then
            mail_paths+=("$maildir")
        fi
        log_info "email: Exported mailbox ${local_part}@${domain}"
    done <<< "$mailboxes"

    # Print mail storage paths for restic (one per line)
    for p in "${mail_paths[@]}"; do
        echo "$p"
    done

    # Forwarders
    local fwds
    fwds=$(discover_email_forwarders "$user_id")
    if [[ -n "$fwds" ]]; then
        echo "[" > "${email_dir}/forwarders.json"
        local first=1
        while IFS=$'\t' read -r local_part domain destinations is_active; do
            [[ -z "$local_part" ]] && continue
            [[ "$first" -eq 0 ]] && echo "," >> "${email_dir}/forwarders.json"
            first=0
            printf '  {"local_part":"%s","domain":"%s","destinations":%s,"is_active":%s}' \
                "$local_part" "$domain" "${destinations:-\"[]\"}" "${is_active:-1}" >> "${email_dir}/forwarders.json"
        done <<< "$fwds"
        echo -e "\n]" >> "${email_dir}/forwarders.json"
        log_info "email: Exported forwarders"
    fi

    # Autoresponders
    local autos
    autos=$(discover_autoresponders "$user_id")
    if [[ -n "$autos" ]]; then
        echo "[" > "${email_dir}/autoresponders.json"
        local first=1
        while IFS=$'\t' read -r a_id local_part domain subject message start end active; do
            [[ -z "$a_id" ]] && continue
            [[ "$first" -eq 0 ]] && echo "," >> "${email_dir}/autoresponders.json"
            first=0
            printf '  {"local_part":"%s","domain":"%s","subject":"%s","start_date":"%s","end_date":"%s","is_active":%s}' \
                "$local_part" "$domain" "${subject//\"/\\\"}" "$start" "$end" "${active:-1}" >> "${email_dir}/autoresponders.json"
        done <<< "$autos"
        echo -e "\n]" >> "${email_dir}/autoresponders.json"
        log_info "email: Exported autoresponders"
    fi

    # Mailbox shares
    local shares
    shares=$(discover_mailbox_shares "$user_id")
    if [[ -n "$shares" ]]; then
        echo "[" > "${email_dir}/shares.json"
        local first=1
        while IFS=$'\t' read -r mb_id shared_with folder acl; do
            [[ -z "$mb_id" ]] && continue
            [[ "$first" -eq 0 ]] && echo "," >> "${email_dir}/shares.json"
            first=0
            printf '  {"mailbox_id":%s,"shared_with_mailbox_id":%s,"folder":"%s","acl_rights":"%s"}' \
                "$mb_id" "$shared_with" "$folder" "$acl" >> "${email_dir}/shares.json"
        done <<< "$shares"
        echo -e "\n]" >> "${email_dir}/shares.json"
    fi
}
