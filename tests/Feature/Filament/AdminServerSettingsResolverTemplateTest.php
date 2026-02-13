<?php

declare(strict_types=1);

namespace Tests\Feature\Filament;

use App\Filament\Admin\Pages\ServerSettings;
use App\Models\User;
use App\Services\Agent\AgentClient;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use Tests\TestCase;

class TestableServerSettings extends ServerSettings
{
    protected function getAgent(): AgentClient
    {
        return new class extends AgentClient
        {
            public function send(string $action, array $params = []): array
            {
                if ($action === 'server.info') {
                    return [
                        'success' => true,
                        'info' => ['hostname' => 'jabali.test'],
                    ];
                }

                return ['success' => true];
            }
        };
    }

    public function mount(): void
    {
        $this->activeTab = 'dns';
        $this->currentLogo = null;
        $this->isSystemdResolved = false;

        $this->brandingData = ['panel_name' => 'Jabali'];
        $this->hostnameData = ['hostname' => 'jabali.test'];
        $this->dnsData = [
            'ns1' => 'ns1.jabali.test',
            'ns1_ip' => '192.168.1.2',
            'ns2' => 'ns2.jabali.test',
            'ns2_ip' => '192.168.1.3',
            'default_ip' => '192.168.1.2',
            'default_ipv6' => '',
            'default_ttl' => '3600',
            'admin_email' => 'admin.jabali.test',
        ];
        $this->resolversData = [
            'resolver1' => '',
            'resolver2' => '',
            'resolver3' => '',
            'search_domain' => '',
        ];
        $this->quotaData = [
            'quotas_enabled' => false,
            'default_quota_mb' => 5120,
        ];
        $this->fileManagerData = [
            'max_upload_size_mb' => 100,
        ];
        $this->emailData = [
            'mail_hostname' => 'mail.jabali.test',
            'mail_default_quota_mb' => 1024,
            'max_mailboxes_per_domain' => 10,
            'webmail_url' => '/webmail',
            'webmail_product_name' => 'Jabali Webmail',
        ];
        $this->notificationsData = [
            'admin_email_recipients' => '',
            'notify_ssl_errors' => true,
            'notify_backup_failures' => true,
            'notify_backup_success' => false,
            'notify_disk_quota' => true,
            'notify_login_failures' => true,
            'notify_ssh_logins' => false,
            'notify_system_updates' => false,
            'notify_service_health' => true,
            'notify_high_load' => true,
            'load_threshold' => 5.0,
            'load_alert_minutes' => 5,
        ];
        $this->phpFpmData = [
            'pm_max_children' => 5,
            'pm_max_requests' => 200,
            'rlimit_files' => 1024,
            'process_priority' => 0,
            'request_terminate_timeout' => 300,
            'memory_limit' => '512M',
        ];
    }
}

class AdminServerSettingsResolverTemplateTest extends TestCase
{
    use RefreshDatabase;

    public function test_resolver_template_updates_form_state(): void
    {
        $admin = User::factory()->admin()->create();

        $this->actingAs($admin);

        Livewire::test(TestableServerSettings::class)
            ->call('applyCloudflareResolvers')
            ->assertSet('resolversData.resolver1', '1.1.1.1')
            ->assertSet('resolversData.resolver2', '1.0.0.1');
    }
}
