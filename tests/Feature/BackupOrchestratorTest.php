<?php

declare(strict_types=1);

namespace Tests\Feature;

use App\Models\Backup;
use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Services\Backup\BackupOrchestrator;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Mockery;
use Tests\TestCase;

class BackupOrchestratorTest extends TestCase
{
    use RefreshDatabase;

    public function test_execute_creates_per_account_backups(): void
    {
        $user = User::factory()->create(['username' => 'testuser', 'is_active' => true]);

        $parentBackup = Backup::create([
            'name' => 'Daily Server Backup',
            'filename' => 'server-backup.tar.gz',
            'type' => 'server',
            'status' => 'pending',
        ]);

        $agent = Mockery::mock(AgentClient::class);
        $agent->shouldReceive('send')
            ->with('backup.create_user_snapshot', Mockery::type('array'))
            ->once()
            ->andReturn([
                'success' => true,
                'snapshot_id' => 'abc12345',
                'size_bytes' => 1024000,
                'file_count' => 42,
                'domains' => ['example.com'],
                'databases' => ['testuser_wp'],
                'mailboxes' => ['info@example.com'],
            ]);

        $orchestrator = new BackupOrchestrator($agent);
        $result = $orchestrator->executePerAccount($parentBackup);

        $this->assertEquals(1, $result['completed']);
        $this->assertEquals(0, $result['failed']);

        // Parent should be deleted
        $this->assertNull(Backup::find($parentBackup->id));

        // Per-user backup should exist
        $userBackup = Backup::where('user_id', $user->id)->first();
        $this->assertNotNull($userBackup);
        $this->assertEquals('completed', $userBackup->status);
        $this->assertEquals('abc12345', $userBackup->snapshot_id);
        $this->assertEquals(1024000, $userBackup->size_bytes);
        $this->assertEquals('account', $userBackup->type);
    }

    public function test_execute_handles_per_account_failure(): void
    {
        $user = User::factory()->create(['username' => 'failuser', 'is_active' => true]);

        $parentBackup = Backup::create([
            'name' => 'Failing Backup',
            'filename' => 'fail-backup.tar.gz',
            'type' => 'server',
            'status' => 'pending',
        ]);

        $agent = Mockery::mock(AgentClient::class);
        $agent->shouldReceive('send')
            ->with('backup.create_user_snapshot', Mockery::type('array'))
            ->once()
            ->andThrow(new \Exception('Disk full'));

        $orchestrator = new BackupOrchestrator($agent);
        $result = $orchestrator->executePerAccount($parentBackup);

        $this->assertEquals(0, $result['completed']);
        $this->assertEquals(1, $result['failed']);

        $userBackup = Backup::where('user_id', $user->id)->first();
        $this->assertNotNull($userBackup);
        $this->assertEquals('failed', $userBackup->status);
        $this->assertEquals('Disk full', $userBackup->error_message);
    }
}
