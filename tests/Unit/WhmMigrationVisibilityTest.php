<?php

declare(strict_types=1);

namespace Tests\Unit;

use Tests\TestCase;

class WhmMigrationVisibilityTest extends TestCase
{
    public function test_account_status_section_requires_migration_status(): void
    {
        $page = file_get_contents(base_path('app/Filament/Admin/Pages/WhmMigration.php'));

        $this->assertIsString($page);
        $this->assertStringContainsString("Section::make(__('Account Status'))", $page);
        $this->assertStringContainsString('->visible(fn () => ! empty($this->migrationStatus))', $page);
    }

    public function test_whm_migration_auto_resets_after_completion(): void
    {
        $page = file_get_contents(base_path('app/Filament/Admin/Pages/WhmMigration.php'));

        $this->assertIsString($page);
        $this->assertStringContainsString("->title(__('Migration complete'))", $page);
        $this->assertStringContainsString('$this->resetMigration();', $page);
        $this->assertStringContainsString('$this->wizardStep = null;', $page);
    }
}
