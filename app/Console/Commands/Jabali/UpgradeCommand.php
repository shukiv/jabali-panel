<?php

declare(strict_types=1);

namespace App\Console\Commands\Jabali;

use Exception;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\Artisan;
use Illuminate\Support\Facades\File;
use Symfony\Component\Process\Process;

class UpgradeCommand extends Command
{
    protected $signature = 'jabali:upgrade
                            {--check : Only check for updates without upgrading}
                            {--force : Force upgrade even if already up to date}';

    protected $description = 'Upgrade Jabali Panel to the latest version';

    private string $basePath;

    private string $versionFile;

    public function __construct()
    {
        parent::__construct();
        $this->basePath = base_path();
        $this->versionFile = $this->basePath.'/VERSION';
    }

    public function handle(): int
    {
        if ($this->option('check')) {
            return $this->checkForUpdates();
        }

        return $this->performUpgrade();
    }

    private function checkForUpdates(): int
    {
        $this->info('Checking for updates...');

        $currentVersion = $this->getCurrentVersion();
        $this->line("Current version: <info>{$currentVersion}</info>");

        try {
            $this->configureGitSafeDirectory();
            $this->ensureGitRepository();
            $updateSource = $this->fetchUpdates();

            // Check if there are updates
            $behindCount = trim($this->executeCommandOrFail("git rev-list HEAD..{$updateSource['remoteRef']} --count"));

            if ($behindCount === '0') {
                $this->info('Jabali Panel is up to date!');

                return 0;
            }

            $this->warn("Updates available: {$behindCount} commit(s) behind");

            // Show recent commits
            $this->line("\nRecent changes:");
            $commits = $this->executeCommandOrFail("git log HEAD..{$updateSource['remoteRef']} --oneline -10");
            if ($commits !== '') {
                $this->line($commits);
            }

            return 0;
        } catch (Exception $e) {
            $this->error('Failed to check for updates: '.$e->getMessage());

            return 1;
        }
    }

    private function performUpgrade(): int
    {
        $this->info('Starting Jabali Panel upgrade...');
        $this->newLine();

        $currentVersion = $this->getCurrentVersion();
        $this->line("Current version: <info>{$currentVersion}</info>");

        // Step 1: Check git status
        $this->info('[1/9] Checking repository status...');
        try {
            $this->configureGitSafeDirectory();
            $this->ensureGitRepository();
            $this->ensureWritableStorage();
            $statusResult = $this->executeCommand('git status --porcelain');
            if ($statusResult['exitCode'] !== 0) {
                throw new Exception($statusResult['output'] ?: 'Unable to read git status.');
            }

            $status = $statusResult['output'];
            if (! empty(trim($status)) && ! $this->option('force')) {
                $this->warn('Working directory has uncommitted changes:');
                $this->line($status);
                if (! $this->confirm('Continue anyway? Local changes may be overwritten.')) {
                    $this->info('Upgrade cancelled.');

                    return 0;
                }
            }
        } catch (Exception $e) {
            $this->error('Git check failed: '.$e->getMessage());

            return 1;
        }

        // Step 2: Fetch updates
        $this->info('[2/9] Fetching updates from repository...');
        try {
            $updateSource = $this->fetchUpdates();
        } catch (Exception $e) {
            $this->error('Failed to fetch updates: '.$e->getMessage());

            return 1;
        }

        // Step 3: Check if updates available
        $behindCount = trim($this->executeCommandOrFail("git rev-list HEAD..{$updateSource['remoteRef']} --count"));
        if ($behindCount === '0' && ! $this->option('force')) {
            $this->info('Already up to date!');

            return 0;
        }

        // Step 4: Pull changes
        $oldHead = trim($this->executeCommandOrFail('git rev-parse HEAD'));
        $this->info('[3/9] Pulling latest changes...');
        try {
            $pullResult = $this->executeCommand("git pull --ff-only {$updateSource['pullRemote']} main");
            if ($pullResult['exitCode'] !== 0) {
                // Fast-forward failed — likely divergent history from a history rewrite.
                // Reset to the remote branch to adopt the canonical history.
                $this->warn('Fast-forward not possible. Resetting to remote branch...');
                $resetResult = $this->executeCommand("git reset --hard {$updateSource['remoteRef']}");
                if ($resetResult['exitCode'] !== 0) {
                    throw new Exception($resetResult['output'] ?: 'Git reset failed.');
                }
                if ($resetResult['output'] !== '') {
                    $this->line($resetResult['output']);
                }
            } elseif ($pullResult['output'] !== '') {
                $this->line($pullResult['output']);
            }
        } catch (Exception $e) {
            $this->error('Failed to pull changes: '.$e->getMessage());
            $this->warn('You may need to resolve conflicts manually.');

            return 1;
        }

        $newHead = trim($this->executeCommandOrFail('git rev-parse HEAD'));
        $changedFiles = $this->getChangedFiles($oldHead, $newHead);

        $hasVendor = File::exists($this->basePath.'/vendor/autoload.php');
        $hasManifest = File::exists($this->basePath.'/public/build/manifest.json');
        $hasPackageJson = File::exists($this->basePath.'/package.json');

        $shouldRunComposer = $this->shouldRunComposerInstall($changedFiles, $this->option('force'), $hasVendor);
        $shouldRunNpm = $this->shouldRunNpmBuild($changedFiles, $this->option('force'), $hasManifest, $hasPackageJson);
        $shouldRunMigrations = $this->shouldRunMigrations($changedFiles, $this->option('force'));

        // Step 5: Install composer dependencies
        $this->info('[4/9] Installing PHP dependencies...');
        if ($shouldRunComposer) {
            try {
                $this->ensureCommandAvailable('composer');
                $this->ensurePublicAssetsPermissions();
                $composerResult = $this->executeCommand('composer install --no-dev --no-interaction --prefer-dist --optimize-autoloader', 1200);
                if ($composerResult['exitCode'] !== 0) {
                    throw new Exception($composerResult['output'] ?: 'Composer install failed.');
                }
                if ($composerResult['output'] !== '') {
                    $this->line($composerResult['output']);
                }
            } catch (Exception $e) {
                $this->error('Failed to install dependencies: '.$e->getMessage());

                return 1;
            }
        } else {
            $this->line('No composer changes detected, skipping.');
        }

        // Step 5b: Install npm dependencies and build assets
        $this->info('[5/9] Building frontend assets...');
        if ($shouldRunNpm) {
            try {
                $this->ensureCommandAvailable('npm');
                $this->ensureNpmCacheDirectory();
                $this->ensureNodeModulesPermissions();
                $this->ensurePublicBuildPermissions();
                $nodeModulesWritable = $this->isNodeModulesWritable();
                $publicBuildWritable = $this->isPublicBuildWritable();
                if (! $nodeModulesWritable || ! $publicBuildWritable) {
                    $this->warn('Skipping frontend build because asset paths are not writable by the current user.');
                    if (! $nodeModulesWritable) {
                        $this->warn('node_modules is not writable.');
                        $this->warn('Run: sudo chown -R www-data:www-data '.$this->getNodeModulesPath());
                    }
                    if (! $publicBuildWritable) {
                        $this->warn('public/build is not writable.');
                        $this->warn('Run: sudo chown -R www-data:www-data '.$this->getPublicBuildPath());
                    }
                } else {
                    $npmInstall = File::exists($this->basePath.'/package-lock.json') ? 'npm ci' : 'npm install';
                    $installResult = $this->executeCommand($npmInstall, 1200);
                    if ($installResult['exitCode'] !== 0) {
                        throw new Exception($installResult['output'] ?: 'npm install failed.');
                    }
                    if ($installResult['output'] !== '') {
                        $this->line($installResult['output']);
                    }

                    $buildResult = $this->executeCommand('npm run build', 1200);
                    if ($buildResult['exitCode'] !== 0) {
                        throw new Exception($buildResult['output'] ?: 'npm build failed.');
                    }
                    if ($buildResult['output'] !== '') {
                        $this->line($buildResult['output']);
                    }

                    $this->ensureNodeModulesPermissions();
                    $this->ensurePublicBuildPermissions();
                }
            } catch (Exception $e) {
                $this->error('Asset build failed: '.$e->getMessage());

                return 1;
            }
        } else {
            $this->line('No frontend changes detected, skipping.');
        }

        // Step 6: Run migrations
        $this->info('[6/9] Running database migrations...');
        if ($shouldRunMigrations) {
            try {
                Artisan::call('migrate', ['--force' => true]);
                $this->line(Artisan::output());
            } catch (Exception $e) {
                $this->error('Migration failed: '.$e->getMessage());

                return 1;
            }
        } else {
            $this->line('No migration changes detected, skipping.');
        }

        // Step 7: Clear caches
        $this->info('[7/9] Clearing caches...');
        try {
            Artisan::call('optimize:clear');
            $this->line(Artisan::output());
        } catch (Exception $e) {
            $this->warn('Cache clear warning: '.$e->getMessage());
        }

        // Step 8: Setup Redis ACL if not configured
        $this->info('[8/9] Checking Redis ACL configuration...');
        $this->setupRedisAcl();

        // Step 9: Restart services
        $this->info('[9/9] Restarting services...');
        $this->restartServices();

        $newVersion = $this->getCurrentVersion();
        $this->newLine();
        $this->info("Upgrade complete! Version: {$newVersion}");

        return 0;
    }

    /**
     * @return array{pullRemote: string, remoteRef: string}
     */
    private function fetchUpdates(): array
    {
        $originUrl = trim($this->executeCommandOrFail('git remote get-url origin'));
        $fetchAttempts = [
            ['remote' => 'origin', 'ref' => 'origin/main', 'type' => 'remote'],
        ];

        if ($this->hasRemote('gitea')) {
            $fetchAttempts[] = ['remote' => 'gitea', 'ref' => 'gitea/main', 'type' => 'remote'];
        }

        if ($this->isGithubSshUrl($originUrl)) {
            $fetchAttempts[] = [
                'remote' => $this->githubHttpsUrlFromSsh($originUrl),
                'ref' => 'jabali-upgrade/main',
                'type' => 'url',
            ];
        }

        $lastError = null;
        foreach ($fetchAttempts as $attempt) {
            $result = $attempt['type'] === 'url'
                ? $this->executeCommand("git fetch {$attempt['remote']} main:refs/remotes/{$attempt['ref']}")
                : $this->executeCommand("git fetch {$attempt['remote']} main");

            if (($result['exitCode'] ?? 1) === 0) {
                return [
                    'pullRemote' => $attempt['remote'],
                    'remoteRef' => $attempt['ref'],
                ];
            }

            $lastError = $result['output'] ?? 'Unknown error';
        }

        throw new Exception($lastError ?: 'Unable to fetch updates from any configured remote.');
    }

    private function hasRemote(string $remote): bool
    {
        $result = $this->executeCommand("git remote get-url {$remote}");

        return ($result['exitCode'] ?? 1) === 0;
    }

    private function isGithubSshUrl(string $url): bool
    {
        return str_starts_with($url, 'git@github.com:') || str_starts_with($url, 'ssh://git@github.com/');
    }

    private function githubHttpsUrlFromSsh(string $url): string
    {
        if (str_starts_with($url, 'git@github.com:')) {
            $path = substr($url, strlen('git@github.com:'));

            return 'https://github.com/'.$path;
        }

        if (str_starts_with($url, 'ssh://git@github.com/')) {
            $path = substr($url, strlen('ssh://git@github.com/'));

            return 'https://github.com/'.$path;
        }

        return $url;
    }

    private function getCurrentVersion(): string
    {
        if (! File::exists($this->versionFile)) {
            return 'unknown';
        }

        $content = File::get($this->versionFile);
        if (preg_match('/VERSION=(.+)/', $content, $matches)) {
            return trim($matches[1]);
        }

        return 'unknown';
    }

    protected function executeCommand(string $command, int $timeout = 600): array
    {
        $process = Process::fromShellCommandline($command, $this->basePath, $this->getCommandEnvironment());
        $process->setTimeout($timeout);
        $process->run();
        $output = trim($process->getOutput().$process->getErrorOutput());

        return [
            'exitCode' => $process->getExitCode() ?? 1,
            'output' => $output,
        ];
    }

    protected function executeCommandOrFail(string $command, int $timeout = 600): string
    {
        $result = $this->executeCommand($command, $timeout);
        if ($result['exitCode'] !== 0) {
            throw new Exception($result['output'] ?: "Command failed: {$command}");
        }

        return $result['output'];
    }

    protected function getCommandEnvironment(): array
    {
        $path = getenv('PATH') ?: '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin';

        return [
            'PATH' => $path,
            'COMPOSER_ALLOW_SUPERUSER' => '1',
            'NPM_CONFIG_CACHE' => $this->getNpmCacheDir(),
            'PUPPETEER_SKIP_DOWNLOAD' => '1',
            'PUPPETEER_CACHE_DIR' => $this->getPuppeteerCacheDir(),
            'XDG_CACHE_HOME' => $this->getXdgCacheDir(),
        ];
    }

    protected function ensureCommandAvailable(string $command): void
    {
        $result = $this->executeCommand("command -v {$command}");
        if ($result['exitCode'] !== 0 || $result['output'] === '') {
            throw new Exception("Required command not found: {$command}");
        }
    }

    protected function ensureGitRepository(): void
    {
        if (! File::isDirectory($this->basePath.'/.git')) {
            throw new Exception('Not a git repository.');
        }
    }

    protected function configureGitSafeDirectory(): void
    {
        foreach ($this->getSafeDirectoryCommands() as $command) {
            $this->executeCommand($command);
        }
    }

    protected function ensureWritableStorage(): void
    {
        foreach ($this->getWritableStorageCommands() as $command) {
            $this->executeCommand($command);
        }
    }

    protected function getSafeDirectoryCommands(): array
    {
        $directory = $this->basePath;
        $commands = [
            "git config --global --add safe.directory {$directory}",
        ];

        if (! $this->isRunningAsRoot()) {
            return $commands;
        }

        $commands[] = "git config --system --add safe.directory {$directory}";

        if ($this->commandExists('sudo') && $this->userExists('www-data')) {
            $commands[] = "sudo -u www-data git config --global --add safe.directory {$directory}";
        }

        return $commands;
    }

    protected function getWritableStorageCommands(): array
    {
        if (! $this->isRunningAsRoot() || ! $this->userExists('www-data')) {
            return [];
        }

        $paths = [
            $this->basePath.'/database',
            $this->basePath.'/storage',
            $this->getNodeModulesPath(),
            $this->getPublicBuildPath(),
            $this->getNpmCacheDir(),
            $this->getPuppeteerCacheDir(),
            $this->getXdgCacheDir(),
            $this->basePath.'/bootstrap/cache',
        ];

        $escapedPaths = array_map('escapeshellarg', $paths);
        $pathList = implode(' ', $escapedPaths);

        return [
            "chgrp -R www-data {$pathList}",
            "chmod -R g+rwX {$pathList}",
            "find {$pathList} -type d -exec chmod g+s {} +",
        ];
    }

    protected function ensureNpmCacheDirectory(): void
    {
        $cacheDirs = [
            $this->getNpmCacheDir(),
            $this->getPuppeteerCacheDir(),
            $this->getXdgCacheDir(),
        ];

        foreach ($cacheDirs as $cacheDir) {
            if (! File::exists($cacheDir)) {
                File::ensureDirectoryExists($cacheDir);
            }

            if ($this->isRunningAsRoot() && $this->userExists('www-data')) {
                $this->executeCommand('chgrp -R www-data '.escapeshellarg($cacheDir));
                $this->executeCommand('chmod -R g+rwX '.escapeshellarg($cacheDir));
            }
        }

    }

    protected function ensureNodeModulesPermissions(): void
    {
        $nodeModules = $this->getNodeModulesPath();

        if (! File::isDirectory($nodeModules)) {
            return;
        }

        if ($this->isRunningAsRoot() && $this->userExists('www-data')) {
            $escaped = escapeshellarg($nodeModules);
            $this->executeCommand("chgrp -R www-data {$escaped}");
            $this->executeCommand("chmod -R g+rwX {$escaped}");
            $this->executeCommand("find {$escaped} -type d -exec chmod g+s {} +");

            return;
        }

        $this->executeCommand('chmod -R u+rwX '.escapeshellarg($nodeModules));
    }

    protected function ensurePublicBuildPermissions(): void
    {
        $buildPath = $this->getPublicBuildPath();

        if (! File::exists($buildPath)) {
            File::ensureDirectoryExists($buildPath);
        }

        if ($this->isRunningAsRoot() && $this->userExists('www-data')) {
            $escaped = escapeshellarg($buildPath);
            $this->executeCommand("chgrp -R www-data {$escaped}");
            $this->executeCommand("chmod -R g+rwX {$escaped}");
            $this->executeCommand("find {$escaped} -type d -exec chmod g+s {} +");

            return;
        }

        $this->executeCommand('chmod -R u+rwX '.escapeshellarg($buildPath));
    }

    protected function ensurePublicAssetsPermissions(): void
    {
        $paths = [
            $this->basePath.'/public/js',
            $this->basePath.'/public/css',
        ];

        foreach ($paths as $path) {
            if (! File::isDirectory($path)) {
                continue;
            }

            $escaped = escapeshellarg($path);
            $this->executeCommand("chmod -R a+rwX {$escaped}");
        }
    }

    protected function isNodeModulesWritable(): bool
    {
        $nodeModules = $this->getNodeModulesPath();

        if (! File::isDirectory($nodeModules)) {
            return true;
        }

        $binPath = $nodeModules.'/.bin';
        if (! is_writable($nodeModules)) {
            return false;
        }

        if (File::isDirectory($binPath) && ! is_writable($binPath)) {
            return false;
        }

        return true;
    }

    protected function isPublicBuildWritable(): bool
    {
        $buildPath = $this->getPublicBuildPath();

        if (! File::isDirectory($buildPath)) {
            return true;
        }

        if (! is_writable($buildPath)) {
            return false;
        }

        $assetsPath = $buildPath.'/assets';
        if (File::isDirectory($assetsPath) && ! is_writable($assetsPath)) {
            return false;
        }

        return true;
    }

    protected function getNpmCacheDir(): string
    {
        return $this->basePath.'/storage/npm-cache';
    }

    protected function getNodeModulesPath(): string
    {
        return $this->basePath.'/node_modules';
    }

    protected function getPublicBuildPath(): string
    {
        return $this->basePath.'/public/build';
    }

    protected function getPuppeteerCacheDir(): string
    {
        return $this->basePath.'/storage/puppeteer-cache';
    }

    protected function getXdgCacheDir(): string
    {
        return $this->basePath.'/storage/.cache';
    }

    protected function commandExists(string $command): bool
    {
        $result = $this->executeCommand("command -v {$command}");

        return $result['exitCode'] === 0 && $result['output'] !== '';
    }

    protected function userExists(string $user): bool
    {
        if (function_exists('posix_getpwnam')) {
            return posix_getpwnam($user) !== false;
        }

        return $this->executeCommand("id -u {$user}")['exitCode'] === 0;
    }

    private function getChangedFiles(string $from, string $to): array
    {
        if ($from === $to) {
            return [];
        }

        $output = $this->executeCommandOrFail("git diff --name-only {$from}..{$to}");
        if ($output === '') {
            return [];
        }

        return array_values(array_filter(array_map('trim', explode("\n", $output))));
    }

    protected function shouldRunComposerInstall(array $changedFiles, bool $force, bool $hasVendor): bool
    {
        if ($force || ! $hasVendor) {
            return true;
        }

        return $this->hasChangedFile($changedFiles, ['composer.json', 'composer.lock']);
    }

    protected function shouldRunNpmBuild(array $changedFiles, bool $force, bool $hasManifest, bool $hasPackageJson): bool
    {
        if (! $hasPackageJson) {
            return false;
        }

        if ($force || ! $hasManifest) {
            return true;
        }

        if ($this->hasChangedFile($changedFiles, [
            'package.json',
            'package-lock.json',
            'vite.config.js',
            'postcss.config.js',
            'tailwind.config.js',
        ])) {
            return true;
        }

        return $this->hasChangedPathPrefix($changedFiles, 'resources/');
    }

    protected function shouldRunMigrations(array $changedFiles, bool $force): bool
    {
        if ($force) {
            return true;
        }

        return $this->hasChangedPathPrefix($changedFiles, 'database/migrations/');
    }

    protected function hasChangedFile(array $changedFiles, array $targets): bool
    {
        foreach ($targets as $target) {
            if (in_array($target, $changedFiles, true)) {
                return true;
            }
        }

        return false;
    }

    protected function hasChangedPathPrefix(array $changedFiles, string $prefix): bool
    {
        foreach ($changedFiles as $file) {
            if (str_starts_with($file, $prefix)) {
                return true;
            }
        }

        return false;
    }

    protected function restartServices(): void
    {
        try {
            Artisan::call('queue:restart');
            $this->line('  - queue restarted');
        } catch (Exception $e) {
            $this->warn('Queue restart warning: '.$e->getMessage());
        }

        if (! $this->isRunningAsRoot()) {
            $this->warn('Skipping system service reloads (requires root).');

            return;
        }

        $agentResult = $this->executeCommand('systemctl restart jabali-agent');
        if ($agentResult['exitCode'] === 0) {
            $this->line('  - jabali-agent restarted');
        } else {
            $this->warn('  - jabali-agent restart failed');
        }

        $fpmResult = $this->executeCommand('systemctl reload php*-fpm');
        if ($fpmResult['exitCode'] === 0) {
            $this->line('  - PHP-FPM reloaded (all versions)');
        } else {
            $this->warn('  - PHP-FPM reload failed');
        }
    }

    protected function isRunningAsRoot(): bool
    {
        if (function_exists('posix_geteuid')) {
            return posix_geteuid() === 0;
        }

        return getmyuid() === 0;
    }

    private function setupRedisAcl(): void
    {
        $credFile = '/root/.jabali_redis_credentials';
        $aclFile = '/etc/redis/users.acl';

        // Check if Redis ACL is already configured
        if (File::exists($credFile) && File::exists($aclFile)) {
            $this->line('  - Redis ACL already configured');

            return;
        }

        // Check if we have permission to write to /root/
        if (! is_writable('/root') && ! File::exists($credFile)) {
            $this->line('  - Skipping Redis ACL setup (requires root privileges)');

            return;
        }

        $this->line('  - Setting up Redis ACL...');

        // Generate admin password
        $password = bin2hex(random_bytes(16));

        // Save credentials
        File::put($credFile, "REDIS_ADMIN_PASSWORD={$password}\n");
        chmod($credFile, 0600);

        // Create ACL file
        $aclContent = "user default off\nuser jabali_admin on >{$password} ~* &* +@all\n";
        File::put($aclFile, $aclContent);
        chmod($aclFile, 0640);
        chown($aclFile, 'redis');
        chgrp($aclFile, 'redis');

        // Check if redis.conf has aclfile directive
        $redisConf = '/etc/redis/redis.conf';
        if (File::exists($redisConf)) {
            $conf = File::get($redisConf);
            if (strpos($conf, 'aclfile') === false) {
                // Add aclfile directive
                $conf .= "\n# ACL configuration\naclfile /etc/redis/users.acl\n";
                File::put($redisConf, $conf);
            }
        }

        // Update Laravel .env with Redis credentials
        $envFile = base_path('.env');
        if (File::exists($envFile)) {
            $env = File::get($envFile);

            // Update or add Redis credentials
            if (strpos($env, 'REDIS_USERNAME=') === false) {
                $env = preg_replace(
                    '/REDIS_HOST=.*/m',
                    "REDIS_HOST=127.0.0.1\nREDIS_USERNAME=jabali_admin\nREDIS_PASSWORD={$password}",
                    $env
                );
            } else {
                $env = preg_replace('/REDIS_PASSWORD=.*/m', "REDIS_PASSWORD={$password}", $env);
            }

            File::put($envFile, $env);
        }

        // Restart Redis
        exec('systemctl restart redis-server 2>&1', $output, $code);
        if ($code === 0) {
            $this->line('  - Redis ACL configured and restarted');

            // Migrate existing users
            $this->line('  - Migrating existing users to Redis ACL...');
            try {
                Artisan::call('jabali:migrate-redis-users');
                $this->line(Artisan::output());
            } catch (Exception $e) {
                $this->warn('  - Redis user migration warning: '.$e->getMessage());
            }
        } else {
            $this->warn('  - Redis restart failed, ACL may not be active');
        }
    }
}
