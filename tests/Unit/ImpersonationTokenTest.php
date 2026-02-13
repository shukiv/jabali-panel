<?php

declare(strict_types=1);

namespace Tests\Unit;

use App\Models\ImpersonationToken;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

class ImpersonationTokenTest extends TestCase
{
    use RefreshDatabase;

    public function test_find_valid_token_respects_ip_address(): void
    {
        $admin = User::factory()->admin()->create();
        $target = User::factory()->create();

        $token = ImpersonationToken::createForUser($admin, $target, '10.0.0.1');

        $this->assertNotNull(ImpersonationToken::findValidToken($token->token, '10.0.0.1'));
        $this->assertNull(ImpersonationToken::findValidToken($token->token, '10.0.0.2'));

        $token->update(['ip_address' => null]);

        $this->assertNotNull(ImpersonationToken::findValidToken($token->token, '10.0.0.2'));
    }
}
