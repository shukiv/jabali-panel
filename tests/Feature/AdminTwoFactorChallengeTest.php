<?php

declare(strict_types=1);

namespace Tests\Feature;

use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Auth;
use Laravel\Fortify\Contracts\TwoFactorAuthenticationProvider;
use Tests\TestCase;

class AdminTwoFactorChallengeTest extends TestCase
{
    use RefreshDatabase;

    public function test_admin_can_view_admin_two_factor_challenge(): void
    {
        $admin = User::factory()->admin()->create();
        $secret = app(TwoFactorAuthenticationProvider::class)->generateSecretKey();

        $admin->forceFill([
            'two_factor_secret' => encrypt($secret),
            'two_factor_confirmed_at' => now(),
            'two_factor_recovery_codes' => encrypt(json_encode(['abcd-efgh-1234'])),
        ])->save();

        $response = $this->withSession([
            'login.id' => $admin->id,
            'login.remember' => false,
        ])->get('/jabali-admin/two-factor-challenge');

        $response->assertOk();
    }

    public function test_non_admin_is_redirected_from_admin_two_factor_challenge(): void
    {
        $user = User::factory()->create();
        $secret = app(TwoFactorAuthenticationProvider::class)->generateSecretKey();

        $user->forceFill([
            'two_factor_secret' => encrypt($secret),
            'two_factor_confirmed_at' => now(),
            'two_factor_recovery_codes' => encrypt(json_encode(['abcd-efgh-1234'])),
        ])->save();

        $response = $this->withSession([
            'login.id' => $user->id,
        ])->get('/jabali-admin/two-factor-challenge');

        $response->assertRedirect('/jabali-admin/login');
        $this->assertFalse(Auth::guard('admin')->check());
    }
}
