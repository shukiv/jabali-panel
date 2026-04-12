<?php

declare(strict_types=1);

namespace App\Console\Commands\Jabali;

use App\Enums\BackgroundTaskStatus;
use App\Models\BackgroundTask;
use App\Services\Agent\AgentClientInterface;
use Illuminate\Console\Command;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Log;
use Throwable;

/**
 * Reconcile the panel's `background_tasks` table with the agent's view of
 * what's actually running.
 *
 * Three reconciliation cases:
 *
 *   1. Agent says a task is done/failed/canceled — sync panel row.
 *   2. Panel says a task is running, but agent has no record and the task
 *      started more than the grace window ago — mark orphan (failed).
 *   3. Panel and agent agree — no-op.
 *
 * Terminal panel rows are NEVER touched, regardless of what the agent says.
 * This prevents flapping when the agent restarts with a fresh SQLite.
 *
 * Scheduled every 60s via Kernel::schedule(). Agent-originated event POSTs
 * (Phase 4 endpoint) are the fast path; this reconciler is the safety net.
 *
 * @see docs/adr/0007-agent-v2-control-plane.md
 */
class ReconcileTasksCommand extends Command
{
    protected $signature = 'jabali:tasks:reconcile {--grace=60 : Seconds a task must have been running before it can be orphaned}';

    protected $description = 'Reconcile panel background_tasks rows with the agent view';

    public function handle(AgentClientInterface $agent): int
    {
        $grace = (int) $this->option('grace');

        try {
            $response = $agent->send('job.list', ['status' => 'running', 'limit' => 500]);
        } catch (Throwable $e) {
            Log::warning('tasks:reconcile — agent unreachable', [
                'error' => $e->getMessage(),
            ]);
            $this->warn('Agent unreachable; skipping this cycle.');

            return self::SUCCESS;
        }

        if (empty($response['success'])) {
            $this->warn('Agent returned failure; skipping.');

            return self::SUCCESS;
        }

        $agentById = [];
        foreach (($response['tasks'] ?? []) as $row) {
            if (isset($row['id'])) {
                $agentById[$row['id']] = $row;
            }
        }

        // Also pull any terminal rows the agent knows about — we want to sync
        // those too. Separate call; small, bounded result set.
        try {
            $termResponse = $agent->send('job.list', ['limit' => 200]);
            foreach (($termResponse['tasks'] ?? []) as $row) {
                if (isset($row['id'])) {
                    $agentById[$row['id']] = $row;
                }
            }
        } catch (Throwable) {
            // Non-fatal; we'll catch terminal rows via per-task status below.
        }

        $panelActive = BackgroundTask::query()->active()->get();
        $orphanCutoff = Carbon::now()->subSeconds($grace);

        $reconciled = 0;
        $orphaned = 0;

        foreach ($panelActive as $task) {
            $agentRow = $agentById[$task->id] ?? null;

            if ($agentRow !== null && isset($agentRow['status'])) {
                if ($this->syncFromAgent($task, $agentRow)) {
                    $reconciled++;
                }

                continue;
            }

            // Agent list didn't include this task — ask directly before
            // declaring orphan. Handles races where the task was dispatched
            // between our `job.list` call and `panelActive` query.
            try {
                $statusResponse = $agent->send('job.status', ['id' => $task->id]);
                if (! empty($statusResponse['success']) && isset($statusResponse['task']['status'])) {
                    if ($this->syncFromAgent($task, $statusResponse['task'])) {
                        $reconciled++;
                    }

                    continue;
                }
            } catch (Throwable) {
                // Fall through to orphan check.
            }

            if ($task->started_at !== null && $task->started_at->lt($orphanCutoff)) {
                $task->markFailed(
                    exitCode: -1,
                    errorMessage: 'orphaned — agent has no record of this task'
                );
                $orphaned++;
                Log::warning('tasks:reconcile orphaned task', ['task_id' => $task->id]);
            }
        }

        $this->info("Reconciled {$reconciled} task(s); orphaned {$orphaned}.");

        return self::SUCCESS;
    }

    /**
     * @param  array<string, mixed>  $agentRow
     */
    private function syncFromAgent(BackgroundTask $task, array $agentRow): bool
    {
        $agentStatus = $agentRow['status'] ?? null;
        if (! is_string($agentStatus)) {
            return false;
        }

        $target = BackgroundTaskStatus::tryFrom($agentStatus);
        if ($target === null || ! $target->isTerminal()) {
            return false; // nothing to do — still running on agent side
        }

        match ($target) {
            BackgroundTaskStatus::Done => $task->markDone(
                exitCode: (int) ($agentRow['exit_code'] ?? 0)
            ),
            BackgroundTaskStatus::Failed => $task->markFailed(
                exitCode: (int) ($agentRow['exit_code'] ?? 1),
                errorMessage: isset($agentRow['error']) ? (string) $agentRow['error'] : null,
            ),
            BackgroundTaskStatus::Canceled => $task->markCanceled(
                isset($agentRow['error']) ? (string) $agentRow['error'] : null
            ),
            default => null,
        };

        return true;
    }
}
