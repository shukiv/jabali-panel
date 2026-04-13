<?php

declare(strict_types=1);

namespace App\JabaliTerminal\Http\Controllers;

use App\JabaliTerminal\JabaliTerminalClient;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Hash;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Facades\RateLimiter;
use Illuminate\Support\Facades\Validator;
use Laravel\Fortify\Contracts\TwoFactorAuthenticationProvider;

/**
 * POST /jabali-admin/terminal/session — re-auth and mint one-use WS token.
 *
 * This controller exists as a separate HTTP endpoint (rather than a
 * Livewire action) so:
 *   - it can sit behind a named RateLimiter with per-admin + IP granularity
 *   - the JSON response is never embedded in the page's HTML
 *   - the feature test suite can exercise it directly
 *
 * Auth surface enforced here, matching docs/SECURITY.md:
 *   - auth:admin guard (attached at route registration)
 *   - Admin's own password re-checked every call
 *   - 2FA code required every call; no opt-out
 *   - 5 failed 2FA attempts in 15 min → hard lockout, same window
 *   - RateLimiter cap: 3/min per (admin_id, ip)
 *   - Generic "invalid credentials" error on any non-server-side failure
 *     so bad password vs bad 2FA vs locked-out aren't distinguishable
 */
class TerminalSessionController
{
    private const LOCKOUT_THRESHOLD = 5;

    private const LOCKOUT_SECONDS = 15 * 60;

    public function __invoke(Request $request): JsonResponse
    {
        // 2FA is conditional on the admin's own account setting. If the
        // admin has a two_factor_secret, a code is required. If not,
        // the field is accepted empty and skipped. Password is always
        // required — the session cookie alone is not enough to open a
        // root PTY.
        $validator = Validator::make($request->all(), [
            'password' => ['required', 'string'],
            'two_factor_code' => ['nullable', 'string', 'max:10'],
        ]);
        if ($validator->fails()) {
            return $this->invalid();
        }

        $user = $request->user('admin') ?? $request->user();
        if ($user === null || ! method_exists($user, 'isAdmin') || ! $user->isAdmin()) {
            return $this->forbidden();
        }

        $lockoutKey = 'jt:lockout:'.$user->getAuthIdentifier();
        if (Cache::has($lockoutKey)) {
            return $this->invalid();
        }

        if (! Hash::check((string) $request->input('password'), (string) $user->getAuthPassword())) {
            $this->recordFailure($lockoutKey);

            return $this->invalid();
        }

        // Only demand a 2FA code when the admin has 2FA configured.
        // Admins without 2FA get in on password alone.
        if (! empty($user->two_factor_secret)) {
            $code = (string) $request->input('two_factor_code', '');
            if ($code === '') {
                $this->recordFailure($lockoutKey);

                return $this->invalid();
            }
            /** @var TwoFactorAuthenticationProvider $provider */
            $provider = app(TwoFactorAuthenticationProvider::class);
            $decryptedSecret = decrypt($user->two_factor_secret);
            if (! $provider->verify($decryptedSecret, $code)) {
                $this->recordFailure($lockoutKey);

                return $this->invalid();
            }
        }

        // Success path — clear failure counter so a user who typed wrong once
        // but then typed right isn't penalised on the next session.
        Cache::forget($lockoutKey.':attempts');

        // Resolved via the container (not the getInstance() singleton) so
        // the feature test can swap in a fake with app()->instance().
        $client = app(JabaliTerminalClient::class);
        if (! $client->isAvailable()) {
            Log::error('JabaliTerminal: daemon unavailable on session mint');

            return response()->json(['error' => 'daemon unavailable'], 503);
        }

        $session = $client->requestSession(
            (int) $user->getAuthIdentifier(),
            (string) $request->ip(),
        );
        if ($session === null) {
            return response()->json(['error' => 'session mint failed'], 502);
        }

        // Construct the ws_url from the panel's own request URL rather than
        // trusting whatever the daemon returns — the daemon is reached via
        // the unix socket, so `request.host` there is "localhost" and the
        // browser would try to dial the wrong origin. Panel-side is the
        // only side that actually knows the public URL the browser used.
        $scheme = $request->isSecure() ? 'wss' : 'ws';
        $wsUrl = $scheme.'://'.$request->getHttpHost().'/terminal-ws';

        // Token is returned in the JSON body only — never rendered into HTML,
        // never logged, never persisted server-side. The browser must keep it
        // in a closure variable and drop it after the WS auth frame is sent.
        return response()->json([
            'ws_url' => $wsUrl,
            'token' => (string) $session['token'],
            'expires_at' => (int) $session['expires_at'],
        ]);
    }

    private function recordFailure(string $lockoutKey): void
    {
        $attemptsKey = $lockoutKey.':attempts';
        $attempts = (int) Cache::get($attemptsKey, 0) + 1;
        Cache::put($attemptsKey, $attempts, self::LOCKOUT_SECONDS);

        if ($attempts >= self::LOCKOUT_THRESHOLD) {
            Cache::put($lockoutKey, true, self::LOCKOUT_SECONDS);
        }
    }

    private function invalid(): JsonResponse
    {
        return response()->json(['error' => 'invalid credentials'], 422);
    }

    private function forbidden(): JsonResponse
    {
        return response()->json(['error' => 'forbidden'], 403);
    }
}
