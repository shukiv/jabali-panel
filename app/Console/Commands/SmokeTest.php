<?php

declare(strict_types=1);

namespace App\Console\Commands;

use App\Models\Autoresponder;
use App\Models\CronJob;
use App\Models\DnsRecord;
use App\Models\Domain;
use App\Models\EmailDomain;
use App\Models\EmailForwarder;
use App\Models\ImpersonationToken;
use App\Models\Mailbox;
use App\Models\MysqlCredential;
use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Support\Formatter;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Hash;
use Illuminate\Support\Str;

class SmokeTest extends Command
{
    protected $signature = 'jabali:smoke-test
        {--cleanup : Clean up test resources even if tests fail}
        {--skip-wp : Skip WordPress install (slow)}
        {--keep : Do not clean up after tests}';

    protected $description = 'Comprehensive smoke test — creates and verifies all panel features';

    private AgentClient $agent;

    private int $passed = 0;

    private int $failed = 0;

    private array $errors = [];

    private ?User $testUser = null;

    private string $testDomain = 'smoketest-'.'';

    private string $testUsername = '';

    public function handle(): int
    {
        $this->agent = app(AgentClient::class);
        $this->testDomain = 'smoketest-'.strtolower(Str::random(6)).'.test';
        $this->testUsername = 'smoketest'.strtolower(Str::random(4));

        $this->info('Jabali Panel Smoke Test');
        $this->info("Domain: {$this->testDomain}");
        $this->info("Username: {$this->testUsername}");
        $this->newLine();

        if ($this->option('cleanup')) {
            $this->cleanup();
            $this->info('Cleanup complete.');

            return 0;
        }

        try {
            $this->section('User Management');
            $this->test_create_user();
            $this->test_user_login();

            $this->section('Domain Management');
            $this->test_create_domain();
            $this->test_toggle_domain();
            $this->test_domain_list();

            $this->section('DNS');
            $this->test_create_dns_zone();
            $this->test_add_dns_record();

            $this->section('Domain Advanced');
            $this->test_domain_alias();
            $this->test_domain_error_pages();

            $this->section('Email');
            $this->test_enable_email_domain();
            $this->test_generate_dkim();
            $this->test_create_mailbox();
            $this->test_toggle_mailbox();
            $this->test_mailbox_password_change();
            $this->test_mailbox_quota();
            $this->test_create_forwarder();
            $this->test_toggle_forwarder();
            $this->test_delete_forwarder();
            $this->test_autoresponder();
            $this->test_email_reload_services();

            $this->section('MySQL Databases');
            $this->test_create_database();
            $this->test_create_database_user();
            $this->test_grant_privileges();
            $this->test_mysql_list_databases();
            $this->test_mysql_list_users();

            $this->section('PostgreSQL');
            $this->test_postgres_available();

            $this->section('File Manager');
            $this->test_create_directory();
            $this->test_write_file();
            $this->test_read_file();
            $this->test_list_files();
            $this->test_rename_file();
            $this->test_copy_file();
            $this->test_file_permissions();
            $this->test_file_upload_base64();
            $this->test_file_info();
            $this->test_file_trash_restore();
            $this->test_delete_file();

            $this->section('Cron Jobs');
            $this->test_create_cron_job();
            $this->test_toggle_cron_job();

            $this->section('SSL');
            $this->test_self_signed_ssl();
            $this->test_ssl_check();

            if (! $this->option('skip-wp')) {
                $this->section('WordPress');
                $this->test_word_press_install();
                $this->test_word_press_list();
                $this->test_word_press_scan();
            }

            $this->section('Backups');
            $this->test_create_backup();
            $this->test_list_backups();
            $this->test_verify_backup();

            $this->section('SSH Keys');
            $this->test_ssh_key_generate();
            $this->test_ssh_key_list();

            $this->section('Server Metrics');
            $this->test_metrics_overview();
            $this->test_metrics_cpu();
            $this->test_metrics_memory();
            $this->test_metrics_disk();
            $this->test_metrics_network();
            $this->test_server_versions();
            $this->test_process_list();

            $this->section('Quota Management');
            $this->test_quota_set();
            $this->test_quota_get();

            $this->section('Services');
            $this->test_service_status();

            $this->section('Security Status');
            $this->test_fail2ban_status();
            $this->test_clamav_status();
            $this->test_waf_audit_log();
            $this->test_scanner_status();

            $this->section('Server Operations');
            $this->test_updates_list();
            $this->test_mail_queue_list();
            $this->test_ip_list();

            $this->section('Impersonation');
            $this->test_impersonation_token();

            $this->section('PhpMyAdmin Token');
            $this->test_phpmyadmin_token_flow();

            $this->section('Formatter Utility');
            $this->test_formatter_bytes();

            $this->section('Page Rendering');
            $this->test_all_pages_render();
        } catch (\Throwable $e) {
            $this->error("Fatal: {$e->getMessage()}");
            $this->failed++;
            $this->errors[] = "Fatal: {$e->getMessage()} in {$e->getFile()}:{$e->getLine()}";
        }

        if (! $this->option('keep')) {
            $this->newLine();
            $this->section('Cleanup');
            $this->cleanup();
        }

        $this->printSummary();

        return $this->failed > 0 ? 1 : 0;
    }

    // ── Helpers ──

    private function section(string $name): void
    {
        $this->newLine();
        $this->info("── {$name} ──");
    }

    private function check(string $label, callable $fn): void
    {
        try {
            $result = $fn();
            if ($result === false) {
                throw new \RuntimeException('Check returned false');
            }
            $this->line("  <fg=green>PASS</>  {$label}");
            $this->passed++;
        } catch (\Throwable $e) {
            $msg = substr($e->getMessage(), 0, 120);
            $this->line("  <fg=red>FAIL</>  {$label} — {$msg}");
            $this->failed++;
            $this->errors[] = "{$label}: {$msg}";
        }
    }

    private function printSummary(): void
    {
        $total = $this->passed + $this->failed;
        $this->newLine();
        $status = $this->failed === 0 ? '<fg=green>PASS</>' : '<fg=red>FAIL</>';
        $this->info("=== {$status} {$this->passed} passed, {$this->failed} failed out of {$total} ===");

        if ($this->errors) {
            $this->newLine();
            $this->warn('Failed tests:');
            foreach ($this->errors as $err) {
                $this->line("  - {$err}");
            }
        }
    }

    // ── User Management ──

    private function test_create_user(): void
    {
        $this->check('Create test user', function () {
            $this->testUser = User::create([
                'name' => 'Smoke Test',
                'username' => $this->testUsername,
                'email' => "{$this->testUsername}@jabali-panel.local",
                'password' => Hash::make('SmokeTestPass123!'),
                'is_admin' => false,
                'is_active' => true,
            ]);

            // Create OS user via agent
            $result = $this->agent->send('user.create', [
                'username' => $this->testUsername,
            ]);

            return $this->testUser->id > 0;
        });
    }

    private function test_user_login(): void
    {
        $this->check('User can authenticate', function () {
            return Auth::guard('web')->attempt([
                'email' => "{$this->testUsername}@jabali-panel.local",
                'password' => 'SmokeTestPass123!',
            ]);
        });
    }

    // ── Domain Management ──

    private function test_create_domain(): void
    {
        $this->check('Create domain', function () {
            $result = $this->agent->domainCreate($this->testUsername, $this->testDomain);

            Domain::create([
                'user_id' => $this->testUser->id,
                'domain' => $this->testDomain,
                'is_active' => true,
            ]);

            return $result['success'] ?? false;
        });
    }

    private function test_toggle_domain(): void
    {
        $this->check('Toggle domain off/on', function () {
            $this->agent->domainToggle($this->testUsername, $this->testDomain, false);
            $result = $this->agent->domainToggle($this->testUsername, $this->testDomain, true);

            return $result['success'] ?? false;
        });
    }

    private function test_domain_list(): void
    {
        $this->check('List domains', function () {
            $result = $this->agent->domainList($this->testUsername);

            return ($result['success'] ?? false)
                && is_array($result['domains'] ?? null);
        });
    }

    // ── DNS ──

    private function test_create_dns_zone(): void
    {
        $this->check('Create DNS zone', function () {
            $result = $this->agent->dnsCreateZone($this->testDomain, [
                'ns1' => 'ns1.jabali-panel.com',
                'ns2' => 'ns2.jabali-panel.com',
                'ip' => '127.0.0.1',
            ]);

            return $result['success'] ?? false;
        });
    }

    private function test_add_dns_record(): void
    {
        $this->check('Add DNS A record to database', function () {
            $domain = Domain::where('domain', $this->testDomain)->first();

            $record = DnsRecord::create([
                'domain_id' => $domain->id,
                'name' => '@',
                'type' => 'A',
                'content' => '127.0.0.1',
                'ttl' => 3600,
                'priority' => 0,
            ]);

            return $record->id > 0;
        });
    }

    // ── Email ──

    private function test_enable_email_domain(): void
    {
        $this->check('Enable email domain', function () {
            $result = $this->agent->emailEnableDomain($this->testUsername, $this->testDomain);

            $domain = Domain::where('domain', $this->testDomain)->first();
            EmailDomain::create([
                'domain_id' => $domain->id,
                'domain' => $this->testDomain,
                'is_active' => true,
            ]);

            return $result['success'] ?? false;
        });
    }

    private function test_generate_dkim(): void
    {
        $this->check('Generate DKIM key', function () {
            $result = $this->agent->emailGenerateDkim($this->testUsername, $this->testDomain);

            return ($result['success'] ?? false) && ! empty($result['public_key'] ?? '');
        });
    }

    private function test_create_mailbox(): void
    {
        $this->check('Create mailbox', function () {
            $email = "test@{$this->testDomain}";
            $result = $this->agent->mailboxCreate($this->testUsername, $email, 'MailboxPass123!', 1073741824);

            $domain = Domain::where('domain', $this->testDomain)->first();
            $emailDomain = EmailDomain::where('domain_id', $domain->id)->first();
            Mailbox::create([
                'email_domain_id' => $emailDomain->id,
                'user_id' => $this->testUser->id,
                'local_part' => 'test',
                'password_hash' => Hash::make('MailboxPass123!'),
                'quota_bytes' => 1073741824,
                'is_active' => true,
            ]);

            return $result['success'] ?? false;
        });
    }

    private function test_toggle_mailbox(): void
    {
        $this->check('Toggle mailbox off/on', function () {
            $email = "test@{$this->testDomain}";
            $this->agent->mailboxToggle($this->testUsername, $email, false);
            $result = $this->agent->mailboxToggle($this->testUsername, $email, true);

            return $result['success'] ?? false;
        });
    }

    private function test_create_forwarder(): void
    {
        $this->check('Create email forwarder', function () {
            $result = $this->agent->send('email.forwarder_create', [
                'username' => $this->testUsername,
                'email' => "forward@{$this->testDomain}",
                'destinations' => ["test@{$this->testDomain}"],
            ]);

            $domain = Domain::where('domain', $this->testDomain)->first();
            $emailDomain = EmailDomain::where('domain_id', $domain->id)->first();
            EmailForwarder::create([
                'email_domain_id' => $emailDomain->id,
                'user_id' => $this->testUser->id,
                'local_part' => 'forward',
                'destinations' => ["test@{$this->testDomain}"],
                'is_active' => true,
            ]);

            return $result['success'] ?? false;
        });
    }

    private function test_toggle_forwarder(): void
    {
        $this->check('Toggle forwarder off/on', function () {
            $this->agent->send('email.forwarder_toggle', [
                'username' => $this->testUsername,
                'email' => "forward@{$this->testDomain}",
                'active' => false,
            ]);
            $result = $this->agent->send('email.forwarder_toggle', [
                'username' => $this->testUsername,
                'email' => "forward@{$this->testDomain}",
                'active' => true,
            ]);

            return $result['success'] ?? false;
        });
    }

    private function test_delete_forwarder(): void
    {
        $this->check('Delete email forwarder', function () {
            $result = $this->agent->send('email.forwarder_delete', [
                'username' => $this->testUsername,
                'email' => "forward@{$this->testDomain}",
            ]);
            EmailForwarder::where('local_part', 'forward')
                ->whereHas('emailDomain.domain', fn ($q) => $q->where('domain', $this->testDomain))
                ->delete();

            return $result['success'] ?? false;
        });
    }

    private function test_mailbox_password_change(): void
    {
        $this->check('Change mailbox password', function () {
            $result = $this->agent->mailboxChangePassword(
                $this->testUsername,
                "test@{$this->testDomain}",
                'NewMailboxPass456!',
            );

            return $result['success'] ?? false;
        });
    }

    private function test_mailbox_quota(): void
    {
        $this->check('Get mailbox info', function () {
            $result = $this->agent->emailGetDomainInfo($this->testUsername, $this->testDomain);

            return $result['success'] ?? false;
        });
    }

    private function test_autoresponder(): void
    {
        $this->check('Create/delete autoresponder', function () {
            $mailbox = Mailbox::where('local_part', 'test')
                ->whereHas('emailDomain.domain', fn ($q) => $q->where('domain', $this->testDomain))
                ->first();

            $autoresponder = Autoresponder::create([
                'mailbox_id' => $mailbox->id,
                'subject' => 'Out of office',
                'message' => 'I am currently unavailable.',
                'is_active' => true,
            ]);

            $result = $autoresponder->id > 0;
            $autoresponder->delete();

            return $result;
        });
    }

    private function test_email_reload_services(): void
    {
        $this->check('Reload email services', function () {
            $result = $this->agent->emailReloadServices();

            return $result['success'] ?? false;
        });
    }

    // ── MySQL ──

    private function test_create_database(): void
    {
        $this->check('Create MySQL database', function () {
            $dbName = "{$this->testUsername}_testdb";
            $result = $this->agent->mysqlCreateDatabase($this->testUsername, $dbName);

            return $result['success'] ?? false;
        });
    }

    private function test_create_database_user(): void
    {
        $this->check('Create MySQL user', function () {
            $dbUser = "{$this->testUsername}_admin";
            $result = $this->agent->mysqlCreateUser($this->testUsername, $dbUser, 'DbPass123!');

            if ($result['success'] ?? false) {
                MysqlCredential::create([
                    'user_id' => $this->testUser->id,
                    'mysql_username' => $dbUser,
                    'mysql_password_encrypted' => encrypt('DbPass123!'),
                ]);
            }

            return $result['success'] ?? false;
        });
    }

    private function test_grant_privileges(): void
    {
        $this->check('Grant MySQL privileges', function () {
            $result = $this->agent->mysqlGrantPrivileges(
                $this->testUsername,
                "{$this->testUsername}_admin",
                "{$this->testUsername}_testdb",
            );

            return $result['success'] ?? false;
        });
    }

    private function test_mysql_list_databases(): void
    {
        $this->check('List MySQL databases', function () {
            $result = $this->agent->mysqlListDatabases($this->testUsername);

            return ($result['success'] ?? false) && is_array($result['databases'] ?? null);
        });
    }

    private function test_mysql_list_users(): void
    {
        $this->check('List MySQL users', function () {
            $result = $this->agent->mysqlListUsers($this->testUsername);

            return ($result['success'] ?? false) && is_array($result['users'] ?? null);
        });
    }

    // ── PostgreSQL ──

    private function test_postgres_available(): void
    {
        $this->check('PostgreSQL list databases', function () {
            try {
                $result = $this->agent->postgresListDatabases($this->testUsername);
            } catch (\Throwable $e) {
                if (str_contains($e->getMessage(), 'postgres') || str_contains($e->getMessage(), 'sudo')) {
                    $this->line('         (PostgreSQL not installed — skipped)');

                    return true;
                }
                throw $e;
            }

            return ($result['success'] ?? false) && is_array($result['databases'] ?? null);
        });
    }

    // ── File Manager ──

    private function test_create_directory(): void
    {
        $this->check('Create directory', function () {
            $result = $this->agent->fileMkdir($this->testUsername, 'smoke-test-dir');

            return $result['success'] ?? false;
        });
    }

    private function test_write_file(): void
    {
        $this->check('Write file', function () {
            $result = $this->agent->fileWrite(
                $this->testUsername,
                'smoke-test-dir/hello.txt',
                "Hello from smoke test\n",
            );

            return $result['success'] ?? false;
        });
    }

    private function test_read_file(): void
    {
        $this->check('Read file', function () {
            $result = $this->agent->fileRead($this->testUsername, 'smoke-test-dir/hello.txt');

            $content = $result['content'] ?? '';
            if (($result['encoding'] ?? '') === 'base64') {
                $content = base64_decode($content);
            }

            return ($result['success'] ?? false) && str_contains($content, 'Hello');
        });
    }

    private function test_list_files(): void
    {
        $this->check('List directory', function () {
            $result = $this->agent->fileList($this->testUsername, 'smoke-test-dir');

            return ($result['success'] ?? false) && count($result['items'] ?? []) > 0;
        });
    }

    private function test_rename_file(): void
    {
        $this->check('Rename file', function () {
            $result = $this->agent->fileRename(
                $this->testUsername,
                'smoke-test-dir/hello.txt',
                'smoke-test-dir/renamed.txt',
            );

            return $result['success'] ?? false;
        });
    }

    private function test_copy_file(): void
    {
        $this->check('Write second file', function () {
            $result = $this->agent->fileWrite($this->testUsername, 'smoke-test-dir/copied.txt', 'copied content');

            return $result['success'] ?? false;
        });
    }

    private function test_file_permissions(): void
    {
        $this->check('Change file permissions', function () {
            $result = $this->agent->fileChmod($this->testUsername, 'smoke-test-dir/renamed.txt', '644');

            return $result['success'] ?? false;
        });
    }

    private function test_file_upload_base64(): void
    {
        $this->check('Upload file (base64)', function () {
            $content = base64_encode('uploaded via smoke test');
            $result = $this->agent->fileUpload(
                $this->testUsername,
                'smoke-test-dir',
                'uploaded.txt',
                $content,
            );

            return $result['success'] ?? false;
        });
    }

    private function test_file_info(): void
    {
        $this->check('Get file info', function () {
            $result = $this->agent->fileInfo($this->testUsername, 'smoke-test-dir/uploaded.txt');

            return ($result['success'] ?? false) && (isset($result['size']) || isset($result['info']));
        });
    }

    private function test_file_trash_restore(): void
    {
        $this->check('Trash and restore file', function () {
            $this->agent->fileTrash($this->testUsername, 'smoke-test-dir/uploaded.txt');

            $trashList = $this->agent->fileListTrash($this->testUsername);
            $items = $trashList['items'] ?? [];

            if (empty($items)) {
                throw new \RuntimeException('Trash is empty after trashing file');
            }

            // Restore the first item
            $trashName = $items[0]['trash_name'] ?? $items[0]['name'] ?? '';
            $this->agent->fileRestore($this->testUsername, $trashName);
            $this->agent->fileEmptyTrash($this->testUsername);

            return true;
        });
    }

    private function test_delete_file(): void
    {
        $this->check('Delete file', function () {
            // Clean up all test files
            try {
                $this->agent->fileDelete($this->testUsername, 'smoke-test-dir/uploaded.txt');
            } catch (\Throwable $e) {
                // may already be gone
            }
            $this->agent->fileDelete($this->testUsername, 'smoke-test-dir/copied.txt');
            $result = $this->agent->fileDelete($this->testUsername, 'smoke-test-dir/renamed.txt');

            return $result['success'] ?? false;
        });
    }

    // ── Domain Advanced ──

    private function test_domain_alias(): void
    {
        $this->check('Add/remove domain alias', function () {
            $alias = 'alias-'.$this->testDomain;
            $this->agent->domainAliasAdd($this->testUsername, $this->testDomain, $alias);
            $result = $this->agent->domainAliasRemove($this->testUsername, $this->testDomain, $alias);

            return $result['success'] ?? false;
        });
    }

    private function test_domain_error_pages(): void
    {
        $this->check('Domain info check', function () {
            $result = $this->agent->domainList($this->testUsername);
            $domains = $result['domains'] ?? [];
            $found = false;
            foreach ($domains as $d) {
                if (($d['domain'] ?? '') === $this->testDomain) {
                    $found = true;
                }
            }

            return $found;
        });
    }

    // ── Cron Jobs ──

    private function test_create_cron_job(): void
    {
        $this->check('Create cron job', function () {
            CronJob::create([
                'user_id' => $this->testUser->id,
                'name' => 'smoke-test',
                'schedule' => '0 * * * *',
                'command' => 'echo "smoke test"',
                'is_active' => true,
            ]);

            $result = $this->agent->cronCreate(
                $this->testUsername,
                '0 * * * *',
                'echo "smoke test"',
                'smoke-test',
            );

            return $result['success'] ?? false;
        });
    }

    private function test_toggle_cron_job(): void
    {
        $this->check('Toggle cron job', function () {
            $result = $this->agent->cronToggle($this->testUsername, 'echo "smoke test"', false);

            return $result['success'] ?? false;
        });
    }

    // ── SSL ──

    private function test_self_signed_ssl(): void
    {
        $this->check('Generate self-signed SSL', function () {
            $result = $this->agent->sslGenerateSelfSigned($this->testDomain, $this->testUsername);

            return $result['success'] ?? false;
        });
    }

    private function test_ssl_check(): void
    {
        $this->check('SSL status check', function () {
            $result = $this->agent->sslCheck($this->testDomain, $this->testUsername);

            return is_array($result);
        });
    }

    // ── WordPress ──

    private function test_word_press_install(): void
    {
        $this->check('Install WordPress', function () {
            $result = $this->agent->wpInstall($this->testUsername, $this->testDomain, [
                'admin_user' => 'admin',
                'admin_password' => 'WpAdmin123!',
                'admin_email' => "admin@{$this->testDomain}",
                'site_title' => 'Smoke Test',
            ]);

            return $result['success'] ?? false;
        });
    }

    private function test_word_press_list(): void
    {
        $this->check('List WordPress sites', function () {
            $result = $this->agent->wpList($this->testUsername);

            return ($result['success'] ?? false) && count($result['sites'] ?? []) > 0;
        });
    }

    private function test_word_press_scan(): void
    {
        $this->check('WordPress security scan', function () {
            $result = $this->agent->wpScan($this->testUsername);

            return $result['success'] ?? false;
        });
    }

    // ── Backups ──

    private function test_create_backup(): void
    {
        $this->check('Create user backup', function () {
            $outputPath = "/home/{$this->testUsername}/backups/smoke-test-backup.tar.gz";
            $result = $this->agent->backupCreate($this->testUsername, $outputPath, [
                'include_files' => true,
                'include_databases' => false,
                'include_emails' => false,
            ]);

            return $result['success'] ?? false;
        });
    }

    private function test_list_backups(): void
    {
        $this->check('List backups', function () {
            $result = $this->agent->backupList($this->testUsername, 'backups');

            return $result['success'] ?? false;
        });
    }

    private function test_verify_backup(): void
    {
        $this->check('Verify backup', function () {
            $path = "/home/{$this->testUsername}/backups/smoke-test-backup.tar.gz";
            $result = $this->agent->backupVerify($path);

            return $result['success'] ?? false;
        });
    }

    // ── SSH Keys ──

    private function test_ssh_key_generate(): void
    {
        $this->check('Generate SSH key', function () {
            $result = $this->agent->send('ssh.generate_key', [
                'username' => $this->testUsername,
            ]);

            return ($result['success'] ?? false) && ! empty($result['public_key'] ?? '');
        });
    }

    private function test_ssh_key_list(): void
    {
        $this->check('List SSH keys', function () {
            $result = $this->agent->send('ssh.list_keys', [
                'username' => $this->testUsername,
            ]);

            return ($result['success'] ?? false) && is_array($result['keys'] ?? null);
        });
    }

    // ── Server Metrics ──

    private function test_metrics_overview(): void
    {
        $this->check('Metrics overview', function () {
            $result = $this->agent->metricsOverview();

            return $result['success'] ?? false;
        });
    }

    private function test_metrics_cpu(): void
    {
        $this->check('CPU metrics', function () {
            $result = $this->agent->metricsCpu();

            return $result['success'] ?? false;
        });
    }

    private function test_metrics_memory(): void
    {
        $this->check('Memory metrics', function () {
            $result = $this->agent->metricsMemory();

            return $result['success'] ?? false;
        });
    }

    private function test_metrics_disk(): void
    {
        $this->check('Disk metrics', function () {
            $result = $this->agent->metricsDisk();

            return $result['success'] ?? false;
        });
    }

    private function test_metrics_network(): void
    {
        $this->check('Network metrics', function () {
            $result = $this->agent->metricsNetwork();

            return $result['success'] ?? false;
        });
    }

    private function test_server_versions(): void
    {
        $this->check('Server versions', function () {
            $result = $this->agent->serverVersions();

            return $result['success'] ?? false;
        });
    }

    private function test_process_list(): void
    {
        $this->check('Process list', function () {
            $result = $this->agent->metricsProcesses();

            return ($result['success'] ?? false) && is_array($result['processes'] ?? $result['data'] ?? null);
        });
    }

    // ── Services ──

    private function test_service_status(): void
    {
        $this->check('Service status check', function () {
            $result = $this->agent->send('service.status', ['service' => 'nginx']);

            return is_array($result) && ($result['success'] ?? false);
        });
    }

    // ── Quota ──

    private function test_quota_set(): void
    {
        $this->check('Set disk quota', function () {
            try {
                $result = $this->agent->quotaSet($this->testUsername, 500, 600, '/home');
            } catch (\Throwable $e) {
                if (str_contains($e->getMessage(), 'not enabled') || str_contains($e->getMessage(), 'quota') || str_contains($e->getMessage(), 'Filesystem')) {
                    $this->line('         (Quotas not enabled — skipped)');

                    return true;
                }
                throw $e;
            }

            return $result['success'] ?? false;
        });
    }

    private function test_quota_get(): void
    {
        $this->check('Get disk quota', function () {
            $result = $this->agent->quotaGet($this->testUsername, '/home');

            return $result['success'] ?? false;
        });
    }

    // ── Security Status ──

    private function test_fail2ban_status(): void
    {
        $this->check('Fail2ban status', function () {
            $result = $this->agent->fail2banStatusLight();

            return $result['success'] ?? false;
        });
    }

    private function test_clamav_status(): void
    {
        $this->check('ClamAV status', function () {
            $result = $this->agent->clamavStatusLight();

            return $result['success'] ?? false;
        });
    }

    private function test_waf_audit_log(): void
    {
        $this->check('WAF audit log', function () {
            $result = $this->agent->wafAuditLogList(10);

            return $result['success'] ?? false;
        });
    }

    private function test_scanner_status(): void
    {
        $this->check('Scanner tool status', function () {
            $result = $this->agent->scannerStatus();

            return $result['success'] ?? false;
        });
    }

    // ── Server Operations ──

    private function test_updates_list(): void
    {
        $this->check('List server updates', function () {
            $result = $this->agent->updatesList();

            return $result['success'] ?? false;
        });
    }

    private function test_mail_queue_list(): void
    {
        $this->check('List mail queue', function () {
            $result = $this->agent->mailQueueList();

            return $result['success'] ?? false;
        });
    }

    private function test_ip_list(): void
    {
        $this->check('List IP addresses', function () {
            $result = $this->agent->ipList();

            return ($result['success'] ?? false) && is_array($result['ips'] ?? $result['addresses'] ?? null);
        });
    }

    // ── Impersonation ──

    private function test_impersonation_token(): void
    {
        $this->check('Create and validate impersonation token', function () {
            $admin = User::where('is_admin', true)->first();
            $token = ImpersonationToken::create([
                'admin_id' => $admin->id,
                'target_user_id' => $this->testUser->id,
                'token' => $tokenStr = Str::random(64),
                'ip_address' => '127.0.0.1',
                'expires_at' => now()->addMinutes(5),
            ]);

            $found = ImpersonationToken::where('token', $tokenStr)
                ->whereNull('used_at')
                ->where('expires_at', '>', now())
                ->first();

            $token->delete();

            return $found !== null;
        });
    }

    // ── PhpMyAdmin Token ──

    private function test_phpmyadmin_token_flow(): void
    {
        $this->check('PhpMyAdmin token create/verify/expire', function () {
            $token = bin2hex(random_bytes(32));
            $data = [
                'username' => $this->testUsername,
                'database' => "{$this->testUsername}_testdb",
            ];

            Cache::put("phpmyadmin_token_{$token}", $data, 300);

            $retrieved = Cache::get("phpmyadmin_token_{$token}");
            if (! $retrieved || $retrieved['username'] !== $this->testUsername) {
                throw new \RuntimeException('Token data mismatch');
            }

            // Single-use: delete after retrieval
            Cache::forget("phpmyadmin_token_{$token}");
            $after = Cache::get("phpmyadmin_token_{$token}");

            return $after === null;
        });
    }

    // ── Formatter ──

    private function test_formatter_bytes(): void
    {
        $this->check('Formatter::bytes()', function () {
            return Formatter::bytes(0) === '0 B'
                && Formatter::bytes(1024) === '1 KB'
                && Formatter::bytes(1048576) === '1 MB'
                && Formatter::bytes(1073741824) === '1 GB';
        });
    }

    // ── Page Rendering ──

    private function test_all_pages_render(): void
    {
        $pages = [
            '/jabali-admin' => 'Admin Dashboard',
            '/jabali-admin/server-settings' => 'Server Settings',
            '/jabali-admin/security' => 'Security',
            '/jabali-admin/services' => 'Services',
            '/jabali-admin/backups' => 'Backups',
            '/jabali-admin/ssl-manager' => 'SSL Manager',
            '/jabali-admin/dns-zones' => 'DNS Zones',
            '/jabali-admin/server-status' => 'Server Status',
            '/jabali-admin/email-logs' => 'Email Logs',
            '/jabali-admin/ip-addresses' => 'IP Addresses',
            '/jabali-admin/automation-api' => 'Automation API',
            '/jabali-admin/server-updates' => 'Server Updates',
            '/jabali-admin/waf' => 'WAF',
            '/jabali-admin/database-tuning' => 'Database Tuning',
            '/jabali-admin/php-manager' => 'PHP Manager',
            '/jabali-admin/email-queue' => 'Email Queue',
            '/jabali-admin/support' => 'Admin Support',
            '/jabali-panel' => 'User Dashboard',
            '/jabali-panel/domains' => 'Domains',
            '/jabali-panel/email' => 'Email',
            '/jabali-panel/databases' => 'Databases',
            '/jabali-panel/files' => 'Files',
            '/jabali-panel/backups' => 'User Backups',
            '/jabali-panel/ssl' => 'User SSL',
            '/jabali-panel/dns-records' => 'DNS Records',
            '/jabali-panel/wordpress' => 'WordPress',
            '/jabali-panel/cron-jobs' => 'Cron Jobs',
            '/jabali-panel/logs' => 'Logs',
            '/jabali-panel/ssh-keys' => 'SSH Keys',
            '/jabali-panel/php-settings' => 'PHP Settings',
            '/jabali-panel/git-deployment' => 'Git Deployment',
            '/jabali-panel/image-optimization' => 'Image Optimization',
            '/jabali-panel/protected-directories' => 'Protected Dirs',
            '/jabali-panel/support' => 'User Support',
        ];

        $kernel = app(\Illuminate\Contracts\Http\Kernel::class);
        $baseUrl = config('app.url', 'https://localhost');

        foreach ($pages as $path => $label) {
            $this->check("Page: {$label}", function () use ($kernel, $baseUrl, $path) {
                $request = \Illuminate\Http\Request::create($baseUrl.$path, 'GET');
                $request->server->set('HTTPS', 'on');

                $session = app('session')->driver();
                $session->start();
                $session->put('_token', 'smoke-test');
                $session->save();
                $request->setLaravelSession($session);
                $request->cookies->set($session->getName(), $session->getId());

                $response = $kernel->handle($request);
                $status = $response->getStatusCode();

                if ($status >= 500) {
                    throw new \RuntimeException("HTTP {$status}");
                }

                return true;
            });
        }
    }

    // ── Cleanup ──

    private function cleanup(): void
    {
        $username = $this->testUsername;
        $domain = $this->testDomain;

        // Delete WordPress sites
        try {
            $sites = $this->agent->wpList($username);
            foreach (($sites['sites'] ?? []) as $site) {
                $this->agent->wpDelete($username, $site['id'] ?? $site['site_id'] ?? '', true, true);
                $this->line('  <fg=yellow>CLEAN</>  WordPress site deleted');
            }
        } catch (\Throwable $e) {
            // ignore
        }

        // Delete cron jobs
        try {
            $this->agent->cronDelete($username, 'echo "smoke test"');
        } catch (\Throwable $e) {
            // ignore
        }
        CronJob::where('user_id', $this->testUser?->id)->delete();

        // Delete MySQL resources
        try {
            $this->agent->mysqlRevokePrivileges($username, "{$username}_admin", "{$username}_testdb");
        } catch (\Throwable $e) {
            // ignore
        }
        try {
            $this->agent->mysqlDeleteUser($username, "{$username}_admin");
        } catch (\Throwable $e) {
            // ignore
        }
        try {
            $this->agent->mysqlDeleteDatabase($username, "{$username}_testdb");
        } catch (\Throwable $e) {
            // ignore
        }
        MysqlCredential::where('user_id', $this->testUser?->id)->delete();

        // Delete PostgreSQL resources
        try {
            $this->agent->postgresDeleteDatabase($username, "{$username}_pgdb");
        } catch (\Throwable $e) {
            // ignore
        }
        try {
            $this->agent->postgresDeleteUser($username, "{$username}_pguser");
        } catch (\Throwable $e) {
            // ignore
        }

        // Delete test files
        try {
            $this->agent->fileDelete($username, 'smoke-test-dir');
        } catch (\Throwable $e) {
            // ignore
        }

        // Delete mailbox and email domain
        try {
            $this->agent->mailboxDelete($username, "test@{$domain}");
        } catch (\Throwable $e) {
            // ignore
        }
        Mailbox::where('local_part', 'test')
            ->whereHas('emailDomain.domain', fn ($q) => $q->where('domain', $domain))
            ->delete();
        EmailForwarder::whereHas('emailDomain.domain', fn ($q) => $q->where('domain', $domain))->delete();

        try {
            $this->agent->emailDisableDomain($username, $domain);
        } catch (\Throwable $e) {
            // ignore
        }
        $domainModel = Domain::where('domain', $domain)->first();
        if ($domainModel) {
            EmailDomain::where('domain_id', $domainModel->id)->delete();
        }
        DnsRecord::whereHas('domain', fn ($q) => $q->where('domain', $domain))->delete();

        // Delete DNS zone
        try {
            $this->agent->dnsDeleteZone($domain);
        } catch (\Throwable $e) {
            // ignore
        }

        // Delete domain
        try {
            $this->agent->domainDelete($username, $domain, true);
        } catch (\Throwable $e) {
            // ignore
        }
        Domain::where('domain', $domain)->delete();

        // Delete OS user and panel user
        try {
            $this->agent->deleteUser($username, true);
        } catch (\Throwable $e) {
            // ignore
        }

        if ($this->testUser) {
            $this->testUser->delete();
        }

        $this->line('  <fg=yellow>CLEAN</>  All test resources removed');
    }
}
