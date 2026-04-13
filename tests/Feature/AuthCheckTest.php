<?php

declare(strict_types=1);

namespace Tests\Feature;

use Illuminate\Support\Facades\Cache;
use Tests\TestCase;

/**
 * Covers /api/auth-check — the endpoint nginx uses for `auth_request` gating
 * on stats/report pages. Accepts either session-authed users (web/admin guard)
 * or a short-lived `_stats_token` signed into the cache by Logs.php.
 */
class AuthCheckTest extends TestCase
{
    public function test_unauthenticated_request_without_token_returns_401(): void
    {
        $this->get('/api/auth-check')->assertStatus(401);
    }

    public function test_valid_stats_token_in_x_original_uri_returns_200(): void
    {
        $token = bin2hex(random_bytes(16));
        Cache::put('stats_token:'.$token, true, now()->addMinutes(5));

        $this->withHeaders([
            'X-Original-URI' => '/stats/report.html?_stats_token='.$token,
        ])->get('/api/auth-check')->assertStatus(200);
    }

    /**
     * Repro for a handler bug: when `_stats_token` is submitted as an array
     * (e.g. `_stats_token[]=x`), parse_str returns an array, the handler
     * concatenates it into a cache key via `.` which triggers a PHP warning
     * ("Array to string conversion") and coerces the value to the literal
     * string 'Array'. Expected behavior: the array-shaped token is rejected
     * outright with 401, no warning logged.
     */
    public function test_array_shaped_stats_token_is_rejected_without_warning(): void
    {
        // Flip PHP to treat warnings as exceptions so the ".Array" concat
        // path fails the test. set_error_handler only catches current-scope
        // warnings — the controller runs in the same request pipeline, so
        // this will see them.
        $previous = set_error_handler(function (int $severity, string $message, string $file, int $line): bool {
            if ($severity === E_WARNING && str_contains($message, 'Array to string conversion')) {
                throw new \RuntimeException('auth-check emitted array-to-string warning: '.$message);
            }

            return false;
        });

        try {
            $this->withHeaders([
                'X-Original-URI' => '/stats/report.html?_stats_token[]=evil',
            ])->get('/api/auth-check')->assertStatus(401);
        } finally {
            if ($previous === null) {
                restore_error_handler();
            } else {
                set_error_handler($previous);
            }
        }
    }

    public function test_malformed_stats_token_is_rejected(): void
    {
        // Real tokens are 32 lowercase hex chars (bin2hex(random_bytes(16))).
        // Anything else — short strings, mixed case, non-hex — must 401,
        // never even hit the cache store.
        Cache::shouldReceive('get')
            ->with(\Mockery::pattern('/^stats_token:/'))
            ->never();

        $this->withHeaders([
            'X-Original-URI' => '/stats/report.html?_stats_token=../etc/passwd',
        ])->get('/api/auth-check')->assertStatus(401);
    }
}
