<?php

declare(strict_types=1);

namespace App\Services\BackgroundTasks;

use App\Enums\BackgroundTaskStatus;
use App\Enums\BackgroundTaskType;
use App\Models\BackgroundTask;
use App\Services\Agent\AgentClientInterface;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Log;
use Throwable;

/**
 * Panel-side façade for starting a background task.
 *
 * Flow:
 *   1. Create a `background_tasks` row (honouring dedupe_key — returns null
 *      if another active task has the same key).
 *   2. Call `agent.job.start` with the row's id and callback_token baked into
 *      the payload. The spawned binary will use `task_id` and
 *      `callback_token` when POSTing progress events back to the panel
 *      (Phase 4 endpoint).
 *   3. If the agent call succeeds, update the row with unit / pid / started_at.
 *      If it fails, mark the row failed so operators can see the error in
 *      the Filament resource (Phase 5).
 *
 * Callers never wait on the agent — dispatch returns as soon as the agent
 * has registered the transient systemd unit.
 *
 * @see docs/adr/0007-agent-v2-control-plane.md
 */
final class BackgroundTaskDispatcher
{
    public function __construct(private readonly AgentClientInterface $agent) {}

    /**
     * @param  list<string>  $argv  Absolute binary path first.
     * @param  array<string, mixed>  $payload  Caller-owned data. callback_token and task_id are added automatically.
     * @param  array{cpu?: int, memory?: string, io?: int}  $limits  cgroup limits passed to systemd-run.
     */
    public function dispatch(
        BackgroundTaskType $type,
        array $argv,
        array $payload = [],
        ?string $dedupeKey = null,
        ?string $targetType = null,
        ?string $targetId = null,
        array $limits = [],
    ): ?BackgroundTask {
        $task = BackgroundTask::tryCreateUnique([
            'type' => $type->value,
            'status' => BackgroundTaskStatus::Pending->value,
            'payload' => $payload,
            'target_type' => $targetType,
            'target_id' => $targetId,
            'dedupe_key' => $dedupeKey,
            'max_retries' => $type->defaultMaxRetries(),
        ]);

        if ($task === null) {
            return null; // dedupe hit
        }

        try {
            $response = $this->agent->send('job.start', [
                'type' => $type->value,
                'argv' => array_values($argv),
                'limits' => $limits,
                'payload' => array_merge($payload, [
                    'task_id' => $task->id,
                    'callback_token' => $task->callback_token,
                ]),
            ]);
        } catch (Throwable $e) {
            Log::error('BackgroundTaskDispatcher: agent call threw', [
                'task_id' => $task->id,
                'exception' => $e->getMessage(),
            ]);
            $task->markFailed(exitCode: -1, errorMessage: $e->getMessage());

            return $task;
        }

        if (empty($response['success'])) {
            $error = (string) ($response['error'] ?? 'agent returned no success flag');
            Log::warning('BackgroundTaskDispatcher: agent reported failure', [
                'task_id' => $task->id,
                'error' => $error,
            ]);
            $task->markFailed(exitCode: -1, errorMessage: $error);

            return $task;
        }

        $task->update([
            'status' => BackgroundTaskStatus::Running->value,
            'systemd_unit' => $response['unit'] ?? null,
            'pid' => isset($response['pid']) ? (int) $response['pid'] : null,
            'started_at' => Carbon::now(),
        ]);

        return $task;
    }
}
