<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Services\Migration\MigrationDnsSyncService;
use App\Services\Migration\WhmApiService;
use App\Services\Migration\WhmMigrationStatusStore;
use App\Support\Formatter;
use App\Support\ServerFacts;
use Exception;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Hash;
use Illuminate\Support\Facades\Log;

class RunWhmMigrationBatch implements ShouldQueue
{
    use Queueable;

    public int $tries = 1;

    public int $timeout = 7200;

    private bool $shouldReloadFpm = false;

    /**
     * @param  array<int, array<string, mixed>>  $accounts
     * @param  array<int, string>  $selectedAccounts
     */
    public function __construct(
        public string $cacheKey,
        public string $hostname,
        public string $whmUsername,
        public string $apiToken,
        public int $port,
        public bool $useSSL,
        public array $accounts,
        public array $selectedAccounts,
        public bool $restoreFiles,
        public bool $restoreDatabases,
        public bool $restoreEmails,
        public bool $restoreSsl,
        public bool $createLinuxUsers,
    ) {}

    public function handle(AgentClient $agent, MigrationDnsSyncService $dnsSyncService): void
    {
        $store = new WhmMigrationStatusStore($this->cacheKey);
        $store->initialize($this->selectedAccounts);
        $reloadDelaySeconds = 15;

        try {
            $whm = new WhmApiService(
                $this->hostname,
                $this->whmUsername,
                $this->apiToken,
                $this->port,
                $this->useSSL,
            );

            $accountsByUser = $this->indexAccountsByUser($this->accounts);

            foreach ($this->selectedAccounts as $cpanelUser) {
                $this->migrateAccount($store, $whm, $agent, $dnsSyncService, $cpanelUser, $accountsByUser[$cpanelUser] ?? []);
            }

            $store->setMigrating(false);

            try {
                $agent->send('service.reload', ['service' => 'nginx']);
            } catch (Exception $e) {
                Log::warning('Failed to reload nginx after WHM migration', ['error' => $e->getMessage()]);
            }

            if ($this->shouldReloadFpm) {
                try {
                    $agent->send('php.reload_all_fpm', [
                        'background' => true,
                        'delay' => $reloadDelaySeconds,
                    ]);
                } catch (Exception $e) {
                    Log::warning('Failed to reload PHP-FPM after WHM migration', ['error' => $e->getMessage()]);
                }
            }
        } catch (Exception $e) {
            Log::error('WHM migration batch failed', ['error' => $e->getMessage()]);
        } finally {
            $state = $store->get();
            if (($state['isMigrating'] ?? false) === true) {
                $store->setMigrating(false);
            }
        }
    }

    /**
     * @param  array<string, mixed>  $account
     */
    protected function migrateAccount(WhmMigrationStatusStore $store, WhmApiService $whm, AgentClient $agent, MigrationDnsSyncService $dnsSyncService, string $cpanelUser, array $account): void
    {
        $store->updateAccountStatus($cpanelUser, 'processing', __('Starting migration...'));

        try {
            $domain = $account['domain'] ?? '';
            $email = $account['email'] ?? ($domain !== '' ? "{$cpanelUser}@{$domain}" : "{$cpanelUser}@example.com");

            $user = $this->createOrGetUser($agent, $cpanelUser, $email);
            if (! $user) {
                throw new Exception(__('Failed to create user'));
            }

            $store->addAccountLog($cpanelUser, __('User ready: :username', ['username' => $user->username]), 'success');

            $store->updateAccountStatus($cpanelUser, 'backup_creating', __('Setting up backup transfer...'));

            $keyName = $this->getSshKeyName();
            $destPath = $this->getBackupDestPath();

            if (! is_dir($destPath)) {
                mkdir($destPath, 0755, true);
            }

            $agent->send('jabali_ssh.ensure_exists', []);

            $publicKeyResult = $agent->send('jabali_ssh.get_public_key', []);
            if (! ($publicKeyResult['success'] ?? false) || ! ($publicKeyResult['exists'] ?? false)) {
                throw new Exception(__('Failed to get Jabali public key'));
            }
            $publicKey = $publicKeyResult['public_key'] ?? null;

            $agent->send('jabali_ssh.add_to_authorized_keys', [
                'public_key' => $publicKey,
                'comment' => 'whm-migration-'.$cpanelUser,
            ]);

            $privateKeyResult = $agent->send('jabali_ssh.get_private_key', []);
            if (! ($privateKeyResult['success'] ?? false) || ! ($privateKeyResult['exists'] ?? false)) {
                throw new Exception(__('Failed to read Jabali private key'));
            }

            $privateKey = $privateKeyResult['private_key'] ?? null;
            if (empty($privateKey)) {
                throw new Exception(__('Private key is empty'));
            }

            $store->addAccountLog($cpanelUser, __('Importing SSH key to cPanel...'), 'pending');

            $importResult = $whm->importSshPrivateKey($cpanelUser, $keyName, $privateKey);
            if (! ($importResult['success'] ?? false)) {
                throw new Exception($importResult['message'] ?? __('Failed to import SSH key'));
            }

            $actualKeyName = $importResult['actual_key_name'] ?? $keyName;
            $store->addAccountLog($cpanelUser, __('SSH key imported'), 'success');

            $authResult = $whm->authorizeSshKey($cpanelUser, $actualKeyName);
            if (! ($authResult['success'] ?? false)) {
                $store->addAccountLog($cpanelUser, __('SSH key authorization skipped'), 'info');
            } else {
                $store->addAccountLog($cpanelUser, __('SSH key authorized'), 'success');
            }

            $store->addAccountLog($cpanelUser, __('Initiating backup transfer...'), 'pending');

            $jabaliIp = $this->getJabaliPublicIp();

            $backupResult = $whm->createBackupToScpWithKey(
                $cpanelUser,
                $jabaliIp,
                'root',
                $destPath,
                $actualKeyName,
                22
            );

            if (! ($backupResult['success'] ?? false)) {
                throw new Exception($backupResult['message'] ?? __('Failed to start backup'));
            }

            $store->addAccountLog($cpanelUser, __('Backup initiated, transferring via SCP...'), 'success');

            $store->updateAccountStatus($cpanelUser, 'backup_downloading', __('Waiting for backup file...'));

            $backupPath = $this->waitForBackupFile($agent, $store, $cpanelUser, $destPath);
            if (! $backupPath) {
                throw new Exception(__('Backup file did not arrive'));
            }

            $store->addAccountLog($cpanelUser, __('Backup received: :size', ['size' => Formatter::bytes(filesize($backupPath))]), 'success');

            $summary = $whm->getUserMigrationSummary($cpanelUser);
            $discoveredData = $whm->convertApiDataToAgentFormat($summary);

            $store->updateAccountStatus($cpanelUser, 'restoring', __('Restoring data...'));

            $result = $agent->send('cpanel.restore_backup', [
                'backup_path' => $backupPath,
                'username' => $user->username,
                'restore_files' => $this->restoreFiles,
                'restore_databases' => $this->restoreDatabases,
                'restore_emails' => $this->restoreEmails,
                'restore_ssl' => $this->restoreSsl,
                'discovered_data' => $discoveredData,
            ]);

            if ($result['success'] ?? false) {
                foreach ($result['log'] ?? [] as $entry) {
                    $store->addAccountLog($cpanelUser, $entry['message'], $entry['status'] ?? 'info');
                }

                $this->syncDnsZones($dnsSyncService, $user, $discoveredData);

                $store->updateAccountStatus($cpanelUser, 'completed', __('Migration completed'));
                @unlink($backupPath);
            } else {
                throw new Exception($result['error'] ?? __('Restore failed'));
            }
        } catch (Exception $e) {
            Log::error('WHM migration failed for user', ['user' => $cpanelUser, 'error' => $e->getMessage()]);
            $store->updateAccountStatus($cpanelUser, 'error', $e->getMessage(), 'error');
        }
    }

    protected function createOrGetUser(AgentClient $agent, string $cpanelUser, string $email): ?User
    {
        $existingUser = User::where('username', $cpanelUser)->first();
        if ($existingUser) {
            return $existingUser;
        }

        if (User::where('email', $email)->exists()) {
            $email = "{$cpanelUser}.".time().'@'.explode('@', $email)[1];
        }

        $password = bin2hex(random_bytes(12));

        try {
            if ($this->createLinuxUsers) {
                $linuxUserExists = false;
                if (function_exists('posix_getpwnam')) {
                    $linuxUserExists = posix_getpwnam($cpanelUser) !== false;
                } else {
                    $linuxUserExists = is_dir('/home/'.$cpanelUser);
                }

                if (! $linuxUserExists) {
                    $result = $agent->send('user.create', [
                        'username' => $cpanelUser,
                        'password' => $password,
                    ]);

                    if (! ($result['success'] ?? false)) {
                        throw new Exception($result['error'] ?? __('Failed to create system user'));
                    }

                    if (($result['fpm_pool_created'] ?? false) === true) {
                        $this->shouldReloadFpm = true;
                    }
                }
            }

            return User::create([
                'name' => ucfirst($cpanelUser),
                'username' => $cpanelUser,
                'email' => $email,
                'password' => Hash::make($password),
                'home_directory' => '/home/'.$cpanelUser,
                'disk_quota_mb' => null,
                'is_active' => true,
                'is_admin' => false,
            ]);
        } catch (Exception $e) {
            Log::error('Failed to create user', ['username' => $cpanelUser, 'error' => $e->getMessage()]);

            return null;
        }
    }

    protected function waitForBackupFile(AgentClient $agent, WhmMigrationStatusStore $store, string $cpanelUser, string $destPath): ?string
    {
        $maxAttempts = 120;
        $attempt = 0;
        $lastSeenSize = 0;
        $sizeStableCount = 0;

        while ($attempt < $maxAttempts) {
            $attempt++;
            sleep(5);

            $pattern = "{$destPath}/backup-*_{$cpanelUser}.tar.gz";
            $files = glob($pattern);

            if (empty($files)) {
                $pattern = "{$destPath}/cpmove-{$cpanelUser}.tar.gz";
                $files = glob($pattern);
            }

            if (empty($files)) {
                if ($attempt % 6 === 0) {
                    $store->addAccountLog($cpanelUser, __('Waiting for backup file... (:count s)', ['count' => $attempt * 5]), 'pending');
                }

                continue;
            }

            usort($files, fn ($a, $b) => filemtime($b) - filemtime($a));
            $backupFile = $files[0];
            $currentSize = filesize($backupFile);

            if ($currentSize > 0 && $currentSize === $lastSeenSize) {
                $sizeStableCount++;
            } else {
                $sizeStableCount = 0;
            }
            $lastSeenSize = $currentSize;

            if ($sizeStableCount >= 3 && $currentSize >= 10 * 1024) {
                $agent->send('file.chown', [
                    'path' => $backupFile,
                    'owner' => 'www-data',
                    'group' => 'www-data',
                ]);

                $handle = fopen($backupFile, 'rb');
                $magic = $handle ? fread($handle, 2) : '';
                if ($handle) {
                    fclose($handle);
                }

                if ($magic === "\x1f\x8b") {
                    return $backupFile;
                }

                $store->addAccountLog($cpanelUser, __('Invalid backup file format, waiting...'), 'warning');
                $sizeStableCount = 0;
            }

            if ($attempt % 6 === 0) {
                $store->addAccountLog($cpanelUser, __('Receiving backup... :size', [
                    'size' => Formatter::bytes($currentSize),
                ]), 'pending');
            }
        }

        return null;
    }

    /**
     * @param  array<int, array<string, mixed>>  $accounts
     * @return array<string, array<string, mixed>>
     */
    protected function indexAccountsByUser(array $accounts): array
    {
        $indexed = [];

        foreach ($accounts as $account) {
            if (! isset($account['user'])) {
                continue;
            }

            $indexed[$account['user']] = $account;
        }

        return $indexed;
    }

    protected function getBackupDestPath(): string
    {
        return '/var/backups/jabali/whm-migrations';
    }

    protected function getSshKeyName(): string
    {
        return 'jabali-system-key';
    }

    /**
     * @param  array<string, mixed>  $discoveredData
     */
    protected function syncDnsZones(MigrationDnsSyncService $dnsSyncService, User $user, array $discoveredData): void
    {
        try {
            $domains = $discoveredData['domains'] ?? [];
            $dnsSyncService->syncDomainsForUser($user, $domains);
        } catch (Exception $e) {
            Log::warning('Failed to sync DNS zones after WHM migration', [
                'user' => $user->username,
                'error' => $e->getMessage(),
            ]);
        }
    }

    protected function getJabaliPublicIp(): string
    {
        $ip = ServerFacts::serverIp('');
        if ($ip !== '') {
            return $ip;
        }

        $fallback = gethostbyname(gethostname() ?: 'localhost');

        return is_string($fallback) ? $fallback : '';
    }
}
