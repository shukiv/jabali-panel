<?php

declare(strict_types=1);

namespace Tests\Unit;

use App\Filament\Admin\Pages\ServerSettings;
use App\Models\DnsSetting;
use App\Models\HostingPackage;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Storage;
use Tests\TestCase;

class ServerSettingsExportImportTest extends TestCase
{
    use RefreshDatabase;

    public function test_export_includes_settings_packages_tokens_and_tuning(): void
    {
        DnsSetting::set('panel_name', 'Jabali Test');

        HostingPackage::create([
            'name' => 'Starter',
            'description' => 'Basic plan',
            'disk_quota_mb' => 1024,
            'bandwidth_gb' => 50,
            'domains_limit' => 1,
            'databases_limit' => 1,
            'mailboxes_limit' => 5,
            'is_active' => true,
        ]);

        $admin = User::factory()->admin()->create([
            'email' => 'admin@example.com',
            'username' => 'admin',
        ]);
        $admin->createToken('Automation', ['automation']);

        $page = new ServerSettings;
        $payload = $page->buildExportPayload();

        $this->assertSame('Jabali Test', $payload['settings']['panel_name'] ?? null);
        $this->assertSame('Starter', $payload['hosting_packages'][0]['name'] ?? null);
        $this->assertSame('Automation', $payload['api_tokens'][0]['name'] ?? null);
        $this->assertSame('admin@example.com', $payload['api_tokens'][0]['owner_email'] ?? null);
        $this->assertIsArray($payload['database_tuning']);
    }

    public function test_import_creates_packages_and_tokens(): void
    {
        $admin = User::factory()->admin()->create([
            'email' => 'admin@example.com',
            'username' => 'admin',
        ]);
        $tokenHash = hash('sha256', 'imported-token-value');

        $payload = [
            'version' => '1.1',
            'settings' => [
                'panel_name' => 'Imported Panel',
            ],
            'hosting_packages' => [
                [
                    'name' => 'Imported Starter',
                    'description' => 'Imported',
                    'disk_quota_mb' => 2048,
                    'bandwidth_gb' => 100,
                    'domains_limit' => 2,
                    'databases_limit' => 2,
                    'mailboxes_limit' => 10,
                    'is_active' => true,
                ],
            ],
            'api_tokens' => [
                [
                    'name' => 'Imported Token',
                    'token' => $tokenHash,
                    'abilities' => ['automation'],
                    'owner_email' => 'admin@example.com',
                ],
            ],
            'database_tuning' => [
                'max_connections' => '200',
            ],
        ];

        Storage::disk('local')->put('tests/jabali-import.json', json_encode($payload));

        $page = new ServerSettings;
        $page->importConfig(['config_file' => 'tests/jabali-import.json']);

        $this->assertDatabaseHas('hosting_packages', [
            'name' => 'Imported Starter',
            'disk_quota_mb' => 2048,
        ]);

        $this->assertDatabaseHas('personal_access_tokens', [
            'name' => 'Imported Token',
            'tokenable_id' => $admin->id,
        ]);

        $this->assertSame('Imported Panel', DnsSetting::get('panel_name'));
    }
}
