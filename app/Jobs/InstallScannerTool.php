<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Services\Agent\AgentClient;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Log;
use InvalidArgumentException;

class InstallScannerTool implements ShouldQueue
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
        $lock = Cache::lock("scanner.install.{$this->tool}", 3600);

        if (! $lock->get()) {
            Log::info("InstallScannerTool: lock already held for {$this->tool}");

            return;
        }

        try {
            // Installs can take a while; keep socket timeouts generous.
            $agent = app(AgentClient::class);
            $result = $agent->send('scanner.install', ['tool' => $this->tool]);

            if (! ($result['success'] ?? false)) {
                $error = (string) ($result['error'] ?? 'Unknown error');
                Log::warning("InstallScannerTool: install failed for {$this->tool}: {$error}", [
                    'output' => $result['output'] ?? null,
                ]);
            } else {
                Log::info("InstallScannerTool: installed {$this->tool}", [
                    'version' => $result['version'] ?? null,
                ]);
            }
        } finally {
            optional($lock)->release();
        }
    }
}
