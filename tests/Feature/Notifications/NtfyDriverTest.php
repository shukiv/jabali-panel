<?php

declare(strict_types=1);

namespace Tests\Feature\Notifications;

use App\Models\NotificationChannel;
use App\Notifications\Channels\NtfyDriver;
use App\Notifications\NotificationPayload;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Http\Client\Request;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

class NtfyDriverTest extends TestCase
{
    use RefreshDatabase;

    public function test_validates_required_url(): void
    {
        $driver = new NtfyDriver;
        $errors = $driver->validateConfig([]);
        $this->assertNotEmpty($errors);
    }

    public function test_rejects_invalid_priority(): void
    {
        $driver = new NtfyDriver;
        $errors = $driver->validateConfig(['url' => 'https://ntfy.sh/x', 'priority' => 'bogus']);
        $this->assertNotEmpty($errors);
    }

    public function test_posts_to_ntfy_url_with_mapped_priority_and_tags(): void
    {
        Http::fake(['ntfy.sh/*' => Http::response('', 200)]);

        $channel = NotificationChannel::factory()->ntfy('https://ntfy.sh/jabali-alerts')->create();
        $payload = new NotificationPayload(
            type: 'backup_failure',
            severity: 'critical',
            subject: 'Backup failed',
            message: 'The nightly backup failed.',
            context: [],
            hostname: 'host-a',
        );

        $result = (new NtfyDriver)->send($channel, $payload);

        $this->assertTrue($result->success);

        // Inspect the recorded request — dump headers for diagnostic clarity.
        $recorded = Http::recorded();
        $this->assertCount(1, $recorded);
        /** @var Request $sent */
        [$sent] = $recorded[0];
        $this->assertStringContainsString('ntfy.sh/jabali-alerts', $sent->url());

        $headers = $sent->headers();
        // Keys may be lowercased by Guzzle — normalise.
        $lower = array_change_key_case($headers, CASE_LOWER);
        $this->assertSame('urgent', $lower['priority'][0] ?? null);
        $this->assertStringContainsString('critical', $lower['tags'][0] ?? '');
    }

    public function test_failed_http_returns_failed_result(): void
    {
        Http::fake(['ntfy.sh/*' => Http::response('boom', 500)]);

        $channel = NotificationChannel::factory()->ntfy('https://ntfy.sh/jabali-alerts')->create();
        $payload = new NotificationPayload('t', 'info', 'subj', 'msg');

        $result = (new NtfyDriver)->send($channel, $payload);

        $this->assertFalse($result->success);
        $this->assertSame('failed', $result->status);
    }
}
