<?php

declare(strict_types=1);

namespace App\Services\Migration;

use Exception;
use Illuminate\Support\Facades\Http;
use Illuminate\Support\Facades\Log;

class CpanelApiService
{
    private string $hostname;

    private string $username;

    private string $apiToken;

    private int $port;

    private bool $ssl;

    private bool $verifySsl;

    public function __construct(string $hostname, string $username, string $apiToken, int $port = 2083, bool $ssl = true)
    {
        // Trim all string inputs to remove copy-paste whitespace/invisible chars
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
     * Make a UAPI request to cPanel
     */
    public function uapi(string $module, string $function, array $params = []): array
    {
        return $this->uapiWithTimeout($module, $function, $params, 30);
    }

    /**
     * Make a UAPI request to cPanel with custom timeout
     */
    public function uapiWithTimeout(string $module, string $function, array $params = [], int $timeout = 30): array
    {
        $url = $this->getBaseUrl()."/execute/{$module}/{$function}";

        Log::info('cPanel UAPI request', ['url' => $url, 'module' => $module, 'function' => $function, 'timeout' => $timeout]);

        try {
            $pending = Http::withHeaders([
                'Authorization' => "cpanel {$this->username}:{$this->apiToken}",
            ])
                ->timeout($timeout)
                ->connectTimeout(10);

            if (! $this->verifySsl) {
                $pending = $pending->withoutVerifying();
            }

            $response = $pending->get($url, $params);

            Log::info('cPanel UAPI response status', ['status' => $response->status(), 'module' => $module]);

            if (! $response->successful()) {
                throw new Exception('API request failed with status: '.$response->status());
            }

            $data = $response->json();

            Log::info('cPanel UAPI response data', ['module' => $module, 'function' => $function, 'data' => $data]);

            if (isset($data['status']) && $data['status'] === 0) {
                throw new Exception($data['errors'][0] ?? 'Unknown API error');
            }

            return $data;
        } catch (Exception $e) {
            Log::error('cPanel API error: '.$e->getMessage(), [
                'module' => $module,
                'function' => $function,
            ]);
            throw $e;
        }
    }

    /**
     * Make an API2 request to cPanel (legacy API)
     */
    public function api2(string $module, string $function, array $params = []): array
    {
        $url = $this->getBaseUrl().'/json-api/cpanel';

        $queryParams = array_merge([
            'cpanel_jsonapi_user' => $this->username,
            'cpanel_jsonapi_apiversion' => '2',
            'cpanel_jsonapi_module' => $module,
            'cpanel_jsonapi_func' => $function,
        ], $params);

        try {
            $pending = Http::withHeaders([
                'Authorization' => "cpanel {$this->username}:{$this->apiToken}",
            ])
                ->timeout(120);

            if (! $this->verifySsl) {
                $pending = $pending->withoutVerifying();
            }

            $response = $pending->get($url, $queryParams);

            if (! $response->successful()) {
                throw new Exception('API request failed with status: '.$response->status());
            }

            return $response->json();
        } catch (Exception $e) {
            Log::error('cPanel API2 error: '.$e->getMessage(), [
                'module' => $module,
                'function' => $function,
            ]);
            throw $e;
        }
    }

    /**
     * Test the connection to cPanel
     */
    public function testConnection(): array
    {
        try {
            $result = $this->uapi('ResourceUsage', 'get_usages');

            return [
                'success' => true,
                'message' => 'Connection successful',
                'data' => $result['result']['data'] ?? [],
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Get account information
     */
    public function getAccountInfo(): array
    {
        try {
            $stats = $this->uapi('StatsBar', 'get_stats', [
                'display' => 'diskusage|bandwidthusage|addondomains|subdomains|parkeddomains|sqldatabases|emailaccounts',
            ]);

            return [
                'success' => true,
                'stats' => $stats['result']['data'] ?? [],
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * List all domains (main, addon, subdomains, parked)
     */
    public function listDomains(): array
    {
        try {
            $result = $this->uapi('DomainInfo', 'list_domains');

            // Log raw response for debugging
            Log::info('cPanel listDomains raw response', ['result' => $result]);

            // Handle different response structures
            $data = $result['result']['data'] ?? $result['data'] ?? $result;

            return [
                'success' => true,
                'main_domain' => $data['main_domain'] ?? '',
                'addon_domains' => $data['addon_domains'] ?? [],
                'sub_domains' => $data['sub_domains'] ?? [],
                'parked_domains' => $data['parked_domains'] ?? [],
            ];
        } catch (Exception $e) {
            Log::error('cPanel listDomains failed', ['error' => $e->getMessage()]);

            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * List MySQL databases
     */
    public function listDatabases(): array
    {
        try {
            $result = $this->uapi('Mysql', 'list_databases');

            // Handle different response structures
            $data = $result['result']['data'] ?? $result['data'] ?? [];

            return [
                'success' => true,
                'databases' => $data,
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * List MySQL users
     */
    public function listDatabaseUsers(): array
    {
        try {
            $result = $this->uapi('Mysql', 'list_users');
            $data = $result['result']['data'] ?? $result['data'] ?? [];

            return [
                'success' => true,
                'users' => $data,
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * List email accounts
     */
    public function listEmailAccounts(): array
    {
        try {
            $result = $this->uapi('Email', 'list_pops_with_disk');
            $data = $result['result']['data'] ?? $result['data'] ?? [];

            return [
                'success' => true,
                'accounts' => $data,
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * List email forwarders
     */
    public function listForwarders(): array
    {
        try {
            $result = $this->uapi('Email', 'list_forwarders');
            $data = $result['result']['data'] ?? $result['data'] ?? [];

            return [
                'success' => true,
                'forwarders' => $data,
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * List SSL certificates
     */
    public function listSslCertificates(): array
    {
        try {
            $result = $this->uapi('SSL', 'list_certs');
            $data = $result['result']['data'] ?? $result['data'] ?? [];

            return [
                'success' => true,
                'certificates' => $data,
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * List cron jobs
     */
    public function listCronJobs(): array
    {
        try {
            $result = $this->uapi('Cron', 'list_cron');
            $data = $result['result']['data'] ?? $result['data'] ?? [];

            return [
                'success' => true,
                'cron_jobs' => $data,
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Create a full backup to homedir
     */
    public function createBackup(): array
    {
        try {
            $result = $this->uapi('Backup', 'fullbackup_to_homedir');

            return [
                'success' => true,
                'message' => 'Backup initiated',
                'pid' => $result['result']['data']['pid'] ?? null,
                'data' => $result['result']['data'] ?? [],
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * List available backups in homedir using API2 (more reliable)
     */
    public function listBackups(): array
    {
        try {
            // Use API2 Backups/listfullbackups as it's more reliable
            $result = $this->api2('Backups', 'listfullbackups', []);
            $backups = $result['cpanelresult']['data'] ?? [];

            // Format the backups consistently
            $formattedBackups = [];
            foreach ($backups as $backup) {
                $formattedBackups[] = [
                    'file' => $backup['file'] ?? '',
                    'status' => $backup['status'] ?? 'unknown',
                    'time' => $backup['time'] ?? 0,
                    'localtime' => $backup['localtime'] ?? '',
                    'path' => "/home/{$this->username}/".$backup['file'],
                ];
            }

            return [
                'success' => true,
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
     * Get the cPanel File Manager URL for downloading a backup
     */
    public function getBackupDownloadUrl(string $filename): string
    {
        $protocol = $this->ssl ? 'https' : 'http';

        return "{$protocol}://{$this->hostname}:{$this->port}/cpsess0/frontend/jupiter/filemanager/index.html";
    }

    /**
     * Get direct instructions for downloading a backup
     */
    public function getDownloadInstructions(string $filename): array
    {
        $protocol = $this->ssl ? 'https' : 'http';
        $loginUrl = "{$protocol}://{$this->hostname}:{$this->port}";

        return [
            'login_url' => $loginUrl,
            'username' => $this->username,
            'steps' => [
                "1. Log in to cPanel at {$loginUrl}",
                '2. Go to File Manager',
                "3. Navigate to the home directory (/home/{$this->username})",
                "4. Right-click on '{$filename}' and select 'Download'",
                '5. Once downloaded, upload the file using the form below',
            ],
            'backup_path' => "/home/{$this->username}/{$filename}",
        ];
    }

    /**
     * Check if a backup is currently in progress
     */
    public function getBackupStatus(): array
    {
        try {
            // Use API2 Fileman/listfiles as UAPI list_files returns empty on some cPanel versions
            $result = $this->api2('Fileman', 'listfiles', [
                'dir' => "/home/{$this->username}",
                'showdotfiles' => 0,
            ]);

            $files = $result['cpanelresult']['data'] ?? [];
            $backupFiles = [];
            $inProgress = false;

            foreach ($files as $file) {
                $name = $file['file'] ?? $file['name'] ?? '';
                // cPanel backup files are named like: backup-MM.DD.YYYY_HH-mm-ss_username.tar.gz
                if (preg_match('/^backup-\d+\.\d+\.\d+_\d+-\d+-\d+_.*\.tar\.gz$/', $name)) {
                    $backupFiles[] = [
                        'name' => $name,
                        'size' => $file['size'] ?? 0,
                        'mtime' => $file['mtime'] ?? 0,
                        'path' => "/home/{$this->username}/{$name}",
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
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * List files in a directory
     */
    public function listFiles(string $dir = '/'): array
    {
        try {
            $result = $this->uapi('Fileman', 'list_files', [
                'dir' => $dir,
                'include_mime' => 0,
                'include_permissions' => 1,
                'include_hash' => 0,
                'include_content' => 0,
            ]);

            return [
                'success' => true,
                'files' => $result['result']['data'] ?? [],
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Download a file from cPanel to a local path (streaming)
     */
    public function downloadFileToPath(string $remotePath, string $localPath, ?callable $progressCallback = null): array
    {
        $url = $this->getBaseUrl().'/download?file='.urlencode($remotePath);

        Log::info('cPanel download starting', ['remote' => $remotePath, 'local' => $localPath]);

        try {
            // First, get the file size
            $files = $this->listFiles(dirname($remotePath));
            $fileSize = 0;
            if ($files['success']) {
                foreach ($files['files'] as $file) {
                    if (($file['file'] ?? $file['name'] ?? '') === basename($remotePath)) {
                        $fileSize = (int) ($file['size'] ?? 0);
                        break;
                    }
                }
            }

            // Create a stream context for downloading
            $contextOpts = [
                'http' => [
                    'method' => 'GET',
                    'header' => "Authorization: cpanel {$this->username}:{$this->apiToken}\r\n",
                    'timeout' => 3600, // 1 hour timeout for large files
                    'ignore_errors' => true,
                ],
            ];

            if (! $this->verifySsl) {
                $contextOpts['ssl'] = [
                    'verify_peer' => false,
                    'verify_peer_name' => false,
                ];
            }

            $context = stream_context_create($contextOpts);

            // Open remote file
            $remoteStream = @fopen($url, 'rb', false, $context);
            if (! $remoteStream) {
                throw new Exception("Failed to open remote file: $remotePath");
            }

            // Ensure local directory exists
            $localDir = dirname($localPath);
            if (! is_dir($localDir)) {
                mkdir($localDir, 0755, true);
            }

            // Open local file for writing
            $localStream = fopen($localPath, 'wb');
            if (! $localStream) {
                fclose($remoteStream);
                throw new Exception("Failed to open local file for writing: $localPath");
            }

            // Download in chunks
            $downloaded = 0;
            $chunkSize = 1024 * 1024; // 1MB chunks

            while (! feof($remoteStream)) {
                $chunk = fread($remoteStream, $chunkSize);
                if ($chunk === false) {
                    break;
                }
                fwrite($localStream, $chunk);
                $downloaded += strlen($chunk);

                if ($progressCallback && $fileSize > 0) {
                    $progressCallback($downloaded, $fileSize);
                }
            }

            fclose($remoteStream);
            fclose($localStream);

            // Verify file was downloaded
            if (! file_exists($localPath) || filesize($localPath) === 0) {
                throw new Exception('Download failed - file is empty or missing');
            }

            Log::info('cPanel download completed', [
                'remote' => $remotePath,
                'local' => $localPath,
                'size' => filesize($localPath),
            ]);

            return [
                'success' => true,
                'path' => $localPath,
                'size' => filesize($localPath),
            ];
        } catch (Exception $e) {
            Log::error('cPanel download error: '.$e->getMessage(), [
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
     * Get homedir path
     */
    public function getHomedir(): string
    {
        return "/home/{$this->username}";
    }

    /**
     * Get the cPanel username
     */
    public function getUsername(): string
    {
        return $this->username;
    }

    /**
     * Import an SSH public key to cPanel
     * Uses API2 SSH::importkey function
     *
     * @param  string  $keyName  Name for the key in cPanel
     * @param  string  $publicKey  The SSH public key content
     */
    public function importSshKey(string $keyName, string $publicKey): array
    {
        try {
            Log::info('Importing SSH key to cPanel', ['key_name' => $keyName, 'key_length' => strlen($publicKey)]);

            // Use API2 SSH::importkey
            $result = $this->api2('SSH', 'importkey', [
                'key' => $publicKey,
                'name' => $keyName,
            ]);

            Log::info('cPanel SSH import response', ['result' => $result]);

            $data = $result['cpanelresult']['data'][0] ?? [];
            $apiError = $result['cpanelresult']['error'] ?? '';

            // Check for "already exists" which is OK (can appear in different places)
            if (str_contains($apiError, 'already exists') || str_contains($data['reason'] ?? '', 'already exists')) {
                Log::info('SSH key already exists on cPanel - treating as success');

                return [
                    'success' => true,
                    'message' => 'SSH key already exists',
                ];
            }

            // Check for API-level error
            if ($apiError) {
                throw new Exception($apiError);
            }

            // Check for success via event.result (cPanel API2 pattern)
            $eventResult = $result['cpanelresult']['event']['result'] ?? null;
            if ($eventResult == 1) {
                return [
                    'success' => true,
                    'message' => 'SSH key imported successfully',
                ];
            }

            // Legacy check for data[0].result
            if (isset($data['result']) && $data['result'] == 1) {
                return [
                    'success' => true,
                    'message' => 'SSH key imported successfully',
                ];
            }

            // Check for alternative error locations
            $errorMsg = ($data['reason'] ?? '')
                ?: ($data['error'] ?? '')
                ?: ($result['cpanelresult']['event']['reason'] ?? '')
                ?: 'Failed to import SSH key (unknown reason)';

            throw new Exception($errorMsg);
        } catch (Exception $e) {
            Log::error('cPanel SSH key import failed: '.$e->getMessage());

            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Delete an SSH key from cPanel
     * Uses API2 SSH::delkey function
     *
     * @param  string  $keyName  Name of the key to delete
     * @param  string  $keyType  Type of key: 'key' for private, 'key.pub' for public
     */
    public function deleteSshKey(string $keyName, string $keyType = 'key'): array
    {
        try {
            Log::info('Deleting SSH key from cPanel', ['key_name' => $keyName, 'type' => $keyType]);

            $result = $this->api2('SSH', 'delkey', [
                'key' => $keyName,
                'pub' => $keyType === 'key.pub' ? 1 : 0,
            ]);

            Log::info('cPanel SSH delete response', ['result' => $result]);

            $apiError = $result['cpanelresult']['error'] ?? '';
            $eventResult = $result['cpanelresult']['event']['result'] ?? null;

            // Check for success
            if ($eventResult == 1 && empty($apiError)) {
                return [
                    'success' => true,
                    'message' => 'SSH key deleted',
                ];
            }

            // Key not found is OK
            if (str_contains($apiError, 'does not exist') || str_contains($apiError, 'not found')) {
                return [
                    'success' => true,
                    'message' => 'Key does not exist',
                ];
            }

            if ($apiError) {
                throw new Exception($apiError);
            }

            return [
                'success' => true,
                'message' => 'SSH key deleted',
            ];
        } catch (Exception $e) {
            Log::error('cPanel SSH key delete failed: '.$e->getMessage());

            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Import an SSH private key to cPanel (for outgoing connections)
     * Uses API2 SSH::importkey function
     *
     * @param  string  $keyName  Name for the key in cPanel
     * @param  string  $privateKey  The SSH private key content
     * @param  string  $passphrase  Optional passphrase for the key
     */
    public function importSshPrivateKey(string $keyName, string $privateKey, string $passphrase = ''): array
    {
        try {
            Log::info('Importing SSH private key to cPanel', ['key_name' => $keyName, 'key_length' => strlen($privateKey)]);

            // Use API2 SSH::importkey - private keys are detected by content
            $params = [
                'key' => $privateKey,
                'name' => $keyName,
            ];

            if (! empty($passphrase)) {
                $params['pass'] = $passphrase;
            }

            $result = $this->api2('SSH', 'importkey', $params);

            Log::info('cPanel SSH private key import response', ['result' => $result]);

            $data = $result['cpanelresult']['data'][0] ?? [];
            $apiError = $result['cpanelresult']['error'] ?? '';

            // Check for "already exists" which is OK
            if (str_contains($apiError, 'already exists') || str_contains($data['reason'] ?? '', 'already exists')) {
                Log::info('SSH private key already exists on cPanel - treating as success');

                return [
                    'success' => true,
                    'message' => 'SSH key already exists',
                ];
            }

            // Check for API-level error
            if ($apiError) {
                throw new Exception($apiError);
            }

            // Check for success via event.result (cPanel API2 pattern)
            $eventResult = $result['cpanelresult']['event']['result'] ?? null;
            if ($eventResult == 1) {
                return [
                    'success' => true,
                    'message' => 'SSH private key imported successfully',
                ];
            }

            // Legacy check for data[0].result
            if (isset($data['result']) && $data['result'] == 1) {
                return [
                    'success' => true,
                    'message' => 'SSH private key imported successfully',
                ];
            }

            // Check for alternative error locations
            $errorMsg = ($data['reason'] ?? '')
                ?: ($data['error'] ?? '')
                ?: ($result['cpanelresult']['event']['reason'] ?? '')
                ?: 'Failed to import SSH private key (unknown reason)';

            throw new Exception($errorMsg);
        } catch (Exception $e) {
            Log::error('cPanel SSH private key import failed: '.$e->getMessage());

            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Authorize an SSH key in cPanel (make it usable)
     * Uses API2 SSH::authkey function
     */
    public function authorizeSshKey(string $keyName): array
    {
        try {
            Log::info('Authorizing SSH key in cPanel', ['key_name' => $keyName]);

            $result = $this->api2('SSH', 'authkey', [
                'key' => $keyName,
                'action' => 'authorize',
            ]);

            Log::info('cPanel SSH authkey response', ['result' => $result]);

            $data = $result['cpanelresult']['data'][0] ?? [];
            $apiError = $result['cpanelresult']['error'] ?? '';

            // Check for "already authorized" which is OK
            if (str_contains($apiError, 'already authorized') || str_contains($data['reason'] ?? '', 'already authorized')) {
                return [
                    'success' => true,
                    'message' => 'SSH key already authorized',
                ];
            }

            // Check for API-level error first
            if ($apiError) {
                throw new Exception($apiError);
            }

            // Check for success via event.result (cPanel API2 pattern)
            $eventResult = $result['cpanelresult']['event']['result'] ?? null;
            if ($eventResult == 1) {
                return [
                    'success' => true,
                    'message' => 'SSH key authorized successfully',
                ];
            }

            // Legacy check for data[0].result
            if (isset($data['result']) && $data['result'] == 1) {
                return [
                    'success' => true,
                    'message' => 'SSH key authorized successfully',
                ];
            }

            $errorMsg = $data['reason'] ?? 'Failed to authorize SSH key';
            throw new Exception($errorMsg);
        } catch (Exception $e) {
            Log::error('cPanel SSH key authorization failed: '.$e->getMessage());

            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Create a full backup and upload to remote server via SCP with key authentication
     * The SSH key must be previously imported to cPanel using importSshKey()
     *
     * @param  string  $remoteHost  The remote server hostname/IP
     * @param  string  $remoteUser  The SSH username on the remote server
     * @param  string  $remotePath  The destination path on the remote server
     * @param  string  $keyName  Name of the SSH key stored in cPanel
     * @param  int  $remotePort  SSH port (default 22)
     */
    public function createBackupToScpWithKey(
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
                'key_passphrase' => '',  // Empty passphrase for keys generated without one
            ];

            Log::info('cPanel backup to SCP with key initiated', [
                'host' => $remoteHost,
                'user' => $remoteUser,
                'path' => $remotePath,
                'key_name' => $keyName,
                'port' => $remotePort,
            ]);

            // Use longer timeout for backup operations
            $result = $this->uapiWithTimeout('Backup', 'fullbackup_to_scp_with_key', $params, 120);

            return [
                'success' => true,
                'message' => 'Backup initiated with SCP transfer',
                'pid' => $result['result']['data']['pid'] ?? null,
                'data' => $result['result']['data'] ?? [],
            ];
        } catch (Exception $e) {
            Log::error('cPanel backup to SCP with key failed: '.$e->getMessage());

            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Legacy method - kept for backward compatibility
     *
     * @deprecated Use createBackupToScpWithKey instead
     */
    public function createBackupToScp(
        string $remoteHost,
        string $remoteUser,
        string $remotePath,
        string $privateKey,
        string $passphrase = '',
        int $remotePort = 22
    ): array {
        // This method is deprecated - the key should be imported first
        Log::warning('createBackupToScp is deprecated, use createBackupToScpWithKey instead');

        return [
            'success' => false,
            'message' => 'This method is deprecated. Import the SSH key first using importSshKey(), then use createBackupToScpWithKey()',
        ];
    }

    /**
     * Create a full backup and upload to remote server via SCP with password authentication
     */
    public function createBackupToScpWithPassword(
        string $remoteHost,
        string $remoteUser,
        string $remotePath,
        string $password,
        int $remotePort = 22
    ): array {
        try {
            $params = [
                'host' => $remoteHost,
                'user' => $remoteUser,
                'directory' => $remotePath,
                'password' => $password,
                'port' => $remotePort,
            ];

            Log::info('cPanel backup to SCP (password) initiated', [
                'host' => $remoteHost,
                'user' => $remoteUser,
                'path' => $remotePath,
                'port' => $remotePort,
            ]);

            // Use longer timeout for backup operations (120 seconds)
            $result = $this->uapiWithTimeout('Backup', 'fullbackup_to_scp_with_password', $params, 120);

            return [
                'success' => true,
                'message' => 'Backup initiated with SCP transfer',
                'pid' => $result['result']['data']['pid'] ?? null,
                'data' => $result['result']['data'] ?? [],
            ];
        } catch (Exception $e) {
            Log::error('cPanel backup to SCP (password) failed: '.$e->getMessage());

            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Export a database
     */
    public function exportDatabase(string $database): array
    {
        try {
            // Use mysqldump via cPanel's backup API
            $result = $this->uapi('Mysql', 'dump_database', [
                'dbname' => $database,
            ]);

            return [
                'success' => true,
                'data' => $result['result']['data'] ?? '',
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Get SSL certificate for a domain
     */
    public function getSslCertificate(string $domain): array
    {
        try {
            $result = $this->uapi('SSL', 'fetch_best_for_domain', [
                'domain' => $domain,
            ]);

            return [
                'success' => true,
                'certificate' => $result['result']['data'] ?? [],
            ];
        } catch (Exception $e) {
            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Download a file from cPanel
     */
    public function downloadFile(string $path): ?string
    {
        $url = $this->getBaseUrl().'/download?file='.urlencode($path);

        try {
            $pending = Http::withHeaders([
                'Authorization' => "cpanel {$this->username}:{$this->apiToken}",
            ])
                ->timeout(300);

            if (! $this->verifySsl) {
                $pending = $pending->withoutVerifying();
            }

            $response = $pending->get($url);

            if ($response->successful()) {
                return $response->body();
            }

            return null;
        } catch (Exception $e) {
            Log::error('cPanel download error: '.$e->getMessage());

            return null;
        }
    }

    /**
     * Delete/revoke the current API token from cPanel
     * This should be called after migration is complete for security
     */
    public function revokeApiToken(): array
    {
        try {
            Log::info('Attempting to revoke cPanel API token');

            // First, list all tokens to find the current one
            $listResult = $this->uapi('Tokens', 'list');
            $tokens = $listResult['result']['data'] ?? [];

            Log::info('Found API tokens', ['count' => count($tokens)]);

            // The current token should be one of these - we'll try to identify and revoke it
            // cPanel doesn't directly tell us which token we're using, but we can revoke by name
            // If the token was created with a specific name, we can target it

            // Try to revoke all tokens (user should create a new one if needed)
            // Or we can try to identify the token by checking which one works
            $revoked = false;
            foreach ($tokens as $token) {
                $tokenName = $token['name'] ?? '';
                if (empty($tokenName)) {
                    continue;
                }

                // Try to revoke this token
                try {
                    $revokeResult = $this->uapi('Tokens', 'revoke', [
                        'name' => $tokenName,
                    ]);

                    if (($revokeResult['result']['status'] ?? 0) == 1) {
                        Log::info('Revoked API token', ['name' => $tokenName]);
                        $revoked = true;
                        // After revoking the current token, subsequent API calls will fail
                        // So we should stop here
                        break;
                    }
                } catch (Exception $e) {
                    // If we get an auth error, we probably just revoked our own token
                    if (str_contains($e->getMessage(), 'Authorization') || str_contains($e->getMessage(), '401')) {
                        Log::info('Token likely revoked (auth failed)', ['name' => $tokenName]);
                        $revoked = true;
                        break;
                    }
                    Log::warning('Failed to revoke token', ['name' => $tokenName, 'error' => $e->getMessage()]);
                }
            }

            if ($revoked) {
                return [
                    'success' => true,
                    'message' => 'API token revoked successfully',
                ];
            }

            return [
                'success' => false,
                'message' => 'Could not identify token to revoke',
            ];
        } catch (Exception $e) {
            // Auth failure after revoke is actually success
            if (str_contains($e->getMessage(), 'Authorization') || str_contains($e->getMessage(), '401')) {
                return [
                    'success' => true,
                    'message' => 'API token revoked (connection closed)',
                ];
            }

            Log::error('Failed to revoke API token: '.$e->getMessage());

            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Revoke a specific API token by name
     */
    public function revokeApiTokenByName(string $tokenName): array
    {
        try {
            Log::info('Revoking cPanel API token by name', ['name' => $tokenName]);

            $result = $this->uapi('Tokens', 'revoke', [
                'name' => $tokenName,
            ]);

            if (($result['result']['status'] ?? 0) == 1) {
                return [
                    'success' => true,
                    'message' => 'API token revoked successfully',
                ];
            }

            $error = $result['result']['errors'][0] ?? 'Unknown error';
            throw new Exception($error);
        } catch (Exception $e) {
            // Auth failure after revoke means it worked
            if (str_contains($e->getMessage(), 'Authorization') || str_contains($e->getMessage(), '401')) {
                return [
                    'success' => true,
                    'message' => 'API token revoked',
                ];
            }

            Log::error('Failed to revoke API token: '.$e->getMessage());

            return [
                'success' => false,
                'message' => $e->getMessage(),
            ];
        }
    }

    /**
     * Get a comprehensive migration summary
     */
    public function getMigrationSummary(): array
    {
        $summary = [
            'success' => true,
            'domains' => [
                'main' => '',
                'addon' => [],
                'sub' => [],
                'parked' => [],
            ],
            'databases' => [],
            'email_accounts' => [],
            'forwarders' => [],
            'ssl_certificates' => [],
            'cron_jobs' => [],
            'errors' => [],
        ];

        // Get domains
        try {
            $domains = $this->listDomains();
            if ($domains['success']) {
                $summary['domains'] = [
                    'main' => $domains['main_domain'] ?? '',
                    'addon' => $domains['addon_domains'] ?? [],
                    'sub' => $domains['sub_domains'] ?? [],
                    'parked' => $domains['parked_domains'] ?? [],
                ];
            } else {
                $summary['errors'][] = 'Domains: '.($domains['message'] ?? 'Unknown error');
            }
        } catch (Exception $e) {
            Log::warning('cPanel migration - failed to list domains: '.$e->getMessage());
            $summary['errors'][] = 'Domains: '.$e->getMessage();
        }

        // Get databases
        try {
            $databases = $this->listDatabases();
            if ($databases['success']) {
                $summary['databases'] = $databases['databases'] ?? [];
            } else {
                $summary['errors'][] = 'Databases: '.($databases['message'] ?? 'Unknown error');
            }
        } catch (Exception $e) {
            Log::warning('cPanel migration - failed to list databases: '.$e->getMessage());
            $summary['errors'][] = 'Databases: '.$e->getMessage();
        }

        // Get email accounts
        try {
            $emails = $this->listEmailAccounts();
            if ($emails['success']) {
                $summary['email_accounts'] = $emails['accounts'] ?? [];
            } else {
                $summary['errors'][] = 'Email: '.($emails['message'] ?? 'Unknown error');
            }
        } catch (Exception $e) {
            Log::warning('cPanel migration - failed to list email accounts: '.$e->getMessage());
            $summary['errors'][] = 'Email: '.$e->getMessage();
        }

        // Get forwarders
        try {
            $forwarders = $this->listForwarders();
            if ($forwarders['success']) {
                $summary['forwarders'] = $forwarders['forwarders'] ?? [];
            }
        } catch (Exception $e) {
            Log::warning('cPanel migration - failed to list forwarders: '.$e->getMessage());
        }

        // Get SSL certificates
        try {
            $ssl = $this->listSslCertificates();
            if ($ssl['success']) {
                $summary['ssl_certificates'] = $ssl['certificates'] ?? [];
            }
        } catch (Exception $e) {
            Log::warning('cPanel migration - failed to list SSL certificates: '.$e->getMessage());
        }

        // Get cron jobs
        try {
            $cron = $this->listCronJobs();
            if ($cron['success']) {
                $summary['cron_jobs'] = $cron['cron_jobs'] ?? [];
            }
        } catch (Exception $e) {
            Log::warning('cPanel migration - failed to list cron jobs: '.$e->getMessage());
        }

        // Log summary for debugging
        Log::info('cPanel migration summary', [
            'domains_main' => $summary['domains']['main'],
            'domains_addon_count' => count($summary['domains']['addon']),
            'databases_count' => count($summary['databases']),
            'email_accounts_count' => count($summary['email_accounts']),
            'errors' => $summary['errors'],
        ]);

        return $summary;
    }
}
