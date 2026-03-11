<?php

declare(strict_types=1);

namespace App\Services\Agent;

use Exception;
use Illuminate\Support\Facades\Cache;

class AgentClient
{
    private string $socketPath;

    private int $timeout;

    public function __construct(string $socketPath = '/var/run/jabali/agent.sock', int $timeout = 120)
    {
        $this->socketPath = $socketPath;
        $this->timeout = $timeout;
    }

    public function send(string $action, array $params = []): array
    {
        $socket = @socket_create(AF_UNIX, SOCK_STREAM, 0);
        if (! $socket) {
            throw new Exception('Failed to create socket');
        }

        socket_set_option($socket, SOL_SOCKET, SO_RCVTIMEO, ['sec' => $this->timeout, 'usec' => 0]);
        socket_set_option($socket, SOL_SOCKET, SO_SNDTIMEO, ['sec' => $this->timeout, 'usec' => 0]);

        if (! @socket_connect($socket, $this->socketPath)) {
            socket_close($socket);
            throw new Exception('Failed to connect to agent socket');
        }

        // Sanitize params to remove any control characters that might break JSON
        $sanitizedParams = $this->sanitizeForJson($params);

        $request = json_encode(['action' => $action, 'params' => $sanitizedParams], JSON_INVALID_UTF8_SUBSTITUTE | JSON_UNESCAPED_UNICODE);
        if ($request === false) {
            socket_close($socket);
            throw new Exception('JSON encode failed: '.json_last_error_msg());
        }
        socket_write($socket, $request, strlen($request));

        $response = '';
        while (true) {
            $buf = socket_read($socket, 8192);
            if ($buf === '' || $buf === false) {
                break;
            }
            $response .= $buf;
        }

        socket_close($socket);

        $decoded = json_decode($response, true);
        if (json_last_error() !== JSON_ERROR_NONE) {
            throw new Exception('Invalid response from agent: '.$response);
        }

        if (isset($decoded['error'])) {
            throw new Exception($decoded['error']);
        }

        return $decoded;
    }

    /**
     * Cache lightweight metrics to reduce socket churn on polling pages.
     */
    private function cachedMetrics(string $key, int $seconds, callable $callback): array
    {
        if ($seconds <= 0) {
            return $callback();
        }

        return Cache::remember($key, now()->addSeconds($seconds), function () use ($callback): array {
            return $callback();
        });
    }

    /**
     * Recursively sanitize array values to ensure they are JSON-safe.
     * Removes control characters (except newlines/tabs) from strings.
     */
    private function sanitizeForJson(array $data): array
    {
        foreach ($data as $key => $value) {
            if (is_array($value)) {
                $data[$key] = $this->sanitizeForJson($value);
            } elseif (is_string($value)) {
                // Remove control characters except tab (0x09), newline (0x0A), carriage return (0x0D)
                // But for base64 content (which should be the 'content' key), it should be safe already
                if ($key !== 'content') {
                    $data[$key] = preg_replace('/[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]/', '', $value);
                }
            }
        }

        return $data;
    }

    // File operations
    public function fileList(string $username, string $path, bool $showHidden = false): array
    {
        return $this->send('file.list', ['username' => $username, 'path' => $path, 'show_hidden' => $showHidden]);
    }

    public function fileRead(string $username, string $path): array
    {
        return $this->send('file.read', ['username' => $username, 'path' => $path]);
    }

    public function fileWrite(string $username, string $path, string $content): array
    {
        return $this->send('file.write', ['username' => $username, 'path' => $path, 'content' => $content]);
    }

    public function fileDelete(string $username, string $path): array
    {
        return $this->send('file.delete', ['username' => $username, 'path' => $path]);
    }

    public function fileMkdir(string $username, string $path): array
    {
        return $this->send('file.mkdir', ['username' => $username, 'path' => $path]);
    }

    public function fileRename(string $username, string $oldPath, string $newPath): array
    {
        // Agent expects 'path' (current file) and 'new_name' (just the filename, no path)
        $newName = basename($newPath);

        return $this->send('file.rename', ['username' => $username, 'path' => $oldPath, 'new_name' => $newName]);
    }

    public function fileCopy(string $username, string $source, string $destination): array
    {
        return $this->send('file.copy', ['username' => $username, 'source' => $source, 'destination' => $destination]);
    }

    public function fileMove(string $username, string $source, string $destination): array
    {
        return $this->send('file.move', ['username' => $username, 'source' => $source, 'destination' => $destination]);
    }

    /**
     * Upload a file. For large files (>1MB), uses temp file approach to avoid JSON encoding issues.
     */
    public function fileUpload(string $username, string $path, string $filename, string $content): array
    {
        // For files larger than 1MB, use temp file approach to avoid JSON encoding issues
        $sizeThreshold = 1 * 1024 * 1024; // 1MB

        if (strlen($content) > $sizeThreshold) {
            return $this->fileUploadLarge($username, $path, $filename, $content);
        }

        return $this->send('file.upload', [
            'username' => $username,
            'path' => $path,
            'filename' => $filename,
            'content' => base64_encode($content),
        ]);
    }

    /**
     * Upload large files by writing to temp location and having agent move them.
     * This avoids JSON encoding issues with large binary content.
     */
    protected function fileUploadLarge(string $username, string $path, string $filename, string $content): array
    {
        // Create temp directory if it doesn't exist
        $tempDir = '/tmp/jabali-uploads';
        if (! is_dir($tempDir)) {
            mkdir($tempDir, 0700, true);
            chmod($tempDir, 0700);
        } else {
            @chmod($tempDir, 0700);
        }

        // Generate unique temp filename
        $tempFile = $tempDir.'/'.uniqid('upload_', true).'_'.preg_replace('/[^a-zA-Z0-9._-]/', '_', $filename);

        // Write content to temp file
        if (file_put_contents($tempFile, $content) === false) {
            throw new Exception('Failed to write temp file for upload');
        }

        // Make sure the file is readable by root (agent)
        chmod($tempFile, 0600);

        try {
            // Call agent to move file from temp to destination
            return $this->send('file.upload_temp', [
                'username' => $username,
                'path' => $path,
                'filename' => $filename,
                'temp_path' => $tempFile,
            ]);
        } finally {
            // Clean up temp file if it still exists (agent should have moved it)
            if (file_exists($tempFile)) {
                @unlink($tempFile);
            }
        }
    }

    public function fileExtract(string $username, string $path): array
    {
        return $this->send('file.extract', ['username' => $username, 'path' => $path]);
    }

    public function fileChmod(string $username, string $path, string $mode): array
    {
        return $this->send('file.chmod', ['username' => $username, 'path' => $path, 'mode' => $mode]);
    }

    public function fileInfo(string $username, string $path): array
    {
        return $this->send('file.info', ['username' => $username, 'path' => $path]);
    }

    public function fileTrash(string $username, string $path): array
    {
        return $this->send('file.trash', ['username' => $username, 'path' => $path]);
    }

    public function fileRestore(string $username, string $trashName): array
    {
        return $this->send('file.restore', ['username' => $username, 'trash_name' => $trashName]);
    }

    public function fileEmptyTrash(string $username): array
    {
        return $this->send('file.empty_trash', ['username' => $username]);
    }

    public function fileListTrash(string $username): array
    {
        return $this->send('file.list_trash', ['username' => $username]);
    }

    // Git deployment
    public function gitGenerateKey(string $username): array
    {
        return $this->send('git.generate_key', ['username' => $username]);
    }

    public function gitDeploy(string $username, string $repoUrl, string $branch, string $deployPath, ?string $deployScript = null): array
    {
        return $this->send('git.deploy', [
            'username' => $username,
            'repo_url' => $repoUrl,
            'branch' => $branch,
            'deploy_path' => $deployPath,
            'deploy_script' => $deployScript,
        ]);
    }

    // Spam settings (Rspamd)
    public function rspamdUserSettings(string $username, array $whitelist = [], array $blacklist = [], ?float $score = null): array
    {
        return $this->send('rspamd.user_settings', [
            'username' => $username,
            'whitelist' => $whitelist,
            'blacklist' => $blacklist,
            'score' => $score,
        ]);
    }

    // Image optimization
    public function imageOptimize(string $username, string $path, bool $convertWebp = false, int $quality = 82): array
    {
        return $this->send('image.optimize', [
            'username' => $username,
            'path' => $path,
            'convert_webp' => $convertWebp,
            'quality' => $quality,
        ]);
    }

    // MySQL operations
    public function mysqlListDatabases(string $username): array
    {
        return $this->send('mysql.list_databases', ['username' => $username]);
    }

    public function mysqlCreateDatabase(string $username, string $database): array
    {
        return $this->send('mysql.create_database', ['username' => $username, 'database' => $database]);
    }

    public function mysqlDeleteDatabase(string $username, string $database): array
    {
        return $this->send('mysql.delete_database', ['username' => $username, 'database' => $database]);
    }

    public function mysqlListUsers(string $username): array
    {
        return $this->send('mysql.list_users', ['username' => $username]);
    }

    public function mysqlCreateUser(string $username, string $dbUser, string $password, string $host = 'localhost'): array
    {
        return $this->send('mysql.create_user', ['username' => $username, 'db_user' => $dbUser, 'password' => $password, 'host' => $host]);
    }

    public function mysqlDeleteUser(string $username, string $dbUser, string $host = 'localhost'): array
    {
        return $this->send('mysql.delete_user', ['username' => $username, 'db_user' => $dbUser, 'host' => $host]);
    }

    public function mysqlChangePassword(string $username, string $dbUser, string $password, string $host = 'localhost'): array
    {
        return $this->send('mysql.change_password', ['username' => $username, 'db_user' => $dbUser, 'password' => $password, 'host' => $host]);
    }

    public function mysqlGrantPrivileges(string $username, string $dbUser, string $database, array $privileges = ['ALL'], string $host = 'localhost'): array
    {
        return $this->send('mysql.grant_privileges', ['username' => $username, 'db_user' => $dbUser, 'database' => $database, 'privileges' => $privileges, 'host' => $host]);
    }

    public function mysqlRevokePrivileges(string $username, string $dbUser, string $database, string $host = 'localhost'): array
    {
        return $this->send('mysql.revoke_privileges', ['username' => $username, 'db_user' => $dbUser, 'database' => $database, 'host' => $host]);
    }

    public function mysqlGetPrivileges(string $username, string $dbUser, string $host = 'localhost'): array
    {
        return $this->send('mysql.get_privileges', ['username' => $username, 'db_user' => $dbUser, 'host' => $host]);
    }

    public function mysqlCreateMasterUser(string $username): array
    {
        return $this->send('mysql.create_master_user', ['username' => $username]);
    }

    public function mysqlImportDatabase(string $username, string $database, string $sqlFile): array
    {
        return $this->send('mysql.import_database', ['username' => $username, 'database' => $database, 'sql_file' => $sqlFile]);
    }

    public function mysqlExportDatabase(string $username, string $database, string $outputFile, string $compress = 'gz'): array
    {
        return $this->send('mysql.export_database', ['username' => $username, 'database' => $database, 'output_file' => $outputFile, 'compress' => $compress]);
    }

    // PostgreSQL operations
    public function postgresListDatabases(string $username): array
    {
        return $this->send('postgres.list_databases', ['username' => $username]);
    }

    public function postgresListUsers(string $username): array
    {
        return $this->send('postgres.list_users', ['username' => $username]);
    }

    public function postgresCreateDatabase(string $username, string $database, string $owner): array
    {
        return $this->send('postgres.create_database', [
            'username' => $username,
            'database' => $database,
            'owner' => $owner,
        ]);
    }

    public function postgresDeleteDatabase(string $username, string $database): array
    {
        return $this->send('postgres.delete_database', [
            'username' => $username,
            'database' => $database,
        ]);
    }

    public function postgresCreateUser(string $username, string $dbUser, string $password): array
    {
        return $this->send('postgres.create_user', [
            'username' => $username,
            'db_user' => $dbUser,
            'password' => $password,
        ]);
    }

    public function postgresDeleteUser(string $username, string $dbUser): array
    {
        return $this->send('postgres.delete_user', [
            'username' => $username,
            'db_user' => $dbUser,
        ]);
    }

    public function postgresChangePassword(string $username, string $dbUser, string $password): array
    {
        return $this->send('postgres.change_password', [
            'username' => $username,
            'db_user' => $dbUser,
            'password' => $password,
        ]);
    }

    public function postgresGrantPrivileges(string $username, string $database, string $dbUser): array
    {
        return $this->send('postgres.grant_privileges', [
            'username' => $username,
            'database' => $database,
            'db_user' => $dbUser,
        ]);
    }

    // Domain operations
    public function domainCreate(string $username, string $domain): array
    {
        return $this->send('domain.create', ['username' => $username, 'domain' => $domain]);
    }

    public function domainAliasAdd(string $username, string $domain, string $alias): array
    {
        return $this->send('domain.alias_add', [
            'username' => $username,
            'domain' => $domain,
            'alias' => $alias,
        ]);
    }

    public function domainAliasRemove(string $username, string $domain, string $alias): array
    {
        return $this->send('domain.alias_remove', [
            'username' => $username,
            'domain' => $domain,
            'alias' => $alias,
        ]);
    }

    public function domainEnsureErrorPages(string $username, string $domain): array
    {
        return $this->send('domain.ensure_error_pages', [
            'username' => $username,
            'domain' => $domain,
        ]);
    }

    public function domainDelete(string $username, string $domain, bool $deleteFiles = false): array
    {
        return $this->send('domain.delete', ['username' => $username, 'domain' => $domain, 'delete_files' => $deleteFiles]);
    }

    public function domainList(string $username): array
    {
        return $this->send('domain.list', ['username' => $username]);
    }

    public function domainToggle(string $username, string $domain, bool $enable): array
    {
        return $this->send('domain.toggle', ['username' => $username, 'domain' => $domain, 'enable' => $enable]);
    }

    // WordPress operations
    public function wpInstall(string $username, string $domain, array $options): array
    {
        return $this->send('wp.install', array_merge(['username' => $username, 'domain' => $domain], $options));
    }

    public function wpList(string $username): array
    {
        return $this->send('wp.list', ['username' => $username]);
    }

    public function wpDelete(string $username, string $siteId, bool $deleteFiles = true, bool $deleteDatabase = true): array
    {
        return $this->send('wp.delete', [
            'username' => $username,
            'site_id' => $siteId,
            'delete_files' => $deleteFiles,
            'delete_database' => $deleteDatabase,
        ]);
    }

    public function wpAutoLogin(string $username, string $siteId): array
    {
        return $this->send('wp.auto_login', ['username' => $username, 'site_id' => $siteId]);
    }

    public function wpUpdate(string $username, string $siteId, string $type = 'all'): array
    {
        return $this->send('wp.update', ['username' => $username, 'site_id' => $siteId, 'type' => $type]);
    }

    public function wpScan(string $username): array
    {
        return $this->send('wp.scan', ['username' => $username]);
    }

    public function wpImport(string $username, string $path, ?int $domainId = null): array
    {
        $params = ['username' => $username, 'path' => $path];
        if ($domainId !== null) {
            $params['domain_id'] = $domainId;
        }

        return $this->send('wp.import', $params);
    }

    public function wpCreateStaging(string $username, string $siteId, string $subdomain = 'staging', ?string $targetDomain = null): array
    {
        $params = [
            'username' => $username,
            'site_id' => $siteId,
            'subdomain' => $subdomain,
        ];

        if ($targetDomain !== null && $targetDomain !== '') {
            $params['target_domain'] = $targetDomain;
        }

        return $this->send('wp.create_staging', $params);
    }

    public function wpPushStaging(string $username, string $stagingSiteId): array
    {
        return $this->send('wp.push_staging', [
            'username' => $username,
            'staging_site_id' => $stagingSiteId,
        ]);
    }

    // WordPress Cache Methods
    public function wpCacheEnable(string $username, string $siteId): array
    {
        return $this->send('wp.cache_enable', ['username' => $username, 'site_id' => $siteId]);
    }

    public function wpCacheDisable(string $username, string $siteId, bool $removePlugin = false, bool $resetData = false): array
    {
        return $this->send('wp.cache_disable', [
            'username' => $username,
            'site_id' => $siteId,
            'remove_plugin' => $removePlugin,
            'reset_data' => $resetData,
        ]);
    }

    public function wpCacheFlush(string $username, string $siteId): array
    {
        return $this->send('wp.cache_flush', ['username' => $username, 'site_id' => $siteId]);
    }

    public function wpCacheStatus(string $username, string $siteId): array
    {
        return $this->send('wp.cache_status', ['username' => $username, 'site_id' => $siteId]);
    }

    // Page Cache (nginx fastcgi_cache) Methods
    public function wpPageCacheEnable(string $username, string $domain, ?string $siteId = null): array
    {
        return $this->send('wp.page_cache_enable', ['username' => $username, 'domain' => $domain, 'site_id' => $siteId]);
    }

    public function wpPageCacheDisable(string $username, string $domain, ?string $siteId = null): array
    {
        return $this->send('wp.page_cache_disable', ['username' => $username, 'domain' => $domain, 'site_id' => $siteId]);
    }

    public function wpPageCachePurge(string $domain, ?string $path = null): array
    {
        return $this->send('wp.page_cache_purge', ['domain' => $domain, 'path' => $path]);
    }

    public function wpPageCacheStatus(string $username, string $domain, ?string $siteId = null): array
    {
        return $this->send('wp.page_cache_status', ['username' => $username, 'domain' => $domain, 'site_id' => $siteId]);
    }

    // DNS Management Methods
    public function dnsCreateZone(string $domain, array $settings = []): array
    {
        return $this->send('dns.create_zone', array_merge(['domain' => $domain], $settings));
    }

    public function dnsSyncZone(string $domain, array $records, array $settings = []): array
    {
        return $this->send('dns.sync_zone', array_merge(['domain' => $domain, 'records' => $records], $settings));
    }

    public function dnsDeleteZone(string $domain): array
    {
        return $this->send('dns.delete_zone', ['domain' => $domain]);
    }

    public function dnsReload(): array
    {
        return $this->send('dns.reload');
    }

    // DNSSEC operations
    public function dnsEnableDnssec(string $domain): array
    {
        return $this->send('dns.enable_dnssec', ['domain' => $domain]);
    }

    public function dnsDisableDnssec(string $domain): array
    {
        return $this->send('dns.disable_dnssec', ['domain' => $domain]);
    }

    public function dnsGetDnssecStatus(string $domain): array
    {
        return $this->send('dns.get_dnssec_status', ['domain' => $domain]);
    }

    public function dnsGetDsRecords(string $domain): array
    {
        return $this->send('dns.get_ds_records', ['domain' => $domain]);
    }

    // User operations
    public function userExists(string $username): bool
    {
        $result = $this->send('user.exists', ['username' => $username]);

        return $result['exists'] ?? false;
    }

    public function deleteUser(string $username, bool $removeHome = false, array $domains = []): array
    {
        return $this->send('user.delete', [
            'username' => $username,
            'remove_home' => $removeHome,
            'domains' => $domains,
        ]);
    }

    public function createUser(string $username, ?string $password = null): array
    {
        return $this->send('user.create', ['username' => $username, 'password' => $password]);
    }

    // Email Domain operations
    public function emailEnableDomain(string $username, string $domain): array
    {
        return $this->send('email.enable_domain', ['username' => $username, 'domain' => $domain]);
    }

    public function emailDisableDomain(string $username, string $domain): array
    {
        return $this->send('email.disable_domain', ['username' => $username, 'domain' => $domain]);
    }

    public function emailGenerateDkim(string $username, string $domain, string $selector = 'default'): array
    {
        return $this->send('email.generate_dkim', ['username' => $username, 'domain' => $domain, 'selector' => $selector]);
    }

    public function emailGetDomainInfo(string $username, string $domain): array
    {
        return $this->send('email.domain_info', ['username' => $username, 'domain' => $domain]);
    }

    // Mailbox operations
    public function mailboxCreate(string $username, string $email, string $password, int $quotaBytes = 1073741824): array
    {
        return $this->send('email.mailbox_create', [
            'username' => $username,
            'email' => $email,
            'password' => $password,
            'quota_bytes' => $quotaBytes,
        ]);
    }

    public function mailboxDelete(string $username, string $email, bool $deleteFiles = false, ?string $maildirPath = null): array
    {
        return $this->send('email.mailbox_delete', [
            'username' => $username,
            'email' => $email,
            'delete_files' => $deleteFiles,
            'maildir_path' => $maildirPath,
        ]);
    }

    public function mailboxChangePassword(string $username, string $email, string $password): array
    {
        return $this->send('email.mailbox_change_password', [
            'username' => $username,
            'email' => $email,
            'password' => $password,
        ]);
    }

    public function mailboxSetQuota(string $username, string $email, int $quotaBytes): array
    {
        return $this->send('email.mailbox_set_quota', [
            'username' => $username,
            'email' => $email,
            'quota_bytes' => $quotaBytes,
        ]);
    }

    public function mailboxGetQuotaUsage(string $username, string $email): array
    {
        return $this->send('email.mailbox_quota_usage', [
            'username' => $username,
            'email' => $email,
        ]);
    }

    public function mailboxToggle(string $username, string $email, bool $active): array
    {
        return $this->send('email.mailbox_toggle', [
            'username' => $username,
            'email' => $email,
            'active' => $active,
        ]);
    }

    // Email sync operations
    public function emailSyncVirtualUsers(string $domain): array
    {
        return $this->send('email.sync_virtual_users', ['domain' => $domain]);
    }

    public function emailSyncMaps(array $domains, array $mailboxes, array $aliases): array
    {
        return $this->send('email.sync_maps', [
            'domains' => $domains,
            'mailboxes' => $mailboxes,
            'aliases' => $aliases,
        ]);
    }

    public function emailReloadServices(): array
    {
        return $this->send('email.reload_services');
    }

    // Server Import operations (cPanel/DirectAdmin migration)
    public function importDiscover(int $importId, string $sourceType, string $importMethod, ?string $backupPath, ?string $remoteHost, ?int $remotePort, ?string $remoteUser, ?string $remotePassword): array
    {
        return $this->send('import.discover', [
            'import_id' => $importId,
            'source_type' => $sourceType,
            'import_method' => $importMethod,
            'backup_path' => $backupPath,
            'remote_host' => $remoteHost,
            'remote_port' => $remotePort,
            'remote_user' => $remoteUser,
            'remote_password' => $remotePassword,
        ]);
    }

    public function importStart(int $importId): array
    {
        return $this->send('import.start', ['import_id' => $importId]);
    }

    // SSL Certificate operations
    public function sslCheck(string $domain, string $username): array
    {
        return $this->send('ssl.check', [
            'domain' => $domain,
            'username' => $username,
        ]);
    }

    public function sslIssue(string $domain, string $username, ?string $email = null, bool $includeWww = true): array
    {
        return $this->send('ssl.issue', [
            'domain' => $domain,
            'username' => $username,
            'email' => $email,
            'include_www' => $includeWww,
        ]);
    }

    public function sslInstall(string $domain, string $username, string $certificate, string $privateKey, ?string $caBundle = null): array
    {
        return $this->send('ssl.install', [
            'domain' => $domain,
            'username' => $username,
            'certificate' => $certificate,
            'private_key' => $privateKey,
            'ca_bundle' => $caBundle,
        ]);
    }

    public function sslRenew(string $domain, string $username): array
    {
        return $this->send('ssl.renew', [
            'domain' => $domain,
            'username' => $username,
        ]);
    }

    public function sslGenerateSelfSigned(string $domain, string $username, int $days = 365): array
    {
        return $this->send('ssl.generate_self_signed', [
            'domain' => $domain,
            'username' => $username,
            'days' => $days,
        ]);
    }

    public function sslMailIssue(string $hostname, ?string $email = null): array
    {
        return $this->send('ssl.mail.issue', [
            'hostname' => $hostname,
            'email' => $email,
        ]);
    }

    public function sslMailStatus(): array
    {
        return $this->send('ssl.mail.status', []);
    }

    // Server config export/import

    /**
     * Export server configuration (nginx vhosts, DNS zones, SSL certs, maildir).
     *
     * @param  string  $outputPath  Path to save the export archive
     * @param  array  $options  Export options (include_nginx, include_dns, include_ssl, include_maildir)
     */
    public function serverExportConfig(string $outputPath = '/tmp/jabali-config-export.tar.gz', array $options = []): array
    {
        return $this->send('server.export_config', array_merge([
            'output_path' => $outputPath,
        ], $options));
    }

    /**
     * Import server configuration from export archive.
     *
     * @param  string  $archivePath  Path to the export archive
     * @param  bool  $importNginx  Whether to import nginx configs
     * @param  bool  $importDns  Whether to import DNS zones
     * @param  bool  $dryRun  Preview what would be imported without making changes
     */
    public function serverImportConfig(string $archivePath, bool $importNginx = true, bool $importDns = true, bool $dryRun = false): array
    {
        return $this->send('server.import_config', [
            'archive_path' => $archivePath,
            'import_nginx' => $importNginx,
            'import_dns' => $importDns,
            'dry_run' => $dryRun,
        ]);
    }

    // Backup operations

    /**
     * Create a backup for a user.
     *
     * @param  string  $username  System username
     * @param  string  $outputPath  Path to save the backup archive
     * @param  array  $options  Backup options (domains, databases, mailboxes, include_files, include_databases, include_mailboxes, include_dns)
     */
    public function backupCreate(string $username, string $outputPath, array $options = []): array
    {
        return $this->send('backup.create', array_merge([
            'username' => $username,
            'output_path' => $outputPath,
        ], $options));
    }

    /**
     * Create a server-wide backup (all users).
     *
     * @param  string  $outputPath  Path to save the backup archive
     * @param  array  $options  Backup options (users, include_files, include_databases, include_mailboxes, include_dns)
     */
    public function backupCreateServer(string $outputPath, array $options = []): array
    {
        return $this->send('backup.create_server', array_merge([
            'output_path' => $outputPath,
        ], $options));
    }

    /**
     * Dirvish-style incremental backup directly to remote.
     * Rsyncs user files directly to remote with --link-dest for hard links.
     *
     * @param  array  $destination  Remote destination config (type, host, username, etc.)
     * @param  array  $options  Backup options (users, include_files, include_databases, include_mailboxes, include_dns)
     */
    public function backupIncrementalDirect(array $destination, array $options = []): array
    {
        return $this->send('backup.incremental_direct', array_merge([
            'destination' => $destination,
        ], $options));
    }

    /**
     * Restore a backup for a user.
     *
     * @param  string  $username  System username
     * @param  string  $backupPath  Path to the backup archive
     * @param  array  $options  Restore options (restore_files, restore_databases, restore_mailboxes, restore_dns, selected_domains, selected_databases, selected_mailboxes)
     */
    public function backupRestore(string $username, string $backupPath, array $options = []): array
    {
        return $this->send('backup.restore', array_merge([
            'username' => $username,
            'backup_path' => $backupPath,
        ], $options));
    }

    /**
     * List backups for a user.
     *
     * @param  string  $username  System username
     * @param  string  $path  Directory to list backups from
     */
    public function backupList(string $username, string $path = ''): array
    {
        return $this->send('backup.list', [
            'username' => $username,
            'path' => $path,
        ]);
    }

    /**
     * Delete a backup file.
     *
     * @param  string  $username  System username
     * @param  string  $backupPath  Path to the backup file
     */
    public function backupDelete(string $username, string $backupPath): array
    {
        return $this->send('backup.delete', [
            'username' => $username,
            'backup_path' => $backupPath,
        ]);
    }

    /**
     * Delete a server backup file (runs as root).
     *
     * @param  string  $backupPath  Path to the backup file
     */
    public function backupDeleteServer(string $backupPath): array
    {
        return $this->send('backup.delete_server', [
            'backup_path' => $backupPath,
        ]);
    }

    /**
     * Verify backup integrity.
     *
     * @param  string  $backupPath  Path to the backup archive
     */
    public function backupVerify(string $backupPath): array
    {
        return $this->send('backup.verify', [
            'backup_path' => $backupPath,
        ]);
    }

    /**
     * Get backup manifest/info.
     *
     * @param  string  $backupPath  Path to the backup archive
     */
    public function backupGetInfo(string $backupPath): array
    {
        return $this->send('backup.get_info', [
            'backup_path' => $backupPath,
        ]);
    }

    /**
     * Upload backup to remote destination.
     *
     * @param  string  $localPath  Local path to the backup file
     * @param  array  $destination  Remote destination config (type, host, port, username, password, path, etc.)
     * @param  string  $backupType  'full' or 'incremental' - incremental uses rsync with hard links
     */
    public function backupUploadRemote(string $localPath, array $destination, string $backupType = 'full'): array
    {
        return $this->send('backup.upload_remote', [
            'local_path' => $localPath,
            'destination' => $destination,
            'backup_type' => $backupType,
        ]);
    }

    /**
     * Download backup from remote destination.
     *
     * @param  string  $remotePath  Remote path to the backup file
     * @param  string  $localPath  Local path to save the backup
     * @param  array  $destination  Remote destination config
     */
    public function backupDownloadRemote(string $remotePath, string $localPath, array $destination): array
    {
        return $this->send('backup.download_remote', [
            'remote_path' => $remotePath,
            'local_path' => $localPath,
            'destination' => $destination,
        ]);
    }

    /**
     * List backups on remote destination.
     *
     * @param  array  $destination  Remote destination config
     * @param  string  $path  Path to list (optional)
     */
    public function backupListRemote(array $destination, string $path = ''): array
    {
        return $this->send('backup.list_remote', [
            'destination' => $destination,
            'path' => $path,
        ]);
    }

    /**
     * Delete backup from remote destination.
     *
     * @param  string  $remotePath  Remote path to the backup file
     * @param  array  $destination  Remote destination config
     */
    public function backupDeleteRemote(string $remotePath, array $destination): array
    {
        return $this->send('backup.delete_remote', [
            'remote_path' => $remotePath,
            'destination' => $destination,
        ]);
    }

    /**
     * Test remote destination connection.
     *
     * @param  array  $destination  Remote destination config (type, host, port, username, password, path, etc.)
     */
    public function backupTestDestination(array $destination): array
    {
        return $this->send('backup.test_destination', [
            'destination' => $destination,
        ]);
    }

    // ============ CRON JOB OPERATIONS ============

    /**
     * List cron jobs for a user.
     */
    public function cronList(string $username): array
    {
        return $this->send('cron.list', [
            'username' => $username,
        ]);
    }

    /**
     * Create a cron job.
     */
    public function cronCreate(string $username, string $schedule, string $command, string $comment = ''): array
    {
        return $this->send('cron.create', [
            'username' => $username,
            'schedule' => $schedule,
            'command' => $command,
            'comment' => $comment,
        ]);
    }

    /**
     * Delete a cron job.
     */
    public function cronDelete(string $username, string $command, string $schedule = ''): array
    {
        return $this->send('cron.delete', [
            'username' => $username,
            'command' => $command,
            'schedule' => $schedule,
        ]);
    }

    /**
     * Toggle a cron job on/off.
     */
    public function cronToggle(string $username, string $command, bool $enable): array
    {
        return $this->send('cron.toggle', [
            'username' => $username,
            'command' => $command,
            'enable' => $enable,
        ]);
    }

    /**
     * Run a cron job immediately.
     */
    public function cronRun(string $username, string $command): array
    {
        return $this->send('cron.run', [
            'username' => $username,
            'command' => $command,
        ]);
    }

    /**
     * Setup WordPress cron (creates cron job and modifies wp-config.php).
     */
    public function cronWordPressSetup(string $username, string $domain, string $schedule = '*/5 * * * *', bool $disable = false): array
    {
        return $this->send('cron.wp_setup', [
            'username' => $username,
            'domain' => $domain,
            'schedule' => $schedule,
            'disable' => $disable,
        ]);
    }

    // ============ SERVER METRICS OPERATIONS ============

    /**
     * Get server metrics overview (CPU, memory, disk, load).
     */
    public function metricsOverview(): array
    {
        return $this->cachedMetrics('agent.metrics.overview', 5, fn (): array => $this->send('metrics.overview', []));
    }

    /**
     * Get CPU metrics.
     */
    public function metricsCpu(): array
    {
        return $this->cachedMetrics('agent.metrics.cpu', 5, fn (): array => $this->send('metrics.cpu', []));
    }

    /**
     * Get memory metrics.
     */
    public function metricsMemory(): array
    {
        return $this->cachedMetrics('agent.metrics.memory', 5, fn (): array => $this->send('metrics.memory', []));
    }

    /**
     * Get disk metrics.
     */
    public function metricsDisk(): array
    {
        return $this->cachedMetrics('agent.metrics.disk', 10, fn (): array => $this->send('metrics.disk', []));
    }

    /**
     * Get network metrics.
     */
    public function metricsNetwork(): array
    {
        return $this->cachedMetrics('agent.metrics.network', 5, fn (): array => $this->send('metrics.network', []));
    }

    /**
     * Get versions of installed server software (best-effort).
     */
    public function serverVersions(): array
    {
        return $this->cachedMetrics('agent.server.versions', 60, fn (): array => $this->send('server.versions', []));
    }

    /**
     * Get top processes.
     */
    public function metricsProcesses(int $limit = 15, string $sortBy = 'cpu'): array
    {
        return $this->send('metrics.processes', [
            'limit' => $limit,
            'sort' => $sortBy,
        ]);
    }

    // ============ DISK QUOTA OPERATIONS ============

    /**
     * Get quota system status.
     */
    public function quotaStatus(string $mount = '/home'): array
    {
        return $this->send('quota.status', ['mount' => $mount]);
    }

    /**
     * Enable quota system on filesystem.
     */
    public function quotaEnable(string $mount = '/home'): array
    {
        return $this->send('quota.enable', ['mount' => $mount]);
    }

    /**
     * Set disk quota for a user.
     */
    public function quotaSet(string $username, int $softMb, int $hardMb = 0, string $mount = '/home'): array
    {
        return $this->send('quota.set', [
            'username' => $username,
            'soft_mb' => $softMb,
            'hard_mb' => $hardMb ?: $softMb,
            'mount' => $mount,
        ]);
    }

    /**
     * Get disk quota for a user.
     */
    public function quotaGet(string $username, string $mount = '/home'): array
    {
        return $this->send('quota.get', [
            'username' => $username,
            'mount' => $mount,
        ]);
    }

    /**
     * Get quota report for all users.
     */
    public function quotaReport(string $mount = '/home'): array
    {
        return $this->send('quota.report', ['mount' => $mount]);
    }

    /**
     * List all IP addresses on the server.
     */
    public function ipList(): array
    {
        return $this->send('ip.list');
    }

    /**
     * Add an IP address to an interface.
     */
    public function ipAdd(string $ip, int $cidr, string $interface): array
    {
        return $this->send('ip.add', [
            'ip' => $ip,
            'cidr' => $cidr,
            'interface' => $interface,
        ]);
    }

    /**
     * Remove an IP address from an interface.
     */
    public function ipRemove(string $ip, int $cidr, string $interface): array
    {
        return $this->send('ip.remove', [
            'ip' => $ip,
            'cidr' => $cidr,
            'interface' => $interface,
        ]);
    }

    /**
     * Get detailed information about an IP address.
     */
    public function ipInfo(string $ip): array
    {
        return $this->send('ip.info', ['ip' => $ip]);
    }

    /**
     * Get light status for Fail2ban (installed/running/version).
     */
    public function fail2banStatusLight(): array
    {
        return $this->send('fail2ban.status_light');
    }

    /**
     * Get light status for ClamAV (installed/running/version).
     */
    public function clamavStatusLight(): array
    {
        return $this->send('clamav.status_light');
    }

    /**
     * Install a security scanner tool.
     */
    public function scannerInstall(string $tool): array
    {
        return $this->send('scanner.install', ['tool' => $tool], 120);
    }

    /**
     * Uninstall a security scanner tool.
     */
    public function scannerUninstall(string $tool): array
    {
        return $this->send('scanner.uninstall', ['tool' => $tool], 60);
    }

    /**
     * Get status of security scanner tools.
     */
    public function scannerStatus(?string $tool = null): array
    {
        return $this->send('scanner.status', ['tool' => $tool]);
    }

    /**
     * Run Lynis security audit.
     */
    public function scannerRunLynis(): array
    {
        return $this->send('scanner.run_lynis', [], 300);
    }

    /**
     * Run Nikto web server scan.
     */
    public function scannerRunNikto(string $target): array
    {
        return $this->send('scanner.run_nikto', ['target' => $target], 300);
    }

    /**
     * Start Lynis scan in background.
     */
    public function scannerStartLynis(): array
    {
        return $this->send('scanner.start_lynis', []);
    }

    /**
     * Get current scan status and output.
     */
    public function scannerGetScanStatus(string $scanner = 'lynis'): array
    {
        return $this->send('scanner.get_scan_status', ['scanner' => $scanner]);
    }

    // Mail queue operations
    public function mailQueueList(): array
    {
        return $this->send('mail.queue_list');
    }

    public function mailQueueRetry(string $id): array
    {
        return $this->send('mail.queue_retry', ['id' => $id]);
    }

    public function mailQueueDelete(string $id): array
    {
        return $this->send('mail.queue_delete', ['id' => $id]);
    }

    // Server updates
    public function updatesList(bool $refresh = false): array
    {
        return $this->send('updates.list', ['refresh' => $refresh]);
    }

    public function updatesRun(): array
    {
        return $this->send('updates.run');
    }

    // WAF / Geo
    public function wafApplySettings(bool $enabled, string $paranoia, bool $auditLog, array $whitelistRules = []): array
    {
        return $this->send('waf.apply', [
            'enabled' => $enabled,
            'paranoia' => $paranoia,
            'audit_log' => $auditLog,
            'whitelist_rules' => $whitelistRules,
        ]);
    }

    public function wafAuditLogList(int $limit = 200): array
    {
        return $this->send('waf.audit_log', [
            'limit' => $limit,
        ]);
    }

    public function geoApplyRules(array $rules): array
    {
        return $this->send('geo.apply_rules', [
            'rules' => $rules,
        ]);
    }

    public function geoUpdateDatabase(string $accountId, string $licenseKey, string $editionIds = 'GeoLite2-Country'): array
    {
        return $this->send('geo.update_database', [
            'account_id' => $accountId,
            'license_key' => $licenseKey,
            'edition_ids' => $editionIds,
        ]);
    }

    public function geoUploadDatabase(string $edition, string $content): array
    {
        return $this->send('geo.upload_database', [
            'edition' => $edition,
            'content' => $content,
        ]);
    }

    public function databasePersistTuning(string $name, string $value): array
    {
        return $this->send('database.persist_tuning', [
            'name' => $name,
            'value' => $value,
        ]);
    }

    /**
     * @param  array<int, string>  $names
     */
    public function databaseGetVariables(array $names): array
    {
        return $this->send('database.get_variables', [
            'names' => $names,
        ]);
    }

    public function databaseSetGlobal(string $name, string $value): array
    {
        return $this->send('database.set_global', [
            'name' => $name,
            'value' => $value,
        ]);
    }
}
