<?php

declare(strict_types=1);

namespace Tests\Feature;

use App\Models\User;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Str;
use Tests\TestCase;

/**
 * /auto-login consumes the one-time token minted by `jabali login-token`
 * (see LoginTokenCommand). The token payload carries both `user_id` and
 * `panel` — but the handler used to derive the guard from $user->is_admin
 * alone and only honour `panel` for the redirect target. That meant an
 * admin opening a "panel=user" token logged into the admin guard and then
 * hit the user panel unauthenticated, which bounces to the login screen.
 *
 * Fix: the `panel` field drives both the guard AND the redirect. A
 * non-admin asking for `panel=admin` is rejected — the token is a
 * capability, never an elevation.
 */
class AutoLoginTest extends TestCase
{
    /** @var array<int, int> */
    private array $createdUserIds = [];

    protected function tearDown(): void
    {
        // Project convention (CLAUDE.md): factories without RefreshDatabase,
        // clean up per-test. Avoids UNIQUE constraint violations in unrelated
        // tests that hard-code IDs.
        if (! empty($this->createdUserIds)) {
            User::whereIn('id', $this->createdUserIds)->forceDelete();
        }
        parent::tearDown();
    }

    private function makeUser(bool $isAdmin): User
    {
        $user = User::factory()->create(['is_admin' => $isAdmin]);
        $this->createdUserIds[] = $user->id;

        return $user;
    }

    private function mintToken(int $userId, string $panel): string
    {
        $token = Str::random(64);
        Cache::put('login_token:'.hash('sha256', $token), [
            'user_id' => $userId,
            'panel' => $panel,
        ], now()->addMinutes(5));

        return $token;
    }

    public function test_admin_with_admin_panel_token_logs_in_and_redirects_to_admin(): void
    {
        $admin = $this->makeUser(true);
        $token = $this->mintToken($admin->id, 'admin');

        $response = $this->get('/auto-login?token='.$token);

        $response->assertRedirect('/jabali-admin/');
        $this->assertAuthenticatedAs($admin, 'admin');
    }

    public function test_user_with_user_panel_token_logs_in_and_redirects_to_user_panel(): void
    {
        $user = $this->makeUser(false);
        $token = $this->mintToken($user->id, 'user');

        $response = $this->get('/auto-login?token='.$token);

        $response->assertRedirect('/jabali-panel/');
        $this->assertAuthenticatedAs($user, 'web');
    }

    public function test_admin_with_user_panel_token_logs_into_web_guard_not_admin(): void
    {
        $admin = $this->makeUser(true);
        $token = $this->mintToken($admin->id, 'user');

        $response = $this->get('/auto-login?token='.$token);

        $response->assertRedirect('/jabali-panel/');
        // Guard must follow `panel`, not is_admin — otherwise the user hits
        // /jabali-panel/ with only the admin guard authenticated and gets
        // bounced to login.
        $this->assertAuthenticatedAs($admin, 'web');
    }

    public function test_non_admin_with_admin_panel_token_is_rejected(): void
    {
        $user = $this->makeUser(false);
        $token = $this->mintToken($user->id, 'admin');

        $response = $this->get('/auto-login?token='.$token);

        // Capability tokens must never elevate. 403 is the right answer —
        // the token is valid, but the claim it carries isn't.
        $response->assertStatus(403);
        $this->assertGuest('admin');
        $this->assertGuest('web');
    }

    public function test_invalid_token_aborts_403(): void
    {
        $this->get('/auto-login?token=garbage')->assertStatus(403);
    }

    public function test_missing_token_aborts_403(): void
    {
        $this->get('/auto-login')->assertStatus(403);
    }

    public function test_array_shaped_token_aborts_403(): void
    {
        // Same family of bug as the stats_token fix: a `token[]=x` query
        // must not reach hash() with an array argument.
        $this->get('/auto-login?token[]=x')->assertStatus(403);
    }
}
