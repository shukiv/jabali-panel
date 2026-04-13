<?php

declare(strict_types=1);

namespace Tests\Feature;

use App\JabaliTerminal\JabaliTerminalClient;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Crypt;
use Illuminate\Support\Facades\Hash;
use Laravel\Fortify\Contracts\TwoFactorAuthenticationProvider;
use Mockery;
use Tests\TestCase;

/**
 * Feature test for the jabali-terminal session-mint endpoint.
 *
 * Ships with the addon; install.sh drops it into the panel's
 * tests/Feature/ directory so the panel's pipeline runs it.
 *
 * Token properties documented in docs/SECURITY.md (256-bit, 60s, IP-bound,
 * single-use) are verified daemon-side by daemon/tests/test_auth.py — this
 * file covers the PHP auth surface only.
 */
class TerminalAuthTest extends TestCase
{
    use RefreshDatabase;

    private const VALID_2FA_CODE = '123456';

    protected function setUp(): void
    {
        parent::setUp();
        Cache::flush();

        // A fake client that always returns a stable "minted" session so the
        // HTTP route can be asserted without a running daemon.
        $fake = Mockery::mock(JabaliTerminalClient::class);
        $fake->shouldReceive('isAvailable')->andReturn(true)->byDefault();
        $fake->shouldReceive('requestSession')->andReturn([
            'ws_url' => 'wss://example.test/terminal-ws',
            'token' => str_repeat('a', 140), // base64url-ish placeholder
            'expires_at' => time() + 60,
        ])->byDefault();
        $this->app->instance(JabaliTerminalClient::class, $fake);

        // Fortify provider stubbed: "123456" is the only valid code.
        $provider = Mockery::mock(TwoFactorAuthenticationProvider::class);
        $provider->shouldReceive('verify')->andReturnUsing(
            fn ($secret, $code) => $code === self::VALID_2FA_CODE,
        );
        $this->app->instance(TwoFactorAuthenticationProvider::class, $provider);
    }

    protected function tearDown(): void
    {
        Mockery::close();
        parent::tearDown();
    }

    private function adminUser(bool $withTwoFactor = true): User
    {
        /** @var User $user */
        $user = User::factory()->create([
            'password' => Hash::make('correct-horse'),
            'is_admin' => true,
            'two_factor_secret' => $withTwoFactor ? Crypt::encryptString('GEZDGNBVGY3TQOJQ') : null,
        ]);

        return $user;
    }

    public function test_rejects_unauthenticated_requests(): void
    {
        $response = $this->postJson('/jabali-admin/terminal/session', [
            'password' => 'x',
            'two_factor_code' => self::VALID_2FA_CODE,
        ]);

        // auth:admin middleware -> 401 (or redirect on web). We only care
        // that the endpoint is not reachable without an authenticated admin.
        $this->assertContains($response->status(), [401, 403, 419, 302]);
    }

    public function test_rejects_non_admin(): void
    {
        /** @var User $user */
        $user = User::factory()->create([
            'password' => Hash::make('correct-horse'),
            'is_admin' => false,
            'two_factor_secret' => Crypt::encryptString('GEZDGNBVGY3TQOJQ'),
        ]);

        $this->actingAs($user, 'admin')
            ->postJson('/jabali-admin/terminal/session', [
                'password' => 'correct-horse',
                'two_factor_code' => self::VALID_2FA_CODE,
            ])
            ->assertStatus(403);
    }

    public function test_rejects_missing_two_factor_code_when_admin_has_2fa(): void
    {
        $this->actingAs($this->adminUser(), 'admin')
            ->postJson('/jabali-admin/terminal/session', [
                'password' => 'correct-horse',
            ])
            ->assertStatus(422);
    }

    public function test_rejects_wrong_password(): void
    {
        $this->actingAs($this->adminUser(), 'admin')
            ->postJson('/jabali-admin/terminal/session', [
                'password' => 'wrong',
                'two_factor_code' => self::VALID_2FA_CODE,
            ])
            ->assertStatus(422)
            ->assertJson(['error' => 'invalid credentials']);
    }

    public function test_rejects_wrong_two_factor_code(): void
    {
        $this->actingAs($this->adminUser(), 'admin')
            ->postJson('/jabali-admin/terminal/session', [
                'password' => 'correct-horse',
                'two_factor_code' => '999999',
            ])
            ->assertStatus(422)
            ->assertJson(['error' => 'invalid credentials']);
    }

    public function test_admin_without_two_factor_enabled_logs_in_on_password_alone(): void
    {
        $this->actingAs($this->adminUser(withTwoFactor: false), 'admin')
            ->postJson('/jabali-admin/terminal/session', [
                'password' => 'correct-horse',
                // No two_factor_code — intentional; this admin has not enrolled.
            ])
            ->assertStatus(200)
            ->assertJsonStructure(['ws_url', 'token', 'expires_at']);
    }

    public function test_issues_session_on_success(): void
    {
        $this->actingAs($this->adminUser(), 'admin')
            ->postJson('/jabali-admin/terminal/session', [
                'password' => 'correct-horse',
                'two_factor_code' => self::VALID_2FA_CODE,
            ])
            ->assertStatus(200)
            ->assertJsonStructure(['ws_url', 'token', 'expires_at']);
    }

    public function test_locks_out_after_five_failed_attempts(): void
    {
        $admin = $this->adminUser();

        for ($i = 0; $i < 5; $i++) {
            $this->actingAs($admin, 'admin')
                ->postJson('/jabali-admin/terminal/session', [
                    'password' => 'wrong',
                    'two_factor_code' => self::VALID_2FA_CODE,
                ])
                ->assertStatus(422);
        }

        // 6th attempt, even with the right credentials, must fail while the
        // lockout is live.
        $this->actingAs($admin, 'admin')
            ->postJson('/jabali-admin/terminal/session', [
                'password' => 'correct-horse',
                'two_factor_code' => self::VALID_2FA_CODE,
            ])
            ->assertStatus(422);
    }

    public function test_rate_limits_after_three_successful_calls_per_minute(): void
    {
        $admin = $this->adminUser();

        for ($i = 0; $i < 3; $i++) {
            $this->actingAs($admin, 'admin')
                ->postJson('/jabali-admin/terminal/session', [
                    'password' => 'correct-horse',
                    'two_factor_code' => self::VALID_2FA_CODE,
                ])
                ->assertStatus(200);
        }

        // 4th within the window -> throttled by the named limiter.
        $this->actingAs($admin, 'admin')
            ->postJson('/jabali-admin/terminal/session', [
                'password' => 'correct-horse',
                'two_factor_code' => self::VALID_2FA_CODE,
            ])
            ->assertStatus(429);
    }

    public function test_daemon_unavailable_returns_503(): void
    {
        $fake = Mockery::mock(JabaliTerminalClient::class);
        $fake->shouldReceive('isAvailable')->andReturn(false);
        $this->app->instance(JabaliTerminalClient::class, $fake);

        $this->actingAs($this->adminUser(), 'admin')
            ->postJson('/jabali-admin/terminal/session', [
                'password' => 'correct-horse',
                'two_factor_code' => self::VALID_2FA_CODE,
            ])
            ->assertStatus(503);
    }
}
