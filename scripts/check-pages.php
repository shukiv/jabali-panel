<?php

/**
 * Jabali Panel — Smoke Test
 *
 * Checks all admin and user panel pages for HTTP errors.
 * Run: php scripts/check-pages.php
 */

use Illuminate\Http\Request;

require __DIR__ . '/../vendor/autoload.php';
$app = require_once __DIR__ . '/../bootstrap/app.php';
$app->make(\Illuminate\Contracts\Console\Kernel::class)->bootstrap();

$user = \App\Models\User::where('is_admin', true)->first();
if (! $user) {
    echo "No admin user found!\n";
    exit(1);
}
echo "Testing as: {$user->email}\n\n";

$pages = [
    // Admin panel
    '/jabali-admin' => 'Admin Dashboard',
    '/jabali-admin/server-settings' => 'Server Settings',
    '/jabali-admin/security' => 'Security',
    '/jabali-admin/services' => 'Services',
    '/jabali-admin/backups' => 'Backups',
    '/jabali-admin/ssl-manager' => 'SSL Manager',
    '/jabali-admin/dns-zones' => 'DNS Zones',
    '/jabali-admin/server-status' => 'Server Status',
    '/jabali-admin/email-logs' => 'Email Logs',
    '/jabali-admin/ip-addresses' => 'IP Addresses',
    '/jabali-admin/automation-api' => 'Automation API',
    '/jabali-admin/server-updates' => 'Server Updates',
    '/jabali-admin/waf' => 'WAF',
    '/jabali-admin/database-tuning' => 'Database Tuning',
    '/jabali-admin/php-manager' => 'PHP Manager',
    '/jabali-admin/email-queue' => 'Email Queue',
    '/jabali-admin/support' => 'Admin Support',

    // User panel
    '/jabali-panel' => 'User Dashboard',
    '/jabali-panel/domains' => 'Domains',
    '/jabali-panel/email' => 'Email',
    '/jabali-panel/databases' => 'Databases',
    '/jabali-panel/files' => 'Files',
    '/jabali-panel/backups' => 'User Backups',
    '/jabali-panel/ssl' => 'User SSL',
    '/jabali-panel/dns-records' => 'DNS Records',
    '/jabali-panel/wordpress' => 'WordPress',
    '/jabali-panel/cron-jobs' => 'Cron Jobs',
    '/jabali-panel/logs' => 'Logs',
    '/jabali-panel/ssh-keys' => 'SSH Keys',
    '/jabali-panel/php-settings' => 'PHP Settings',
    '/jabali-panel/git-deployment' => 'Git Deployment',
    '/jabali-panel/image-optimization' => 'Image Optimization',
    '/jabali-panel/protected-directories' => 'Protected Dirs',
    '/jabali-panel/support' => 'User Support',
];

$kernel = $app->make(\Illuminate\Contracts\Http\Kernel::class);
$ok = 0;
$fail = 0;
$errors = [];
$baseUrl = config('app.url', 'https://localhost');

foreach ($pages as $path => $label) {
    try {
        $request = Request::create($baseUrl . $path, 'GET');
        $request->server->set('HTTPS', 'on');

        $session = $app->make('session')->driver();
        $session->start();
        $session->put('_token', 'smoke-test');

        if (str_starts_with($path, '/jabali-admin')) {
            $session->put('login_admin_' . sha1(\Filament\Http\Middleware\Authenticate::class), $user->id);
        }
        $session->put('login_web_' . sha1(\Illuminate\Auth\SessionGuard::class), $user->id);
        $session->save();

        $request->setLaravelSession($session);
        $request->cookies->set($session->getName(), $session->getId());

        $response = $kernel->handle($request);
        $status = $response->getStatusCode();
        $size = strlen($response->getContent());

        if ($status >= 500) {
            echo "  FAIL $status  " . str_pad(round($size / 1024) . 'K', 6) . " $label ($path)\n";
            $fail++;
            $errors[] = "$label: HTTP $status";
        } elseif ($status >= 400) {
            echo "  WARN $status  " . str_pad(round($size / 1024) . 'K', 6) . " $label ($path)\n";
            $fail++;
            $errors[] = "$label: HTTP $status";
        } elseif ($status === 200) {
            echo "  OK   $status  " . str_pad(round($size / 1024) . 'K', 6) . " $label\n";
            $ok++;
        } else {
            $loc = $response->headers->get('Location', '');
            echo "  ->   $status        $label -> " . basename($loc) . "\n";
            $ok++;
        }
    } catch (\Throwable $e) {
        $msg = substr($e->getMessage(), 0, 120);
        echo "  ERR  ---        $label -- $msg\n";
        $fail++;
        $errors[] = "$label: $msg";
    }
}

echo "\n=== " . ($fail === 0 ? 'PASS' : 'FAIL') . ": $ok passed, $fail failed out of " . count($pages) . " ===\n";

if ($errors) {
    echo "\nIssues:\n";
    foreach ($errors as $e) {
        echo "  - $e\n";
    }
}

exit($fail > 0 ? 1 : 0);
