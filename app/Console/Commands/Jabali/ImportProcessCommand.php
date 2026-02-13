<?php

declare(strict_types=1);

namespace App\Console\Commands\Jabali;

use App\Models\Domain;
use App\Models\ServerImport;
use App\Models\ServerImportAccount;
use App\Models\User;
use App\Services\Agent\AgentClient;
use Exception;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\Hash;
use Illuminate\Support\Facades\Storage;
use Illuminate\Support\Str;

class ImportProcessCommand extends Command
{
    protected $signature = 'import:process {import_id : The server import ID to process}';

    protected $description = 'Process a server import job (cPanel/DirectAdmin migration)';

    private ?AgentClient $agent = null;

    public function handle(): int
    {
        $importId = (int) $this->argument('import_id');

        $import = ServerImport::with('accounts')->find($importId);
        if (! $import) {
            $this->error("Import not found: $importId");

            return 1;
        }

        $this->info("Processing import: {$import->name} (ID: {$import->id})");

        $selectedAccountIds = $import->selected_accounts ?? [];
        $options = $import->import_options ?? [];

        if (empty($selectedAccountIds)) {
            $import->update([
                'status' => 'failed',
                'current_task' => null,
            ]);
            $import->addError('No accounts selected for import');

            return 1;
        }

        $accounts = ServerImportAccount::whereIn('id', $selectedAccountIds)
            ->where('server_import_id', $import->id)
            ->get();

        $totalAccounts = $accounts->count();
        $completedAccounts = 0;

        $import->addLog("Starting import of $totalAccounts account(s)");

        foreach ($accounts as $account) {
            try {
                $this->processAccount($import, $account, $options);
                $completedAccounts++;

                $progress = (int) (($completedAccounts / $totalAccounts) * 100);
                $import->update(['progress' => $progress]);
            } catch (Exception $e) {
                $account->update([
                    'status' => 'failed',
                    'error' => $e->getMessage(),
                ]);
                $account->addLog('Import failed: '.$e->getMessage());
                $import->addError("Account {$account->source_username}: ".$e->getMessage());
            }
        }

        $failedCount = $accounts->where('status', 'failed')->count();

        if ($failedCount === $totalAccounts) {
            $import->update([
                'status' => 'failed',
                'current_task' => null,
                'completed_at' => now(),
                'progress' => 100,
            ]);
        } elseif ($failedCount > 0) {
            $import->update([
                'status' => 'completed',
                'current_task' => null,
                'completed_at' => now(),
                'progress' => 100,
            ]);
            $import->addLog("Completed with $failedCount failed account(s)");
        } else {
            $import->update([
                'status' => 'completed',
                'current_task' => null,
                'completed_at' => now(),
                'progress' => 100,
            ]);
            $import->addLog('All accounts imported successfully');
        }

        $this->info('Import completed. Success: '.($totalAccounts - $failedCount).", Failed: $failedCount");

        return 0;
    }

    private function getAgent(): AgentClient
    {
        if ($this->agent === null) {
            $this->agent = new AgentClient;
        }

        return $this->agent;
    }

    private function processAccount(ServerImport $import, ServerImportAccount $account, array $options): void
    {
        $account->update([
            'status' => 'importing',
            'progress' => 0,
            'current_task' => 'Starting import...',
        ]);
        $account->addLog("Starting import for account: {$account->source_username}");

        $import->update(['current_task' => "Importing account: {$account->source_username}"]);

        // Step 1: Create user
        $account->update(['current_task' => 'Creating user...', 'progress' => 10]);
        $user = $this->createUser($account);
        $account->addLog("Created user: {$user->email}");

        // Step 2: Create domains
        if ($account->main_domain) {
            $account->update(['current_task' => 'Creating domains...', 'progress' => 20]);
            $this->createDomains($account, $user);
            $account->addLog('Created domains');
        }

        // Step 3: Import files
        if ($options['files'] ?? true) {
            $account->update(['current_task' => 'Importing files...', 'progress' => 40]);
            $this->importFiles($import, $account, $user);
            $account->addLog('Files imported');
        }

        // Step 4: Import databases
        if (($options['databases'] ?? true) && ! empty($account->databases)) {
            $account->update(['current_task' => 'Importing databases...', 'progress' => 60]);
            $this->importDatabases($import, $account, $user);
            $account->addLog('Databases imported');
        }

        // Step 5: Import emails
        if (($options['emails'] ?? true) && ! empty($account->email_accounts)) {
            $account->update(['current_task' => 'Importing email accounts...', 'progress' => 80]);
            $this->importEmails($import, $account, $user);
            $account->addLog('Email accounts imported');
        }

        $account->update([
            'status' => 'completed',
            'progress' => 100,
            'current_task' => null,
        ]);
        $account->addLog('Import completed successfully');
    }

    private function createUser(ServerImportAccount $account): User
    {
        // Check if user already exists with this username
        $existingUser = User::where('username', $account->target_username)->first();
        if ($existingUser) {
            $account->addLog("User already exists: {$account->target_username}");

            return $existingUser;
        }

        // Generate a temporary password
        $password = Str::random(16);

        // Create user via agent
        $result = $this->getAgent()->createUser($account->target_username, $password);

        if (! ($result['success'] ?? false)) {
            throw new Exception('Failed to create system user: '.($result['error'] ?? 'Unknown error'));
        }

        // Create user in database
        $user = User::create([
            'name' => $account->target_username,
            'email' => $account->email ?: "{$account->target_username}@localhost",
            'username' => $account->target_username,
            'password' => Hash::make($password),
        ]);

        $account->addLog('Created user with temporary password. User should reset password.');

        return $user;
    }

    private function createDomains(ServerImportAccount $account, User $user): void
    {
        // Create main domain
        if ($account->main_domain) {
            $existingDomain = Domain::where('domain', $account->main_domain)->first();
            if (! $existingDomain) {
                $result = $this->getAgent()->domainCreate($user->username, $account->main_domain);

                if ($result['success'] ?? false) {
                    Domain::create([
                        'domain' => $account->main_domain,
                        'user_id' => $user->id,
                        'document_root' => "/home/{$user->username}/domains/{$account->main_domain}/public_html",
                        'is_active' => true,
                    ]);
                    $account->addLog("Created main domain: {$account->main_domain}");
                } else {
                    $account->addLog('Warning: Failed to create main domain: '.($result['error'] ?? 'Unknown'));
                }
            } else {
                $account->addLog("Main domain already exists: {$account->main_domain}");
            }
        }

        // Create addon domains
        foreach ($account->addon_domains ?? [] as $domain) {
            $existingDomain = Domain::where('domain', $domain)->first();
            if (! $existingDomain) {
                $result = $this->getAgent()->domainCreate($user->username, $domain);

                if ($result['success'] ?? false) {
                    Domain::create([
                        'domain' => $domain,
                        'user_id' => $user->id,
                        'document_root' => "/home/{$user->username}/domains/{$domain}/public_html",
                        'is_active' => true,
                    ]);
                    $account->addLog("Created addon domain: {$domain}");
                } else {
                    $account->addLog("Warning: Failed to create addon domain: {$domain}");
                }
            }
        }
    }

    private function importFiles(ServerImport $import, ServerImportAccount $account, User $user): void
    {
        if ($import->import_method !== 'backup_file' || ! $import->backup_path) {
            $account->addLog('File import skipped - not a backup file import');

            return;
        }

        $backupPath = $this->resolveBackupFullPath($import);
        if (! $backupPath) {
            $account->addLog('Warning: Backup file not found');

            return;
        }

        $extractDir = "/tmp/import_{$import->id}_{$account->id}_".time();
        if (! mkdir($extractDir, 0755, true)) {
            $account->addLog('Warning: Failed to create extraction directory');

            return;
        }

        try {
            $username = $account->source_username;
            $tarExtract = $this->getTarExtractCommandPrefix($backupPath);

            if ($import->source_type === 'cpanel') {
                // Extract home directory from cPanel backup
                $cmd = "{$tarExtract} ".escapeshellarg($backupPath).' -C '.escapeshellarg($extractDir).
                    " --wildcards '*/{$username}/homedir/*' '*/homedir/*' 2>/dev/null";
                exec($cmd, $output, $code);
                if ($code !== 0) {
                    $account->addLog('Warning: Failed to extract backup archive');
                }

                // Find extracted files
                $homeDirs = glob("$extractDir/**/homedir", GLOB_ONLYDIR) ?:
                    glob("$extractDir/*/homedir", GLOB_ONLYDIR) ?:
                    glob("$extractDir/homedir", GLOB_ONLYDIR) ?: [];

                foreach ($homeDirs as $homeDir) {
                    // Copy public_html to the domain
                    $publicHtml = "$homeDir/public_html";
                    if (is_dir($publicHtml) && $account->main_domain) {
                        $destDir = "/home/{$user->username}/domains/{$account->main_domain}/public_html";
                        if (is_dir($destDir)) {
                            exec('cp -r '.escapeshellarg($publicHtml).'/* '.escapeshellarg($destDir).'/ 2>&1');
                            exec('chown -R '.escapeshellarg($user->username).':'.escapeshellarg($user->username).' '.escapeshellarg($destDir).' 2>&1');
                            $account->addLog("Copied public_html to {$account->main_domain}");
                        }
                    }
                }
            } else {
                // Extract from DirectAdmin backup
                $cmd = "{$tarExtract} ".escapeshellarg($backupPath).' -C '.escapeshellarg($extractDir).
                    " --wildcards 'domains/*' 'backup/domains/*' 2>/dev/null";
                exec($cmd, $output, $code);
                if ($code !== 0) {
                    $account->addLog('Warning: Failed to extract DirectAdmin backup archive');
                }

                // Find domain directories
                $domainDirs = glob("$extractDir/**/domains/*", GLOB_ONLYDIR) ?:
                    glob("$extractDir/domains/*", GLOB_ONLYDIR) ?: [];

                foreach ($domainDirs as $domainDir) {
                    $domain = basename($domainDir);
                    $publicHtml = "$domainDir/public_html";

                    if (is_dir($publicHtml)) {
                        $destDir = "/home/{$user->username}/domains/{$domain}/public_html";
                        if (is_dir($destDir)) {
                            exec('cp -r '.escapeshellarg($publicHtml).'/* '.escapeshellarg($destDir).'/ 2>&1');
                            exec('chown -R '.escapeshellarg($user->username).':'.escapeshellarg($user->username).' '.escapeshellarg($destDir).' 2>&1');
                            $account->addLog("Copied files for domain: {$domain}");
                        }
                    }
                }
            }
        } finally {
            // Cleanup
            exec('rm -rf '.escapeshellarg($extractDir));
        }
    }

    private function importDatabases(ServerImport $import, ServerImportAccount $account, User $user): void
    {
        if ($import->import_method !== 'backup_file' || ! $import->backup_path) {
            $account->addLog('Database import skipped - not a backup file import');

            return;
        }

        $backupPath = $this->resolveBackupFullPath($import);
        if (! $backupPath) {
            return;
        }

        $extractDir = "/tmp/import_db_{$import->id}_{$account->id}_".time();
        if (! mkdir($extractDir, 0755, true)) {
            return;
        }

        try {
            $tarExtract = $this->getTarExtractCommandPrefix($backupPath);

            // Extract MySQL dumps
            if ($import->source_type === 'cpanel') {
                $cmd = "{$tarExtract} ".escapeshellarg($backupPath).' -C '.escapeshellarg($extractDir).
                    " --wildcards '*/mysql/*.sql*' 'mysql/*.sql*' 2>/dev/null";
            } else {
                $cmd = "{$tarExtract} ".escapeshellarg($backupPath).' -C '.escapeshellarg($extractDir).
                    " --wildcards 'backup/databases/*.sql*' 'databases/*.sql*' 2>/dev/null";
            }
            exec($cmd, $output, $code);
            if ($code !== 0) {
                $account->addLog('Warning: Failed to extract database dumps from backup archive');
            }

            // Find SQL files
            $sqlFiles = [];
            exec('find '.escapeshellarg($extractDir)." -type f \\( -name '*.sql' -o -name '*.sql.gz' -o -name '*.sql.zst' \\) 2>/dev/null", $sqlFiles);

            foreach ($sqlFiles as $sqlFile) {
                $fileName = basename($sqlFile);
                $dbName = preg_replace('/\\.(sql|sql\\.gz|sql\\.zst)$/i', '', $fileName);

                if (! is_string($dbName) || $dbName === '') {
                    continue;
                }

                // Create database name with user prefix
                $newDbName = substr($user->username.'_'.preg_replace('/^[^_]+_/', '', $dbName), 0, 64);

                // Create database via agent
                $result = $this->getAgent()->mysqlCreateDatabase($user->username, $newDbName);

                if ($result['success'] ?? false) {
                    $sqlToImport = $sqlFile;
                    $tmpSql = null;

                    try {
                        $lower = strtolower($sqlFile);

                        if (str_ends_with($lower, '.sql.gz')) {
                            $tmpSql = $extractDir.'/import_'.$account->id.'_'.$dbName.'_'.uniqid('', true).'.sql';
                            $decompressCmd = 'gzip -dc '.escapeshellarg($sqlFile).' > '.escapeshellarg($tmpSql).' 2>/dev/null';
                            exec($decompressCmd, $decompressOutput, $decompressCode);

                            if ($decompressCode !== 0) {
                                $account->addLog("Warning: Failed to decompress database dump: {$fileName}");

                                continue;
                            }

                            $sqlToImport = $tmpSql;
                        } elseif (str_ends_with($lower, '.sql.zst')) {
                            $tmpSql = $extractDir.'/import_'.$account->id.'_'.$dbName.'_'.uniqid('', true).'.sql';
                            $decompressCmd = 'zstd -dc '.escapeshellarg($sqlFile).' > '.escapeshellarg($tmpSql).' 2>/dev/null';
                            exec($decompressCmd, $decompressOutput, $decompressCode);

                            if ($decompressCode !== 0) {
                                $account->addLog("Warning: Failed to decompress database dump: {$fileName}");

                                continue;
                            }

                            $sqlToImport = $tmpSql;
                        }

                        // Import data
                        $cmd = 'mysql '.escapeshellarg($newDbName).' < '.escapeshellarg($sqlToImport).' 2>&1';
                        exec($cmd, $importOutput, $importCode);

                        if ($importCode === 0) {
                            $account->addLog("Imported database: {$newDbName}");
                        } else {
                            $account->addLog("Warning: Database created but import failed: {$newDbName}");
                        }
                    } finally {
                        if ($tmpSql && file_exists($tmpSql)) {
                            @unlink($tmpSql);
                        }
                    }
                } else {
                    $account->addLog("Warning: Failed to create database: {$newDbName}");
                }
            }
        } finally {
            exec('rm -rf '.escapeshellarg($extractDir));
        }
    }

    private function importEmails(ServerImport $import, ServerImportAccount $account, User $user): void
    {
        // Email import is complex and requires the email system to be configured
        // For now, just log the email accounts that would be created
        foreach ($account->email_accounts ?? [] as $emailAccount) {
            $account->addLog("Email account found (not imported): {$emailAccount}@{$account->main_domain}");
        }

        $account->addLog('Note: Email accounts must be recreated manually');
    }

    private function resolveBackupFullPath(ServerImport $import): ?string
    {
        $path = trim((string) ($import->backup_path ?? ''));
        if ($path === '') {
            return null;
        }

        if (str_starts_with($path, '/') && file_exists($path)) {
            return $path;
        }

        $localCandidate = Storage::disk('local')->path($path);
        if (file_exists($localCandidate)) {
            return $localCandidate;
        }

        $backupCandidate = Storage::disk('backups')->path($path);
        if (file_exists($backupCandidate)) {
            return $backupCandidate;
        }

        return file_exists($path) ? $path : null;
    }

    private function getTarExtractCommandPrefix(string $archivePath): string
    {
        $archivePath = strtolower($archivePath);

        if (str_ends_with($archivePath, '.tar.zst') || str_ends_with($archivePath, '.zst')) {
            return 'tar --zstd -xf';
        }

        if (str_ends_with($archivePath, '.tar.gz') || str_ends_with($archivePath, '.tgz')) {
            return 'tar -xzf';
        }

        return 'tar -xf';
    }
}
