<?php

declare(strict_types=1);

namespace Tests\Feature;

use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\File;
use Tests\TestCase;

class BackupDownloadSecurityTest extends TestCase
{
    use RefreshDatabase;

    protected string $homeRoot;

    protected function setUp(): void
    {
        parent::setUp();

        $this->homeRoot = storage_path('testing/home');
    }

    protected function tearDown(): void
    {
        File::deleteDirectory($this->homeRoot);

        parent::tearDown();
    }

    public function test_user_can_download_backup_from_home_backup_directory(): void
    {
        $user = User::factory()->create([
            'username' => 'backupuser',
            'home_directory' => $this->homeRoot.'/backupuser',
        ]);

        $backupDir = $user->home_directory.'/backups';
        File::makeDirectory($backupDir, 0755, true);

        $backupPath = $backupDir.'/backup.tar.gz';
        File::put($backupPath, 'test-backup');

        $response = $this->actingAs($user)
            ->get('/jabali-panel/backup-download?path='.base64_encode($backupPath));

        $response->assertOk();
    }

    public function test_user_cannot_download_outside_backup_directory(): void
    {
        $user = User::factory()->create([
            'username' => 'backupuser',
            'home_directory' => $this->homeRoot.'/backupuser',
        ]);

        $backupDir = $user->home_directory.'/backups';
        File::makeDirectory($backupDir, 0755, true);

        $secretPath = $user->home_directory.'/secret.txt';
        File::put($secretPath, 'secret');

        $traversalPath = $backupDir.'/../secret.txt';

        $response = $this->actingAs($user)
            ->get('/jabali-panel/backup-download?path='.base64_encode($traversalPath));

        $response->assertForbidden();
    }
}
