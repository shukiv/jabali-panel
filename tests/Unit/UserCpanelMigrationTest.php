<?php

declare(strict_types=1);

namespace Tests\Unit;

use Tests\TestCase;

class UserCpanelMigrationTest extends TestCase
{
    public function test_user_cpanel_migration_uses_unique_query_string_key(): void
    {
        $page = file_get_contents(base_path('app/Filament/Jabali/Pages/CpanelMigration.php'));

        $this->assertIsString($page);
        $this->assertStringContainsString("#[Url(as: 'cpanel-step')]", $page);
        $this->assertStringContainsString("->persistStepInQueryString('cpanel-step')", $page);
    }

    public function test_user_cpanel_migration_dispatches_restore_job(): void
    {
        $page = file_get_contents(base_path('app/Filament/Jabali/Pages/CpanelMigration.php'));

        $this->assertIsString($page);
        $this->assertStringContainsString('RunCpanelRestore::dispatch', $page);
    }

    public function test_user_cpanel_migration_polls_restore_logs(): void
    {
        $page = file_get_contents(base_path('app/Filament/Jabali/Pages/CpanelMigration.php'));

        $this->assertIsString($page);
        $this->assertStringContainsString("->extraAttributes(\$this->isProcessing ? ['wire:poll.1s' => 'pollMigrationLog'] : [])", $page);
        $this->assertStringContainsString('pollMigrationLog', $page);
    }

    public function test_user_cpanel_migration_supports_local_backups(): void
    {
        $page = file_get_contents(base_path('app/Filament/Jabali/Pages/CpanelMigration.php'));

        $this->assertIsString($page);
        $this->assertStringContainsString("Radio::make('sourceType')", $page);
        $this->assertStringContainsString("Select::make('localBackupPath')", $page);
        $this->assertStringContainsString("'file.list'", $page);
    }
}
