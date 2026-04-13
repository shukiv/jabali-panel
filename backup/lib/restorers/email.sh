#!/usr/bin/env bash
# Restorer: email domains, mailboxes, forwarders, autoresponders, shares

restore_email() {
    local extract_dir="$1" username="$2" user_id="$3" force="${4:-0}"
    local staging="${extract_dir}/tmp/jabali-backup/${username}/email"

    [[ ! -d "$staging" ]] && return 0

    # Restore email domains
    if [[ -d "${staging}/domains" ]]; then
        for domain_json in "${staging}/domains"/*.json; do
            [[ ! -f "$domain_json" ]] && continue
            local domain_name
            domain_name=$(basename "$domain_json" .json)

            local domain_id
            domain_id=$(_db_query "SELECT id FROM domains WHERE domain='$(mysql_escape "$domain_name")' LIMIT 1")
            [[ -z "$domain_id" ]] && { log_warn "restore/email: Domain $domain_name not in DB"; continue; }

            # Parse email domain config
            local catch_all_en catch_all_addr max_mbox max_quota dkim_sel
            catch_all_en=$(grep -oP '"catch_all_enabled"\s*:\s*\K[0-9]+' "$domain_json" 2>/dev/null || echo "0")
            catch_all_addr=$(grep -oP '"catch_all_address"\s*:\s*"\K[^"]*' "$domain_json" 2>/dev/null || true)
            max_mbox=$(grep -oP '"max_mailboxes"\s*:\s*\K[0-9]+' "$domain_json" 2>/dev/null || echo "0")
            max_quota=$(grep -oP '"max_quota_bytes"\s*:\s*\K[0-9]+' "$domain_json" 2>/dev/null || echo "0")
            dkim_sel=$(grep -oP '"dkim_selector"\s*:\s*"\K[^"]*' "$domain_json" 2>/dev/null || true)

            local existing_ed
            existing_ed=$(_db_query "SELECT id FROM email_domains WHERE domain_id=$domain_id LIMIT 1")

            # Re-encrypt DKIM key if available
            local dkim_key_file="${staging}/domains/${domain_name}.dkim.key"
            local dkim_enc=""
            if [[ -f "$dkim_key_file" ]] && [[ -n "$CFG_APP_KEY" ]]; then
                dkim_enc=$(JABALI_APP_KEY="$CFG_APP_KEY" php "$LIB_DIR/jabali-encrypt.php" "$(cat "$dkim_key_file")" 2>/dev/null) || true
            fi

            if [[ -n "$existing_ed" ]]; then
                if [[ "$force" -eq 1 ]]; then
                    if _db_write "UPDATE email_domains SET catch_all_enabled=$catch_all_en, catch_all_address='$(mysql_escape "$catch_all_addr")', max_mailboxes=$max_mbox, max_quota_bytes=$max_quota, dkim_selector='$(mysql_escape "$dkim_sel")', updated_at=NOW() WHERE id=$existing_ed"; then
                        [[ -n "$dkim_enc" ]] && _db_write "UPDATE email_domains SET dkim_private_key='$(mysql_escape "$dkim_enc")' WHERE id=$existing_ed"
                        log_info "restore/email: Updated email domain $domain_name"
                    else
                        log_warn "restore/email: Failed to update email domain $domain_name"
                    fi
                else
                    log_info "restore/email: Email domain $domain_name exists, skipping"
                fi
            else
                local dkim_col="" dkim_val=""
                if [[ -n "$dkim_enc" ]]; then
                    dkim_col=", dkim_private_key"
                    dkim_val=", '$(mysql_escape "$dkim_enc")'"
                fi
                if _db_write "INSERT INTO email_domains (domain_id, is_active, dkim_selector, catch_all_enabled, catch_all_address, max_mailboxes, max_quota_bytes, created_at, updated_at${dkim_col}) VALUES ($domain_id, 1, '$(mysql_escape "$dkim_sel")', $catch_all_en, '$(mysql_escape "$catch_all_addr")', $max_mbox, $max_quota, NOW(), NOW()${dkim_val})"; then
                    log_info "restore/email: Created email domain $domain_name"
                else
                    log_warn "restore/email: Failed to create email domain $domain_name"
                fi
            fi
        done
    fi

    # Restore mailboxes
    if [[ -d "${staging}/mailboxes" ]]; then
        for mb_json in "${staging}/mailboxes"/*.json; do
            [[ ! -f "$mb_json" ]] && continue
            local local_part mb_domain mb_quota mb_imap mb_pop3 mb_smtp mb_uid mb_gid
            local_part=$(grep -oP '"local_part"\s*:\s*"\K[^"]+' "$mb_json")
            mb_domain=$(grep -oP '"domain"\s*:\s*"\K[^"]+' "$mb_json")
            mb_quota=$(grep -oP '"quota_bytes"\s*:\s*\K[0-9]+' "$mb_json" || echo "0")
            mb_imap=$(grep -oP '"imap_enabled"\s*:\s*\K[0-9]+' "$mb_json" || echo "1")
            mb_pop3=$(grep -oP '"pop3_enabled"\s*:\s*\K[0-9]+' "$mb_json" || echo "1")
            mb_smtp=$(grep -oP '"smtp_enabled"\s*:\s*\K[0-9]+' "$mb_json" || echo "1")
            mb_uid=$(grep -oP '"system_uid"\s*:\s*\K[0-9]+' "$mb_json" || echo "0")
            mb_gid=$(grep -oP '"system_gid"\s*:\s*\K[0-9]+' "$mb_json" || echo "0")

            # Get backed up password hash and encrypted password
            local pw_hash pw_encrypted
            pw_hash=$(grep -oP '"password_hash"\s*:\s*"\K[^"]+' "$mb_json" || true)
            pw_encrypted=$(grep -oP '"password_encrypted"\s*:\s*"\K[^"]+' "$mb_json" || true)

            # Use backed up hash, or placeholder if not available
            local restore_hash="$pw_hash"
            if [[ -z "$restore_hash" ]] || [[ "$restore_hash" == "null" ]]; then
                restore_hash='$2y$10$RESET.REQUIRED.000000000000000000000000000000000000000'
            fi

            # Find email_domain_id
            local ed_id
            ed_id=$(_db_query "SELECT ed.id FROM email_domains ed JOIN domains d ON ed.domain_id=d.id WHERE d.domain='$(mysql_escape "$mb_domain")' LIMIT 1")
            [[ -z "$ed_id" ]] && { log_warn "restore/email: Email domain for $mb_domain not found"; continue; }

            local existing_mb
            existing_mb=$(_db_query "SELECT id FROM mailboxes WHERE email_domain_id=$ed_id AND local_part='$(mysql_escape "$local_part")' LIMIT 1")

            if [[ -n "$existing_mb" ]]; then
                if [[ "$force" -eq 1 ]]; then
                    local pw_update=""
                    if [[ -n "$pw_hash" ]] && [[ "$pw_hash" != "null" ]]; then
                        pw_update=", password_hash='$(mysql_escape "$pw_hash")'"
                    fi
                    if [[ -n "$pw_encrypted" ]] && [[ "$pw_encrypted" != "null" ]]; then
                        pw_update="${pw_update}, password_encrypted='$(mysql_escape "$pw_encrypted")'"
                    fi
                    if _db_write "UPDATE mailboxes SET quota_bytes=$mb_quota, imap_enabled=$mb_imap, pop3_enabled=$mb_pop3, smtp_enabled=$mb_smtp${pw_update}, updated_at=NOW() WHERE id=$existing_mb"; then
                        log_info "restore/email: Updated mailbox ${local_part}@${mb_domain}"
                    else
                        log_warn "restore/email: Failed to update mailbox ${local_part}@${mb_domain}"
                    fi
                else
                    log_info "restore/email: Mailbox ${local_part}@${mb_domain} exists, skipping"
                fi
            else
                local pw_enc_col="" pw_enc_val=""
                if [[ -n "$pw_encrypted" ]] && [[ "$pw_encrypted" != "null" ]]; then
                    pw_enc_col=", password_encrypted"
                    pw_enc_val=", '$(mysql_escape "$pw_encrypted")'"
                fi
                if _db_write "INSERT INTO mailboxes (email_domain_id, user_id, local_part, password_hash, quota_bytes, imap_enabled, pop3_enabled, smtp_enabled, system_uid, system_gid, created_at, updated_at${pw_enc_col}) VALUES ($ed_id, $user_id, '$(mysql_escape "$local_part")', '$(mysql_escape "$restore_hash")', $mb_quota, $mb_imap, $mb_pop3, $mb_smtp, $mb_uid, $mb_gid, NOW(), NOW()${pw_enc_val})"; then
                    if [[ -n "$pw_hash" ]] && [[ "$pw_hash" != "null" ]] && [[ "$pw_hash" != *"RESET.REQUIRED"* ]]; then
                        log_info "restore/email: Created mailbox ${local_part}@${mb_domain} (password restored)"
                    else
                        log_info "restore/email: Created mailbox ${local_part}@${mb_domain} (password must be reset)"
                    fi
                else
                    log_warn "restore/email: Failed to create mailbox ${local_part}@${mb_domain}"
                fi
            fi
        done
    fi

    # Restore forwarders
    _restore_email_forwarders "$staging" "$user_id" "$force"

    # Restore autoresponders
    _restore_email_autoresponders "$staging" "$user_id" "$force"

    log_info "restore/email: Done"
}

_restore_email_forwarders() {
    local staging="$1" user_id="$2" force="$3"
    local fwd_file="${staging}/forwarders.json"
    [[ ! -f "$fwd_file" ]] && return 0

    while IFS= read -r line; do
        local lp domain dests active
        lp=$(echo "$line" | grep -oP '"local_part"\s*:\s*"\K[^"]+' || true)
        domain=$(echo "$line" | grep -oP '"domain"\s*:\s*"\K[^"]+' || true)
        active=$(echo "$line" | grep -oP '"is_active"\s*:\s*\K[0-9]+' || echo "1")
        [[ -z "$lp" || -z "$domain" ]] && continue

        local ed_id
        ed_id=$(_db_query "SELECT ed.id FROM email_domains ed JOIN domains d ON ed.domain_id=d.id WHERE d.domain='$(mysql_escape "$domain")' LIMIT 1")
        [[ -z "$ed_id" ]] && continue

        local exists
        exists=$(_db_query "SELECT 1 FROM email_forwarders WHERE email_domain_id=$ed_id AND local_part='$(mysql_escape "$lp")' LIMIT 1")
        if [[ -z "$exists" ]] || [[ "$force" -eq 1 ]]; then
            [[ -n "$exists" ]] && _db_write "DELETE FROM email_forwarders WHERE email_domain_id=$ed_id AND local_part='$(mysql_escape "$lp")'"
            # Extract destinations JSON
            dests=$(echo "$line" | grep -oP '"destinations"\s*:\s*\K\[[^\]]*\]' || echo '[]')
            _db_write "INSERT INTO email_forwarders (email_domain_id, user_id, local_part, destinations, is_active, created_at, updated_at) VALUES ($ed_id, $user_id, '$(mysql_escape "$lp")', '$(mysql_escape "$dests")', $active, NOW(), NOW())"
            log_info "restore/email: Restored forwarder ${lp}@${domain}"
        fi
    done < <(grep -oP '\{[^}]+\}' "$fwd_file")
}

_restore_email_autoresponders() {
    local staging="$1" user_id="$2" force="$3"
    local auto_file="${staging}/autoresponders.json"
    [[ ! -f "$auto_file" ]] && return 0

    while IFS= read -r line; do
        local lp domain subject start_d end_d active
        lp=$(echo "$line" | grep -oP '"local_part"\s*:\s*"\K[^"]+' || true)
        domain=$(echo "$line" | grep -oP '"domain"\s*:\s*"\K[^"]+' || true)
        subject=$(echo "$line" | grep -oP '"subject"\s*:\s*"\K[^"]+' || true)
        start_d=$(echo "$line" | grep -oP '"start_date"\s*:\s*"\K[^"]+' || true)
        end_d=$(echo "$line" | grep -oP '"end_date"\s*:\s*"\K[^"]+' || true)
        active=$(echo "$line" | grep -oP '"is_active"\s*:\s*\K[0-9]+' || echo "1")
        [[ -z "$lp" || -z "$domain" ]] && continue

        # Find mailbox_id
        local mb_id
        mb_id=$(_db_query "SELECT m.id FROM mailboxes m JOIN email_domains ed ON m.email_domain_id=ed.id JOIN domains d ON ed.domain_id=d.id WHERE m.local_part='$(mysql_escape "$lp")' AND d.domain='$(mysql_escape "$domain")' LIMIT 1")
        [[ -z "$mb_id" ]] && continue

        local exists
        exists=$(_db_query "SELECT 1 FROM autoresponders WHERE mailbox_id=$mb_id LIMIT 1")
        if [[ -z "$exists" ]] || [[ "$force" -eq 1 ]]; then
            [[ -n "$exists" ]] && _db_write "DELETE FROM autoresponders WHERE mailbox_id=$mb_id"
            _db_write "INSERT INTO autoresponders (mailbox_id, subject, start_date, end_date, is_active, created_at, updated_at) VALUES ($mb_id, '$(mysql_escape "$subject")', '$(mysql_escape "$start_d")', '$(mysql_escape "$end_d")', $active, NOW(), NOW())"
            log_info "restore/email: Restored autoresponder for ${lp}@${domain}"
        fi
    done < <(grep -oP '\{[^}]+\}' "$auto_file")
}
