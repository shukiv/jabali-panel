<?php

return [
    'demo' => env('JABALI_DEMO', false),

    'agent' => [
        'socket' => env('JABALI_AGENT_SOCKET', '/var/run/jabali/agent.sock'),
        'timeout' => env('JABALI_AGENT_TIMEOUT', 30),
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
