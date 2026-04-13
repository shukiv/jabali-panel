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
require __DIR__.'/../vendor/autoload.php';
$app = require_once __DIR__.'/../bootstrap/app.php';
$kernel = $app->make(Illuminate\Contracts\Http\Kernel::class);
$request = Illuminate\Http\Request::capture();
$kernel->handle($request);

// Must be authenticated admin
$user = Illuminate\Support\Facades\Auth::guard('admin')->user();
if (! $user || ! $user->is_admin) {
    http_response_code(403);
    exit('Forbidden');
}

// Validate parameters
$users = $_GET['users'] ?? $_GET['username'] ?? '';
$snapshotId = $_GET['snapshot'] ?? 'latest';

// Detect server backup download
$isServerDownload = ($users === '__server__');

if (! $isServerDownload) {
    // Validate each username
    $userList = array_filter(array_map('trim', explode(',', $users)));
    foreach ($userList as $u) {
        if (! preg_match('/^[a-z][a-z0-9_-]{0,31}$/', $u)) {
            http_response_code(400);
            exit('Invalid username: '.htmlspecialchars($u));
        }
    }
    if (empty($userList)) {
        http_response_code(400);
        exit('No username specified');
    }
} else {
    $userList = [];
}

if (! preg_match('/^[a-f0-9]+$|^latest$/', $snapshotId)) {
    http_response_code(400);
    exit('Invalid snapshot ID');
}

// Call agent to create the named pipe and start streaming
try {
    if ($isServerDownload) {
        $result = app(\App\Services\Agent\AgentClient::class)->send('jb.server_download_pipe', [
            'snapshot_id' => $snapshotId,
        ]);
    } else {
        $result = app(\App\Services\Agent\AgentClient::class)->send('jb.download_pipe', [
            'users' => implode(',', $userList),
            'snapshot_id' => $snapshotId,
        ]);
    }
} catch (\Throwable $e) {
    http_response_code(500);
    exit('Agent error');
}

if (! ($result['success'] ?? false)) {
    http_response_code(500);
    exit($result['error'] ?? 'Export failed');
}

// Disable output buffering
while (ob_get_level()) {
    ob_end_clean();
}

// Send headers
$filename = $isServerDownload
    ? 'server-backup-'.date('Y-m-d').'.tar.gz'
    : implode('-', $userList).'-backup-'.date('Y-m-d').'.tar.gz';

// Server downloads use a temp archive file (not a pipe) to avoid blocking workers.
// Per-user downloads still use the named pipe for instant streaming.
if ($isServerDownload) {
    $archivePath = $result['archive'] ?? '';
    $doneSentinel = $result['done'] ?? '';

    if (empty($archivePath) || empty($doneSentinel)) {
        http_response_code(500);
        exit('Export failed');
    }

    // Poll for completion (background script creates .done when archive is ready)
    $maxWait = 900; // 15 minutes
    $waited = 0;
    while ($waited < $maxWait) {
        if (file_exists($doneSentinel)) {
            break;
        }
        sleep(3);
        $waited += 3;
    }

    if (! file_exists($doneSentinel)) {
        http_response_code(504);
        exit('Export timed out');
    }

    @unlink($doneSentinel);

    if (! file_exists($archivePath)) {
        http_response_code(500);
        exit('Archive not found');
    }

    header('Content-Type: application/gzip');
    header('Content-Disposition: attachment; filename="'.$filename.'"');
    header('Content-Length: '.filesize($archivePath));
    header('Cache-Control: no-cache, no-store');
    header('X-Accel-Buffering: no');

    readfile($archivePath);
    @unlink($archivePath);
    exit;
}

// Per-user download: stream from named pipe
$pipePath = $result['pipe'] ?? '';
if (empty($pipePath) || ! file_exists($pipePath)) {
    http_response_code(500);
    exit('Pipe not created');
}

header('Content-Type: application/gzip');
header('Content-Disposition: attachment; filename="'.$filename.'"');
header('Cache-Control: no-cache, no-store');
header('X-Accel-Buffering: no');

$fp = fopen($pipePath, 'rb');
if (! $fp) {
    http_response_code(500);
    exit('Cannot open pipe');
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
