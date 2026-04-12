<?php

declare(strict_types=1);

namespace Tests\Feature\Notifications;

use App\Filament\Admin\Pages\ServerSettings;
use App\Models\DnsSetting;
use App\Models\NotificationChannel;
use App\Models\NotificationTypeChannel;
use App\Models\User;
use App\Services\AdminNotificationService;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Http;
use Livewire\Livewire;
use Tests\TestCase;

/**
 * Integration tests for GitHub issue #102 follow-up — the unified
 * notifications UI now lives inside the Server Settings "Notifications"
 * tab, and the 298 legacy AdminNotificationService callers must route
 * through the new channel dispatcher without any feature flag.
 */
class NotificationsTabIntegrationTest extends TestCase
{
    use RefreshDatabase;

    private User $admin;

    protected function setUp(): void
    {
        parent::setUp();
        $this->admin = User::factory()->admin()->create();
    }

    // ========================================================================
    // UI consolidation — channels + severity matrix live inside the tab
    // ========================================================================

    public function test_notifications_tab_exposes_channel_list_section(): void
    {
        $channel = NotificationChannel::factory()
            ->email(['sre@example.com'])
            ->create(['name' => 'primary-ops-inbox']);

        Livewire::actingAs($this->admin, 'admin')
            ->test(ServerSettings::class, ['tab' => 'notifications'])
            ->assertSee('primary-ops-inbox')
            ->assertSee('Channels');
    }

    public function test_notifications_tab_exposes_severity_matrix_with_all_three_severities(): void
    {
        NotificationChannel::factory()->email()->create(['name' => 'ops-inbox']);

        Livewire::actingAs($this->admin, 'admin')
            ->test(ServerSettings::class, ['tab' => 'notifications'])
            ->assertSee('Info')
            ->assertSee('Warning')
            ->assertSee('Critical');
    }

    public function test_notifications_tab_save_routing_matrix_persists_rows(): void
    {
        $ch = NotificationChannel::factory()->email()->create();

        Livewire::actingAs($this->admin, 'admin')
            ->test(ServerSettings::class, ['tab' => 'notifications'])
            ->set('severityRouting.info', [$ch->id])
            ->set('severityRouting.warning', [$ch->id])
            ->set('severityRouting.critical', [$ch->id])
            ->call('saveSeverityRouting');

        $this->assertDatabaseHas('notification_type_channels', [
            'severity' => 'info',
            'channel_id' => $ch->id,
        ]);
        $this->assertDatabaseHas('notification_type_channels', [
            'severity' => 'warning',
            'channel_id' => $ch->id,
        ]);
        $this->assertDatabaseHas('notification_type_channels', [
            'severity' => 'critical',
            'channel_id' => $ch->id,
        ]);
    }

    public function test_standalone_notification_settings_page_is_gone(): void
    {
        // We've folded the standalone page into the tab. The old URL must
        // no longer resolve to a Filament page — either 404 or redirect.
        $response = $this->actingAs($this->admin, 'admin')
            ->get('/jabali-admin/notification-settings');

        $this->assertContains($response->status(), [301, 302, 404]);
    }

    // ========================================================================
    // Legacy-to-channel bridge — the 298 AdminNotificationService callers
    // now always flow through the v2 dispatcher. No NOTIFICATIONS_V2_ENABLED
    // env toggle required.
    // ========================================================================

    public function test_admin_notification_service_uses_dispatcher_by_default(): void
    {
        // No v2_enabled config set, no flag flipped — pure default behavior.
        // With at least one channel configured for the resolved severity, the
        // dispatcher path is taken.
        Http::fake(['ntfy.sh/*' => Http::response('', 200)]);
        DnsSetting::set('notify_test', true);

        $ch = NotificationChannel::factory()->ntfy()->create();
        NotificationTypeChannel::factory()->info()->create(['channel_id' => $ch->id]);

        $this->assertTrue(AdminNotificationService::send('test', 'subj', 'msg'));

        Http::assertSent(fn ($req) => str_contains($req->url(), 'ntfy.sh'));
    }

    public function test_saving_admin_email_recipients_syncs_default_email_channel(): void
    {
        Livewire::actingAs($this->admin, 'admin')
            ->test(ServerSettings::class, ['tab' => 'notifications'])
            ->set('notificationsData.admin_email_recipients', 'alerts@example.com, ops@example.com')
            ->call('saveEmailNotificationSettings');

        /** @var NotificationChannel|null $default */
        $default = NotificationChannel::query()
            ->where('name', 'default_email')
            ->where('type', 'email')
            ->first();

        $this->assertNotNull($default, 'default_email channel must be auto-created');
        $this->assertTrue($default->enabled);
        $this->assertEqualsCanonicalizing(
            ['alerts@example.com', 'ops@example.com'],
            $default->config['recipients'] ?? [],
        );

        // And the severity matrix should include this channel for every
        // severity so legacy per-type notifications fan out to it.
        foreach (['info', 'warning', 'critical'] as $sev) {
            $this->assertDatabaseHas('notification_type_channels', [
                'severity' => $sev,
                'channel_id' => $default->id,
            ]);
        }
    }

    public function test_legacy_notification_sends_when_only_recipients_configured(): void
    {
        // 298-caller invariant: a pristine install with only
        // admin_email_recipients set (no channels seeded) must still
        // deliver notifications — proves auto-sync is transparent to
        // legacy callers.
        config()->set('mail.default', 'array');
        DnsSetting::set('notify_test', true);
        DnsSetting::set('admin_email_recipients', 'ops@example.com');

        $this->assertTrue(AdminNotificationService::send('test', 'subj', 'msg'));

        $sent = app('mail.manager')->mailer('array')->getSymfonyTransport()->messages();
        $this->assertCount(1, $sent);
    }
}
