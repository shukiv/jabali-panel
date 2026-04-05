<?php

declare(strict_types=1);

namespace App\Http\Controllers;

use App\Models\Backup;
use App\Models\BackupDestination;
use App\Services\Agent\AgentClient;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Auth;
use Symfony\Component\HttpFoundation\BinaryFileResponse;
use Symfony\Component\HttpFoundation\StreamedResponse;

class BackupDownloadController extends Controller
{
    public function download(Request $request): BinaryFileResponse
    {
        $encodedPath = (string) $request->query('path', '');
        $decodedPath = base64_decode($encodedPath, true);

        if ($decodedPath === false || $decodedPath === '') {
            abort(404, 'Backup file not found');
        }

        $realPath = realpath($decodedPath);

        if ($realPath === false || ! is_file($realPath)) {
            abort(404, 'Backup file not found');
        }

        // Verify the user owns this backup (path should be in their home directory)
        $user = Auth::guard('web')->user();
        if (! $user) {
            abort(403, 'Unauthorized');
        }

        $homeDirectory = $user->home_directory ?: "/home/{$user->username}";
        $backupDirectory = rtrim($homeDirectory, '/').'/backups';
        $backupRealPath = realpath($backupDirectory);

        if ($backupRealPath === false) {
            abort(404, 'Backup directory not found');
        }

        if ($realPath !== $backupRealPath && ! str_starts_with($realPath, $backupRealPath.DIRECTORY_SEPARATOR)) {
            abort(403, 'Unauthorized access to this backup');
        }

        return response()->download($realPath);
    }

    public function adminDownload(Request $request): BinaryFileResponse|StreamedResponse|\Illuminate\Http\Response
    {
        $backupId = $request->query('id');

        if (empty($backupId)) {
            abort(404, 'Backup ID required');
        }

        // Verify admin access
        $user = Auth::guard('admin')->user();
        if (! $user || ! $user->is_admin) {
            abort(403, 'Unauthorized');
        }

        $backup = Backup::find($backupId);
        if (! $backup) {
            abort(404, 'Backup not found');
        }

        // For restic snapshots without a local file, export first
        if ($backup->snapshot_id && (empty($backup->local_path) || ! file_exists($backup->local_path))) {
            return $this->downloadResticSnapshot($backup);
        }

        $path = $backup->local_path;

        if (empty($path)) {
            abort(404, 'Backup file not found on disk');
        }

        $realPath = realpath($path);

        if ($realPath === false) {
            abort(404, 'Backup file not found on disk');
        }

        // Validate backup path is under an allowed directory
        $allowedPrefixes = ['/home/', '/var/backups/', rtrim(storage_path('app/backups'), '/').'/'];
        $isAllowed = false;
        foreach ($allowedPrefixes as $prefix) {
            if (str_starts_with($realPath, $prefix)) {
                $isAllowed = true;
                break;
            }
        }
        if (! $isAllowed) {
            abort(403, 'Backup path is not in an allowed directory');
        }

        // Handle directory backups by creating a zip archive on-the-fly
        if (is_dir($realPath)) {
            $backupName = basename($realPath).'.zip';

            return response()->streamDownload(function () use ($realPath) {
                $zip = new \ZipArchive;
                $tempFile = tempnam(sys_get_temp_dir(), 'backup_');
                $zip->open($tempFile, \ZipArchive::CREATE | \ZipArchive::OVERWRITE);

                $files = new \RecursiveIteratorIterator(
                    new \RecursiveDirectoryIterator($realPath, \RecursiveDirectoryIterator::SKIP_DOTS),
                    \RecursiveIteratorIterator::LEAVES_ONLY
                );

                foreach ($files as $file) {
                    if (! $file->isDir()) {
                        $filePath = $file->getRealPath();
                        $relativePath = substr($filePath, strlen($realPath) + 1);
                        $zip->addFile($filePath, $relativePath);
                    }
                }

                $zip->close();

                readfile($tempFile);
                unlink($tempFile);
            }, $backupName, [
                'Content-Type' => 'application/zip',
            ]);
        }

        // For single file backups (tar.gz)
        if (! is_file($realPath)) {
            abort(404, 'Backup file not found on disk');
        }

        return response()->download($realPath);
    }

    private function downloadResticSnapshot(Backup $backup): StreamedResponse|\Illuminate\Http\Response
    {
        try {
            $repo = $backup->destination
                ? $backup->destination->getResticRepoUrl()
                : BackupDestination::defaultRepo();
            $destConfig = $backup->destination
                ? array_merge($backup->destination->config ?? [], ['type' => $backup->destination->type])
                : [];

            $result = app(AgentClient::class)->send('backup.export_snapshot', [
                'snapshot_id' => $backup->snapshot_id,
                'repo' => $repo,
                'destination' => $destConfig,
            ]);

            if (! ($result['success'] ?? false)) {
                return response($result['error'] ?? 'Failed to export snapshot', 500)
                    ->header('Content-Type', 'text/plain');
            }

            $pipePath = $result['pipe'];
            $filename = ($backup->name ?: 'backup').'.tar.gz';

            // Stream directly from the named pipe — no temp file on disk
            return response()->stream(function () use ($pipePath): void {
                $fp = fopen($pipePath, 'rb');
                if (! $fp) {
                    return;
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
            }, 200, [
                'Content-Type' => 'application/gzip',
                'Content-Disposition' => 'attachment; filename="'.$filename.'"',
                'Cache-Control' => 'no-cache',
            ]);
        } catch (\Throwable $e) {
            return response('Backup export failed: '.$e->getMessage(), 500)
                ->header('Content-Type', 'text/plain');
        }
    }
}
