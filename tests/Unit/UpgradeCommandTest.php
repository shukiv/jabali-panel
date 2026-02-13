<?php

declare(strict_types=1);

namespace Tests\Unit;

use App\Console\Commands\Jabali\UpgradeCommand;
use Tests\TestCase;

class UpgradeCommandTest extends TestCase
{
    public function test_composer_runs_when_lock_changed(): void
    {
        $command = new TestableUpgradeCommand;

        $this->assertTrue(
            $command->exposesShouldRunComposer(['composer.lock'], false, true)
        );
    }

    public function test_composer_runs_when_vendor_missing(): void
    {
        $command = new TestableUpgradeCommand;

        $this->assertTrue(
            $command->exposesShouldRunComposer([], false, false)
        );
    }

    public function test_composer_skips_without_changes(): void
    {
        $command = new TestableUpgradeCommand;

        $this->assertFalse(
            $command->exposesShouldRunComposer(['routes/web.php'], false, true)
        );
    }

    public function test_npm_runs_when_resources_change(): void
    {
        $command = new TestableUpgradeCommand;

        $this->assertTrue(
            $command->exposesShouldRunNpm(['resources/css/app.css'], false, true, true)
        );
    }

    public function test_npm_runs_when_manifest_missing(): void
    {
        $command = new TestableUpgradeCommand;

        $this->assertTrue(
            $command->exposesShouldRunNpm([], false, false, true)
        );
    }

    public function test_npm_skips_when_no_package_json(): void
    {
        $command = new TestableUpgradeCommand;

        $this->assertFalse(
            $command->exposesShouldRunNpm([], false, true, false)
        );
    }

    public function test_migrations_run_when_migration_changes(): void
    {
        $command = new TestableUpgradeCommand;

        $this->assertTrue(
            $command->exposesShouldRunMigrations(['database/migrations/2026_01_24_000001.php'], false)
        );
    }

    public function test_migrations_skip_without_changes(): void
    {
        $command = new TestableUpgradeCommand;

        $this->assertFalse(
            $command->exposesShouldRunMigrations(['app/Models/User.php'], false)
        );
    }

    public function test_safe_directory_commands_for_non_root(): void
    {
        $command = new SafeDirectoryUpgradeCommand(false, false, false);

        $this->assertSame([
            'git config --global --add safe.directory '.base_path(),
        ], $command->exposesSafeDirectoryCommands());
    }

    public function test_safe_directory_commands_for_root_without_www_data(): void
    {
        $command = new SafeDirectoryUpgradeCommand(true, false, false);

        $this->assertSame([
            'git config --global --add safe.directory '.base_path(),
            'git config --system --add safe.directory '.base_path(),
        ], $command->exposesSafeDirectoryCommands());
    }

    public function test_safe_directory_commands_for_root_with_www_data(): void
    {
        $command = new SafeDirectoryUpgradeCommand(true, true, true);

        $this->assertSame([
            'git config --global --add safe.directory '.base_path(),
            'git config --system --add safe.directory '.base_path(),
            'sudo -u www-data git config --global --add safe.directory '.base_path(),
        ], $command->exposesSafeDirectoryCommands());
    }

    public function test_writable_storage_commands_for_non_root(): void
    {
        $command = new SafeDirectoryUpgradeCommand(false, true, true);

        $this->assertSame([], $command->exposesWritableStorageCommands());
    }

    public function test_writable_storage_commands_without_www_data(): void
    {
        $command = new SafeDirectoryUpgradeCommand(true, true, false);

        $this->assertSame([], $command->exposesWritableStorageCommands());
    }

    public function test_writable_storage_commands_for_root_with_www_data(): void
    {
        $command = new SafeDirectoryUpgradeCommand(true, true, true);

        $paths = [
            base_path().'/database',
            base_path().'/storage',
            base_path().'/node_modules',
            base_path().'/public/build',
            base_path().'/storage/npm-cache',
            base_path().'/storage/puppeteer-cache',
            base_path().'/storage/.cache',
            base_path().'/bootstrap/cache',
        ];

        $escaped = array_map('escapeshellarg', $paths);
        $list = implode(' ', $escaped);

        $this->assertSame([
            "chgrp -R www-data {$list}",
            "chmod -R g+rwX {$list}",
            "find {$list} -type d -exec chmod g+s {} +",
        ], $command->exposesWritableStorageCommands());
    }
}

class TestableUpgradeCommand extends UpgradeCommand
{
    public function exposesShouldRunComposer(array $changedFiles, bool $force, bool $hasVendor): bool
    {
        return $this->shouldRunComposerInstall($changedFiles, $force, $hasVendor);
    }

    public function exposesShouldRunNpm(array $changedFiles, bool $force, bool $hasManifest, bool $hasPackageJson): bool
    {
        return $this->shouldRunNpmBuild($changedFiles, $force, $hasManifest, $hasPackageJson);
    }

    public function exposesShouldRunMigrations(array $changedFiles, bool $force): bool
    {
        return $this->shouldRunMigrations($changedFiles, $force);
    }
}

class SafeDirectoryUpgradeCommand extends UpgradeCommand
{
    public function __construct(
        private bool $runningAsRoot,
        private bool $sudoExists,
        private bool $wwwDataExists
    ) {
        parent::__construct();
    }

    public function exposesSafeDirectoryCommands(): array
    {
        return $this->getSafeDirectoryCommands();
    }

    public function exposesWritableStorageCommands(): array
    {
        return $this->getWritableStorageCommands();
    }

    protected function isRunningAsRoot(): bool
    {
        return $this->runningAsRoot;
    }

    protected function commandExists(string $command): bool
    {
        return $command === 'sudo' && $this->sudoExists;
    }

    protected function userExists(string $user): bool
    {
        return $user === 'www-data' && $this->wwwDataExists;
    }
}
