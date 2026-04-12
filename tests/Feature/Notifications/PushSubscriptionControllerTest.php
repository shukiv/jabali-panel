<?php

declare(strict_types=1);

namespace Tests\Feature\Notifications;

use App\Models\PushSubscription;
use App\Models\User;
use Illuminate\Foundation\Http\Middleware\ValidateCsrfToken;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

class PushSubscriptionControllerTest extends TestCase
{
    use RefreshDatabase;

    protected function setUp(): void
    {
        parent::setUp();
        // Browser clients send an X-XSRF-TOKEN header; in tests we bypass it.
        $this->withoutMiddleware(ValidateCsrfToken::class);
    }

    private function admin(): User
    {
        $u = User::factory()->create();
        $u->is_admin = true;
        $u->save();

        return $u;
    }

    public function test_unauthenticated_store_is_rejected(): void
    {
        // JSON requests get 401 from the auth middleware; non-JSON get a 302
        // redirect to login. Accept either — the contract is "no auth -> no subscribe".
        $response = $this->postJson('/jabali-admin/push-subscriptions', [
            'endpoint' => 'https://fcm.googleapis.com/x',
            'keys' => ['p256dh' => 'p', 'auth' => 'a'],
        ]);
        $this->assertContains($response->status(), [302, 401, 403]);
    }

    public function test_authenticated_admin_can_register_subscription(): void
    {
        $admin = $this->admin();

        $this->actingAs($admin, 'admin')
            ->postJson('/jabali-admin/push-subscriptions', [
                'endpoint' => 'https://fcm.googleapis.com/wp/abc',
                'keys' => ['p256dh' => str_repeat('p', 80), 'auth' => str_repeat('a', 20)],
            ])
            ->assertCreated()
            ->assertJson(['ok' => true]);

        $this->assertDatabaseHas('push_subscriptions', [
            'admin_user_id' => $admin->id,
            'endpoint' => 'https://fcm.googleapis.com/wp/abc',
        ]);
    }

    public function test_duplicate_endpoint_for_same_admin_upserts(): void
    {
        $admin = $this->admin();
        $payload = [
            'endpoint' => 'https://fcm.googleapis.com/wp/abc',
            'keys' => ['p256dh' => 'p1', 'auth' => 'a1'],
        ];
        $this->actingAs($admin, 'admin')->postJson('/jabali-admin/push-subscriptions', $payload)->assertCreated();
        $this->actingAs($admin, 'admin')
            ->postJson('/jabali-admin/push-subscriptions', array_merge($payload, [
                'keys' => ['p256dh' => 'p2', 'auth' => 'a2'],
            ]))
            ->assertCreated();

        $this->assertSame(1, PushSubscription::query()->count());
        $this->assertSame('p2', PushSubscription::query()->first()?->p256dh);
    }

    public function test_admin_can_unregister_subscription(): void
    {
        $admin = $this->admin();
        $sub = PushSubscription::query()->create([
            'admin_user_id' => $admin->id,
            'endpoint' => 'https://fcm.googleapis.com/wp/abc',
            'p256dh' => 'p',
            'auth' => 'a',
        ]);

        $this->actingAs($admin, 'admin')
            ->deleteJson('/jabali-admin/push-subscriptions', ['endpoint' => $sub->endpoint])
            ->assertOk()
            ->assertJson(['deleted' => 1]);

        $this->assertSame(0, PushSubscription::query()->count());
    }

    public function test_vapid_public_key_endpoint_returns_configured_key(): void
    {
        config()->set('notifications.web_push.public_key', 'test-public-key');
        $admin = $this->admin();

        $this->actingAs($admin, 'admin')
            ->getJson('/jabali-admin/push-subscriptions/vapid-public-key')
            ->assertOk()
            ->assertJson(['public_key' => 'test-public-key']);
    }

    public function test_vapid_public_key_endpoint_503s_when_unset(): void
    {
        config()->set('notifications.web_push.public_key', '');
        $admin = $this->admin();

        $this->actingAs($admin, 'admin')
            ->getJson('/jabali-admin/push-subscriptions/vapid-public-key')
            ->assertStatus(503);
    }
}
