<?php

declare(strict_types=1);

namespace App\Console\Commands\Jabali;

use App\Services\BackgroundTasks\TaskEventRelay;
use Illuminate\Console\Command;

/**
 * Scan every running background task's JSONL log and apply new events.
 *
 * Scheduled every 10 seconds. See TaskEventRelay for details.
 */
class RelayTasksCommand extends Command
{
    protected $signature = 'jabali:tasks:relay';

    protected $description = 'Relay JSONL events from spawned binaries to their background_tasks rows';

    public function handle(TaskEventRelay $relay): int
    {
        $stats = $relay->relayAll();
        $this->info("Scanned {$stats['scanned']} tasks, applied {$stats['applied']} events.");

        return self::SUCCESS;
    }
}
