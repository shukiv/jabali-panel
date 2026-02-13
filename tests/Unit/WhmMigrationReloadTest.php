<?php

declare(strict_types=1);

namespace Tests\Unit;

use Tests\TestCase;

class WhmMigrationReloadTest extends TestCase
{
    public function test_whm_migration_page_does_not_reload_services(): void
    {
        $page = file_get_contents(base_path('app/Filament/Admin/Pages/WhmMigration.php'));

        $this->assertIsString($page);
        $this->assertStringNotContainsString('php.reload_all_fpm', $page);
        $this->assertStringNotContainsString('service.reload', $page);
    }

    public function test_whm_migration_batch_reload_is_conditional(): void
    {
        $job = file_get_contents(base_path('app/Jobs/RunWhmMigrationBatch.php'));

        $this->assertIsString($job);
        $this->assertStringContainsString('shouldReloadFpm', $job);
        $this->assertStringContainsString('if ($this->shouldReloadFpm)', $job);
        $this->assertStringContainsString('php.reload_all_fpm', $job);
    }
}
