<?php

declare(strict_types=1);

namespace App\Services\Migration;

use Exception;
use Illuminate\Support\Facades\Http;
use Illuminate\Support\Facades\Log;

class WhmApiService
{
    private string $hostname;

    private string $username;

    private string $apiToken;

    private int $port;

    private bool $ssl;

    private bool $verifySsl;

    public function __construct(
        string $hostname,
        string $username,
        string $apiToken,
        int $port = 2087,
        bool $ssl = true
    ) {
        $this->hostname = rtrim(trim($hostname), '/');
        $this->username = trim($username);
        $this->apiToken = trim($apiToken);
        $this->port = $port;
        $this->ssl = $ssl;
        $this->verifySsl = ! config('app.import_insecure_tls', false);
    }

    /**
     * Get the base URL for API calls
     */
    private function getBaseUrl(): string
    {
        $protocol = $this->ssl ? 'https' : 'http';

        return "{$protocol}://{$this->hostname}:{$this->port}";
    }

    /**
     * Make a WHMAPI1 request
     */
    public function whmapi(string $function, array $params = [], int $timeout = 120): array
    {
        $url = $this->getBaseUrl()."/json-api/{$function}";
        $params['api.version'] = 1;

        Log::info('WHM API request', ['function' => $function, 'url' => $url]);

        try {
            $pending = Http::withHeaders([
                'Authorization' => "whm {$this->username}:{$this->apiToken}",
            ])
                ->timeout($timeout)
                ->connectTimeout(10);

            if (! $this->verifySsl) {
                $pending = $pending->withoutVerifying();
            }

            $response = $pending->get($url, $params);

            if (! $response->successful()) {
                throw new Exception('WHM API request failed: '.$response->status());
            }

            $data = $response->json();

            // WHM API returns metadata.result = 1 on success
            if (($data['metadata']['result'] ?? 0) !== 1) {
                $reason = $data['metadata']['reason'] ?? 'Unknown error';
                throw new Exception("WHM API error: {$reason}");
            }

            return $data;
        } catch (Exception $e) {
            Log::error('WHM API error', ['function' => $function, 'error' => $e->getMessage()]);
            throw $e;
        }
    }

    /**
     * Test connection to WHM
     */
    public function testConnection(): array
    {
        try {
            $result = $this->whmapi('version');

            return [
                'success' => true,
                'version' => $result['data']['version'] ?? 'Unknown',
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * List all cPanel accounts on the WHM server
     */
    public function listAccounts(): array
    {
        try {
            $result = $this->whmapi('listaccts');
            $accounts = $result['data']['acct'] ?? [];

            return [
                'success' => true,
                'accounts' => array_map(fn ($acct) => [
                    'user' => $acct['user'] ?? '',
                    'domain' => $acct['domain'] ?? '',
                    'email' => $acct['email'] ?? '',
                    'diskused' => $acct['diskused'] ?? '0M',
                    'disklimit' => $acct['disklimit'] ?? 'unlimited',
                    'plan' => $acct['plan'] ?? '',
                    'startdate' => $acct['startdate'] ?? '',
                    'suspended' => ($acct['suspended'] ?? 0) == 1,
                    'ip' => $acct['ip'] ?? '',
                    'shell' => $acct['shell'] ?? '',
                    'owner' => $acct['owner'] ?? '',
                ], $accounts),
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Get summary for a specific cPanel account
     */
    public function getAccountSummary(string $user): array
    {
        try {
            $result = $this->whmapi('accountsummary', ['user' => $user]);
            $acct = $result['data']['acct'][0] ?? [];

            return [
                'success' => true,
                'account' => [
                    'user' => $acct['user'] ?? '',
                    'domain' => $acct['domain'] ?? '',
                    'email' => $acct['email'] ?? '',
                    'diskused' => $acct['diskused'] ?? '0M',
                    'disklimit' => $acct['disklimit'] ?? 'unlimited',
                    'plan' => $acct['plan'] ?? '',
                    'suspended' => ($acct['suspended'] ?? 0) == 1,
                    'ip' => $acct['ip'] ?? '',
                    'partition' => $acct['partition'] ?? '',
                    'homedir' => $acct['homedir'] ?? "/home/{$user}",
                ],
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Make an API2 request through WHM for a specific user
     * Uses /json-api/cpanel endpoint with cpanel_jsonapi_apiversion=2
     */
    public function api2(string $user, string $module, string $function, array $params = [], int $timeout = 120): array
    {
        $url = $this->getBaseUrl().'/json-api/cpanel';

        // Build query params with correct WHM API2 proxy parameter names
        $queryParams = [
            'cpanel_jsonapi_user' => $user,
            'cpanel_jsonapi_module' => $module,
            'cpanel_jsonapi_func' => $function,
            'cpanel_jsonapi_apiversion' => 2,
        ];

        // Add function-specific parameters
        foreach ($params as $key => $value) {
            $queryParams[$key] = $value;
        }

        Log::info('WHM API2 request', [
            'user' => $user,
            'module' => $module,
            'function' => $function,
        ]);

        try {
            $pending = Http::withHeaders([
                'Authorization' => "whm {$this->username}:{$this->apiToken}",
            ])
                ->timeout($timeout)
                ->connectTimeout(10);

            if (! $this->verifySsl) {
                $pending = $pending->withoutVerifying();
            }

            $response = $pending->get($url, $queryParams);

            if (! $response->successful()) {
                throw new Exception('WHM API2 request failed: '.$response->status());
            }

            $data = $response->json();

            Log::info('WHM API2 response', ['user' => $user, 'module' => $module, 'function' => $function]);

            // Return data - let calling function handle cpanelresult errors
            // (some errors like "already exists" should be handled gracefully)
            return $data;
        } catch (Exception $e) {
            Log::error('WHM API2 error', ['user' => $user, 'module' => $module, 'function' => $function, 'error' => $e->getMessage()]);
            throw $e;
        }
    }

    /**
     * Make a UAPI request through WHM for a specific user
     * Uses /json-api/cpanel endpoint with cpanel_jsonapi_apiversion=3
     */
    public function uapi(string $user, string $module, string $function, array $params = [], int $timeout = 120): array
    {
        // WHM UAPI proxy endpoint is /json-api/cpanel with apiversion=3
        $url = $this->getBaseUrl().'/json-api/cpanel';

        // Build query params with correct WHM UAPI proxy parameter names
        $queryParams = [
            'cpanel_jsonapi_user' => $user,
            'cpanel_jsonapi_module' => $module,
            'cpanel_jsonapi_func' => $function,
            'cpanel_jsonapi_apiversion' => 3,
        ];

        // Add function-specific parameters
        foreach ($params as $key => $value) {
            $queryParams[$key] = $value;
        }

        Log::info('WHM UAPI request', [
            'user' => $user,
            'module' => $module,
            'function' => $function,
            'url' => $url,
            'params' => array_keys($queryParams),
        ]);

        try {
            $pending = Http::withHeaders([
                'Authorization' => "whm {$this->username}:{$this->apiToken}",
            ])
                ->timeout($timeout)
                ->connectTimeout(10);

            if (! $this->verifySsl) {
                $pending = $pending->withoutVerifying();
            }

            $response = $pending->get($url, $queryParams);

            if (! $response->successful()) {
                throw new Exception('WHM UAPI request failed: '.$response->status());
            }

            $data = $response->json();

            Log::info('WHM UAPI response', ['user' => $user, 'module' => $module, 'function' => $function, 'data' => $data]);

            // UAPI through WHM returns result.status = 1 on success
            // But the structure may be wrapped differently
            if (isset($data['result']['status'])) {
                if (($data['result']['status'] ?? 0) !== 1) {
                    $errors = $data['result']['errors'] ?? [];
                    $errorMsg = is_array($errors) ? ($errors[0] ?? 'Unknown error') : $errors;
                    throw new Exception("UAPI error: {$errorMsg}");
                }
            } elseif (isset($data['cpanelresult']['error'])) {
                // Legacy response format
                throw new Exception('UAPI error: '.$data['cpanelresult']['error']);
            } elseif (isset($data['error'])) {
                throw new Exception('UAPI error: '.$data['error']);
            }

            return $data;
        } catch (Exception $e) {
            Log::error('WHM UAPI error', ['user' => $user, 'module' => $module, 'function' => $function, 'error' => $e->getMessage()]);
            throw $e;
        }
    }

    /**
     * Create a full backup for a specific user via WHM
     * Uses UAPI through WHM proxy
     */
    public function createBackupForUser(string $user): array
    {
        try {
            // Use UAPI Backup::fullbackup_to_homedir via WHM proxy
            $result = $this->uapi($user, 'Backup', 'fullbackup_to_homedir', [], 300);

            Log::info('WHM createBackupForUser response', ['user' => $user, 'result' => $result]);

            // Extract data from potentially different response structures
            $data = $result['result']['data'] ?? $result['cpanelresult']['data'] ?? $result['data'] ?? [];

            // Handle array or object data
            if (is_array($data) && isset($data[0])) {
                $data = $data[0];
            }

            return [
                'success' => true,
                'message' => 'Backup initiated',
                'pid' => $data['pid'] ?? $data['backup_id'] ?? null,
                'data' => $data,
            ];
        } catch (Exception $e) {
            Log::error('WHM createBackupForUser error', ['user' => $user, 'error' => $e->getMessage()]);

            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * List backup files for a user via WHM UAPI
     */
    public function listBackupsForUser(string $user): array
    {
        try {
            // Get account info to find homedir
            $acctResult = $this->whmapi('accountsummary', ['user' => $user]);
            $acct = $acctResult['data']['acct'][0] ?? [];
            $homedir = $acct['homedir'] ?? "/home/{$user}";

            // Use UAPI Backup::list_backups via WHM proxy
            $result = $this->uapi($user, 'Backup', 'list_backups');

            // Extract backups from potentially different response structures
            $backups = $result['result']['data'] ?? $result['cpanelresult']['data'] ?? $result['data'] ?? [];

            // Format the backups
            $formattedBackups = [];
            foreach ($backups as $backup) {
                $file = $backup['file'] ?? $backup['backupID'] ?? $backup['backup'] ?? '';
                if (empty($file)) {
                    continue;
                }

                $formattedBackups[] = [
                    'file' => $file,
                    'status' => $backup['status'] ?? 'complete',
                    'time' => $backup['mtime'] ?? $backup['time'] ?? 0,
                    'localtime' => $backup['localtime'] ?? '',
                    'path' => "{$homedir}/{$file}",
                ];
            }

            return [
                'success' => true,
                'homedir' => $homedir,
                'backups' => $formattedBackups,
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Check backup status for a user by looking for backup files
     */
    public function getBackupStatusForUser(string $user): array
    {
        try {
            // Get account info first
            $acctResult = $this->whmapi('accountsummary', ['user' => $user]);
            $acct = $acctResult['data']['acct'][0] ?? [];
            $homedir = $acct['homedir'] ?? "/home/{$user}";

            // Use UAPI Fileman::list_files to check homedir via WHM proxy
            $result = $this->uapi($user, 'Fileman', 'list_files', [
                'dir' => $homedir,
                'include_mime' => 0,
                'include_permissions' => 0,
                'include_hash' => 0,
                'include_content' => 0,
            ]);

            // Extract from potentially different response structures
            $files = $result['result']['data'] ?? $result['cpanelresult']['data'] ?? $result['data'] ?? [];

            $backupFiles = [];
            $inProgress = false;

            foreach ($files as $file) {
                $name = $file['file'] ?? $file['name'] ?? $file['fullpath'] ?? '';

                // Extract just the filename if full path
                if (str_contains($name, '/')) {
                    $name = basename($name);
                }

                // cPanel backup files: backup-MM.DD.YYYY_HH-mm-ss_username.tar.gz
                if (preg_match('/^backup-\d+\.\d+\.\d+_\d+-\d+-\d+_.*\.tar\.gz$/', $name)) {
                    $backupFiles[] = [
                        'name' => $name,
                        'size' => (int) ($file['size'] ?? 0),
                        'mtime' => (int) ($file['mtime'] ?? $file['ctime'] ?? 0),
                        'path' => "{$homedir}/{$name}",
                    ];
                }

                // Check for in-progress backup indicator
                if (str_contains($name, 'backup') && str_ends_with($name, '.log')) {
                    $inProgress = true;
                }
            }

            // Sort by modification time, newest first
            usort($backupFiles, fn ($a, $b) => ($b['mtime'] ?? 0) - ($a['mtime'] ?? 0));

            return [
                'success' => true,
                'in_progress' => $inProgress,
                'backups' => $backupFiles,
                'homedir' => $homedir,
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Get migration summary for a specific user via WHM UAPI
     */
    public function getUserMigrationSummary(string $user): array
    {
        $summary = [
            'success' => true,
            'user' => $user,
            'domains' => [
                'main' => '',
                'addon' => [],
                'sub' => [],
                'parked' => [],
            ],
            'databases' => [],
            'email_accounts' => [],
            'email_forwarders' => [],
            'ssl_certificates' => [],
            'errors' => [],
        ];

        // Get domains
        try {
            $result = $this->uapi($user, 'DomainInfo', 'list_domains');

            // Extract from potentially different response structures
            $data = $result['result']['data'] ?? $result['cpanelresult']['data'] ?? $result['data'] ?? [];

            // Handle array or single object
            if (is_array($data) && isset($data[0]) && is_array($data[0])) {
                $data = $data[0];
            }

            $summary['domains'] = [
                'main' => $data['main_domain'] ?? '',
                'addon' => $data['addon_domains'] ?? [],
                'sub' => $data['sub_domains'] ?? [],
                'parked' => $data['parked_domains'] ?? $data['alias_domains'] ?? [],
            ];
        } catch (Exception $e) {
            Log::warning("WHM migration - failed to list domains for {$user}: ".$e->getMessage());
            $summary['errors'][] = 'Domains: '.$e->getMessage();
        }

        // Get databases
        try {
            $result = $this->uapi($user, 'Mysql', 'list_databases');

            $summary['databases'] = $result['result']['data'] ?? $result['cpanelresult']['data'] ?? $result['data'] ?? [];
        } catch (Exception $e) {
            Log::warning("WHM migration - failed to list databases for {$user}: ".$e->getMessage());
            $summary['errors'][] = 'Databases: '.$e->getMessage();
        }

        // Get email accounts
        try {
            $result = $this->uapi($user, 'Email', 'list_pops_with_disk');

            $summary['email_accounts'] = $result['result']['data'] ?? $result['cpanelresult']['data'] ?? $result['data'] ?? [];
        } catch (Exception $e) {
            Log::warning("WHM migration - failed to list email accounts for {$user}: ".$e->getMessage());
            $summary['errors'][] = 'Email: '.$e->getMessage();
        }

        // Get email forwarders
        try {
            $result = $this->uapi($user, 'Email', 'list_forwarders');

            $summary['email_forwarders'] = $result['result']['data'] ?? $result['cpanelresult']['data'] ?? $result['data'] ?? [];
        } catch (Exception $e) {
            Log::warning("WHM migration - failed to list email forwarders for {$user}: ".$e->getMessage());
            // Forwarders are optional, don't add to errors
        }

        // Get SSL certificates
        try {
            $result = $this->uapi($user, 'SSL', 'list_certs');

            $summary['ssl_certificates'] = $result['result']['data'] ?? $result['cpanelresult']['data'] ?? $result['data'] ?? [];
        } catch (Exception $e) {
            Log::warning("WHM migration - failed to list SSL certificates for {$user}: ".$e->getMessage());
        }

        return $summary;
    }

    /**
     * Get WHM server hostname
     */
    public function getHostname(): string
    {
        return $this->hostname;
    }

    /**
     * Get the authenticated WHM username
     */
    public function getUsername(): string
    {
        return $this->username;
    }

    /**
     * Get WHM version information
     */
    public function getVersion(): array
    {
        try {
            $result = $this->whmapi('version');

            return [
                'success' => true,
                'version' => $result['data']['version'] ?? 'Unknown',
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Download a file from a cPanel user's homedir via WHM
     * Uses cPanel session-based download for reliability
     */
    public function downloadFileFromUser(string $user, string $remotePath, string $localPath, ?callable $progressCallback = null): array
    {
        Log::info('WHM download starting', ['user' => $user, 'remote' => $remotePath, 'local' => $localPath]);

        try {
            // Step 1: Create a cPanel session for the user
            $sessionResult = $this->whmapi('create_user_session', [
                'user' => $user,
                'service' => 'cpaneld',
            ]);

            $sessionUrl = $sessionResult['data']['url'] ?? null;
            if (! $sessionUrl) {
                throw new Exception('Failed to create cPanel session');
            }

            Log::info('WHM session created', ['user' => $user, 'session_url' => $sessionUrl]);

            // Extract the session token and base URL from the session URL
            // Session URL format: https://hostname:2083/cpsess1234567890/...
            if (preg_match('#^(https?://[^/]+)(/.*)$#', $sessionUrl, $matches)) {
                $baseUrl = $matches[1];
                $sessionPath = $matches[2];

                // Extract session token from path (cpsessXXXXXX)
                if (preg_match('#/(cpsess[^/]+)/#', $sessionPath, $sessMatches)) {
                    $sessionToken = $sessMatches[1];
                } else {
                    throw new Exception('Could not extract session token from URL');
                }
            } else {
                throw new Exception('Invalid session URL format');
            }

            // Step 2: Build the download URL using the session
            // cPanel download URL format: /cpsessXXXX/download?file=/path/to/file
            $downloadUrl = "{$baseUrl}/{$sessionToken}/download?skipencode=1&file=".urlencode($remotePath);

            Log::info('WHM download URL', ['url' => $downloadUrl]);

            // Step 3: Download using curl for better reliability
            $ch = curl_init();
            $curlOpts = [
                CURLOPT_URL => $downloadUrl,
                CURLOPT_RETURNTRANSFER => false,
                CURLOPT_FOLLOWLOCATION => true,
                CURLOPT_TIMEOUT => 3600,
                CURLOPT_CONNECTTIMEOUT => 30,
            ];

            if (! $this->verifySsl) {
                $curlOpts[CURLOPT_SSL_VERIFYPEER] = false;
                $curlOpts[CURLOPT_SSL_VERIFYHOST] = false;
            }

            curl_setopt_array($ch, $curlOpts);

            // Ensure local directory exists
            $localDir = dirname($localPath);
            if (! is_dir($localDir)) {
                mkdir($localDir, 0755, true);
            }

            // Open local file for writing
            $fp = fopen($localPath, 'wb');
            if (! $fp) {
                curl_close($ch);
                throw new Exception("Failed to open local file for writing: {$localPath}");
            }

            curl_setopt($ch, CURLOPT_FILE, $fp);

            // Execute download
            $result = curl_exec($ch);
            $httpCode = curl_getinfo($ch, CURLINFO_HTTP_CODE);
            $error = curl_error($ch);
            curl_close($ch);
            fclose($fp);

            // Check for errors
            if ($result === false || ! empty($error)) {
                @unlink($localPath);
                throw new Exception("cURL error: {$error}");
            }

            if ($httpCode !== 200) {
                // Read what was downloaded to check for error message
                $content = file_get_contents($localPath);
                @unlink($localPath);

                if (str_contains($content, '<html') || str_contains($content, 'error')) {
                    throw new Exception("HTTP {$httpCode}: Server returned error page");
                }
                throw new Exception("HTTP {$httpCode}: Download failed");
            }

            // Verify file was downloaded
            if (! file_exists($localPath) || filesize($localPath) === 0) {
                throw new Exception('Download failed - file is empty or missing');
            }

            $fileSize = filesize($localPath);

            Log::info('WHM download completed', [
                'user' => $user,
                'remote' => $remotePath,
                'local' => $localPath,
                'size' => $fileSize,
            ]);

            return [
                'success' => true,
                'path' => $localPath,
                'size' => $fileSize,
            ];
        } catch (Exception $e) {
            Log::error('WHM download error: '.$e->getMessage(), [
                'user' => $user,
                'remote' => $remotePath,
                'local' => $localPath,
            ]);

            // Clean up partial download
            if (file_exists($localPath)) {
                @unlink($localPath);
            }

            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Import an SSH private key to a cPanel user account via WHM
     * Uses API2 SSH::importkey through WHM proxy
     */
    public function importSshPrivateKey(string $user, string $keyName, string $privateKey, string $passphrase = ''): array
    {
        try {
            Log::info('WHM: Importing SSH private key to cPanel user', ['user' => $user, 'key_name' => $keyName]);

            $params = [
                'key' => $privateKey,
                'name' => $keyName,
            ];

            if (! empty($passphrase)) {
                $params['pass'] = $passphrase;
            }

            $result = $this->api2($user, 'SSH', 'importkey', $params);

            Log::info('WHM SSH private key import response', ['user' => $user, 'result' => $result]);

            $data = $result['cpanelresult']['data'][0] ?? [];
            $apiError = $result['cpanelresult']['error'] ?? '';

            // Check for "already exists" which is OK - extract actual key name if different
            $reasonText = $apiError ?: ($data['reason'] ?? '');
            if (str_contains($reasonText, 'already exists')) {
                // Extract the actual key name if provided (format: "already exists as keyname")
                $actualKeyName = $keyName;
                if (preg_match('/already exists as ([^\s.]+)/', $reasonText, $matches)) {
                    $actualKeyName = $matches[1];
                }

                return [
                    'success' => true,
                    'message' => 'SSH key already exists',
                    'actual_key_name' => $actualKeyName,
                ];
            }

            // Check for API-level error
            if ($apiError) {
                throw new Exception($apiError);
            }

            // Check for success
            $eventResult = $result['cpanelresult']['event']['result'] ?? null;
            if ($eventResult == 1 || (isset($data['result']) && $data['result'] == 1)) {
                return [
                    'success' => true,
                    'message' => 'SSH private key imported successfully',
                ];
            }

            $errorMsg = $data['reason'] ?? $data['error'] ?? 'Failed to import SSH key';
            throw new Exception($errorMsg);
        } catch (Exception $e) {
            Log::error('WHM SSH private key import failed', ['user' => $user, 'error' => $e->getMessage()]);

            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Authorize an SSH key for a cPanel user via WHM
     * Uses API2 SSH::authkey through WHM proxy
     */
    public function authorizeSshKey(string $user, string $keyName): array
    {
        try {
            Log::info('WHM: Authorizing SSH key for cPanel user', ['user' => $user, 'key_name' => $keyName]);

            $result = $this->api2($user, 'SSH', 'authkey', [
                'key' => $keyName,
                'action' => 'authorize',
            ]);

            Log::info('WHM SSH authkey response', ['user' => $user, 'result' => $result]);

            $data = $result['cpanelresult']['data'][0] ?? [];
            $apiError = $result['cpanelresult']['error'] ?? '';

            // Check for "already authorized" which is OK
            if (str_contains($apiError, 'already authorized') || str_contains($data['reason'] ?? '', 'already authorized')) {
                return [
                    'success' => true,
                    'message' => 'SSH key already authorized',
                ];
            }

            // Check for API error
            if ($apiError) {
                throw new Exception($apiError);
            }

            // Check for success
            $eventResult = $result['cpanelresult']['event']['result'] ?? null;
            if ($eventResult == 1 || (isset($data['result']) && $data['result'] == 1)) {
                return [
                    'success' => true,
                    'message' => 'SSH key authorized successfully',
                ];
            }

            $errorMsg = $data['reason'] ?? 'Failed to authorize SSH key';
            throw new Exception($errorMsg);
        } catch (Exception $e) {
            Log::error('WHM SSH key authorization failed', ['user' => $user, 'error' => $e->getMessage()]);

            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Create a full backup and upload to remote server via SCP with key authentication
     * Uses UAPI Backup::fullbackup_to_scp_with_key through WHM proxy
     */
    public function createBackupToScpWithKey(
        string $user,
        string $remoteHost,
        string $remoteUser,
        string $remotePath,
        string $keyName,
        int $remotePort = 22
    ): array {
        try {
            $params = [
                'host' => $remoteHost,
                'username' => $remoteUser,
                'directory' => $remotePath,
                'key_name' => $keyName,
                'port' => $remotePort,
                'key_passphrase' => '',
            ];

            Log::info('WHM: Initiating backup to SCP for user', [
                'cpanel_user' => $user,
                'host' => $remoteHost,
                'remote_user' => $remoteUser,
                'path' => $remotePath,
                'key_name' => $keyName,
            ]);

            $result = $this->uapi($user, 'Backup', 'fullbackup_to_scp_with_key', $params, 120);

            // Extract data from response
            $data = $result['result']['data'] ?? $result['cpanelresult']['data'] ?? [];

            return [
                'success' => true,
                'message' => 'Backup initiated with SCP transfer',
                'pid' => $data['pid'] ?? null,
                'data' => $data,
            ];
        } catch (Exception $e) {
            Log::error('WHM backup to SCP failed', ['user' => $user, 'error' => $e->getMessage()]);

            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Convert API migration summary data to agent-compatible format
     */
    public function convertApiDataToAgentFormat(array $apiData): array
    {
        $result = [
            'domains' => [],
            'databases' => [],
            'mailboxes' => [],
            'forwarders' => [],
            'ssl_certificates' => [],
        ];

        // Convert domains
        $domains = $apiData['domains'] ?? [];
        if (! empty($domains['main'])) {
            $result['domains'][] = ['name' => $domains['main'], 'type' => 'main'];
        }
        foreach ($domains['addon'] ?? [] as $domain) {
            $result['domains'][] = ['name' => $domain, 'type' => 'addon'];
        }
        foreach ($domains['sub'] ?? [] as $domain) {
            $result['domains'][] = ['name' => $domain, 'type' => 'sub'];
        }
        foreach ($domains['parked'] ?? [] as $domain) {
            $result['domains'][] = ['name' => $domain, 'type' => 'parked'];
        }

        // Convert databases
        foreach ($apiData['databases'] ?? [] as $db) {
            $dbName = is_array($db) ? ($db['database'] ?? $db['name'] ?? '') : $db;
            if ($dbName) {
                $result['databases'][] = ['name' => $dbName, 'file' => "mysql/{$dbName}.sql"];
            }
        }

        // Convert email accounts to mailboxes format
        foreach ($apiData['email_accounts'] ?? [] as $email) {
            $emailAddr = is_array($email) ? ($email['email'] ?? '') : $email;
            if ($emailAddr && str_contains($emailAddr, '@')) {
                [$localPart, $domain] = explode('@', $emailAddr, 2);
                $result['mailboxes'][] = [
                    'email' => $emailAddr,
                    'local_part' => $localPart,
                    'domain' => $domain,
                ];
            }
        }

        // Convert email forwarders
        // cPanel forwarder format: {'dest' => 'dest@example.com', 'forward' => 'source@domain.com', 'html_dest' => '...'}
        foreach ($apiData['email_forwarders'] ?? [] as $forwarder) {
            if (is_array($forwarder)) {
                $source = $forwarder['forward'] ?? $forwarder['source'] ?? '';
                $dest = $forwarder['dest'] ?? $forwarder['destination'] ?? '';

                if ($source && str_contains($source, '@') && $dest) {
                    [$localPart, $domain] = explode('@', $source, 2);
                    $result['forwarders'][] = [
                        'email' => $source,
                        'local_part' => $localPart,
                        'domain' => $domain,
                        'destinations' => $dest, // Will be parsed in the restore function
                    ];
                }
            }
        }

        // Convert SSL certificates
        foreach ($apiData['ssl_certificates'] ?? [] as $cert) {
            if (is_array($cert)) {
                $domain = $cert['domain'] ?? $cert['friendly_name'] ?? null;
                if (! $domain && ! empty($cert['domains'])) {
                    $domains = is_array($cert['domains']) ? $cert['domains'] : explode(',', $cert['domains']);
                    $domain = trim($domains[0] ?? '');
                }
                if ($domain) {
                    $result['ssl_certificates'][] = [
                        'domain' => $domain,
                        'has_key' => true,
                        'has_cert' => true,
                    ];
                }
            } elseif (is_string($cert) && ! empty($cert)) {
                $result['ssl_certificates'][] = [
                    'domain' => $cert,
                    'has_key' => true,
                    'has_cert' => true,
                ];
            }
        }

        return $result;
    }
}
