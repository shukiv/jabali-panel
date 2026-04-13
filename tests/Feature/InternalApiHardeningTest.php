<?php

declare(strict_types=1);

namespace Tests\Feature;

use Tests\TestCase;

/**
 * The /api/internal/page-cache* endpoints live behind the shared
 * JABALI_INTERNAL_API_TOKEN. Callers today are the jabali-cache WP plugin
 * and occasionally server-side tooling; both send well-formed payloads.
 *
 * If either caller (or a bug-ridden future one) sent `domain[]=x.com` or
 * `secret[]=x`, the handler would call preg_match / strlen on an array,
 * which in PHP 8.4 throws TypeError — so the internal caller would see a
 * 500 with no context instead of a clean validation error. Harden input
 * so the bad-shape path returns the same HTTP status as bad-value-but-
 * right-shape (400 for domain, 401 for secret).
 */
class InternalApiHardeningTest extends TestCase
{
    private const TEST_TOKEN = 'test-internal-token-48chars-for-hash_equals';

    protected function setUp(): void
    {
        parent::setUp();
        config(['app.internal_api_token' => self::TEST_TOKEN]);
    }

    private function headers(): array
    {
        return ['X-Jabali-Internal-Token' => self::TEST_TOKEN];
    }

    public function test_page_cache_rejects_array_shaped_domain_cleanly(): void
    {
        $response = $this->withHeaders($this->headers())
            ->postJson('/api/internal/page-cache', [
                'domain' => ['evil.com'],
                'enabled' => true,
                'secret' => str_repeat('a', 32),
            ]);

        $response->assertStatus(400);
        $response->assertJson(['error' => 'Invalid domain']);
    }

    public function test_page_cache_rejects_array_shaped_secret_cleanly(): void
    {
        $response = $this->withHeaders($this->headers())
            ->postJson('/api/internal/page-cache', [
                'domain' => 'example.com',
                'enabled' => true,
                'secret' => ['x'],
            ]);

        $response->assertStatus(401);
        $response->assertJson(['error' => 'Invalid secret']);
    }

    public function test_page_cache_purge_rejects_array_shaped_domain_cleanly(): void
    {
        $response = $this->withHeaders($this->headers())
            ->postJson('/api/internal/page-cache-purge', [
                'domain' => ['evil.com'],
                'secret' => str_repeat('a', 32),
            ]);

        $response->assertStatus(400);
        $response->assertJson(['error' => 'Invalid domain']);
    }

    public function test_page_cache_purge_rejects_array_shaped_secret_cleanly(): void
    {
        $response = $this->withHeaders($this->headers())
            ->postJson('/api/internal/page-cache-purge', [
                'domain' => 'example.com',
                'secret' => ['x'],
            ]);

        $response->assertStatus(401);
        $response->assertJson(['error' => 'Invalid secret']);
    }

    /**
     * Sanity check: the existing 403-on-missing-token path still works and
     * the bad-shape path is NOT more informative than the unauthorised one.
     * (No auth, no feedback about input shape — fail at the boundary.)
     */
    public function test_bad_shape_input_without_internal_token_returns_403(): void
    {
        $this->postJson('/api/internal/page-cache', [
            'domain' => ['evil.com'],
            'secret' => ['x'],
        ])->assertStatus(403);
    }
}
