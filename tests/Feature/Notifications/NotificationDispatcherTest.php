<?php

declare(strict_types=1);

namespace Tests\Feature\Notifications;

use App\Models\NotificationChannel;
use App\Models\NotificationLog;
use App\Models\NotificationTypeChannel;
use App\Services\Notifications\NotificationDispatcher;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

class NotificationDispatcherTest extends TestCase
{
    use RefreshDatabase;

    public function test_dispatch_returns_false_when_no_channels_configured(): void
    {
        /** @var NotificationDispatcher $dispatcher */
        $dispatcher = app(NotificationDispatcher::class);

        $this->assertFalse($dispatcher->dispatch('test', 'hi', 'body', []));
        $this->assertDatabaseHas('notification_logs', ['status' => 'skipped']);
    }

    public function test_dispatch_routes_by_severity_and_logs_one_row_per_channel(): void
    {
        Http::fake([
            'ntfy.sh/*' => Http::response('', 200),
            'api.telegram.org/*' => Http::response(['ok' => true, 'result' => []], 200),
        ]);

        $ntfy = NotificationChannel::factory()->ntfy('https://ntfy.sh/jabali-alerts')->create();
        $tg = NotificationChannel::factory()
            ->telegram('1234567890:AAHnoiseHnoiseHnoiseHnoiseHnoiseHnoise', '42')
            ->create();

        NotificationTypeChannel::factory()->critical()->create(['channel_id' => $ntfy->id]);
        NotificationTypeChannel::factory()->critical()->create(['channel_id' => $tg->id]);

        /** @var NotificationDispatcher $dispatcher */
        $dispatcher = app(NotificationDispatcher::class);
        $ok = $dispatcher->dispatch('backup_failure', 'Backup broke', 'See logs', []);

        $this->assertTrue($ok);
        $this->assertSame(2, NotificationLog::query()->where('status', 'sent')->count());
    }

    public function test_dispatch_returns_true_if_any_channel_succeeds(): void
    {
        Http::fake([
            'ntfy.sh/*' => Http::response('', 200),
            'api.telegram.org/*' => Http::response('rate limited', 429),
        ]);

        $ntfy = NotificationChannel::factory()->ntfy('https://ntfy.sh/jabali-alerts')->create();
        $tg = NotificationChannel::factory()
            ->telegram('1234567890:AAHnoiseHnoiseHnoiseHnoiseHnoiseHnoise', '42')
            ->create();

        NotificationTypeChannel::factory()->critical()->create(['channel_id' => $ntfy->id]);
        NotificationTypeChannel::factory()->critical()->create(['channel_id' => $tg->id]);

        /** @var NotificationDispatcher $dispatcher */
        $dispatcher = app(NotificationDispatcher::class);

        $this->assertTrue($dispatcher->dispatch('backup_failure', 'subj', 'msg', []));

        $this->assertSame(1, NotificationLog::query()->where('status', 'sent')->count());
        $this->assertSame(1, NotificationLog::query()->where('status', 'failed')->count());
    }

    public function test_disabled_channels_are_skipped(): void
    {
        $ch = NotificationChannel::factory()->email()->disabled()->create();
        NotificationTypeChannel::factory()->info()->create(['channel_id' => $ch->id]);

        /** @var NotificationDispatcher $dispatcher */
        $dispatcher = app(NotificationDispatcher::class);
        $this->assertFalse($dispatcher->dispatch('test', 'subj', 'msg'));
    }
}
