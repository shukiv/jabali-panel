<?php

/**
 * Generate an authenticated session cookie for ZAP scanning.
 * Run on the server: php scripts/zap/generate-session.php
 */

require __DIR__ . '/../../vendor/autoload.php';
$app = require_once __DIR__ . '/../../bootstrap/app.php';
$app->make(\Illuminate\Contracts\Console\Kernel::class)->bootstrap();

$user = \App\Models\User::where('is_admin', true)->first();
if (! $user) {
    echo "No admin user found!\n";
    exit(1);
}

// Create a session and log in
$session = app('session')->driver();
$session->start();

// Set session auth for both guards
$session->put('login_admin_' . sha1(\Filament\Http\Middleware\Authenticate::class), $user->id);
$session->put('login_web_' . sha1(\Illuminate\Auth\SessionGuard::class), $user->id);
$session->put('_token', \Illuminate\Support\Str::random(40));
$session->save();

$sessionName = config('session.cookie', 'jabali_session');
$sessionId = $session->getId();

echo "Session Name: {$sessionName}\n";
echo "Session ID: {$sessionId}\n";
echo "User: {$user->email}\n";
echo "\nFor ZAP, use this cookie:\n";
echo "{$sessionName}={$sessionId}\n";
