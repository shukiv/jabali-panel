<?php

return [
    'demo' => env('JABALI_DEMO', false),

    'agent' => [
        'socket' => env('JABALI_AGENT_SOCKET', '/var/run/jabali/agent.sock'),
        'timeout' => env('JABALI_AGENT_TIMEOUT', 30),
    ],

    /*
    |--------------------------------------------------------------------------
    | Agent v2 rollout flags (ADR-0007)
    |--------------------------------------------------------------------------
    |
    | Per-subsystem feature flags for migrating existing work into the
    | background-tasks control plane. Each flag is opt-in so we can ship
    | one migration at a time and roll back by flipping a single toggle.
    |
    */
    'agent_v2' => [
        // Phase 7 (shipped):
        'ssl_enabled' => env('JABALI_AGENT_V2_SSL', false),
        // Phase 8 (shipped — binary side owned by jabali-backup team):
        'backup_binary' => env('JABALI_BACKUP_BINARY', '/opt/jabali-backup/bin/jabali-backup'),
        // Phase 9:
        'addons_enabled' => env('JABALI_AGENT_V2_ADDONS', false),
        // Phase 9 follow-ups — flags reserved, binaries not yet implemented:
        'git_deploy_enabled' => env('JABALI_AGENT_V2_GIT_DEPLOY', false),
        'cpanel_restore_enabled' => env('JABALI_AGENT_V2_CPANEL_RESTORE', false),
        'whm_migration_enabled' => env('JABALI_AGENT_V2_WHM_MIGRATION', false),
        'imap_sync_enabled' => env('JABALI_AGENT_V2_IMAP_SYNC', false),
    ],

    'panel' => [
        'hostname' => env('PANEL_HOSTNAME', env('SERVER_HOSTNAME', '')),
        'port' => (int) env('PANEL_PORT', 8443),
        'tls_cert' => env('PANEL_TLS_CERT', '/etc/ssl/jabali/panel.crt'),
    ],

    /*
    |--------------------------------------------------------------------------
    | PHP Extensions
    |--------------------------------------------------------------------------
    |
    | All known PHP extensions available for installation via apt packages.
    | true = installed by default with new PHP versions (php.install).
    | false = available for manual installation via PHP Manager.
    |
    | Package naming: php{version}-{key} (e.g. php8.4-bcmath)
    |
    */
    'php' => [
        'extensions' => [
            // Default modules (installed automatically with new PHP versions)
            'bcmath' => true,
            'curl' => true,
            'gd' => true,
            'imagick' => true,
            'imap' => true,
            'intl' => true,
            'mbstring' => true,
            'mysql' => true,
            'opcache' => true,
            'redis' => true,
            'xml' => true,
            'zip' => true,

            // Optional modules (available for manual installation)
            'apcu' => false,
            'bz2' => false,
            'dba' => false,
            'enchant' => false,
            'exif' => false,
            'gmp' => false,
            'gnupg' => false,
            'igbinary' => false,
            'ldap' => false,
            'mailparse' => false,
            'memcached' => false,
            'mongodb' => false,
            'msgpack' => false,
            'odbc' => false,
            'pgsql' => false,
            'pspell' => false,
            'readline' => false,
            'snmp' => false,
            'soap' => false,
            'sqlite3' => false,
            'ssh2' => false,
            'tidy' => false,
            'xdebug' => false,
            'xsl' => false,
            'yaml' => false,
        ],
    ],
];
