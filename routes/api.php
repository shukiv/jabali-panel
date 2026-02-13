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

Route::post('/phpmyadmin/verify-token', function (Request $request) {
    $token = $request->input('token');

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
    $remoteAddr = (string) $request->server('REMOTE_ADDR', $request->ip());
    $isLocalRequest = in_array($remoteAddr, ['127.0.0.1', '::1'], true);

    $configuredToken = trim((string) config('app.internal_api_token', ''));
    $providedToken = trim((string) (
        $request->header('X-Jabali-Internal-Token')
        ?? $request->input('internal_token')
        ?? ''
    ));
    $hasValidToken = $configuredToken !== '' && $providedToken !== '' && hash_equals($configuredToken, $providedToken);

    return $isLocalRequest || $hasValidToken;
};

// Internal API for jabali-cache WordPress plugin
Route::post('/internal/page-cache', function (Request $request) use ($allowInternalRequest) {
    if (! $allowInternalRequest($request)) {
        return response()->json(['error' => 'Forbidden'], 403);
    }

    $domain = $request->input('domain');
    $enabled = $request->boolean('enabled');
    $secret = $request->input('secret');

    if (empty($domain)) {
        return response()->json(['error' => 'Domain is required'], 400);
    }

    // Validate secret matches what's in wp-config.php
    // The secret is the first 32 chars of AUTH_KEY from wp-config
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
        $expectedSecret = substr(md5($authKey), 0, 32);
        if (! hash_equals($expectedSecret, $secret)) {
            return response()->json(['error' => 'Invalid secret'], 401);
        }
    } else {
        return response()->json(['error' => 'Cannot verify secret'], 401);
    }

    try {
        $agent = new AgentClient;

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
        return response()->json(['error' => $e->getMessage()], 500);
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

    if (empty($domain)) {
        return response()->json(['error' => 'Domain is required'], 400);
    }

    // Validate secret matches what's in wp-config.php
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
        $expectedSecret = substr(md5($authKey), 0, 32);
        if (! hash_equals($expectedSecret, $secret)) {
            return response()->json(['error' => 'Invalid secret'], 401);
        }
    } else {
        return response()->json(['error' => 'Cannot verify secret'], 401);
    }

    try {
        $agent = new AgentClient;

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
        return response()->json(['error' => $e->getMessage()], 500);
    }
})->middleware('throttle:internal-api');

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
