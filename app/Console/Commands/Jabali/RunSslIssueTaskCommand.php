<?php

declare(strict_types=1);

namespace App\Console\Commands\Jabali;

use App\Models\Domain;
use App\Services\BackgroundTasks\Binaries\TaskLogWriter;
use App\Services\SslManagementService;
use Illuminate\Console\Command;

/**
 * Spawned binary that fulfills an SSL-issuance background task.
 *
 * Contract (ADR-0007): emits JSONL progress events to the --progress-log
 * file and exits 0 on success / non-zero on failure. Does not call the
 * panel HTTP API — the agent relays events.
 */
class RunSslIssueTaskCommand extends Command
{
    protected $signature = 'jabali:tasks:run-ssl-issue
        {--task-id= : Background task UUID}
        {--progress-log= : Path to write JSONL events to}
        {--domain-id= : Domain id to issue cert for}
        {--service=web : Which service (web|mail)}';

    protected $description = 'Spawned binary: issue an SSL certificate and emit task progress';

    public function handle(SslManagementService $ssl): int
    {
        $logPath = (string) $this->option('progress-log');
        if ($logPath === '') {
            $this->error('--progress-log is required');

            return self::FAILURE;
        }

        $writer = new TaskLogWriter($logPath);
        $writer->started();

        $domainId = (int) $this->option('domain-id');
        $service = (string) $this->option('service');

        $domain = Domain::find($domainId);
        if ($domain === null) {
            $writer->failed("Domain {$domainId} not found");

            return self::FAILURE;
        }

        $writer->step('issuing certbot for '.$domain->domain);

        try {
            $cert = $ssl->issue($domain, $service);
        } catch (\Throwable $e) {
            $writer->failed($e->getMessage());

            return self::FAILURE;
        }

        if (($cert->status ?? null) === 'failed') {
            $writer->failed((string) ($cert->last_error ?? 'unknown certbot error'));

            return self::FAILURE;
        }

        $writer->done(['status' => $cert->status ?? 'active']);

        return self::SUCCESS;
    }
}
