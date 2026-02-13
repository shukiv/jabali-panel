<?php

declare(strict_types=1);

namespace Tests\Unit;

use App\Services\Migration\WhmMigrationStatusStore;
use Tests\TestCase;

class WhmMigrationStatusStoreTest extends TestCase
{
    public function test_it_initializes_and_updates_accounts(): void
    {
        $store = new WhmMigrationStatusStore('whm_migration_status_test');

        $state = $store->initialize(['alpha', 'bravo']);

        $this->assertTrue($state['isMigrating']);
        $this->assertSame(['alpha', 'bravo'], $state['selectedAccounts']);
        $this->assertSame('pending', $state['migrationStatus']['alpha']['status']);

        $store->updateAccountStatus('alpha', 'processing', 'Starting');
        $updated = $store->get();

        $this->assertSame('processing', $updated['migrationStatus']['alpha']['status']);
        $this->assertCount(1, $updated['migrationStatus']['alpha']['log']);

        $store->setMigrating(false);
        $finished = $store->get();

        $this->assertFalse($finished['isMigrating']);
        $this->assertNotEmpty($finished['completedAt']);
    }
}
