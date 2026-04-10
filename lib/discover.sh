#!/usr/bin/env bash
# Account discovery from Jabali MySQL database

# Run a mysql query, return TSV
_db_query() {
    mysql -h "$CFG_DB_HOST" -u "$CFG_DB_USER" -p"$CFG_DB_PASS" \
        -N -B "$CFG_DB_NAME" -e "$1" 2>/dev/null
}

discover_accounts() {
    _db_query "SELECT id, username, COALESCE(home_directory, CONCAT('/home/', username)), is_active FROM users WHERE is_active = 1 ORDER BY username"
}

discover_all_accounts() {
    _db_query "SELECT id, username, COALESCE(home_directory, CONCAT('/home/', username)), is_active FROM users ORDER BY username"
}

discover_single_account() {
    local username="$1"
    _db_query "SELECT id, username, COALESCE(home_directory, CONCAT('/home/', username)), is_active FROM users WHERE username = '$(mysql_escape "$username")' LIMIT 1"
}

discover_domains() {
    local user_id="$1"
    _db_query "SELECT id, domain, document_root, ssl_enabled, custom_nginx_directives FROM domains WHERE user_id = $user_id"
}

discover_mysql_credentials() {
    local user_id="$1"
    _db_query "SELECT mysql_username FROM mysql_credentials WHERE user_id = $user_id"
}

discover_email_domains() {
    local user_id="$1"
    _db_query "SELECT ed.id, d.domain, ed.dkim_selector, ed.catch_all_enabled, ed.catch_all_address, ed.max_mailboxes, ed.max_quota_bytes FROM email_domains ed JOIN domains d ON ed.domain_id = d.id WHERE d.user_id = $user_id"
}

discover_mailboxes() {
    local user_id="$1"
    _db_query "SELECT m.id, m.local_part, d.domain, m.quota_bytes, m.imap_enabled, m.pop3_enabled, m.smtp_enabled, m.system_uid, m.system_gid FROM mailboxes m JOIN email_domains ed ON m.email_domain_id = ed.id JOIN domains d ON ed.domain_id = d.id WHERE d.user_id = $user_id"
}

discover_dns_records() {
    local domain_id="$1"
    _db_query "SELECT name, type, content, ttl, priority FROM dns_records WHERE domain_id = $domain_id ORDER BY type, name"
}

discover_ssl_certs() {
    local user_id="$1"
    _db_query "SELECT sc.id, d.domain, sc.service, sc.type, sc.status, sc.issuer, sc.certificate, sc.ca_bundle, sc.expires_at, sc.auto_renew FROM ssl_certificates sc JOIN domains d ON sc.domain_id = d.id WHERE d.user_id = $user_id"
}

discover_email_forwarders() {
    local user_id="$1"
    _db_query "SELECT ef.local_part, d.domain, ef.destinations, ef.is_active FROM email_forwarders ef JOIN email_domains ed ON ef.email_domain_id = ed.id JOIN domains d ON ed.domain_id = d.id WHERE d.user_id = $user_id"
}

discover_autoresponders() {
    local user_id="$1"
    _db_query "SELECT a.id, m.local_part, d.domain, a.subject, a.message, a.start_date, a.end_date, a.is_active FROM autoresponders a JOIN mailboxes m ON a.mailbox_id = m.id JOIN email_domains ed ON m.email_domain_id = ed.id JOIN domains d ON ed.domain_id = d.id WHERE d.user_id = $user_id"
}

discover_cron_jobs() {
    local user_id="$1"
    _db_query "SELECT id, name, schedule, command, type, is_active FROM cron_jobs WHERE user_id = $user_id"
}

discover_domain_aliases() {
    local user_id="$1"
    _db_query "SELECT da.alias, d.domain FROM domain_aliases da JOIN domains d ON da.domain_id = d.id WHERE d.user_id = $user_id"
}

discover_mailbox_shares() {
    local user_id="$1"
    _db_query "SELECT ms.mailbox_id, ms.shared_with_mailbox_id, ms.folder, ms.acl_rights FROM mailbox_shares ms JOIN mailboxes m ON ms.mailbox_id = m.id JOIN email_domains ed ON m.email_domain_id = ed.id JOIN domains d ON ed.domain_id = d.id WHERE d.user_id = $user_id"
}

# Escape single quotes for safe SQL interpolation
mysql_escape() {
    echo "${1//\'/\'\'}"
}

# Run a write (INSERT/UPDATE/DELETE) SQL against Jabali DB
_db_write() {
    mysql -h "$CFG_DB_HOST" -u "$CFG_DB_USER" -p"$CFG_DB_PASS" \
        "$CFG_DB_NAME" -e "$1" 2>/dev/null
}
