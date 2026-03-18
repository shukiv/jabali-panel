<?php

declare(strict_types=1);

namespace App\Console\Commands;

use Illuminate\Console\Command;
use Illuminate\Support\Facades\DB;
use Symfony\Component\Process\Process;

class MailMigrateCommand extends Command
{
    protected $signature = 'jabali:mail-migrate
        {--rollback : Restore legacy Postfix+Dovecot stack}
        {--dry-run : Show what would be done without making changes}
        {--skip-test : Skip SMTP/IMAP connectivity tests}
        {--force : Skip confirmation prompts}';

    protected $description = 'Migrate from Postfix+Dovecot+OpenDKIM+Rspamd to Stalwart Mail Server';

    private string $basePath;

    public function __construct()
    {
        parent::__construct();
        $this->basePath = base_path();
    }

    public function handle(): int
    {
        if ($this->option('rollback')) {
            return $this->rollback();
        }

        $dryRun = (bool) $this->option('dry-run');
        $skipTest = (bool) $this->option('skip-test');

        $this->info('Jabali Mail Stack Migration');
        $this->info('==========================');
        $this->newLine();

        // Step 1: Detect current stack
        $this->info('Step 1: Detecting current mail stack...');
        $legacy = $this->detectLegacyStack();

        if (empty($legacy)) {
            $this->warn('No legacy mail stack detected (Postfix/Dovecot not installed).');
            if (config('jabali.mail_backend') === 'stalwart') {
                $this->info('Already using Stalwart backend.');

                return self::SUCCESS;
            }
        } else {
            $this->table(['Service', 'Status'], $legacy);
        }

        if (config('jabali.mail_backend') === 'stalwart') {
            $this->warn('MAIL_BACKEND is already set to "stalwart".');
            if (! $this->option('force') && ! $this->confirm('Continue anyway?')) {
                return self::SUCCESS;
            }
        }

        // Step 2: Pre-flight checks
        $this->newLine();
        $this->info('Step 2: Pre-flight checks...');
        $domains = DB::table('email_domains')
            ->join('domains', 'email_domains.domain_id', '=', 'domains.id')
            ->where('email_domains.is_active', true)
            ->pluck('domains.domain');

        $mailboxCount = DB::table('mailboxes')->where('is_active', true)->count();

        $this->line("  Active domains: {$domains->count()}");
        $this->line("  Active mailboxes: {$mailboxCount}");

        if ($dryRun) {
            $this->newLine();
            $this->info('[DRY RUN] Would perform the following:');
            $this->line('  - Install Stalwart Mail Server binary');
            $this->line('  - Generate Stalwart TOML config with SQL backend');
            $this->line("  - Import DKIM keys for {$domains->count()} domains");
            $this->line('  - Migrate Sieve scripts');
            $this->line('  - Stop Postfix, Dovecot, OpenDKIM, Rspamd');
            $this->line('  - Start Stalwart Mail Server');
            $this->line('  - Set MAIL_BACKEND=stalwart in .env');

            return self::SUCCESS;
        }

        if (! $this->option('force')) {
            $this->warn('This will stop Postfix, Dovecot, OpenDKIM, and Rspamd and start Stalwart.');
            if (! $this->confirm('Proceed with migration?')) {
                return self::SUCCESS;
            }
        }

        // Step 3: Check Stalwart binary
        $this->newLine();
        $this->info('Step 3: Checking Stalwart installation...');
        if (! $this->ensureStalwartInstalled()) {
            return self::FAILURE;
        }

        // Step 4: Generate Stalwart config
        $this->newLine();
        $this->info('Step 4: Generating Stalwart configuration...');
        if (! $this->generateStalwartConfig()) {
            return self::FAILURE;
        }

        // Step 5: Import DKIM keys
        $this->newLine();
        $this->info('Step 5: Importing DKIM keys...');
        $this->importDkimKeys();

        // Step 6: Migrate Sieve scripts
        $this->newLine();
        $this->info('Step 6: Migrating Sieve scripts...');
        $this->migrateSieveScripts();

        // Step 7: Stop legacy services, start Stalwart
        $this->newLine();
        $this->info('Step 7: Switching mail services...');
        $this->switchServices();

        // Step 8: Connectivity tests
        if (! $skipTest) {
            $this->newLine();
            $this->info('Step 8: Running connectivity tests...');
            $testResult = $this->testConnectivity();
            if (! $testResult) {
                $this->error('Connectivity tests failed! Run --rollback to restore legacy stack.');

                return self::FAILURE;
            }
        }

        // Step 9: Update .env
        $this->newLine();
        $this->info('Step 9: Updating .env...');
        $this->updateEnvFile('MAIL_BACKEND', 'stalwart');

        // Clear config cache
        $this->call('config:clear');

        $this->newLine();
        $this->info('Migration complete! Stalwart Mail Server is now active.');
        $this->line('Run "jabali:mail-migrate --rollback" to restore legacy stack if needed.');

        return self::SUCCESS;
    }

    /**
     * @return array<int, array{0: string, 1: string}>
     */
    private function detectLegacyStack(): array
    {
        $services = [];
        foreach (['postfix', 'dovecot', 'opendkim', 'rspamd'] as $service) {
            $result = $this->executeCommand('systemctl is-active '.escapeshellarg($service).' 2>/dev/null');
            $status = trim($result['output']) ?: 'not installed';
            $services[] = [$service, $status];
        }

        return $services;
    }

    private function ensureStalwartInstalled(): bool
    {
        $result = $this->executeCommand('which stalwart 2>/dev/null || which /usr/local/bin/stalwart 2>/dev/null');
        if ($result['exitCode'] === 0 && $result['output'] !== '') {
            $this->line('  Stalwart binary found: '.trim($result['output']));

            return true;
        }

        $this->line('  Stalwart not found. Installing...');
        $archResult = $this->executeCommand('dpkg --print-architecture');
        $dpkgArch = trim($archResult['output']);
        $rustArch = match ($dpkgArch) {
            'amd64' => 'x86_64',
            'arm64' => 'aarch64',
            'armhf' => 'armv7',
            default => $dpkgArch,
        };
        $url = "https://github.com/stalwartlabs/mail-server/releases/latest/download/stalwart-{$rustArch}-unknown-linux-gnu.tar.gz";

        $result = $this->executeCommand(
            'curl -fsSL '.escapeshellarg($url).' | tar -xz -C /usr/local/bin/ stalwart && chmod 755 /usr/local/bin/stalwart',
            120
        );

        if ($result['exitCode'] !== 0) {
            $this->error('Failed to install Stalwart: '.$result['output']);

            return false;
        }

        $this->executeCommand('mkdir -p /etc/stalwart-mail /var/lib/stalwart-mail /var/log/stalwart-mail');
        $this->line('  Stalwart installed successfully.');

        return true;
    }

    private function generateStalwartConfig(): bool
    {
        $hostname = DB::table('dns_settings')->where('key', 'mail_hostname')->value('value')
            ?? gethostname();

        $apiToken = bin2hex(random_bytes(32));

        $confDir = '/etc/jabali';
        if (! is_dir($confDir)) {
            mkdir($confDir, 0750, true);
        }
        file_put_contents("{$confDir}/stalwart-api.conf", "STALWART_API_TOKEN={$apiToken}\n");
        chmod("{$confDir}/stalwart-api.conf", 0640);
        chown("{$confDir}/stalwart-api.conf", 'root');
        chgrp("{$confDir}/stalwart-api.conf", 'www-data');

        $dbConnection = config('database.default', 'mysql');
        $dbHost = config("database.connections.{$dbConnection}.host", '127.0.0.1');
        $dbUser = config("database.connections.{$dbConnection}.username", 'jabali');
        $dbPass = config("database.connections.{$dbConnection}.password", '');
        $dbName = config("database.connections.{$dbConnection}.database", 'jabali');

        $config = $this->buildStalwartToml($hostname, $apiToken, $dbHost, $dbUser, $dbPass, $dbName);

        // Check for existing Let's Encrypt cert before writing
        $certPath = "/etc/letsencrypt/live/{$hostname}/fullchain.pem";
        $keyPath = "/etc/letsencrypt/live/{$hostname}/privkey.pem";
        if (file_exists($certPath) && file_exists($keyPath)) {
            $config = str_replace(
                'certificate = "/etc/ssl/certs/ssl-cert-snakeoil.pem"',
                "certificate = \"{$certPath}\"",
                $config
            );
            $config = str_replace(
                'private-key = "/etc/ssl/private/ssl-cert-snakeoil.key"',
                "private-key = \"{$keyPath}\"",
                $config
            );
            $this->line("  Using existing Let's Encrypt certificate for {$hostname}");
        }

        if (file_put_contents('/etc/stalwart-mail/config.toml', $config) === false) {
            $this->error('Failed to write Stalwart configuration');

            return false;
        }
        chmod('/etc/stalwart-mail/config.toml', 0640);

        // Create systemd service
        if (! file_exists('/etc/systemd/system/stalwart-mail.service')) {
            $service = <<<'SYSTEMD'
[Unit]
Description=Stalwart Mail Server
After=network.target mysql.service mariadb.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/stalwart --config /etc/stalwart-mail/config.toml
Restart=always
RestartSec=5
# TODO: Run as dedicated 'stalwart' user instead of root (requires user creation and port capabilities)
User=root
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
SYSTEMD;
            file_put_contents('/etc/systemd/system/stalwart-mail.service', $service);
            $this->executeCommand('systemctl daemon-reload');
        }

        $this->line('  Stalwart configuration generated.');

        return true;
    }

    private function buildStalwartToml(
        string $hostname,
        string $apiToken,
        string $dbHost,
        string $dbUser,
        string $dbPass,
        string $dbName,
    ): string {
        $dbPassEncoded = rawurlencode($dbPass);

        return <<<TOML
# Stalwart Mail Server Configuration - Generated by Jabali Mail Migration
# https://stalw.art/docs/configuration

[server]
hostname = "{$hostname}"
admin-token = "{$apiToken}"

[server.listener.smtp]
bind = ["0.0.0.0:25"]
protocol = "smtp"

[server.listener.submission]
bind = ["0.0.0.0:587"]
protocol = "smtp"
tls.implicit = false
tls.starttls = true

[server.listener.smtps]
bind = ["0.0.0.0:465"]
protocol = "smtp"
tls.implicit = true

[server.listener.imap]
bind = ["0.0.0.0:143"]
protocol = "imap"
tls.implicit = false
tls.starttls = true

[server.listener.imaps]
bind = ["0.0.0.0:993"]
protocol = "imap"
tls.implicit = true

[server.listener.pop3s]
bind = ["0.0.0.0:995"]
protocol = "pop3"
tls.implicit = true

[server.listener.jmap]
bind = ["0.0.0.0:8080"]
protocol = "http"

[server.listener.managesieve]
bind = ["0.0.0.0:4190"]
protocol = "managesieve"

[server.tls]
certificate = "/etc/ssl/certs/ssl-cert-snakeoil.pem"
private-key = "/etc/ssl/private/ssl-cert-snakeoil.key"

[server.http]
admin-allowed-ips = ["127.0.0.1", "::1"]

[directory."sql"]
type = "sql"
address = "mysql://{$dbUser}:{$dbPassEncoded}@{$dbHost}/{$dbName}"

[directory."sql".query]
login = "SELECT CONCAT(m.local_part, '@', d.domain) AS name, m.password_hash AS secret FROM mailboxes m JOIN email_domains e ON m.email_domain_id = e.id JOIN domains d ON e.domain_id = d.id WHERE CONCAT(m.local_part, '@', d.domain) = ? AND m.is_active = 1 AND e.is_active = 1"
name = "SELECT CONCAT(m.local_part, '@', d.domain) AS name FROM mailboxes m JOIN email_domains e ON m.email_domain_id = e.id JOIN domains d ON e.domain_id = d.id WHERE CONCAT(m.local_part, '@', d.domain) = ? AND m.is_active = 1"
members = "SELECT CONCAT(m.local_part, '@', d.domain) AS member FROM mailboxes m JOIN email_domains e ON m.email_domain_id = e.id JOIN domains d ON e.domain_id = d.id WHERE m.is_active = 1"
domains = "SELECT DISTINCT d.domain FROM email_domains e JOIN domains d ON e.domain_id = d.id WHERE e.is_active = 1"

[storage]
directory = "sql"

[storage.data]
path = "/var/lib/stalwart-mail/data"

[storage.blob]
type = "fs"
path = "/var/lib/stalwart-mail/blobs"

[signature.dkim]
verify = true
sign = true

[spam-filter]
enabled = true

[spam-filter.header]
is-spam = "X-Spam-Status"

[queue]
path = "/var/lib/stalwart-mail/queue"

[tracing.log]
path = "/var/log/stalwart-mail/stalwart.log"
level = "info"
TOML;
    }

    private function importDkimKeys(): void
    {
        $dkimDir = '/etc/opendkim/keys';
        if (! is_dir($dkimDir)) {
            $this->line('  No DKIM keys directory found, skipping.');

            return;
        }

        $domains = glob($dkimDir.'/*', GLOB_ONLYDIR);
        $imported = 0;
        foreach ($domains as $domainDir) {
            $domain = basename($domainDir);
            if (! preg_match('/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*\.[a-z]{2,}$/i', $domain)) {
                continue;
            }
            $privateKey = $domainDir.'/default.private';
            if (! file_exists($privateKey) || is_link($privateKey)) {
                continue;
            }

            $stalwartDkimDir = "/var/lib/stalwart-mail/data/dkim/{$domain}";
            if (! is_dir($stalwartDkimDir)) {
                mkdir($stalwartDkimDir, 0700, true);
            }
            copy($privateKey, "{$stalwartDkimDir}/default.private");
            chmod("{$stalwartDkimDir}/default.private", 0600);

            $publicKeyFile = $domainDir.'/default.txt';
            if (file_exists($publicKeyFile)) {
                copy($publicKeyFile, "{$stalwartDkimDir}/default.txt");
            }

            $imported++;
        }

        $this->line("  Imported DKIM keys for {$imported} domain(s).");
    }

    private function migrateSieveScripts(): void
    {
        $vmailDir = '/var/vmail';
        if (! is_dir($vmailDir)) {
            $this->line('  No Sieve scripts directory found, skipping.');

            return;
        }

        $migrated = 0;
        $domains = glob($vmailDir.'/*', GLOB_ONLYDIR);
        foreach ($domains as $domainDir) {
            $domain = basename($domainDir);
            if (! preg_match('/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*\.[a-z]{2,}$/i', $domain)) {
                continue;
            }
            $users = glob($domainDir.'/*', GLOB_ONLYDIR);
            foreach ($users as $userDir) {
                $localPart = basename($userDir);
                if (! preg_match('/^[a-z0-9][a-z0-9._-]{0,63}$/i', $localPart)) {
                    continue;
                }
                $sieveFile = $userDir.'/sieve/vacation.sieve';
                if (! file_exists($sieveFile) || is_link($sieveFile)) {
                    continue;
                }

                $stalwartSieveDir = "/var/lib/stalwart-mail/data/sieve/{$localPart}@{$domain}";
                if (! is_dir($stalwartSieveDir)) {
                    mkdir($stalwartSieveDir, 0700, true);
                }
                copy($sieveFile, "{$stalwartSieveDir}/vacation.sieve");
                $migrated++;
            }
        }

        $this->line("  Migrated {$migrated} Sieve script(s).");
    }

    private function switchServices(): void
    {
        foreach (['postfix', 'dovecot', 'opendkim', 'rspamd'] as $service) {
            $this->executeCommand('systemctl stop '.escapeshellarg($service).' 2>/dev/null');
            $this->executeCommand('systemctl disable '.escapeshellarg($service).' 2>/dev/null');
            $this->line("  Stopped and disabled {$service}");
        }

        $this->executeCommand('systemctl enable stalwart-mail');
        $result = $this->executeCommand('systemctl start stalwart-mail');
        if ($result['exitCode'] !== 0) {
            $this->error('Failed to start Stalwart: '.$result['output']);
            $this->warn('You may need to run --rollback to restore services.');

            return;
        }

        $this->line('  Stalwart Mail Server started.');
    }

    private function testConnectivity(): bool
    {
        $success = true;

        // Test SMTP
        $this->line('  Testing SMTP (port 25)...');
        $result = $this->executeCommand("bash -c 'echo QUIT | nc -w 5 127.0.0.1 25'", 10);
        if (! str_contains($result['output'], '220')) {
            $this->warn('  SMTP test failed');
            $success = false;
        } else {
            $this->line('  SMTP: OK');
        }

        // Test IMAP
        $this->line('  Testing IMAP (port 143)...');
        $result = $this->executeCommand("bash -c 'echo \"a1 LOGOUT\" | nc -w 5 127.0.0.1 143'", 10);
        if (! str_contains($result['output'], 'OK') && ! str_contains($result['output'], 'IMAP')) {
            $this->warn('  IMAP test failed');
            $success = false;
        } else {
            $this->line('  IMAP: OK');
        }

        // Test JMAP
        $this->line('  Testing JMAP (port 8080)...');
        $result = $this->executeCommand('curl -sf http://127.0.0.1:8080/.well-known/jmap 2>/dev/null', 10);
        if ($result['exitCode'] !== 0) {
            $this->warn('  JMAP test failed (may need authentication)');
        } else {
            $this->line('  JMAP: OK');
        }

        return $success;
    }

    private function rollback(): int
    {
        $this->info('Rolling back to legacy mail stack...');

        $this->executeCommand('systemctl stop stalwart-mail 2>/dev/null');
        $this->executeCommand('systemctl disable stalwart-mail 2>/dev/null');
        $this->line('  Stopped Stalwart');

        foreach (['opendkim', 'dovecot', 'postfix'] as $service) {
            $this->executeCommand('systemctl enable '.escapeshellarg($service).' 2>/dev/null');
            $this->executeCommand('systemctl start '.escapeshellarg($service).' 2>/dev/null');
            $this->line("  Started {$service}");
        }

        $this->updateEnvFile('MAIL_BACKEND', 'legacy');
        $this->call('config:clear');

        $this->info('Rollback complete. Legacy mail stack restored.');

        return self::SUCCESS;
    }

    private function updateEnvFile(string $key, string $value): void
    {
        $envPath = base_path('.env');
        if (! file_exists($envPath)) {
            return;
        }

        $content = file_get_contents($envPath);
        $escaped = preg_quote($key, '/');

        if (preg_match("/^{$escaped}=.*/m", $content)) {
            $content = preg_replace("/^{$escaped}=.*/m", "{$key}={$value}", $content);
        } else {
            $content .= "\n{$key}={$value}\n";
        }

        file_put_contents($envPath, $content);
        $this->line("  Updated .env: {$key}={$value}");
    }

    /**
     * @return array{exitCode: int, output: string}
     */
    private function executeCommand(string $command, int $timeout = 60): array
    {
        $process = Process::fromShellCommandline($command, $this->basePath);
        $process->setTimeout($timeout);
        $process->run();
        $output = trim($process->getOutput().$process->getErrorOutput());

        return [
            'exitCode' => $process->getExitCode() ?? 1,
            'output' => $output,
        ];
    }
}
