<?php

declare(strict_types=1);

return [
    'jabali-backup' => [
        'name' => 'Backup',
        'description' => 'Automated encrypted backups with Restic. Per-user and full server snapshots with remote storage support.',
        'icon' => 'heroicon-o-cloud-arrow-up',
        'binary' => '/usr/local/bin/jabali-backup',
        'service' => null,
        'install_url' => 'https://raw.githubusercontent.com/shukiv/jabali-backup/main/install.sh',
        'uninstall_command' => 'jabali-backup uninstall',
        'version_command' => 'jabali-backup version',
        'repo' => 'shukiv/jabali-backup',
    ],

    'jabali-security' => [
        'name' => 'Security',
        'description' => 'Firewall, brute-force protection, ModSecurity WAF, and real-time threat monitoring.',
        'icon' => 'heroicon-o-shield-check',
        'binary' => '/usr/local/bin/jabali-security',
        'service' => 'jabali-security',
        'install_url' => 'https://raw.githubusercontent.com/shukiv/jabali-security/master/install.sh',
        'uninstall_command' => 'curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-security/master/install.sh | bash -s -- --uninstall',
        'version_command' => 'jabali-security status',
        'repo' => 'shukiv/jabali-security',
    ],

    'jabali-terminal' => [
        'name' => 'Terminal',
        'description' => 'Browser-based root shell with mandatory 2FA, HMAC-sealed audit transcripts, and session timeout controls.',
        'icon' => 'heroicon-o-command-line',
        'binary' => '/usr/local/bin/jabali-terminal',
        'service' => 'jabali-terminal',
        'install_url' => 'https://raw.githubusercontent.com/shukiv/jabali-terminal/main/install.sh',
        'uninstall_command' => 'curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-terminal/main/install.sh | bash -s -- --uninstall',
        'version_command' => 'jabali-terminal version',
        'repo' => 'shukiv/jabali-terminal',
    ],
];
