<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Services\Agent\AgentClient;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Log;
use InvalidArgumentException;

class UninstallScannerTool implements ShouldQueue
{
    use Queueable;

    public int $tries = 1;

    public int $timeout = 1800;

    public function __construct(
        public string $tool,
    ) {
        $this->tool = strtolower(trim($this->tool));
        if (! in_array($this->tool, ['lynis', 'wpscan', 'nikto'], true)) {
            throw new InvalidArgumentException("Invalid scanner tool: {$this->tool}");
        }
    }

    public function handle(): void
    {
        $lock = Cache::lock("scanner.uninstall.{$this->tool}", 3600);

        if (! $lock->get()) {
            Log::info("UninstallScannerTool: lock already held for {$this->tool}");

            return;
        }

        try {
            $agent = app(AgentClient::class);
            $result = $agent->send('scanner.uninstall', ['tool' => $this->tool]);

            if (! ($result['success'] ?? false)) {
                $error = (string) ($result['error'] ?? 'Unknown error');
                Log::warning("UninstallScannerTool: uninstall failed for {$this->tool}: {$error}", [
                    'output' => $result['output'] ?? null,
                ]);
            } else {
                Log::info("UninstallScannerTool: uninstalled {$this->tool}");
            }
        } finally {
            optional($lock)->release();
        }
    }
}
