<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Services\Agent\AgentClient;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Log;

class RunLynisScan implements ShouldQueue
{
    use Queueable;

    public int $tries = 1;

    public int $timeout = 1800;

    public function handle(): void
    {
        $lock = Cache::lock('scanner.run.lynis', 3600);

        if (! $lock->get()) {
            Log::info('RunLynisScan: lock already held');
            return;
        }

        try {
            $agent = new AgentClient(timeout: 900);
            $result = $agent->send('scanner.run_lynis');

            if (! ($result['success'] ?? false)) {
                Log::warning('RunLynisScan: scan failed', [
                    'error' => $result['error'] ?? null,
                ]);
                return;
            }

            $scanDir = storage_path('app/security-scans');
            if (! is_dir($scanDir)) {
                mkdir($scanDir, 0755, true);
            }

            $results = $result['results'] ?? [];
            $results['scan_time'] = $results['scan_time'] ?? date('Y-m-d H:i:s');

            file_put_contents(
                $scanDir.'/lynis-latest.json',
                json_encode($results, JSON_PRETTY_PRINT),
            );
        } finally {
            optional($lock)->release();
        }
    }
}

