<?php

declare(strict_types=1);

namespace App\Console\Commands\Jabali;

use App\Enums\BackgroundTaskStatus;
use App\Models\BackgroundTask;
use Illuminate\Console\Command;

/**
 * Operator-facing CLI for listing background tasks.
 *
 * Complements the Filament dashboard with a terminal-friendly view for
 * incident response / scripting.
 */
class TasksListCommand extends Command
{
    protected $signature = 'jabali:tasks:ls
        {--status= : Filter by status (pending|running|done|failed|canceled)}
        {--type= : Filter by task type}
        {--limit=20 : Max rows to return}';

    protected $description = 'List background tasks (unified view of SSL, backups, addons, etc.)';

    public function handle(): int
    {
        $query = BackgroundTask::query()->orderByDesc('created_at');

        if ($status = $this->option('status')) {
            $case = BackgroundTaskStatus::tryFrom($status);
            if ($case === null) {
                $this->error("Invalid status: {$status}");

                return self::FAILURE;
            }
            $query->where('status', $case->value);
        }

        if ($type = $this->option('type')) {
            $query->where('type', $type);
        }

        $tasks = $query->limit((int) $this->option('limit'))->get();

        if ($tasks->isEmpty()) {
            $this->info('No tasks match.');

            return self::SUCCESS;
        }

        $rows = $tasks->map(fn (BackgroundTask $t) => [
            substr($t->id, 0, 8),
            $t->type->value,
            $t->status->value,
            $t->progress !== null ? $t->progress.'%' : '—',
            $t->step ?? '—',
            $t->started_at?->diffForHumans() ?? '—',
        ])->all();

        $this->table(
            ['ID', 'Type', 'Status', 'Progress', 'Step', 'Started'],
            $rows,
        );

        return self::SUCCESS;
    }
}
