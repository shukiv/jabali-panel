<?php

declare(strict_types=1);

namespace Tests\Unit;

use Tests\TestCase;

class ServerStatusBulkActionsTest extends TestCase
{
    public function test_server_status_uses_default_bulk_actions(): void
    {
        $page = file_get_contents(base_path('app/Filament/Admin/Pages/ServerStatus.php'));

        $this->assertIsString($page);
        $this->assertStringContainsString('->bulkActions([', $page);
        $this->assertStringContainsString('BulkAction::make(\'killSelected\')', $page);
    }
}
