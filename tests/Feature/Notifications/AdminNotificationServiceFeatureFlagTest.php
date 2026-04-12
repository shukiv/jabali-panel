<?php

declare(strict_types=1);

namespace Tests\Feature\Notifications;

use App\Models\DnsSetting;
use App\Models\NotificationChannel;
use App\Models\NotificationTypeChannel;
use App\Services\AdminNotificationService;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

class AdminNotificationServiceFeatureFlagTest extends TestCase
{
    use RefreshDatabase;

    public function test_flag_off_keeps_legacy_path(): void
    {
        config()->set('notifications.v2_enabled', false);
        config()->set('mail.default', 'array');
        DnsSetting::set('notify_test', true);
        DnsSetting::set('admin_email_recipients', 'ops@example.com');

        $this->assertTrue(AdminNotificationService::send('test', 'subj', 'msg'));
        // Array mailer is captured on the transport, not the fake facade.
        $sent = app('mail.manager')->mailer('array')->getSymfonyTransport()->messages();
        $this->assertCount(1, $sent);
    }

    public function test_flag_on_uses_dispatcher(): void
    {
        config()->set('notifications.v2_enabled', true);
        Http::fake(['ntfy.sh/*' => Http::response('', 200)]);
        DnsSetting::set('notify_test', true);

        $ch = NotificationChannel::factory()->ntfy()->create();
        NotificationTypeChannel::factory()->info()->create(['channel_id' => $ch->id]);

        $this->assertTrue(AdminNotificationService::send('test', 'subj', 'msg'));
        Http::assertSent(fn ($req) => str_contains($req->url(), 'ntfy.sh'));
    }

    public function test_flag_on_still_respects_per_type_disable(): void
    {
        config()->set('notifications.v2_enabled', true);
        DnsSetting::set('notify_test', false);

        $this->assertFalse(AdminNotificationService::send('test', 'subj', 'msg'));
    }
}
