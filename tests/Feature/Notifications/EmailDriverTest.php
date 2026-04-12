<?php

declare(strict_types=1);

namespace Tests\Feature\Notifications;

use App\Models\NotificationChannel;
use App\Notifications\Channels\EmailDriver;
use App\Notifications\NotificationPayload;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

class EmailDriverTest extends TestCase
{
    use RefreshDatabase;

    public function test_rejects_empty_recipients(): void
    {
        $errors = (new EmailDriver)->validateConfig([]);
        $this->assertNotEmpty($errors);
    }

    public function test_rejects_malformed_email(): void
    {
        $errors = (new EmailDriver)->validateConfig(['recipients' => ['not-an-email']]);
        $this->assertNotEmpty($errors);
    }

    public function test_sends_email_to_configured_recipients(): void
    {
        // Use the array mailer — captures outgoing messages without SMTP.
        config()->set('mail.default', 'array');

        $channel = NotificationChannel::factory()->email(['ops@example.com', 'sre@example.com'])->create();
        $payload = new NotificationPayload('t', 'info', 'subj', 'msg', [], 'host');

        $result = (new EmailDriver)->send($channel, $payload);

        $this->assertTrue($result->success);
        $this->assertCount(2, $result->recipients);
        $this->assertContains('ops@example.com', $result->recipients);
    }
}
