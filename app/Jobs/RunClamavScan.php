<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Services\Agent\AgentClient;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Log;
use InvalidArgumentException;

class RunClamavScan implements ShouldQueue
{
    use Queueable;

    public int $tries = 1;

    public int $timeout = 7200;

    public function __construct(
        public string $path,
        public string $scanType = 'server',
        public ?string $username = null,
    ) {
        $this->path = trim($this->path) ?: '/home';
        $this->scanType = in_array($this->scanType, ['server', 'user'], true) ? $this->scanType : 'server';

        if (! str_starts_with($this->path, '/')) {
            throw new InvalidArgumentException('Invalid path');
        }

        if ($this->scanType === 'user' && ($this->username === null || $this->username === '')) {
            throw new InvalidArgumentException('Username required for user scan');
        }
    }

    public function handle(): void
    {
        $lockKey = $this->scanType === 'user'
            ? "clamav.scan.user.{$this->username}"
            : 'clamav.scan.server';

        $lock = Cache::lock($lockKey, 7200);

        if (! $lock->get()) {
            Log::info('RunClamavScan: lock already held', ['key' => $lockKey]);
            return;
        }

        try {
            $agent = new AgentClient(timeout: 3600);
            $result = $agent->send('clamav.scan', ['path' => $this->path]);

            if (! ($result['success'] ?? false)) {
                Log::warning('RunClamavScan: scan failed', [
                    'error' => $result['error'] ?? null,
                ]);
                return;
            }

            $output = (string) ($result['output'] ?? '');
            $lines = $output === '' ? [] : preg_split("/\\r\\n|\\n|\\r/", $output);
            $lines = array_values(array_filter(array_map('trim', $lines ?? []), static fn ($v) => $v !== ''));

            $parsed = $this->parseClamScanOutput($lines);
            $parsed['scan_time'] = date('Y-m-d H:i:s');
            $parsed['scan_type'] = $this->scanType;
            $parsed['target'] = $this->path;
            $parsed['username'] = $this->scanType === 'user' ? $this->username : null;
            $parsed['raw_output'] = $output;

            $scanDir = storage_path('app/security-scans');
            if (! is_dir($scanDir)) {
                mkdir($scanDir, 0755, true);
            }

            file_put_contents(
                $scanDir.'/clamscan-latest.json',
                json_encode($parsed, JSON_PRETTY_PRINT),
            );
        } finally {
            optional($lock)->release();
        }
    }

    /**
     * @param  array<int, string>  $outputLines
     * @return array<string, mixed>
     */
    private function parseClamScanOutput(array $outputLines): array
    {
        $results = [
            'scanned_files' => 0,
            'infected_files' => 0,
            'threats' => [],
        ];

        $fullOutput = implode("\n", $outputLines);

        if (preg_match('/Scanned files:\\s*(\\d+)/i', $fullOutput, $matches)) {
            $results['scanned_files'] = (int) $matches[1];
        }
        if (preg_match('/Infected files:\\s*(\\d+)/i', $fullOutput, $matches)) {
            $results['infected_files'] = (int) $matches[1];
        }

        foreach ($outputLines as $line) {
            if (preg_match('/^(.+?):\\s*(.+?)\\s*FOUND$/i', $line, $matches)) {
                $results['threats'][] = [
                    'file' => trim($matches[1]),
                    'threat' => trim($matches[2]),
                ];
            }
        }

        return $results;
    }
}

