<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Models\ImapSyncTask;
use App\Services\Agent\AgentClient;
use Exception;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\File;
use Illuminate\Support\Facades\Log;
use Throwable;

class RunImapSync implements ShouldQueue
{
    use Queueable;

    public int $tries = 1;

    public int $timeout = 7200; // 2 hours max

    public function __construct(
        public int $syncTaskId,
    ) {}

    public function handle(AgentClient $agent): void
    {
        $task = ImapSyncTask::findOrFail($this->syncTaskId);

        // Only proceed if task is still pending (handles cancellation race)
        if ($task->status !== 'pending') {
            return;
        }

        $task->update([
            'status' => 'running',
            'started_at' => now(),
        ]);

        $this->ensureLogPath($task);
        $this->appendLog($task, __('IMAP sync started'), 'pending');
        Cache::put($this->getCacheKey(), ['status' => 'running'], now()->addHours(3));

        try {
            $mailbox = $task->mailbox;
            $destPassword = $mailbox->plain_password;

            if (! $destPassword) {
                throw new Exception(__('Destination mailbox password not available'));
            }

            $result = $agent->call('email.imap_sync_run', [
                'source_host' => $task->source_host,
                'source_port' => $task->source_port,
                'source_encryption' => $task->source_encryption,
                'source_email' => $task->source_email,
                'source_password' => $task->source_password,
                'dest_email' => $mailbox->email,
                'dest_password' => $destPassword,
                'folders' => $task->folders,
                'log_path' => $task->log_path,
            ]);

            if ($result->success) {
                // Only update if not cancelled during sync
                $task->refresh();
                if ($task->status === 'running') {
                    $task->update([
                        'status' => 'completed',
                        'messages_synced' => $result->get('messages_synced', 0),
                        'messages_total' => $result->get('messages_total', 0),
                        'completed_at' => now(),
                    ]);
                }
                $this->appendLog($task, __('IMAP sync completed successfully.'), 'success');
                Cache::put($this->getCacheKey(), ['status' => 'completed'], now()->addHours(3));

                return;
            }

            $error = $result->error ?? __('IMAP sync failed');
            $task->refresh();
            if ($task->status === 'running') {
                $task->update([
                    'status' => 'failed',
                    'error_message' => $error,
                    'completed_at' => now(),
                ]);
            }
            $this->appendLog($task, __('IMAP sync failed: :error', ['error' => $error]), 'error');
            Cache::put($this->getCacheKey(), ['status' => 'failed'], now()->addHours(3));
        } catch (Exception $e) {
            $task->refresh();
            if ($task->status === 'running') {
                $task->update([
                    'status' => 'failed',
                    'error_message' => $e->getMessage(),
                    'completed_at' => now(),
                ]);
            }
            $this->appendLog($task, __('IMAP sync failed: :error', ['error' => $e->getMessage()]), 'error');
            Cache::put($this->getCacheKey(), ['status' => 'failed'], now()->addHours(3));
            Log::error('RunImapSync failed', [
                'task_id' => $this->syncTaskId,
                'error' => $e->getMessage(),
            ]);
        }
    }

    public function failed(Throwable $exception): void
    {
        $task = ImapSyncTask::find($this->syncTaskId);

        if ($task && $task->status !== 'cancelled') {
            $task->update([
                'status' => 'failed',
                'error_message' => $exception->getMessage(),
                'completed_at' => now(),
            ]);
        }

        Cache::put($this->getCacheKey(), ['status' => 'failed'], now()->addHours(3));

        Log::error('RunImapSync job failed', [
            'task_id' => $this->syncTaskId,
            'error' => $exception->getMessage(),
        ]);
    }

    protected function ensureLogPath(ImapSyncTask $task): void
    {
        if (! $task->log_path) {
            return;
        }
        File::ensureDirectoryExists(dirname($task->log_path));
        if (! File::exists($task->log_path)) {
            File::put($task->log_path, '');
            @chmod($task->log_path, 0640);
        }
    }

    protected function appendLog(ImapSyncTask $task, string $message, string $status): void
    {
        if (! $task->log_path) {
            return;
        }
        $entry = [
            'message' => $message,
            'status' => $status,
            'time' => now()->format('H:i:s'),
        ];
        file_put_contents($task->log_path, json_encode($entry).PHP_EOL, FILE_APPEND | LOCK_EX);
        @chmod($task->log_path, 0640);
    }

    protected function getCacheKey(): string
    {
        return 'imap_sync_status_'.$this->syncTaskId;
    }
}
