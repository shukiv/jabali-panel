<?php

declare(strict_types=1);

/**
 * Jabali-Backup Agent Addon
 *
 * Installed to /etc/jabali/agent.d/jabali-backup.php
 * Provides RPC routes that wrap the jabali-backup CLI for the panel UI.
 */

define('JABALI_BACKUP_BIN', '/usr/local/bin/jabali-backup');

// ─── Route Table ───

return [
    'jb.list_accounts'  => fn (array $p) => jbListAccounts($p),
    'jb.list_snapshots' => fn (array $p) => jbListSnapshots($p),
    'jb.backup_accounts' => fn (array $p) => jbBackupAccounts($p),
    'jb.run'            => fn (array $p) => jbRun($p),
    'jb.restore'        => fn (array $p) => jbRestore($p),
    'jb.restore_files'  => fn (array $p) => jbRestoreFiles($p),
    'jb.browse'         => fn (array $p) => jbBrowse($p),
    'jb.read_file'      => fn (array $p) => jbReadFile($p),
    'jb.delete'         => fn (array $p) => jbDelete($p),
    'jb.check'          => fn (array $p) => jbCheck($p),
    'jb.doctor'         => fn (array $p) => jbDoctor($p),
    'jb.config_test'    => fn (array $p) => jbConfigTest($p),
    'jb.init'           => fn (array $p) => jbInit($p),
    'jb.forget'         => fn (array $p) => jbForget($p),
    'jb.snapshot_inventory' => fn (array $p) => jbSnapshotInventory($p),
    'jb.download'          => fn (array $p) => jbDownload($p),
    'jb.download_status'   => fn (array $p) => jbDownloadStatus($p),
    'jb.download_pipe'     => fn (array $p) => jbDownloadPipe($p),
    'jb.schedule'          => fn (array $p) => jbSchedulesList($p),
    'jb.schedules_list'    => fn (array $p) => jbSchedulesList($p),
    'jb.schedules_add'     => fn (array $p) => jbSchedulesAdd($p),
    'jb.schedules_update'  => fn (array $p) => jbSchedulesUpdate($p),
    'jb.schedules_remove'  => fn (array $p) => jbSchedulesRemove($p),
    'jb.schedules_enable'  => fn (array $p) => jbSchedulesEnable($p),
    'jb.schedules_disable' => fn (array $p) => jbSchedulesDisable($p),
    'jb.logs'              => fn (array $p) => jbLogs($p),
    'jb.destinations_list' => fn (array $p) => jbDestinationsList($p),
    'jb.destinations_add'  => fn (array $p) => jbDestinationsAdd($p),
    'jb.destinations_update' => fn (array $p) => jbDestinationsUpdate($p),
    'jb.destinations_remove' => fn (array $p) => jbDestinationsRemove($p),
    'jb.destinations_test' => fn (array $p) => jbDestinationsTest($p),
    'jb.destinations_init' => fn (array $p) => jbDestinationsInit($p),
    'jb.destinations_set_default' => fn (array $p) => jbDestinationsSetDefault($p),
    'jb.destinations_show' => fn (array $p) => jbDestinationsShow($p),
    'jb.destinations_reinit' => fn (array $p) => jbDestinationsReinit($p),
    'jb.ssh_keys'          => fn (array $p) => jbSshKeys($p),
    'jb.destination'       => fn (array $p) => jbDestinationsList($p),
    'jb.stalwart_status'   => fn (array $p) => jbStalwartStatus($p),
    'jb.stalwart_test'     => fn (array $p) => jbStalwartTest($p),
    'jb.stalwart_toggle'   => fn (array $p) => jbStalwartToggle($p),
    'jb.upload_preview'    => fn (array $p) => jbUploadPreview($p),
    'jb.upload_restore'    => fn (array $p) => jbUploadRestore($p),
    'jb.server_backup'        => fn (array $p) => jbServerBackup($p),
    'jb.server_snapshots'     => fn (array $p) => jbServerSnapshots($p),
    'jb.server_restore'       => fn (array $p) => jbServerRestore($p),
    'jb.server_download_pipe' => fn (array $p) => jbServerDownloadPipe($p),
];

// ─── Helpers ───

function jbExec(array $args, int $timeout = 120): array
{
    $cmd = array_merge([JABALI_BACKUP_BIN], $args);

    $descriptors = [
        1 => ['pipe', 'w'],
        2 => ['pipe', 'w'],
    ];

    $proc = proc_open($cmd, $descriptors, $pipes);
    if (! is_resource($proc)) {
        return ['success' => false, 'error' => 'Failed to execute jabali-backup'];
    }

    stream_set_blocking($pipes[1], false);
    stream_set_blocking($pipes[2], false);

    $stdout = '';
    $stderr = '';
    $start = time();

    while (true) {
        $status = proc_get_status($proc);
        if (! $status['running']) {
            break;
        }
        if ((time() - $start) > $timeout) {
            proc_terminate($proc, 9);
            fclose($pipes[1]);
            fclose($pipes[2]);
            proc_close($proc);

            return ['success' => false, 'error' => 'Command timed out'];
        }
        $stdout .= stream_get_contents($pipes[1]);
        $stderr .= stream_get_contents($pipes[2]);
        usleep(50000);
    }

    $stdout .= stream_get_contents($pipes[1]);
    $stderr .= stream_get_contents($pipes[2]);
    fclose($pipes[1]);
    fclose($pipes[2]);
    $exitCode = proc_close($proc);

    return [
        'exitCode' => $exitCode,
        'stdout' => trim($stdout),
        'stderr' => trim($stderr),
    ];
}

function jbExecBackground(array $args): array
{
    $cmd = implode(' ', array_map('escapeshellarg', array_merge([JABALI_BACKUP_BIN], $args)));
    $logFile = '/tmp/jabali-backup-' . bin2hex(random_bytes(4)) . '.log';

    $bg = sprintf('nohup %s > %s 2>&1 &', $cmd, escapeshellarg($logFile));
    proc_open(['bash', '-c', $bg], [], $pipes);

    return ['success' => true, 'log' => $logFile];
}

function jbValidateUsername(string $username): bool
{
    return (bool) preg_match('/^[a-z][a-z0-9_-]{0,31}$/', $username);
}

function jbValidateSnapshotId(string $id): bool
{
    return $id === 'latest' || (bool) preg_match('/^[a-f0-9]{6,64}$/', $id);
}

function jbValidateComponentList(string $csv): array
{
    $valid = ['files', 'mysql', 'postgres', 'email', 'dns', 'ssl', 'nginx', 'php', 'cron', 'wordpress', 'metadata'];

    return array_values(array_intersect(array_map('trim', explode(',', $csv)), $valid));
}

function jbValidateName(string $name): bool
{
    return (bool) preg_match('/^[a-zA-Z0-9 _\-\.]{1,64}$/', $name);
}

function jbValidateCron(string $cron): bool
{
    return (bool) preg_match('/^[0-9 *\/,\-]+$/', $cron) && strlen($cron) <= 100;
}

function jbValidateDestinationId(string $id): bool
{
    return (bool) preg_match('/^[a-zA-Z0-9_\-\.]+$/', $id) && strlen($id) <= 64;
}

/**
 * Resolve the /server/ root path inside a server snapshot.
 * Server snapshots store data at /tmp/jabali-backup/server-XXXXX/server/
 * This function reads the snapshot metadata to find the backup root path,
 * then appends /server/ to it.
 */
function jbResolveServerRoot(string $snapshotId, ?array $env): ?string
{
    if ($env === null) {
        return null;
    }

    // Use restic snapshots --json to get the paths array for this snapshot
    $cmd = ['restic', 'snapshots', $snapshotId, '--json'];
    foreach ($env['args'] as $arg) {
        $cmd[] = $arg;
    }
    $envMap = ! empty($env['env']) ? array_merge(getenv(), $env['env']) : null;
    $proc = proc_open($cmd, [1 => ['pipe', 'w'], 2 => ['pipe', 'w']], $pipes, null, $envMap);
    $stdout = stream_get_contents($pipes[1]);
    fclose($pipes[1]);
    fclose($pipes[2]);
    proc_close($proc);

    $snapshots = @json_decode(trim($stdout), true);
    if (! is_array($snapshots) || empty($snapshots)) {
        return null;
    }

    // Get the first (only) snapshot's paths
    $paths = $snapshots[0]['paths'] ?? [];
    if (empty($paths)) {
        return null;
    }

    // The server data is at {backup_root}/server/
    // e.g. /tmp/jabali-backup/server-159513 → /tmp/jabali-backup/server-159513/server
    $rootPath = rtrim($paths[0], '/') . '/server';

    return $rootPath;
}

/**
 * Load restic env/args for the default destination.
 * Reads destinations.json, falls back to config.conf.
 * Returns ['env' => [...], 'args' => [...]] or null on failure.
 */
function jbLoadResticEnv(): ?array
{
    $destFile = '/etc/jabali-backup/destinations.json';
    $configFile = '/etc/jabali-backup/config.conf';
    $env = [];
    $args = [];

    // Try destinations.json first (multi-destination system)
    if (file_exists($destFile) && ($json = @json_decode((string) file_get_contents($destFile), true))) {
        $dest = null;
        foreach ($json as $d) {
            if ($d['is_default'] ?? false) {
                $dest = $d;
                break;
            }
        }
        if ($dest === null && ! empty($json)) {
            $dest = reset($json);
        }
        if ($dest !== null) {
            $repo = $dest['path'] ?? '';
            $creds = $dest['credentials'] ?? [];
            if ($repo === '') {
                return null;
            }

            $args[] = '--repo';
            $args[] = $repo;

            // Restic repo password — check insecure_no_password first since the
            // password file may exist but be empty (created by --no-encryption)
            if ($creds['insecure_no_password'] ?? false) {
                $args[] = '--insecure-no-password';
            } else {
                $resPwFile = $creds['restic_password_file'] ?? '';
                if ($resPwFile !== '' && file_exists($resPwFile) && trim((string) file_get_contents($resPwFile)) !== '') {
                    $args[] = '--password-file';
                    $args[] = $resPwFile;
                }
            }

            // SFTP auth — parse user@host from the repo URI (sftp:user@host:/path)
            $type = $dest['type'] ?? '';
            if ($type === 'sftp') {
                // Extract user@host from URI like sftp:user@host:/path
                if (preg_match('/^sftp:([^@]+)@([^:]+)/', $repo, $m)) {
                    $sshUser = $m[1];
                    $sshHost = $m[2];
                    $sshPwFile = $creds['ssh_password_file'] ?? '';
                    $sshKeyFile = $creds['ssh_key_file'] ?? '';

                    if ($sshKeyFile !== '' && file_exists($sshKeyFile)) {
                        $sftpCmd = "ssh -i {$sshKeyFile} -o StrictHostKeyChecking=accept-new {$sshUser}@{$sshHost} -s sftp";
                    } elseif ($sshPwFile !== '' && file_exists($sshPwFile)) {
                        $env['SSHPASS'] = trim((string) file_get_contents($sshPwFile));
                        $sftpCmd = "sshpass -e ssh -o StrictHostKeyChecking=accept-new {$sshUser}@{$sshHost} -s sftp";
                    } else {
                        $sftpCmd = "ssh -o StrictHostKeyChecking=accept-new {$sshUser}@{$sshHost} -s sftp";
                    }
                    $args[] = '-o';
                    $args[] = "sftp.command={$sftpCmd}";
                }
            } elseif ($type === 's3') {
                if (! empty($creds['aws_access_key'])) {
                    $env['AWS_ACCESS_KEY_ID'] = $creds['aws_access_key'];
                }
                if (! empty($creds['aws_secret_key'])) {
                    $env['AWS_SECRET_ACCESS_KEY'] = $creds['aws_secret_key'];
                }
            } elseif ($type === 'b2') {
                if (! empty($creds['b2_account_id'])) {
                    $env['B2_ACCOUNT_ID'] = $creds['b2_account_id'];
                }
                if (! empty($creds['b2_account_key'])) {
                    $env['B2_ACCOUNT_KEY'] = $creds['b2_account_key'];
                }
            }

            return ['env' => $env, 'args' => $args];
        }
    }

    // Fall back to config.conf [repository]
    if (file_exists($configFile)) {
        $repoPath = '';
        $inSection = false;
        foreach (file($configFile) as $line) {
            $line = trim($line);
            if ($line === '[repository]') { $inSection = true; continue; }
            if (str_starts_with($line, '[')) { $inSection = false; }
            if ($inSection && str_starts_with($line, 'path=')) { $repoPath = substr($line, 5); }
        }
        $pwFile = '/etc/jabali-backup/restic-password';
        if ($repoPath !== '' && file_exists($pwFile)) {
            return ['env' => [], 'args' => ['--repo', $repoPath, '--password-file', $pwFile]];
        }
    }

    return null;
}

// ─── Route Handlers ───

function jbListAccounts(array $params): array
{
    $r = jbExec(['list', 'accounts']);
    if ($r['exitCode'] !== 0) {
        return ['success' => false, 'error' => $r['stderr'] ?: $r['stdout']];
    }

    // Parse the table output into structured data
    $lines = explode("\n", $r['stdout']);
    $accounts = [];
    foreach ($lines as $line) {
        if (preg_match('/^\s*(\d+)\s+(\S+)\s+(\/\S+)\s+(yes|no)/', $line, $m)) {
            $accounts[] = [
                'id' => (int) $m[1],
                'username' => $m[2],
                'home' => $m[3],
                'active' => $m[4] === 'yes',
            ];
        }
    }

    if (empty($accounts) && ! empty($lines)) {
        return ['success' => false, 'error' => 'Failed to parse account list output'];
    }

    return ['success' => true, 'accounts' => $accounts];
}

function jbListSnapshots(array $params): array
{
    $username = $params['username'] ?? '';
    $args = ['list', 'snapshots'];

    if ($username !== '' && jbValidateUsername($username)) {
        $args[] = '--user=' . $username;
    }

    $r = jbExec($args);
    if ($r['exitCode'] !== 0) {
        return ['success' => false, 'error' => $r['stderr'] ?: $r['stdout']];
    }

    // Parse restic snapshots table output
    $lines = explode("\n", $r['stdout']);
    $snapshots = [];
    foreach ($lines as $line) {
        if (preg_match('/^([a-f0-9]{8})\s+(\d{4}-\d{2}-\d{2}\s\d{2}:\d{2}:\d{2})\s+(\S+)\s+(.+?)\s{2,}(\/\S.*)$/', $line, $m)) {
            $id = $m[1];
            $tags = array_map('trim', explode(',', $m[4]));

            $username = '';
            $date = '';
            $runId = '';
            foreach ($tags as $tag) {
                if (str_starts_with($tag, 'account:')) {
                    $username = substr($tag, 8);
                }
                if (str_starts_with($tag, 'date:')) {
                    $date = substr($tag, 5);
                }
                if (str_starts_with($tag, 'run:')) {
                    $runId = substr($tag, 4);
                }
            }

            $snapshots[] = [
                'id' => $id,
                'time' => trim($m[2]),
                'run_id' => $runId,
                'host' => trim($m[3]),
                'username' => $username,
                'date' => $date ?: substr(trim($m[2]), 0, 10),
                'size' => preg_match('/(\d+[\.\d]*\s*(?:B|KiB|MiB|GiB|TiB))/', $m[5], $sm) ? $sm[1] : '',
                'tags' => $tags,
                'paths' => array_map('trim', explode("\n", trim($m[5]))),
            ];
        }
    }

    return ['success' => true, 'snapshots' => $snapshots];
}

function jbBackupAccounts(array $params): array
{
    // Get all snapshots, then group by account
    $result = jbListSnapshots($params);
    if (! ($result['success'] ?? false)) {
        return $result;
    }

    $byAccount = [];
    foreach ($result['snapshots'] ?? [] as $snap) {
        $username = $snap['username'] ?? '';
        if ($username === '') {
            continue;
        }
        if (! isset($byAccount[$username])) {
            $byAccount[$username] = [
                'username' => $username,
                'snapshot_count' => 0,
                'latest_snapshot_id' => '',
                'latest_date' => '',
                'latest_time' => '',
            ];
        }
        $byAccount[$username]['snapshot_count']++;
        $snapDate = $snap['date'] ?? $snap['time'] ?? '';
        if ($snapDate > $byAccount[$username]['latest_date'] || $byAccount[$username]['latest_date'] === '') {
            $byAccount[$username]['latest_date'] = $snap['date'] ?? '';
            $byAccount[$username]['latest_time'] = $snap['time'] ?? '';
            $byAccount[$username]['latest_snapshot_id'] = $snap['id'] ?? '';
        }
    }

    return ['success' => true, 'accounts' => array_values($byAccount)];
}

function jbRun(array $params): array
{
    $username = $params['username'] ?? '';
    if ($username !== '' && ! jbValidateUsername($username)) {
        return ['success' => false, 'error' => 'Invalid username'];
    }

    $args = ['run'];

    // Destination — validate alphanumeric/dash/underscore/dot only
    $destination = $params['destination'] ?? '';
    if ($destination !== '' && preg_match('/^[a-zA-Z0-9_\-\.]+$/', $destination)) {
        $args[] = '--destination=' . $destination;
    }

    // Accounts: single username, comma-separated list, or all
    $accounts = $params['accounts'] ?? '';
    if ($username !== '') {
        $args[] = $username;
    } elseif ($accounts !== '' && $accounts !== 'all') {
        foreach (explode(',', $accounts) as $acct) {
            $acct = trim($acct);
            if ($acct !== '' && jbValidateUsername($acct)) {
                $args[] = $acct;
            }
        }
    }

    // Exclude accounts — validate each username individually
    $excludeAccounts = $params['exclude_accounts'] ?? '';
    if ($excludeAccounts !== '') {
        $safe = [];
        foreach (explode(',', $excludeAccounts) as $acct) {
            $acct = trim($acct);
            if ($acct !== '' && jbValidateUsername($acct)) {
                $safe[] = $acct;
            }
        }
        if (! empty($safe)) {
            $args[] = '--exclude-accounts=' . implode(',', $safe);
        }
    }

    // Build --only from component flags
    $components = [];
    $componentMap = [
        'include_files' => 'files',
        'include_databases' => 'mysql',
        'include_postgres' => 'postgres',
        'include_mailboxes' => 'email',
        'include_dns' => 'dns',
        'include_ssl' => 'ssl',
        'include_nginx' => 'nginx',
        'include_php' => 'php',
        'include_wordpress' => 'wordpress',
        'include_cron' => 'cron',
        'include_metadata' => 'metadata',
    ];

    $hasExclusion = false;
    foreach ($componentMap as $key => $component) {
        if (isset($params[$key]) && $params[$key] === false) {
            $hasExclusion = true;
        }
    }

    if ($hasExclusion) {
        foreach ($componentMap as $key => $component) {
            if (! isset($params[$key]) || $params[$key] !== false) {
                $components[] = $component;
            }
        }
        if (! empty($components)) {
            $args[] = '--only=' . implode(',', $components);
        }
    }

    // Run in background — backup can take minutes
    $result = jbExecBackground($args);

    return [
        'success' => true,
        'message' => $username
            ? "Backup started for {$username}"
            : 'Backup started for all accounts',
        'log' => $result['log'],
    ];
}

function jbRestore(array $params): array
{
    $username = $params['username'] ?? '';
    $snapshotId = $params['snapshot_id'] ?? 'latest';
    $components = $params['components'] ?? [];
    $force = $params['force'] ?? false;

    if (! jbValidateUsername($username)) {
        return ['success' => false, 'error' => 'Invalid username'];
    }
    if (! jbValidateSnapshotId($snapshotId)) {
        return ['success' => false, 'error' => 'Invalid snapshot ID'];
    }

    $args = ['restore', $username, '--snapshot=' . $snapshotId];

    if (! empty($components)) {
        $validComponents = ['files', 'mysql', 'postgres', 'email', 'dns', 'ssl', 'nginx', 'php', 'cron', 'wordpress', 'stalwart', 'metadata'];
        $safe = array_intersect($components, $validComponents);
        if (! empty($safe)) {
            $args[] = '--only=' . implode(',', $safe);
        }
    }
    if ($force) {
        $args[] = '--force';
    }

    // Selective file restore — pass individual file/folder paths
    $files = $params['files'] ?? [];
    foreach ($files as $file) {
        if (is_string($file) && $file !== '' && ! str_contains($file, '..') && ! str_contains($file, "\0")) {
            if (preg_match('#^[a-zA-Z0-9/_\-.\s]+$#', $file)) {
                $args[] = '--file=' . $file;
            }
        }
    }

    $r = jbExec($args, 300);

    if ($r['exitCode'] !== 0) {
        return ['success' => false, 'error' => $r['stderr'] ?: $r['stdout'], 'output' => $r['stdout']];
    }

    return ['success' => true, 'output' => $r['stdout']];
}

function jbRestoreFiles(array $params): array
{
    $username = $params['username'] ?? '';
    $snapshotId = $params['snapshot_id'] ?? 'latest';
    $files = $params['files'] ?? [];
    $targetDir = $params['target'] ?? '';

    if (! jbValidateUsername($username)) {
        return ['success' => false, 'error' => 'Invalid username'];
    }
    if (! jbValidateSnapshotId($snapshotId)) {
        return ['success' => false, 'error' => 'Invalid snapshot ID'];
    }

    $args = ['restore', $username, '--snapshot=' . $snapshotId];

    foreach ($files as $file) {
        // Reject any file with traversal patterns or invalid characters
        if (strpos($file, '..') !== false || strpos($file, "\0") !== false) {
            continue;
        }
        if (! preg_match('#^[a-zA-Z0-9/_\-\.\s]+$#', $file)) {
            continue;
        }
        $args[] = '--file=' . $file;
    }

    if ($targetDir !== '') {
        if (strpos($targetDir, '..') !== false || strpos($targetDir, "\0") !== false) {
            return ['success' => false, 'error' => 'Invalid target directory'];
        }
        if (! preg_match('#^[a-zA-Z0-9/_\-\.\s]+$#', $targetDir)) {
            return ['success' => false, 'error' => 'Invalid target directory'];
        }
        $args[] = '--target=' . $targetDir;
    }

    $r = jbExec($args, 300);

    if ($r['exitCode'] !== 0) {
        return ['success' => false, 'error' => $r['stderr'] ?: $r['stdout']];
    }

    return ['success' => true, 'output' => $r['stdout']];
}

function jbBrowse(array $params): array
{
    $username = $params['username'] ?? '';
    $path = $params['path'] ?? '';
    $snapshotId = $params['snapshot_id'] ?? 'latest';
    $isServer = ($username === '__server__');

    if (! $isServer && ! jbValidateUsername($username)) {
        return ['success' => false, 'error' => 'Invalid username'];
    }
    if ($snapshotId !== 'latest' && ! jbValidateSnapshotId($snapshotId)) {
        return ['success' => false, 'error' => 'Invalid snapshot ID'];
    }

    // Sanitize path — reject traversal patterns
    if (strpos($path, '..') !== false || strpos($path, "\0") !== false) {
        return ['success' => false, 'error' => 'Invalid path'];
    }

    // Resolve snapshot ID if "latest"
    $resolvedId = $snapshotId;
    if ($snapshotId === 'latest') {
        if ($isServer) {
            $listResult = jbExec(['list', 'snapshots', '--type=server']);
        } else {
            $listResult = jbExec(['list', 'snapshots', '--user=' . $username]);
        }
        if (preg_match_all('/^([a-f0-9]{8})\s/m', $listResult['stdout'], $matches)) {
            $resolvedId = end($matches[1]);
        } else {
            return ['success' => false, 'error' => 'No snapshots found'];
        }
    }

    // Build full path for restic ls
    if ($isServer) {
        // Server snapshots store data at /tmp/jabali-backup/server-XXXXX/server/
        // Discover the actual /server/ root by listing the snapshot top level
        $serverRoot = jbResolveServerRoot($resolvedId, jbLoadResticEnv());
        if ($serverRoot === null) {
            return ['success' => false, 'error' => 'Cannot resolve server snapshot root'];
        }
        $fullPath = $serverRoot;
        if ($path !== '') {
            $fullPath = rtrim($serverRoot, '/') . '/' . ltrim($path, '/');
        }
    } else {
        $fullPath = '/home/' . $username;
        if ($path !== '') {
            $fullPath .= '/' . ltrim($path, '/');
        }
    }

    // Load destination config — use default destination from destinations.json
    $env = jbLoadResticEnv();
    if ($env === null) {
        return ['success' => false, 'error' => 'Backup not configured'];
    }

    $cmd = [
        'restic', 'ls', $resolvedId, $fullPath, '--json',
    ];
    // Add repo and password args from env
    foreach ($env['args'] as $arg) {
        $cmd[] = $arg;
    }

    $proc = proc_open($cmd, [1 => ['pipe', 'w'], 2 => ['pipe', 'w']], $pipes, null, ! empty($env['env']) ? array_merge(getenv(), $env['env']) : null);
    $stdout = stream_get_contents($pipes[1]);
    $stderr = stream_get_contents($pipes[2]);
    fclose($pipes[1]);
    fclose($pipes[2]);
    $exitCode = proc_close($proc);

    if ($exitCode !== 0) {
        return ['success' => false, 'error' => trim($stderr ?: $stdout ?: 'restic ls failed')];
    }

    // Parse JSON lines output
    $items = [];
    $parentPath = rtrim($fullPath, '/');

    foreach (explode("\n", $stdout) as $line) {
        $line = trim($line);
        if ($line === '') {
            continue;
        }
        $entry = @json_decode($line, true);
        if (! $entry || ! isset($entry['path'])) {
            continue;
        }
        if (($entry['struct_type'] ?? '') === 'snapshot') {
            continue;
        }

        $entryPath = $entry['path'];
        // Skip the parent directory itself
        if ($entryPath === $parentPath || $entryPath === $parentPath . '/') {
            continue;
        }
        // Only show direct children (one level deep)
        $relative = ltrim(substr($entryPath, strlen($parentPath)), '/');
        if ($relative === '' || str_contains($relative, '/')) {
            continue;
        }

        $items[] = [
            'name' => basename($entryPath),
            'path' => ($path !== '' ? rtrim($path, '/') . '/' : '') . basename($entryPath),
            'is_dir' => ($entry['type'] ?? '') === 'dir',
            'size' => $entry['size'] ?? 0,
            'permissions' => $entry['permissions'] ?? '',
            'modified' => isset($entry['mtime']) ? strtotime($entry['mtime']) : 0,
        ];
    }

    // Sort: directories first, then alphabetical
    usort($items, function ($a, $b) {
        if ($a['is_dir'] !== $b['is_dir']) {
            return $b['is_dir'] <=> $a['is_dir'];
        }

        return strcasecmp($a['name'], $b['name']);
    });

    return ['success' => true, 'items' => $items, 'snapshot_id' => $resolvedId];
}

function jbSnapshotInventory(array $params): array
{
    $username = $params['username'] ?? '';
    $snapshotId = $params['snapshot_id'] ?? 'latest';

    if (! jbValidateUsername($username)) {
        return ['success' => false, 'error' => 'Invalid username'];
    }
    if (! jbValidateSnapshotId($snapshotId)) {
        return ['success' => false, 'error' => 'Invalid snapshot ID'];
    }

    $env = jbLoadResticEnv();
    if (! $env) {
        return ['success' => false, 'error' => 'Backup not configured'];
    }

    $procEnv = ! empty($env['env']) ? array_merge(getenv(), $env['env']) : null;

    // Resolve snapshot ID
    if ($snapshotId === 'latest') {
        $cmd = array_merge(['restic', 'snapshots', '--json', '--tag', 'account:' . $username], $env['args']);
        $proc = proc_open($cmd, [1 => ['pipe', 'w'], 2 => ['file', '/dev/null', 'w']], $pipes, null, $procEnv);
        $json = stream_get_contents($pipes[1]);
        fclose($pipes[1]);
        proc_close($proc);
        $snaps = json_decode($json ?: '[]', true);
        if (empty($snaps)) {
            return ['success' => false, 'error' => 'No snapshots found for ' . $username];
        }
        usort($snaps, fn ($a, $b) => strcmp($a['time'] ?? '', $b['time'] ?? ''));
        $snapshotId = end($snaps)['short_id'] ?? '';
    }

    // List all files in snapshot — stderr to /dev/null to prevent pipe deadlock
    $cmd = array_merge(['restic', 'ls', $snapshotId, '--tag', 'account:' . $username], $env['args']);
    $proc = proc_open($cmd, [1 => ['pipe', 'w'], 2 => ['file', '/dev/null', 'w']], $pipes, null, $procEnv);
    $listing = stream_get_contents($pipes[1]);
    fclose($pipes[1]);
    proc_close($proc);
    $lines = array_filter(explode("\n", $listing ?: ''));

    $staging = "/tmp/jabali-backup/{$username}/";
    $home = "/home/{$username}/";

    $inventory = [
        'metadata' => ['exists' => false],
        'files' => ['exists' => false, 'top_dirs' => []],
        'mysql' => ['exists' => false, 'databases' => [], 'users' => []],
        'postgres' => ['exists' => false, 'databases' => []],
        'dns' => ['exists' => false, 'zones' => []],
        'email' => ['exists' => false, 'domains' => [], 'mailboxes' => []],
        'nginx' => ['exists' => false, 'configs' => []],
        'php' => ['exists' => false, 'pools' => []],
        'ssl' => ['exists' => false, 'domains' => []],
        'cron' => ['exists' => false, 'jobs' => []],
        'wordpress' => ['exists' => false, 'sites' => []],
        'stalwart' => ['exists' => false, 'accounts' => []],
    ];

    $homeDirs = [];
    $homePaths = [];  // 2-level deep directory tree
    $sslDomains = [];

    foreach ($lines as $line) {
        $line = trim($line);
        if ($line === '' || str_starts_with($line, 'snapshot ')) {
            continue;
        }

        // Staging directory items
        if (str_starts_with($line, $staging)) {
            $rel = substr($line, strlen($staging));
            $parts = explode('/', $rel, 2);
            $component = $parts[0];
            $subpath = $parts[1] ?? '';

            switch ($component) {
                case 'metadata':
                    $inventory['metadata']['exists'] = true;
                    break;
                case 'mysql':
                    $inventory['mysql']['exists'] = true;
                    if (str_ends_with($subpath, '.sql.gz')) {
                        $inventory['mysql']['databases'][] = basename($subpath, '.sql.gz');
                    } elseif ($subpath === 'users.txt') {
                        $inventory['mysql']['has_users'] = true;
                    }
                    break;
                case 'postgres':
                    $inventory['postgres']['exists'] = true;
                    if (str_ends_with($subpath, '.dump')) {
                        $inventory['postgres']['databases'][] = basename($subpath, '.dump');
                    }
                    break;
                case 'dns':
                    $inventory['dns']['exists'] = true;
                    if (str_ends_with($subpath, '.json')) {
                        $inventory['dns']['zones'][] = basename($subpath, '.json');
                    }
                    break;
                case 'email':
                    $inventory['email']['exists'] = true;
                    if (str_starts_with($subpath, 'domains/') && str_ends_with($subpath, '.json')) {
                        $inventory['email']['domains'][] = basename($subpath, '.json');
                    }
                    break;
                case 'nginx':
                    $inventory['nginx']['exists'] = true;
                    if (str_ends_with($subpath, '.conf')) {
                        $inventory['nginx']['configs'][] = basename($subpath, '.conf');
                    }
                    break;
                case 'php':
                    $inventory['php']['exists'] = true;
                    if (str_ends_with($subpath, '.conf')) {
                        $inventory['php']['pools'][] = basename($subpath, '.conf');
                    }
                    break;
                case 'ssl':
                    $inventory['ssl']['exists'] = true;
                    if (str_contains($subpath, '/')) {
                        $d = explode('/', $subpath)[0];
                        if ($d !== '' && ! in_array($d, $sslDomains, true)) {
                            $sslDomains[] = $d;
                        }
                    }
                    break;
                case 'cron':
                    $inventory['cron']['exists'] = true;
                    if ($subpath !== '' && ! str_contains($subpath, '/')) {
                        $inventory['cron']['jobs'][] = $subpath;
                    }
                    break;
                case 'wordpress':
                    $inventory['wordpress']['exists'] = true;
                    if ($subpath !== '' && ! str_contains($subpath, '/')) {
                        $inventory['wordpress']['sites'][] = $subpath;
                    }
                    break;
                case 'stalwart':
                    $inventory['stalwart']['exists'] = true;
                    // Account directories are named user@domain
                    if (str_contains($subpath, '/')) {
                        $acct = explode('/', $subpath)[0];
                        if ($acct !== '' && str_contains($acct, '@') && ! in_array($acct, $inventory['stalwart']['accounts'], true)) {
                            $inventory['stalwart']['accounts'][] = $acct;
                        }
                    }
                    break;
            }
        }

        // Home directory — collect up to 2 levels deep
        if (str_starts_with($line, $home)) {
            $rel = substr($line, strlen($home));
            if ($rel === '') {
                continue;
            }
            $parts = explode('/', $rel);
            // Top-level entry
            if (count($parts) === 1) {
                $homeDirs[$rel] = true;
            }
            // 2-level path (e.g. domains/123123.com)
            if (count($parts) === 2 && $parts[1] !== '') {
                $homePaths[$parts[0] . '/' . $parts[1]] = true;
            }
        }
    }

    $inventory['files']['exists'] = ! empty($homeDirs);
    $inventory['files']['top_dirs'] = array_keys($homeDirs);
    sort($inventory['files']['top_dirs']);

    // Build tree: group 2-level paths under their parent
    $tree = [];
    foreach (array_keys($homeDirs) as $dir) {
        if (str_starts_with($dir, '.')) {
            continue;
        }
        $children = [];
        foreach (array_keys($homePaths) as $path) {
            if (str_starts_with($path, $dir . '/')) {
                $children[] = $path;
            }
        }
        sort($children);
        $tree[$dir] = $children;
    }
    $inventory['files']['tree'] = $tree;
    $inventory['ssl']['domains'] = $sslDomains;

    // Deduplicate arrays
    foreach (['mysql' => 'databases', 'postgres' => 'databases', 'dns' => 'zones', 'email' => 'domains', 'wordpress' => 'sites', 'stalwart' => 'accounts'] as $comp => $key) {
        $inventory[$comp][$key] = array_values(array_unique($inventory[$comp][$key]));
    }

    // Dump account.json for domain list
    // Use --quiet and discard stderr to prevent pipe deadlock when restic
    // writes progress to stderr before outputting file content on stdout.
    $dumpCmd = array_merge(
        ['restic', 'dump', '--quiet', $snapshotId],
        $env['args'],
        ["/tmp/jabali-backup/{$username}/metadata/account.json"],
    );
    $proc = proc_open($dumpCmd, [1 => ['pipe', 'w'], 2 => ['file', '/dev/null', 'w']], $pipes, null, $procEnv);
    $accountJson = stream_get_contents($pipes[1]);
    fclose($pipes[1]);
    proc_close($proc);
    $account = json_decode($accountJson ?: '{}', true);

    if (! empty($account)) {
        $inventory['metadata']['user'] = $account['user'] ?? [];
        $inventory['metadata']['domains'] = array_column($account['domains'] ?? [], 'domain');
    }

    // Fallback: if dump failed but DNS/nginx inventory found domains, use those
    if (empty($inventory['metadata']['domains'])) {
        $fallbackDomains = $inventory['dns']['zones'] ?? [];
        if (empty($fallbackDomains)) {
            $fallbackDomains = $inventory['nginx']['configs'] ?? [];
        }
        if (! empty($fallbackDomains)) {
            $inventory['metadata']['domains'] = $fallbackDomains;
        }
    }

    // Dump mysql/users.txt for MySQL user list
    if ($inventory['mysql']['has_users'] ?? false) {
        $usersCmd = array_merge(
            ['restic', 'dump', '--quiet', $snapshotId],
            $env['args'],
            ["/tmp/jabali-backup/{$username}/mysql/users.txt"],
        );
        $proc = proc_open($usersCmd, [1 => ['pipe', 'w'], 2 => ['file', '/dev/null', 'w']], $pipes, null, $procEnv);
        $usersTxt = stream_get_contents($pipes[1]);
        fclose($pipes[1]);
        proc_close($proc);
        $inventory['mysql']['users'] = array_values(array_filter(array_map('trim', explode("\n", $usersTxt ?: ''))));
    }

    return [
        'success' => true,
        'username' => $username,
        'snapshot_id' => $snapshotId,
        'inventory' => $inventory,
    ];
}

function jbDelete(array $params): array
{
    $snapshotId = $params['snapshot_id'] ?? '';
    if (! jbValidateSnapshotId($snapshotId) || $snapshotId === 'latest') {
        return ['success' => false, 'error' => 'Invalid snapshot ID'];
    }

    $env = jbLoadResticEnv();
    if ($env === null) {
        return ['success' => false, 'error' => 'Backup not configured'];
    }

    $cmd = ['restic', 'forget', $snapshotId, '--prune'];
    foreach ($env['args'] as $arg) {
        $cmd[] = $arg;
    }

    $envMap = ! empty($env['env']) ? array_merge(getenv(), $env['env']) : null;
    $proc = proc_open($cmd, [1 => ['pipe', 'w'], 2 => ['pipe', 'w']], $pipes, null, $envMap);
    $stdout = stream_get_contents($pipes[1]);
    $stderr = stream_get_contents($pipes[2]);
    fclose($pipes[1]);
    fclose($pipes[2]);
    $exitCode = proc_close($proc);

    if ($exitCode !== 0) {
        return ['success' => false, 'error' => trim($stderr ?: $stdout)];
    }

    return ['success' => true, 'message' => 'Snapshot deleted'];
}

function jbCheck(array $params): array
{
    $r = jbExec(['check'], 300);

    return [
        'success' => $r['exitCode'] === 0,
        'output' => $r['stdout'],
        'error' => $r['exitCode'] !== 0 ? ($r['stderr'] ?: $r['stdout']) : '',
    ];
}

function jbDoctor(array $params): array
{
    $r = jbExec(['doctor']);

    return [
        'success' => $r['exitCode'] === 0,
        'output' => $r['stdout'],
    ];
}

function jbConfigTest(array $params): array
{
    $r = jbExec(['config', 'test']);

    return [
        'success' => $r['exitCode'] === 0,
        'output' => $r['stdout'],
    ];
}

function jbInit(array $params): array
{
    $r = jbExec(['init']);

    return [
        'success' => $r['exitCode'] === 0,
        'output' => $r['stdout'],
        'error' => $r['exitCode'] !== 0 ? ($r['stderr'] ?: $r['stdout']) : '',
    ];
}

function jbForget(array $params): array
{
    $dryRun = $params['dry_run'] ?? false;
    $args = ['forget'];
    if ($dryRun) {
        $args[] = '--dry-run';
    }

    $r = jbExec($args, 300);

    return [
        'success' => $r['exitCode'] === 0,
        'output' => $r['stdout'],
        'error' => $r['exitCode'] !== 0 ? ($r['stderr'] ?: $r['stdout']) : '',
    ];
}

function jbDownload(array $params): array
{
    $accounts = $params['accounts'] ?? [];
    if (empty($accounts)) {
        return ['success' => false, 'error' => 'No accounts specified'];
    }

    $args = ['download'];
    foreach ($accounts as $acct) {
        if (jbValidateUsername($acct)) {
            $args[] = $acct;
        }
    }

    $only = $params['only'] ?? '';
    if ($only !== '') {
        $safe = jbValidateComponentList($only);
        if (! empty($safe)) {
            $args[] = '--only=' . implode(',', $safe);
        }
    }

    $snapshot = $params['snapshot'] ?? 'latest';
    if (! jbValidateSnapshotId($snapshot)) {
        return ['success' => false, 'error' => 'Invalid snapshot ID'];
    }
    $args[] = '--snapshot=' . $snapshot;

    // Generate output path with token for secure download
    $token = bin2hex(random_bytes(16));
    $outputFile = '/tmp/jabali-download-' . $token . '.tar.gz';
    $args[] = '--output=' . $outputFile;

    // Store download metadata with restricted permissions
    $meta = [
        'token' => $token,
        'file' => $outputFile,
        'accounts' => $accounts,
        'started' => time(),
        'expires' => time() + 3600,
    ];
    $metaFile = '/tmp/jabali-download-' . $token . '.json';
    file_put_contents($metaFile, json_encode($meta));
    chmod($metaFile, 0644);

    // Run in background, chmod the output so www-data can read it
    $cmd = implode(' ', array_map('escapeshellarg', array_merge([JABALI_BACKUP_BIN], $args)));
    $logFile = '/tmp/jabali-backup-' . bin2hex(random_bytes(4)) . '.log';
    $bg = sprintf('nohup bash -c %s > %s 2>&1; chmod 644 %s 2>/dev/null &',
        escapeshellarg($cmd),
        escapeshellarg($logFile),
        escapeshellarg($outputFile),
    );
    proc_open(['bash', '-c', $bg], [], $pipes);

    return [
        'success' => true,
        'token' => $token,
        'message' => 'Download started for ' . implode(', ', $accounts),
        'log' => $logFile,
    ];
}

function jbDownloadPipe(array $params): array
{
    // Support single username or comma-separated list
    $users = $params['users'] ?? $params['username'] ?? '';
    $snapshotId = $params['snapshot_id'] ?? 'latest';

    // Parse and validate usernames
    $userList = array_filter(array_map('trim', explode(',', $users)));
    $validUsers = [];
    foreach ($userList as $u) {
        if (jbValidateUsername($u)) {
            $validUsers[] = $u;
        }
    }
    if (empty($validUsers)) {
        return ['success' => false, 'error' => 'No valid usernames'];
    }
    if (! jbValidateSnapshotId($snapshotId)) {
        return ['success' => false, 'error' => 'Invalid snapshot ID'];
    }

    $pipeDir = '/tmp/jabali-exports';
    if (! is_dir($pipeDir)) {
        mkdir($pipeDir, 0755, true);
    }
    chmod($pipeDir, 0755);

    // Clean up old pipes (older than 10 minutes)
    foreach (glob($pipeDir . '/pipe-*') as $old) {
        if (filemtime($old) < time() - 600) {
            @unlink($old);
        }
    }

    $pipePath = $pipeDir . '/pipe-' . bin2hex(random_bytes(8));
    posix_mkfifo($pipePath, 0600);
    chmod($pipePath, 0644); // www-data needs to read it

    // Load restic env for this destination
    $env = jbLoadResticEnv();
    $envPrefix = '';
    foreach (['RESTIC_REPOSITORY', 'RESTIC_PASSWORD_FILE', 'SSHPASS'] as $var) {
        if (! empty($env[$var])) {
            $envPrefix .= sprintf('export %s=%s; ', $var, escapeshellarg($env[$var]));
        }
    }

    // Build username args for CLI (supports multiple)
    $userArgs = implode(' ', array_map('escapeshellarg', $validUsers));

    // Fully detach: jabali-backup download writes tar.gz to the pipe, then removes it
    $cmd = sprintf(
        "nohup bash -c '%s/usr/local/bin/jabali-backup download %s --snapshot=%s --output=- > %s; rm -f %s' > /dev/null 2>&1 &",
        $envPrefix,
        $userArgs,
        escapeshellarg($snapshotId),
        escapeshellarg($pipePath),
        escapeshellarg($pipePath),
    );
    proc_open(['bash', '-c', $cmd], [], $pipes);

    return ['success' => true, 'pipe' => $pipePath, 'users' => $validUsers];
}

function jbDownloadStatus(array $params): array
{
    $token = $params['token'] ?? '';
    if ($token === '' || ! preg_match('/^[a-f0-9]{32}$/', $token)) {
        return ['success' => false, 'error' => 'Invalid token'];
    }

    $metaFile = '/tmp/jabali-download-' . $token . '.json';
    if (! file_exists($metaFile)) {
        return ['success' => false, 'error' => 'Download not found'];
    }

    $meta = json_decode(file_get_contents($metaFile), true);
    $file = $meta['file'] ?? '';
    $ready = file_exists($file) && filesize($file) > 0;

    // Check expiry
    if (time() > ($meta['expires'] ?? 0)) {
        @unlink($file);
        @unlink($metaFile);

        return ['success' => false, 'error' => 'Download expired'];
    }

    $size = $ready ? filesize($file) : 0;

    return [
        'success' => true,
        'ready' => $ready,
        'size' => $size,
        'accounts' => $meta['accounts'] ?? [],
        'token' => $token,
        'file' => $ready ? basename($file) : null,
    ];
}

// ─── Server Backup / Restore Routes ───

function jbServerBackup(array $params): array
{
    $args = ['server-backup'];
    if (! empty($params['skip_users'])) {
        $args[] = '--skip-users';
    }
    $dest = $params['destination'] ?? '';
    if ($dest !== '' && jbValidateDestinationId($dest)) {
        $args[] = '--destination=' . $dest;
    }

    // Run in background — server backup can take a long time
    $logFile = '/tmp/jabali-server-backup-' . date('YmdHis') . '.log';
    $cmd = sprintf(
        'nohup /usr/local/bin/jabali-backup %s > %s 2>&1 &',
        implode(' ', array_map('escapeshellarg', $args)),
        escapeshellarg($logFile)
    );
    proc_open(['bash', '-c', $cmd], [], $pipes);

    return ['success' => true, 'message' => 'Server backup started', 'log' => $logFile];
}

function jbServerSnapshots(array $params): array
{
    $r = jbExec(['list', 'snapshots', '--type=server', '--json']);

    // The CLI outputs restic's table format, parse it
    $stdout = trim($r['stdout'] ?? '');
    if ($r['exitCode'] !== 0) {
        return ['success' => false, 'error' => $r['stderr'] ?: $r['stdout']];
    }

    // Parse restic snapshot table output into structured data
    $snapshots = [];
    $lines = explode("\n", $stdout);
    foreach ($lines as $line) {
        if (preg_match('/^(\w{8})\s+(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\s+(\S+)\s+(.+?)\s{2,}(.+?)\s{2,}(.+)$/', trim($line), $m)) {
            $snapshots[] = [
                'id' => $m[1],
                'time' => $m[2],
                'host' => $m[3],
                'tags' => $m[4],
                'paths' => trim($m[5]),
                'size' => trim($m[6]),
            ];
        }
    }

    return ['success' => true, 'snapshots' => $snapshots, 'raw' => $stdout];
}

function jbServerRestore(array $params): array
{
    $snapshot = $params['snapshot'] ?? 'latest';
    if (! preg_match('/^[a-f0-9]+$|^latest$/', $snapshot)) {
        return ['success' => false, 'error' => 'Invalid snapshot ID'];
    }

    $args = ['server-restore', '--snapshot=' . $snapshot];
    if (! empty($params['skip_users'])) {
        $args[] = '--skip-users';
    }
    if (! empty($params['force'])) {
        $args[] = '--force';
    }

    // Run in background — server restore can take a very long time
    $logFile = '/tmp/jabali-server-restore-' . date('YmdHis') . '.log';
    $cmd = sprintf(
        'nohup /usr/local/bin/jabali-backup %s > %s 2>&1 &',
        implode(' ', array_map('escapeshellarg', $args)),
        escapeshellarg($logFile)
    );
    proc_open(['bash', '-c', $cmd], [], $pipes);

    return ['success' => true, 'message' => 'Server restore started', 'log' => $logFile];
}

function jbServerDownloadPipe(array $params): array
{
    $snapshotId = $params['snapshot_id'] ?? 'latest';
    if (! preg_match('/^[a-f0-9]+$|^latest$/', $snapshotId)) {
        return ['success' => false, 'error' => 'Invalid snapshot ID'];
    }

    // Resolve 'latest' to actual server snapshot ID
    if ($snapshotId === 'latest') {
        $r = jbExec(['list', 'snapshots', '--type=server']);
        if (preg_match_all('/^([a-f0-9]{8})\s/m', $r['stdout'] ?? '', $matches)) {
            $snapshotId = end($matches[1]);
        }
        if (! $snapshotId || $snapshotId === 'latest') {
            return ['success' => false, 'error' => 'No server snapshots found'];
        }
    }

    // Collect the latest per-user snapshot IDs to include in the archive
    $userSnapshotIds = [];
    $r = jbExec(['list', 'snapshots']);
    if (preg_match_all('/^([a-f0-9]{8})\s+(\d{4}-\d{2}-\d{2}\s\d{2}:\d{2}:\d{2})\s+\S+\s+(.*?)\s{2,}/m', $r['stdout'] ?? '', $matches, PREG_SET_ORDER)) {
        $byUser = [];
        foreach ($matches as $m) {
            $id = $m[1];
            $time = $m[2];
            $tags = $m[3];
            if (preg_match('/account:([^,\s]+)/', $tags, $tm)) {
                $user = $tm[1];
                if (! isset($byUser[$user]) || $time > $byUser[$user]['time']) {
                    $byUser[$user] = ['id' => $id, 'time' => $time];
                }
            }
        }
        $userSnapshotIds = array_map(fn ($u) => $u['id'], $byUser);
    }

    // Create named pipe
    $pipeDir = '/tmp/jabali-exports';
    @mkdir($pipeDir, 0700, true);
    $pipeName = 'pipe-' . bin2hex(random_bytes(8));
    $pipePath = $pipeDir . '/' . $pipeName;
    posix_mkfifo($pipePath, 0600);

    // Build restore + tar script: restore server snapshot + all user snapshots
    // into a temp dir, then tar.gz the result into the pipe
    $tmpDir = '/tmp/jabali-server-export-' . bin2hex(random_bytes(4));
    $snapshotIdEsc = escapeshellarg($snapshotId);
    $pipePathEsc = escapeshellarg($pipePath);
    $tmpDirEsc = escapeshellarg($tmpDir);

    $restoreCmds = "mkdir -p {$tmpDirEsc}\n";
    $restoreCmds .= "jabali-backup restore-raw --snapshot={$snapshotIdEsc} --target={$tmpDirEsc}/server 2>/dev/null\n";
    foreach ($userSnapshotIds as $user => $uid) {
        $uidEsc = escapeshellarg($uid);
        $userEsc = escapeshellarg($user);
        $restoreCmds .= "jabali-backup restore-raw --snapshot={$uidEsc} --target={$tmpDirEsc}/users/{$userEsc} 2>/dev/null\n";
    }
    $restoreCmds .= "tar czf - -C {$tmpDirEsc} . > {$pipePathEsc}\n";
    $restoreCmds .= "rm -rf {$tmpDirEsc} {$pipePathEsc}\n";

    // Use restic restore directly instead of jabali-backup restore-raw (which may not exist)
    $env = jbLoadResticEnv();
    if ($env === null) {
        @unlink($pipePath);
        return ['success' => false, 'error' => 'Backup not configured'];
    }

    $resticArgs = implode(' ', array_map('escapeshellarg', $env['args']));
    $envExports = '';
    foreach ($env['env'] as $k => $v) {
        $envExports .= 'export ' . escapeshellarg($k) . '=' . escapeshellarg($v) . '; ';
    }

    $script = "set -e\n{$envExports}\nmkdir -p {$tmpDirEsc}\n";
    $script .= "restic restore {$snapshotIdEsc} --target {$tmpDirEsc}/server {$resticArgs} 2>/dev/null\n";
    foreach ($userSnapshotIds as $user => $uid) {
        $uidEsc = escapeshellarg($uid);
        $userEsc = escapeshellarg($user);
        $script .= "restic restore {$uidEsc} --target {$tmpDirEsc}/users/{$userEsc} {$resticArgs} 2>/dev/null\n";
    }
    $script .= "tar czf - -C {$tmpDirEsc} . > {$pipePathEsc}\n";
    $script .= "rm -rf {$tmpDirEsc} {$pipePathEsc}\n";

    $cmd = sprintf('nohup bash -c %s > /dev/null 2>&1 &', escapeshellarg($script));
    proc_open(['bash', '-c', $cmd], [], $pipes);

    usleep(200000);

    return ['success' => true, 'pipe' => $pipePath];
}

// ─── Schedule Routes (wrap CLI) ───

function jbSchedulesList(array $params): array
{
    $r = jbExec(['schedule', 'list', '--json']);
    if ($r['exitCode'] !== 0) {
        return ['success' => false, 'error' => $r['stderr'] ?: $r['stdout']];
    }

    $schedules = @json_decode($r['stdout'], true);
    if (! is_array($schedules)) {
        return ['success' => false, 'error' => 'Failed to parse schedules'];
    }

    return ['success' => true, 'schedules' => $schedules];
}

function jbSchedulesAdd(array $params): array
{
    $args = ['schedule', 'add'];

    $name = $params['name'] ?? '';
    if ($name !== '' && jbValidateName($name)) {
        $args[] = '--name=' . $name;
    }

    $dest = $params['destination'] ?? '';
    if ($dest !== '' && jbValidateDestinationId($dest)) {
        $args[] = '--destination=' . $dest;
    }

    $cron = $params['cron'] ?? '';
    if ($cron !== '' && jbValidateCron($cron)) {
        $args[] = '--cron=' . $cron;
    }

    // Validate account lists
    foreach (['accounts', 'exclude'] as $field) {
        $val = $params[$field] ?? '';
        if ($val !== '' && $val !== 'all') {
            $safe = [];
            foreach (explode(',', $val) as $acct) {
                if (jbValidateUsername(trim($acct))) {
                    $safe[] = trim($acct);
                }
            }
            if (! empty($safe)) {
                $args[] = '--' . $field . '=' . implode(',', $safe);
            }
        } elseif ($val === 'all') {
            $args[] = '--' . $field . '=all';
        }
    }

    // Numeric retention values
    foreach (['keep_last', 'keep_daily', 'keep_weekly', 'keep_monthly'] as $key) {
        $val = $params[$key] ?? '';
        if ($val !== '' && is_numeric($val)) {
            $args[] = '--' . str_replace('_', '-', $key) . '=' . (int) $val;
        }
    }

    // Server backup flag
    if (! empty($params['server_backup'])) {
        $args[] = '--server-backup';
    }

    $r = jbExec($args);

    return [
        'success' => $r['exitCode'] === 0,
        'output' => $r['stdout'],
        'error' => $r['exitCode'] !== 0 ? ($r['stderr'] ?: $r['stdout']) : '',
    ];
}

function jbSchedulesUpdate(array $params): array
{
    $id = $params['id'] ?? '';
    if ($id === '' || ! preg_match('/^[a-z0-9-]+$/', $id)) {
        return ['success' => false, 'error' => 'Invalid schedule ID'];
    }

    $args = ['schedule', 'update', '--id=' . $id];

    $name = $params['name'] ?? '';
    if ($name !== '' && jbValidateName($name)) {
        $args[] = '--name=' . $name;
    }

    $dest = $params['destination'] ?? '';
    if ($dest !== '' && jbValidateDestinationId($dest)) {
        $args[] = '--destination=' . $dest;
    }

    $cron = $params['cron'] ?? '';
    if ($cron !== '' && jbValidateCron($cron)) {
        $args[] = '--cron=' . $cron;
    }

    foreach (['accounts', 'exclude'] as $field) {
        $val = $params[$field] ?? '';
        if ($val !== '' && $val !== 'all') {
            $safe = [];
            foreach (explode(',', $val) as $acct) {
                if (jbValidateUsername(trim($acct))) {
                    $safe[] = trim($acct);
                }
            }
            if (! empty($safe)) {
                $args[] = '--' . $field . '=' . implode(',', $safe);
            }
        } elseif ($val === 'all') {
            $args[] = '--' . $field . '=all';
        }
    }

    foreach (['keep_last', 'keep_daily', 'keep_weekly', 'keep_monthly'] as $key) {
        $val = $params[$key] ?? '';
        if ($val !== '' && is_numeric($val)) {
            $args[] = '--' . str_replace('_', '-', $key) . '=' . (int) $val;
        }
    }

    // Server backup flag
    if (isset($params['server_backup'])) {
        $args[] = $params['server_backup'] ? '--server-backup' : '--no-server-backup';
    }

    $r = jbExec($args);

    return [
        'success' => $r['exitCode'] === 0,
        'output' => $r['stdout'],
        'error' => $r['exitCode'] !== 0 ? ($r['stderr'] ?: $r['stdout']) : '',
    ];
}

function jbSchedulesRemove(array $params): array
{
    $id = $params['id'] ?? '';
    if ($id === '' || ! preg_match('/^[a-z0-9-]+$/', $id)) {
        return ['success' => false, 'error' => 'Invalid schedule ID'];
    }

    $r = jbExec(['schedule', 'remove', '--id=' . $id]);

    return [
        'success' => $r['exitCode'] === 0,
        'output' => $r['stdout'],
        'error' => $r['exitCode'] !== 0 ? ($r['stderr'] ?: $r['stdout']) : '',
    ];
}

function jbSchedulesEnable(array $params): array
{
    $id = $params['id'] ?? '';
    if ($id === '' || ! preg_match('/^[a-z0-9-]+$/', $id)) {
        return ['success' => false, 'error' => 'Invalid schedule ID'];
    }

    $r = jbExec(['schedule', 'enable', '--id=' . $id]);

    return [
        'success' => $r['exitCode'] === 0,
        'output' => $r['stdout'],
        'error' => $r['exitCode'] !== 0 ? ($r['stderr'] ?: $r['stdout']) : '',
    ];
}

function jbSchedulesDisable(array $params): array
{
    $id = $params['id'] ?? '';
    if ($id === '' || ! preg_match('/^[a-z0-9-]+$/', $id)) {
        return ['success' => false, 'error' => 'Invalid schedule ID'];
    }

    $r = jbExec(['schedule', 'disable', '--id=' . $id]);

    return [
        'success' => $r['exitCode'] === 0,
        'output' => $r['stdout'],
        'error' => $r['exitCode'] !== 0 ? ($r['stderr'] ?: $r['stdout']) : '',
    ];
}

function jbReadFile(array $params): array
{
    $username = $params['username'] ?? '';
    $snapshotId = $params['snapshot_id'] ?? 'latest';
    $path = $params['path'] ?? '';

    if (! jbValidateUsername($username)) {
        return ['success' => false, 'error' => 'Invalid username'];
    }
    if (! jbValidateSnapshotId($snapshotId)) {
        return ['success' => false, 'error' => 'Invalid snapshot ID'];
    }

    if (strpos($path, '..') !== false || strpos($path, "\0") !== false) {
        return ['success' => false, 'error' => 'Invalid path'];
    }
    if ($path === '') {
        return ['success' => false, 'error' => 'Path is required'];
    }

    // Resolve snapshot if latest
    $resolvedId = $snapshotId;
    if ($snapshotId === 'latest') {
        $listResult = jbExec(['list', 'snapshots', '--user=' . $username]);
        if (preg_match_all('/^([a-f0-9]{8})\s/m', $listResult['stdout'], $matches)) {
            $resolvedId = end($matches[1]);
        } else {
            return ['success' => false, 'error' => 'No snapshots found'];
        }
    }

    $fullPath = '/home/' . $username . '/' . ltrim($path, '/');

    $env = jbLoadResticEnv();
    if ($env === null) {
        return ['success' => false, 'error' => 'Backup not configured'];
    }

    $cmd = ['restic', 'dump', $resolvedId, $fullPath];
    foreach ($env['args'] as $arg) {
        $cmd[] = $arg;
    }

    $proc = proc_open($cmd, [1 => ['pipe', 'w'], 2 => ['pipe', 'w']], $pipes, null, ! empty($env['env']) ? array_merge(getenv(), $env['env']) : null);
    $stdout = stream_get_contents($pipes[1], 1048576); // 1MB limit
    $stderr = stream_get_contents($pipes[2]);
    fclose($pipes[1]);
    fclose($pipes[2]);
    $exitCode = proc_close($proc);

    if ($exitCode !== 0) {
        return ['success' => false, 'error' => trim($stderr ?: 'Failed to read file')];
    }

    return ['success' => true, 'content' => $stdout];
}

function jbLogs(array $params): array
{
    $lines = (int) ($params['lines'] ?? 500);
    $lines = max(10, min($lines, 2000));

    $logFile = '/var/log/jabali-backup.log';

    // Try reading from config
    $configFile = '/etc/jabali-backup/config.conf';
    if (file_exists($configFile)) {
        $inSection = false;
        foreach (file($configFile) as $line) {
            $line = trim($line);
            if ($line === '[logging]') { $inSection = true; continue; }
            if (str_starts_with($line, '[')) { $inSection = false; }
            if ($inSection && str_starts_with($line, 'file=')) { $logFile = substr($line, 5); }
        }
    }

    if (! file_exists($logFile)) {
        return ['success' => true, 'jobs' => [], 'log_file' => $logFile];
    }

    // Read last N lines
    $cmd = ['tail', '-n', (string) $lines, $logFile];
    $proc = proc_open($cmd, [1 => ['pipe', 'w'], 2 => ['pipe', 'w']], $pipes);
    $stdout = stream_get_contents($pipes[1]);
    fclose($pipes[1]);
    fclose($pipes[2]);
    proc_close($proc);

    $logLines = explode("\n", trim($stdout ?: ''));

    // Parse into jobs — each "Backup run:" or "Restore" starts a new job
    $jobs = [];
    $currentJob = null;
    $currentAccount = null;

    foreach ($logLines as $line) {
        $line = trim($line);
        if ($line === '') {
            continue;
        }

        // Extract timestamp
        $ts = '';
        if (preg_match('/^\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\]/', $line, $m)) {
            $ts = $m[1];
        }

        // New server backup (disaster recovery)
        if (preg_match('/Server backup \(disaster recovery\) starting/', $line)) {
            if ($currentJob !== null) {
                if ($currentJob['status'] === 'running') {
                    $currentJob['status'] = $currentJob['errors'] > 0 ? 'partial' : 'success';
                }
                $jobs[] = $currentJob;
            }
            $currentJob = [
                'id' => 'server-' . ($ts ? str_replace(['-', ' ', ':'], '', $ts) : uniqid()),
                'type' => 'server-backup',
                'started_at' => $ts,
                'ended_at' => $ts,
                'accounts' => [],
                'status' => 'running',
                'errors' => 0,
                'events' => [],
                'log' => [],
            ];
            $currentAccount = null;
        }

        // New backup run
        if (preg_match('/Backup run: (run-\S+)/', $line, $m)) {
            // If inside a server-backup, this is a sub-run — don't create a new job
            if ($currentJob !== null && $currentJob['type'] === 'server-backup') {
                // Let it continue in the server-backup job
            } else {
                if ($currentJob !== null) {
                    $jobs[] = $currentJob;
                }
                $currentJob = [
                    'id' => $m[1],
                    'type' => 'backup',
                    'started_at' => $ts,
                    'ended_at' => $ts,
                    'accounts' => [],
                    'status' => 'running',
                    'errors' => 0,
                    'events' => [],
                    'log' => [],
                ];
                $currentAccount = null;
            }
        }

        // New restore run — close any previous job first
        if (preg_match('/═══ Restoring account: (\S+)/', $line, $m)) {
            // Only start a new restore job if we're not already inside one
            $isNewRestore = $currentJob === null || $currentJob['type'] !== 'restore';
            if ($isNewRestore) {
                if ($currentJob !== null) {
                    if ($currentJob['status'] === 'running') {
                        $currentJob['status'] = $currentJob['errors'] > 0 ? 'partial' : 'success';
                    }
                    $jobs[] = $currentJob;
                }
                $currentJob = [
                    'id' => 'restore-' . ($ts ? str_replace(['-', ' ', ':'], '', $ts) : uniqid()),
                    'type' => 'restore',
                    'started_at' => $ts,
                    'ended_at' => $ts,
                    'accounts' => [],
                    'status' => 'running',
                    'errors' => 0,
                    'events' => [],
                    'log' => [],
                ];
            }
        }

        // File-level restore (standalone, no account wrapper)
        if (preg_match('/File-level restore: (\d+) path/', $line, $m) && $currentJob === null) {
            $currentJob = [
                'id' => 'file-restore-' . ($ts ? str_replace(['-', ' ', ':'], '', $ts) : uniqid()),
                'type' => 'file-restore',
                'started_at' => $ts,
                'ended_at' => $ts,
                'accounts' => [],
                'status' => 'success',
                'errors' => 0,
                'events' => [['level' => 'info', 'text' => 'Restored ' . $m[1] . ' file path(s)']],
                'log' => [],
            ];
        }

        if ($currentJob === null) {
            continue;
        }

        // Track end time
        if ($ts !== '') {
            $currentJob['ended_at'] = $ts;
        }

        // Account start
        if (preg_match('/═══ Backing up account: (\S+)/', $line, $m)) {
            $currentAccount = $m[1];
            if (! in_array($currentAccount, $currentJob['accounts'], true)) {
                $currentJob['accounts'][] = $currentAccount;
            }
            $currentJob['events'][] = ['level' => 'heading', 'text' => $currentAccount];
        }
        if (preg_match('/═══ Restoring account: (\S+)/', $line, $m)) {
            $currentAccount = $m[1];
            if (! in_array($currentAccount, $currentJob['accounts'], true)) {
                $currentJob['accounts'][] = $currentAccount;
            }
            $currentJob['events'][] = ['level' => 'heading', 'text' => $currentAccount];
        }
        if (preg_match('/Resolved latest snapshot for (\S+): (\S+)/', $line, $m)) {
            if (! in_array($m[1], $currentJob['accounts'], true)) {
                $currentJob['accounts'][] = $m[1];
            }
        }

        // Translate log lines into human events
        jbLogTranslateEvent($currentJob, $line);

        // Errors
        if (str_contains($line, '[ERROR]') || str_contains($line, 'FAILED')) {
            $currentJob['errors']++;
        }

        // Job completed markers
        if (str_contains($line, 'Applying retention policy')) {
            $currentJob['events'][] = ['level' => 'info', 'text' => 'Retention policy applied'];
            $currentJob['status'] = $currentJob['errors'] > 0 ? 'partial' : 'success';
        }
        if (preg_match('/═══ (Backup|Restore) complete for/', $line)) {
            $currentJob['status'] = $currentJob['errors'] > 0 ? 'partial' : 'success';
        }
        if (preg_match('/═══ Server backup complete/', $line)) {
            $currentJob['status'] = $currentJob['errors'] > 0 ? 'partial' : 'success';
        }
        if (preg_match('/Backup FAILED|Fatal|Aborted/', $line)) {
            $currentJob['status'] = 'failed';
        }

        $currentJob['log'][] = $line;
    }

    if ($currentJob !== null) {
        // If no completion marker was found and it has accounts, assume it finished
        if ($currentJob['status'] === 'running' && ! empty($currentJob['accounts'])) {
            $currentJob['status'] = $currentJob['errors'] > 0 ? 'partial' : 'success';
        }
        $jobs[] = $currentJob;
    }

    // Most recent first
    $jobs = array_reverse($jobs);

    // Calculate duration for each job
    foreach ($jobs as &$job) {
        if ($job['started_at'] && $job['ended_at'] && $job['started_at'] !== $job['ended_at']) {
            $start = strtotime($job['started_at']);
            $end = strtotime($job['ended_at']);
            if ($start && $end) {
                $job['duration_seconds'] = $end - $start;
            }
        }
    }
    unset($job);

    return ['success' => true, 'jobs' => $jobs, 'log_file' => $logFile];
}

/**
 * Translate a raw log line into a human-readable event.
 * Appends to $job['events'] when a line matches a known pattern.
 */
function jbLogTranslateEvent(array &$job, string $line): void
{
    // Backup collectors
    if (preg_match('/files: Added \/home\/\S+ to backup paths/', $line)) {
        $job['events'][] = ['level' => 'success', 'text' => 'Home directory files collected'];
    } elseif (preg_match('/mysql: Backed up (\d+) database/', $line, $m)) {
        $job['events'][] = ['level' => 'success', 'text' => $m[1] . ' database(s) backed up'];
    } elseif (preg_match('/mysql: No databases found/', $line)) {
        $job['events'][] = ['level' => 'skip', 'text' => 'No MySQL databases'];
    } elseif (preg_match('/mysql: Dumping database (\S+)/', $line, $m)) {
        $job['events'][] = ['level' => 'info', 'text' => 'Dumping database ' . $m[1]];
    } elseif (preg_match('/postgres: psql not installed/', $line)) {
        $job['events'][] = ['level' => 'skip', 'text' => 'PostgreSQL not installed'];
    } elseif (preg_match('/dns: Exported (\d+) zones/', $line, $m)) {
        $job['events'][] = ['level' => 'success', 'text' => $m[1] . ' DNS zone(s) exported'];
    } elseif (preg_match('/dns: Exported (\S+) \((\d+) records\)/', $line, $m)) {
        $job['events'][] = ['level' => 'info', 'text' => $m[1] . ': ' . $m[2] . ' DNS records'];
    } elseif (preg_match('/email: Exported domain (\S+)/', $line, $m)) {
        $job['events'][] = ['level' => 'info', 'text' => 'Email domain: ' . $m[1]];
    } elseif (preg_match('/email: Exported mailbox (\S+)/', $line, $m)) {
        $job['events'][] = ['level' => 'info', 'text' => 'Mailbox: ' . $m[1]];
    } elseif (preg_match('/email: Skipping maildir/', $line)) {
        $job['events'][] = ['level' => 'skip', 'text' => 'Mail files handled by Stalwart'];
    } elseif (preg_match('/ssl: Exported (\d+) certificate/', $line, $m)) {
        $job['events'][] = ['level' => 'success', 'text' => $m[1] . ' SSL certificate(s) exported'];
    } elseif (preg_match('/nginx: Exported (\d+) domain config/', $line, $m)) {
        $job['events'][] = ['level' => 'success', 'text' => $m[1] . ' Nginx config(s) exported'];
    } elseif (preg_match('/php: Exported (\d+) pool/', $line, $m)) {
        $job['events'][] = ['level' => 'success', 'text' => $m[1] . ' PHP pool(s) exported'];
    } elseif (preg_match('/wordpress: Exported (\d+) WordPress/', $line, $m)) {
        $job['events'][] = ['level' => 'success', 'text' => $m[1] . ' WordPress site(s) exported'];
    } elseif (preg_match('/metadata: Exported account metadata/', $line)) {
        $job['events'][] = ['level' => 'success', 'text' => 'Account metadata saved'];
    } elseif (preg_match('/stalwart: Exported account (\S+)/', $line, $m)) {
        $job['events'][] = ['level' => 'success', 'text' => 'Stalwart mail: ' . $m[1]];

    // Restore events
    } elseif (preg_match('/restore\/metadata: User (\S+) exists/', $line, $m)) {
        $job['events'][] = ['level' => 'info', 'text' => 'User ' . $m[1] . ' already exists'];
    } elseif (preg_match('/restore\/metadata: Created user (\S+)/', $line, $m)) {
        $job['events'][] = ['level' => 'success', 'text' => 'Created user ' . $m[1]];
    } elseif (preg_match('/restore\/metadata: Domain (\S+) exists, skipping/', $line, $m)) {
        $job['events'][] = ['level' => 'skip', 'text' => 'Domain ' . $m[1] . ' exists, skipped'];
    } elseif (preg_match('/restore\/dns: Restored (\d+) records for (\S+)/', $line, $m)) {
        $job['events'][] = ['level' => $m[1] === '0' ? 'skip' : 'success', 'text' => $m[2] . ': ' . $m[1] . ' DNS records restored'];
    } elseif (preg_match('/restore\/ssl: (Updated|Created) (\S+) cert/', $line, $m)) {
        $job['events'][] = ['level' => 'success', 'text' => 'SSL ' . strtolower($m[1]) . ': ' . $m[2]];
    } elseif (preg_match('/restore\/ssl: (\S+) cert exists, skipping/', $line, $m)) {
        $job['events'][] = ['level' => 'skip', 'text' => 'SSL ' . $m[1] . ' exists, skipped'];
    } elseif (preg_match('/restore\/nginx: Restored (\S+\.conf)/', $line, $m)) {
        $job['events'][] = ['level' => 'success', 'text' => 'Nginx config: ' . $m[1]];
    } elseif (preg_match('/restore\/nginx: (\S+\.conf) exists, skipping/', $line, $m)) {
        $job['events'][] = ['level' => 'skip', 'text' => 'Nginx ' . $m[1] . ' exists, skipped'];
    } elseif (preg_match('/restore\/nginx: nginx config test failed/', $line)) {
        $job['events'][] = ['level' => 'warn', 'text' => 'Nginx config test failed, reload skipped'];
    } elseif (preg_match('/restore\/php: Restored \S+ → (\S+)/', $line, $m)) {
        $job['events'][] = ['level' => 'success', 'text' => 'PHP pool: ' . basename($m[1])];
    } elseif (preg_match('/restore\/php: (\S+\.conf) exists, skipping/', $line, $m)) {
        $job['events'][] = ['level' => 'skip', 'text' => 'PHP pool ' . $m[1] . ' exists, skipped'];
    } elseif (preg_match('/restore\/mysql: Imported (\S+) → (\S+)/', $line, $m)) {
        $label = $m[1] === $m[2] ? $m[1] : $m[1] . ' → ' . $m[2];
        $job['events'][] = ['level' => 'success', 'text' => 'Database restored: ' . $label];
    } elseif (preg_match('/restore\/mysql: (\S+) exists, restoring as (\S+)/', $line, $m)) {
        $job['events'][] = ['level' => 'warn', 'text' => $m[1] . ' exists, saved as ' . $m[2]];
    } elseif (preg_match('/restore\/mysql: Dropping and recreating (\S+)/', $line, $m)) {
        $job['events'][] = ['level' => 'info', 'text' => 'Overwriting database ' . $m[1]];
    } elseif (preg_match('/restore\/files: Restored \/home\//', $line)) {
        $job['events'][] = ['level' => 'success', 'text' => 'Home directory files restored'];
    } elseif (preg_match('/File-level restore: (\d+) path/', $line, $m)) {
        $job['events'][] = ['level' => 'info', 'text' => 'Restoring ' . $m[1] . ' selected file path(s)'];

    // Errors
    } elseif (str_contains($line, '[ERROR]')) {
        $msg = preg_replace('/^\[.*?\]\s*\[ERROR\]\s*/', '', $line);
        $job['events'][] = ['level' => 'error', 'text' => $msg];
    } elseif (str_contains($line, '[WARN]') && ! str_contains($line, 'nginx config test failed')) {
        $msg = preg_replace('/^\[.*?\]\s*\[WARN\]\s*/', '', $line);
        $job['events'][] = ['level' => 'warn', 'text' => $msg];
    } elseif (preg_match('/═══ Backup FAILED for (\S+)/', $line, $m)) {
        $job['events'][] = ['level' => 'error', 'text' => 'Backup failed for ' . $m[1]];
    } elseif (preg_match('/═══ Backup complete for (\S+)/', $line, $m)) {
        $job['events'][] = ['level' => 'success', 'text' => 'Backup complete for ' . $m[1]];
    } elseif (preg_match('/═══ Restore complete for (\S+)/', $line, $m)) {
        $job['events'][] = ['level' => 'success', 'text' => 'Restore complete for ' . $m[1]];
    } elseif (preg_match('/═══ Server backup complete/', $line)) {
        $job['events'][] = ['level' => 'success', 'text' => 'Server backup complete'];

    // Server collector events
    } elseif (preg_match('/server: Dumped (jabali|powerdns) database/', $line, $m)) {
        $job['events'][] = ['level' => 'success', 'text' => 'Database: ' . $m[1]];
    } elseif (preg_match('/server: Backed up (.+)/', $line, $m)) {
        $job['events'][] = ['level' => 'success', 'text' => $m[1]];
    } elseif (preg_match('/server: Exported (.+)/', $line, $m)) {
        $job['events'][] = ['level' => 'info', 'text' => $m[1]];
    } elseif (preg_match('/server: Generated server manifest/', $line)) {
        $job['events'][] = ['level' => 'success', 'text' => 'Server manifest generated'];
    } elseif (str_contains($line, 'Server-level backup complete')) {
        $job['events'][] = ['level' => 'success', 'text' => 'Server config snapshot saved'];
    } elseif (str_contains($line, 'Starting per-user backups')) {
        $job['events'][] = ['level' => 'heading', 'text' => 'Per-user backups'];
    }
}

// ─── Destination Routes (wrap CLI) ───

function jbDestinationsList(array $params): array
{
    $r = jbExec(['destination', 'list', '--json']);
    if ($r['exitCode'] !== 0) {
        return ['success' => false, 'error' => $r['stderr'] ?: $r['stdout']];
    }

    $destinations = @json_decode($r['stdout'], true);
    if (! is_array($destinations)) {
        return ['success' => false, 'error' => 'Failed to parse destinations'];
    }

    // Read encryption key values from credential files
    foreach ($destinations as &$dest) {
        $pwdFile = $dest['credentials']['restic_password_file'] ?? '';
        $dest['encryption_key'] = ($pwdFile !== '' && is_file($pwdFile))
            ? trim((string) file_get_contents($pwdFile))
            : '';
    }
    unset($dest);

    return ['success' => true, 'destinations' => $destinations];
}

function jbDestinationsAdd(array $params): array
{
    // Pre-validate required fields before calling CLI
    if (empty($params['name'])) {
        return ['success' => false, 'error' => 'Name is required'];
    }
    if (empty($params['type'])) {
        return ['success' => false, 'error' => 'Type is required'];
    }

    $args = ['destination', 'add'];

    if (! empty($params['no_encryption'])) {
        $args[] = '--no-encryption';
    }

    // Validated fields with specific rules
    $nameTypeFields = ['name', 'type', 'user', 'bucket', 'region', 'remote', 'account_name'];
    foreach ($nameTypeFields as $key) {
        $val = $params[$key] ?? '';
        if ($val !== '' && preg_match('/^[a-zA-Z0-9 _\-\.@]{1,128}$/', $val)) {
            $args[] = '--' . str_replace('_', '-', $key) . '=' . $val;
        }
    }

    // Path/host/endpoint — allow more chars but reject shell metacharacters
    $pathFields = ['path', 'host', 'endpoint', 'ssh_key'];
    foreach ($pathFields as $key) {
        $val = $params[$key] ?? '';
        if ($val !== '' && ! preg_match('/[;&|`$(){}]/', $val) && strlen($val) <= 512) {
            $args[] = '--' . str_replace('_', '-', $key) . '=' . $val;
        }
    }

    // Port — numeric only
    $port = $params['port'] ?? '';
    if ($port !== '' && is_numeric($port) && (int) $port > 0 && (int) $port <= 65535) {
        $args[] = '--port=' . (int) $port;
    }

    // Credentials — allow broad chars but reject shell metacharacters
    $credFields = ['access_key', 'secret_key', 'account_id', 'account_key', 'ssh_password', 'restic_password'];
    foreach ($credFields as $key) {
        $val = $params[$key] ?? '';
        if ($val !== '' && ! preg_match('/[;&|`$()]/', $val) && strlen($val) <= 256) {
            $args[] = '--' . str_replace('_', '-', $key) . '=' . $val;
        }
    }

    // Retention — numeric only
    foreach (['keep_last', 'keep_daily', 'keep_weekly', 'keep_monthly'] as $key) {
        $val = $params[$key] ?? '';
        if ($val !== '' && is_numeric($val)) {
            $args[] = '--' . str_replace('_', '-', $key) . '=' . (int) $val;
        }
    }

    $r = jbExec($args);

    $error = '';
    if (($r['exitCode'] ?? -1) !== 0) {
        $error = $r['stderr'] ?? '';
        if ($error === '') {
            $error = $r['stdout'] ?? '';
        }
        if ($error === '') {
            $error = $r['error'] ?? 'CLI exited with code ' . ($r['exitCode'] ?? 'unknown');
        }
    }

    return [
        'success' => ($r['exitCode'] ?? -1) === 0,
        'output' => $r['stdout'] ?? '',
        'error' => $error,
    ];
}

function jbDestinationsUpdate(array $params): array
{
    $id = $params['id'] ?? '';
    if ($id === '' || ! preg_match('/^[a-z0-9-]+$/', $id)) {
        return ['success' => false, 'error' => 'Invalid destination ID'];
    }

    $args = ['destination', 'update', '--id=' . $id];

    $nameTypeFields = ['name', 'type', 'user', 'bucket', 'region', 'remote', 'account_name'];
    foreach ($nameTypeFields as $key) {
        $val = $params[$key] ?? '';
        if ($val !== '' && preg_match('/^[a-zA-Z0-9 _\-\.@]{1,128}$/', $val)) {
            $args[] = '--' . str_replace('_', '-', $key) . '=' . $val;
        }
    }

    $pathFields = ['path', 'host', 'endpoint', 'ssh_key'];
    foreach ($pathFields as $key) {
        $val = $params[$key] ?? '';
        if ($val !== '' && ! preg_match('/[;&|`$(){}]/', $val) && strlen($val) <= 512) {
            $args[] = '--' . str_replace('_', '-', $key) . '=' . $val;
        }
    }

    $credFields = ['access_key', 'secret_key', 'account_id', 'account_key', 'ssh_password'];
    foreach ($credFields as $key) {
        $val = $params[$key] ?? '';
        if ($val !== '' && ! preg_match('/[;&|`$()]/', $val) && strlen($val) <= 256) {
            $args[] = '--' . str_replace('_', '-', $key) . '=' . $val;
        }
    }

    $r = jbExec($args);

    return [
        'success' => $r['exitCode'] === 0,
        'output' => $r['stdout'],
        'error' => $r['exitCode'] !== 0 ? ($r['stderr'] ?: $r['stdout']) : '',
    ];
}

function jbDestinationsRemove(array $params): array
{
    $id = $params['id'] ?? '';
    if ($id === '' || ! preg_match('/^[a-z0-9-]+$/', $id)) {
        return ['success' => false, 'error' => 'Invalid destination ID'];
    }

    $r = jbExec(['destination', 'remove', '--id=' . $id]);

    return [
        'success' => $r['exitCode'] === 0,
        'output' => $r['stdout'],
        'error' => $r['exitCode'] !== 0 ? ($r['stderr'] ?: $r['stdout']) : '',
    ];
}

function jbDestinationsTest(array $params): array
{
    $id = $params['id'] ?? '';
    if ($id === '' || ! preg_match('/^[a-z0-9-]+$/', $id)) {
        return ['success' => false, 'error' => 'Invalid destination ID'];
    }

    $r = jbExec(['destination', 'test', '--id=' . $id], 30);

    return [
        'success' => $r['exitCode'] === 0,
        'output' => $r['stdout'],
        'error' => $r['exitCode'] !== 0 ? ($r['stderr'] ?: $r['stdout']) : '',
    ];
}

function jbDestinationsInit(array $params): array
{
    $id = $params['id'] ?? '';
    if ($id === '' || ! preg_match('/^[a-z0-9-]+$/', $id)) {
        return ['success' => false, 'error' => 'Invalid destination ID'];
    }

    $r = jbExec(['destination', 'init', '--id=' . $id], 60);

    return [
        'success' => $r['exitCode'] === 0,
        'output' => $r['stdout'],
        'error' => $r['exitCode'] !== 0 ? ($r['stderr'] ?: $r['stdout']) : '',
    ];
}

function jbDestinationsSetDefault(array $params): array
{
    $id = $params['id'] ?? '';
    if ($id === '' || ! preg_match('/^[a-z0-9-]+$/', $id)) {
        return ['success' => false, 'error' => 'Invalid destination ID'];
    }

    $r = jbExec(['destination', 'set-default', '--id=' . $id]);

    return [
        'success' => $r['exitCode'] === 0,
        'output' => $r['stdout'],
        'error' => $r['exitCode'] !== 0 ? ($r['stderr'] ?: $r['stdout']) : '',
    ];
}

function jbDestinationsReinit(array $params): array
{
    $id = $params['id'] ?? '';
    if ($id === '' || ! preg_match('/^[a-z0-9-]+$/', $id)) {
        return ['success' => false, 'error' => 'Invalid destination ID'];
    }

    $r = jbExec(['destination', 'reinit', '--id=' . $id, '--yes'], 120);

    return [
        'success' => $r['exitCode'] === 0,
        'output' => $r['stdout'],
        'error' => $r['exitCode'] !== 0 ? ($r['stderr'] ?: $r['stdout']) : '',
    ];
}

function jbDestinationsShow(array $params): array
{
    $id = $params['id'] ?? '';
    if ($id === '' || ! preg_match('/^[a-z0-9-]+$/', $id)) {
        return ['success' => false, 'error' => 'Invalid destination ID'];
    }

    $r = jbExec(['destination', 'show', '--id=' . $id]);
    if ($r['exitCode'] !== 0) {
        return ['success' => false, 'error' => $r['stderr'] ?: $r['stdout']];
    }

    $dest = @json_decode($r['stdout'], true);
    if (! is_array($dest)) {
        return ['success' => false, 'error' => 'Failed to parse destination'];
    }

    return ['success' => true, 'destination' => $dest];
}

function jbSshKeys(array $params): array
{
    $sshDir = '/root/.ssh';
    $keys = [];

    if (! is_dir($sshDir)) {
        return ['success' => true, 'keys' => []];
    }

    $files = scandir($sshDir);
    foreach ($files as $file) {
        if ($file === '.' || $file === '..') {
            continue;
        }
        $path = $sshDir . '/' . $file;
        if (! is_file($path)) {
            continue;
        }

        // Skip public keys, known_hosts, authorized_keys, config
        if (preg_match('/\.(pub|old|bak)$/', $file)) {
            continue;
        }
        if (in_array($file, ['known_hosts', 'authorized_keys', 'config'], true)) {
            continue;
        }

        // Check if it looks like a private key
        $firstLine = @file_get_contents($path, false, null, 0, 50);
        if ($firstLine === false) {
            continue;
        }

        $type = '';
        if (str_contains($firstLine, 'OPENSSH PRIVATE KEY')) {
            $type = 'openssh';
        } elseif (str_contains($firstLine, 'RSA PRIVATE KEY')) {
            $type = 'rsa';
        } elseif (str_contains($firstLine, 'EC PRIVATE KEY')) {
            $type = 'ecdsa';
        } elseif (str_contains($firstLine, 'PRIVATE KEY')) {
            $type = 'private key';
        } else {
            continue; // Not a private key
        }

        $keys[] = [
            'name' => $file,
            'path' => $path,
            'type' => $type,
        ];
    }

    return ['success' => true, 'keys' => $keys];
}

// ─── Stalwart ───

function jbStalwartStatus(array $params): array
{
    $configFile = '/etc/jabali-backup/config.conf';
    if (! file_exists($configFile)) {
        return ['success' => true, 'installed' => false];
    }

    $config = parse_ini_file($configFile, true);
    $stalwart = $config['stalwart'] ?? [];
    $enabled = ($stalwart['enabled'] ?? 'false') === 'true';
    $url = $stalwart['url'] ?? 'http://localhost:8080';
    $tokenFile = $stalwart['admin_token_file'] ?? '/etc/jabali-backup/stalwart-token';
    $hasToken = file_exists($tokenFile) && trim((string) file_get_contents($tokenFile)) !== '';
    $cliAvailable = trim((string) shell_exec('which stalwart-cli 2>/dev/null')) !== '';

    return [
        'success' => true,
        'installed' => isset($config['stalwart']),
        'enabled' => $enabled,
        'url' => $url,
        'has_token' => $hasToken,
        'cli_available' => $cliAvailable,
    ];
}

function jbStalwartTest(array $params): array
{
    $configFile = '/etc/jabali-backup/config.conf';
    if (! file_exists($configFile)) {
        return ['success' => false, 'error' => 'Config file not found'];
    }

    $config = parse_ini_file($configFile, true);
    $stalwart = $config['stalwart'] ?? [];
    $url = rtrim($stalwart['url'] ?? 'http://localhost:8080', '/');
    $tokenFile = $stalwart['admin_token_file'] ?? '/etc/jabali-backup/stalwart-token';

    if (! file_exists($tokenFile)) {
        return ['success' => false, 'error' => 'Admin token file not found: ' . $tokenFile];
    }

    $token = trim((string) file_get_contents($tokenFile));
    if ($token === '') {
        return ['success' => false, 'error' => 'Admin token file is empty'];
    }

    $ch = curl_init($url . '/api/principal?limit=1');
    curl_setopt_array($ch, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_TIMEOUT => 10,
        CURLOPT_HTTPHEADER => [
            'Authorization: Bearer ' . $token,
            'Accept: application/json',
        ],
    ]);
    $response = curl_exec($ch);
    $httpCode = curl_getinfo($ch, CURLINFO_HTTP_CODE);
    $curlError = curl_error($ch);
    curl_close($ch);

    if ($curlError !== '') {
        return ['success' => false, 'error' => 'Connection failed: ' . $curlError];
    }

    if ($httpCode === 401 || $httpCode === 403) {
        return ['success' => false, 'error' => 'Authentication failed (HTTP ' . $httpCode . ')'];
    }

    if ($httpCode !== 200) {
        return ['success' => false, 'error' => 'Unexpected response (HTTP ' . $httpCode . ')'];
    }

    $data = json_decode($response ?: '{}', true);
    $total = $data['data']['total'] ?? 0;

    return ['success' => true, 'message' => 'Connected — ' . $total . ' principal(s)', 'url' => $url];
}

function jbStalwartToggle(array $params): array
{
    $enable = (bool) ($params['enable'] ?? false);
    $configFile = '/etc/jabali-backup/config.conf';

    if (! file_exists($configFile)) {
        return ['success' => false, 'error' => 'Config file not found'];
    }

    // If enabling, verify token exists
    if ($enable) {
        $config = parse_ini_file($configFile, true);
        $tokenFile = $config['stalwart']['admin_token_file'] ?? '/etc/jabali-backup/stalwart-token';
        if (! file_exists($tokenFile) || trim((string) file_get_contents($tokenFile)) === '') {
            return ['success' => false, 'error' => 'Set admin token before enabling: ' . $tokenFile];
        }
    }

    $content = file_get_contents($configFile);
    $newValue = $enable ? 'true' : 'false';

    if (preg_match('/^\[stalwart\].*?^enabled\s*=\s*.*/ms', $content)) {
        // Replace existing enabled= line within [stalwart] section
        $content = preg_replace(
            '/(^\[stalwart\].*?^enabled\s*=\s*).*/ms',
            '${1}' . $newValue,
            $content,
            1,
        );
    } else {
        return ['success' => false, 'error' => '[stalwart] section not found in config'];
    }

    file_put_contents($configFile, $content);

    return ['success' => true, 'enabled' => $enable];
}

// ─── Upload / Import ───

function jbUploadPreview(array $params): array
{
    $filePath = $params['file'] ?? '';

    if ($filePath === '' || ! file_exists($filePath)) {
        return ['success' => false, 'error' => 'Archive file not found'];
    }

    // Validate MIME type via magic bytes
    $finfo = new \finfo(FILEINFO_MIME_TYPE);
    $mime = $finfo->file($filePath);
    if (! in_array($mime, ['application/gzip', 'application/x-gzip', 'application/x-tar', 'application/x-compressed-tar', 'application/octet-stream'], true)) {
        return ['success' => false, 'error' => 'Not a valid tar.gz archive (detected: ' . $mime . ')'];
    }

    $r = jbExec(['import', $filePath, '--dry-run'], 60);

    if ($r['exitCode'] !== 0 && $r['stdout'] === '') {
        return ['success' => false, 'error' => $r['stderr'] ?: 'Preview failed'];
    }

    // Parse dry-run output to extract users and components
    $users = [];
    $currentUser = null;
    foreach (explode("\n", $r['stdout']) as $line) {
        $line = trim($line);
        if (preg_match('/^Account:\s+(\S+)/', $line, $m)) {
            $currentUser = $m[1];
            $users[$currentUser] = ['username' => $currentUser, 'components' => []];
        } elseif ($currentUser !== null && preg_match('/^-\s+(\S+)/', $line, $m)) {
            $comp = $m[1];
            if (! str_contains($comp, '(skipped')) {
                $users[$currentUser]['components'][] = $comp;
            }
        }
    }

    return ['success' => true, 'users' => array_values($users), 'output' => $r['stdout']];
}

function jbUploadRestore(array $params): array
{
    $filePath = $params['file'] ?? '';
    $force = $params['force'] ?? false;
    $only = $params['only'] ?? '';

    if ($filePath === '' || ! file_exists($filePath)) {
        return ['success' => false, 'error' => 'Archive file not found'];
    }

    // Validate MIME type
    $finfo = new \finfo(FILEINFO_MIME_TYPE);
    $mime = $finfo->file($filePath);
    if (! in_array($mime, ['application/gzip', 'application/x-gzip', 'application/x-tar', 'application/x-compressed-tar', 'application/octet-stream'], true)) {
        return ['success' => false, 'error' => 'Not a valid tar.gz archive'];
    }

    $args = ['import', $filePath];
    if ($force) {
        $args[] = '--force';
    }
    if ($only !== '') {
        $safe = jbValidateComponentList($only);
        if (! empty($safe)) {
            $args[] = '--only=' . implode(',', $safe);
        }
    }

    $r = jbExec($args, 600);

    if ($r['exitCode'] !== 0) {
        return ['success' => false, 'error' => $r['stderr'] ?: 'Import failed', 'output' => $r['stdout']];
    }

    // Clean up the uploaded file
    @unlink($filePath);

    return ['success' => true, 'output' => $r['stdout']];
}
