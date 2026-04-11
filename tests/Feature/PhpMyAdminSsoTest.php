<?php

declare(strict_types=1);

namespace Tests\Feature;

use App\Models\MysqlCredential;
use App\Models\User;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Crypt;
use Tests\TestCase;

class PhpMyAdminSsoTest extends TestCase
{
    public function test_phpmyadmin_redirect_route_generates_token_and_redirects(): void
    {
        $user = User::factory()->create(['username' => 'pmatest_'.bin2hex(random_bytes(4))]);
        MysqlCredential::create([
            'user_id' => $user->id,
            'mysql_username' => $user->username.'_admin',
            'mysql_password_encrypted' => Crypt::encryptString('testpass123'),
        ]);

        // Test without database param (opens all databases)
        $response = $this->actingAs($user)
            ->get(route('phpmyadmin.redirect'));

        $response->assertRedirect();
        $location = $response->headers->get('Location');

        $this->assertNotNull($location);
        $this->assertStringContainsString('/phpmyadmin/jabali-signon.php?token=', $location);

        // Token must be 64 hex chars
        preg_match('/token=([0-9a-f]{64})/', $location, $matches);
        $this->assertNotEmpty($matches[1] ?? '', 'Token must be 64 hex chars');

        // Token must be stored in cache with admin credentials
        $cached = Cache::get('phpmyadmin_token_'.$matches[1]);
        $this->assertNotNull($cached);
        $this->assertEquals($user->username.'_admin', $cached['username']);
        $this->assertEquals('testpass123', $cached['password']);

        // Cleanup
        MysqlCredential::where('user_id', $user->id)->delete();
        $user->delete();
    }

    public function test_phpmyadmin_redirect_requires_authentication(): void
    {
        $response = $this->get('/phpmyadmin-sso?database=test_db');

        $this->assertTrue(in_array($response->status(), [302, 401, 403]));
    }

    public function test_phpmyadmin_redirect_returns_404_without_credentials(): void
    {
        $user = User::factory()->create(['username' => 'pmanocred_'.bin2hex(random_bytes(4))]);

        $response = $this->actingAs($user)
            ->get(route('phpmyadmin.redirect'));

        $response->assertNotFound();

        $user->delete();
    }

    public function test_verify_token_api_returns_credentials(): void
    {
        $token = bin2hex(random_bytes(32));
        Cache::put('phpmyadmin_token_'.$token, [
            'username' => 'test_admin',
            'password' => 'secret',
            'database' => 'test_db',
        ], now()->addMinutes(5));

        $response = $this->postJson('/api/phpmyadmin/verify-token', ['token' => $token]);

        $response->assertOk()
            ->assertJson([
                'username' => 'test_admin',
                'password' => 'secret',
                'database' => 'test_db',
            ]);

        // Token is single-use
        $this->assertNull(Cache::get('phpmyadmin_token_'.$token));
    }

    public function test_verify_token_api_rejects_invalid_token(): void
    {
        $response = $this->postJson('/api/phpmyadmin/verify-token', ['token' => 'invalid']);

        $response->assertStatus(401);
    }

    public function test_verify_token_api_rejects_missing_token(): void
    {
        $token = bin2hex(random_bytes(32));
        // Not stored in cache — simulates expired

        $response = $this->postJson('/api/phpmyadmin/verify-token', ['token' => $token]);

        $response->assertStatus(401);
    }
}
