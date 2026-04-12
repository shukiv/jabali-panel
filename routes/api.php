<?php

use App\Http\Controllers\AutomationApiController;
use App\Http\Controllers\GitWebhookController;
use App\Services\Agent\AgentClient;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Route;

Route::get('/user', function (Request $request) {
    return $request->user();
})->middleware('auth:sanctum');

// Auth check for nginx auth_request (stats, protected areas)
Route::get('/auth-check', function (Request $request) {
    // Check session-based auth first (same-domain access)
    if ($request->user('web') || $request->user('admin')) {
        return response('', 200);
    }

    // Check signed stats token (cross-domain access from panel)
    $originalUri = $request->header('X-Original-URI', '');
    parse_str(parse_url($originalUri, PHP_URL_QUERY) ?? '', $query);
    $token = $query['_stats_token'] ?? '';

    if ($token !== '' && Cache::get('stats_token:'.$token)) {
        return response('', 200);
    }

    return response('', 401);
})->middleware('web');

Route::post('/phpmyadmin/verify-token', function (Request $request) {
    $token = $request->input('token');

    if (! is_string($token) || ! preg_match('/^[0-9a-f]{64}$/', $token)) {
        return response()->json(['error' => 'Invalid token'], 401);
    }

    // Use Cache::get() which handles the prefix automatically
    $data = Cache::get('phpmyadmin_token_'.$token);

    if (! $data) {
        return response()->json(['error' => 'Invalid token'], 401);
    }

    // Delete token after use (single use)
    Cache::forget('phpmyadmin_token_'.$token);

    return response()->json($data);
})->middleware('throttle:internal-api');

$allowInternalRequest = static function (Request $request): bool {
    $configuredToken = trim((string) config('app.internal_api_token', ''));
    $providedToken = trim((string) (
        $request->header('X-Jabali-Internal-Token')
        ?? $request->input('internal_token')
        ?? ''
    ));

    return $configuredToken !== '' && $providedToken !== '' && hash_equals($configuredToken, $providedToken);
};

// Internal API for jabali-cache WordPress plugin
Route::post('/internal/page-cache', function (Request $request) use ($allowInternalRequest) {
    if (! $allowInternalRequest($request)) {
        return response()->json(['error' => 'Forbidden'], 403);
    }

    $domain = $request->input('domain');
    $enabled = $request->boolean('enabled');
    $secret = $request->input('secret');

    if (empty($domain) || ! preg_match('/^(?:[a-z0-9](?:[a-z0-9\-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$/i', $domain)) {
        return response()->json(['error' => 'Invalid domain'], 400);
    }

    if (empty($secret) || strlen($secret) < 16) {
        return response()->json(['error' => 'Invalid secret'], 401);
    }

    // Find the user who owns this domain
    $user = \App\Models\User::whereHas('domains', function ($query) use ($domain) {
        $query->where('domain', $domain);
    })->first();

    if (! $user) {
        return response()->json(['error' => 'Domain not found'], 404);
    }

    // Verify the secret by checking wp-config.php
    $wpConfigPath = "/home/{$user->username}/domains/{$domain}/public_html/wp-config.php";
    if (! file_exists($wpConfigPath)) {
        return response()->json(['error' => 'WordPress not found'], 404);
    }

    $wpConfig = file_get_contents($wpConfigPath);
    if (preg_match("/define\s*\(\s*['\"]AUTH_KEY['\"]\s*,\s*['\"]([^'\"]+)['\"]\s*\)/", $wpConfig, $matches)) {
        $authKey = $matches[1];
        $expectedSecret = hash('sha256', $authKey);
        if (! hash_equals($expectedSecret, $secret)) {
            return response()->json(['error' => 'Invalid secret'], 401);
        }
    } else {
        return response()->json(['error' => 'Cannot verify secret'], 401);
    }

    try {
        $agent = app(AgentClient::class);

        if ($enabled) {
            $result = $agent->send('wp.page_cache_enable', [
                'username' => $user->username,
                'domain' => $domain,
            ]);
        } else {
            $result = $agent->send('wp.page_cache_disable', [
                'username' => $user->username,
                'domain' => $domain,
            ]);
        }

        return response()->json($result);
    } catch (\Exception $e) {
        \Illuminate\Support\Facades\Log::error('Page cache error: '.$e->getMessage());

        return response()->json(['error' => 'An internal error occurred'], 500);
    }
})->middleware('throttle:internal-api');

// Internal API for smart page cache purging (called by jabali-cache WordPress plugin)
Route::post('/internal/page-cache-purge', function (Request $request) use ($allowInternalRequest) {
    if (! $allowInternalRequest($request)) {
        return response()->json(['error' => 'Forbidden'], 403);
    }

    $domain = $request->input('domain');
    $paths = $request->input('paths', []);
    $purgeAll = $request->boolean('purge_all');
    $secret = $request->input('secret');

    if (empty($domain) || ! preg_match('/^(?:[a-z0-9](?:[a-z0-9\-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$/i', $domain)) {
        return response()->json(['error' => 'Invalid domain'], 400);
    }

    // Sanitize paths array
    if (! is_array($paths)) {
        $paths = [];
    }
    $paths = array_filter($paths, fn ($p) => is_string($p) && preg_match('#^/[a-zA-Z0-9/_.\-]*$#', $p));
    $paths = array_values($paths);

    if (empty($secret) || strlen($secret) < 16) {
        return response()->json(['error' => 'Invalid secret'], 401);
    }

    // Find the user who owns this domain
    $user = \App\Models\User::whereHas('domains', function ($query) use ($domain) {
        $query->where('domain', $domain);
    })->first();

    if (! $user) {
        return response()->json(['error' => 'Domain not found'], 404);
    }

    // Verify the secret by checking wp-config.php
    $wpConfigPath = "/home/{$user->username}/domains/{$domain}/public_html/wp-config.php";
    if (! file_exists($wpConfigPath)) {
        return response()->json(['error' => 'WordPress not found'], 404);
    }

    $wpConfig = file_get_contents($wpConfigPath);
    if (preg_match("/define\s*\(\s*['\"]AUTH_KEY['\"]\s*,\s*['\"]([^'\"]+)['\"]\s*\)/", $wpConfig, $matches)) {
        $authKey = $matches[1];
        $expectedSecret = hash('sha256', $authKey);
        if (! hash_equals($expectedSecret, $secret)) {
            return response()->json(['error' => 'Invalid secret'], 401);
        }
    } else {
        return response()->json(['error' => 'Cannot verify secret'], 401);
    }

    try {
        $agent = app(AgentClient::class);

        if ($purgeAll || empty($paths)) {
            // Purge entire domain cache
            $result = $agent->send('wp.page_cache_purge', [
                'domain' => $domain,
            ]);
        } else {
            // Purge specific paths
            $result = $agent->send('wp.page_cache_purge', [
                'domain' => $domain,
                'paths' => $paths,
            ]);
        }

        return response()->json($result);
    } catch (\Exception $e) {
        \Illuminate\Support\Facades\Log::error('Page cache purge error: '.$e->getMessage());

        return response()->json(['error' => 'An internal error occurred'], 500);
    }
})->middleware('throttle:internal-api');

// Agent-to-panel callback for background task progress (ADR-0007).
// Auth is per-task via callback_token in the body, validated inside the
// controller — rate limiting + internal-api throttle still apply.
Route::post('/internal/tasks/{id}/event', \App\Http\Controllers\Internal\BackgroundTaskEventController::class)
    ->middleware('throttle:internal-api');

Route::post('/webhooks/git/{deployment}', GitWebhookController::class)
    ->middleware('throttle:git-webhooks');
Route::post('/webhooks/git/{deployment}/{token}', GitWebhookController::class)
    ->middleware('throttle:git-webhooks');

Route::middleware(['auth:sanctum', 'abilities:automation'])
    ->prefix('automation')
    ->group(function () {
        Route::get('/users', [AutomationApiController::class, 'listUsers']);
        Route::post('/users', [AutomationApiController::class, 'createUser']);
        Route::post('/domains', [AutomationApiController::class, 'createDomain']);
    });
