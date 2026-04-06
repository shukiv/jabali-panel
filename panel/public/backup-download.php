<?php

declare(strict_types=1);

/**
 * Backup archive streaming download endpoint.
 *
 * Bootstraps Laravel for admin auth, then calls the agent to create a named
 * pipe and stream the backup archive directly to the browser. No temp files.
 *
 * Installed to {jabali}/public/backup-download.php by install-panel.sh.
 *
 * Usage: GET /backup-download.php?username=shuki&snapshot=latest
 */

// No time limit for streaming large archives
set_time_limit(0);

// Bootstrap Laravel
require __DIR__ . '/../vendor/autoload.php';
$app = require_once __DIR__ . '/../bootstrap/app.php';
$kernel = $app->make(Illuminate\Contracts\Http\Kernel::class);
$request = Illuminate\Http\Request::capture();
$kernel->handle($request);

// Must be authenticated admin
$user = Illuminate\Support\Facades\Auth::guard('admin')->user();
if (! $user || ! $user->is_admin) {
    http_response_code(403);
    die('Forbidden');
}

// Validate parameters
$users = $_GET['users'] ?? $_GET['username'] ?? '';
$snapshotId = $_GET['snapshot'] ?? 'latest';

// Validate each username
$userList = array_filter(array_map('trim', explode(',', $users)));
foreach ($userList as $u) {
    if (! preg_match('/^[a-z][a-z0-9_-]{0,31}$/', $u)) {
        http_response_code(400);
        die('Invalid username: ' . htmlspecialchars($u));
    }
}
if (empty($userList)) {
    http_response_code(400);
    die('No username specified');
}
if (! preg_match('/^[a-f0-9]+$|^latest$/', $snapshotId)) {
    http_response_code(400);
    die('Invalid snapshot ID');
}

// Call agent to create the named pipe and start streaming
try {
    $result = app(\App\Services\Agent\AgentClient::class)->send('jb.download_pipe', [
        'users' => implode(',', $userList),
        'snapshot_id' => $snapshotId,
    ]);
} catch (\Throwable $e) {
    http_response_code(500);
    die('Agent error');
}

if (! ($result['success'] ?? false)) {
    http_response_code(500);
    die($result['error'] ?? 'Export failed');
}

$pipePath = $result['pipe'] ?? '';
if (empty($pipePath) || ! file_exists($pipePath)) {
    http_response_code(500);
    die('Pipe not created');
}

// Disable output buffering
while (ob_get_level()) {
    ob_end_clean();
}

// Send headers
$filename = implode('-', $userList) . '-backup-' . date('Y-m-d') . '.tar.gz';
header('Content-Type: application/gzip');
header('Content-Disposition: attachment; filename="' . $filename . '"');
header('Cache-Control: no-cache, no-store');
header('X-Accel-Buffering: no');

// Stream from the named pipe
$fp = fopen($pipePath, 'rb');
if (! $fp) {
    http_response_code(500);
    die('Cannot open pipe');
}

while (! feof($fp)) {
    $chunk = fread($fp, 65536);
    if ($chunk !== false && $chunk !== '') {
        echo $chunk;
        flush();
    }
}
fclose($fp);
@unlink($pipePath);

exit;
