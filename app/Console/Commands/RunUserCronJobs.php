<?php

declare(strict_types=1);

namespace App\Console\Commands;

use App\Models\CronJob;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\Log;

class RunUserCronJobs extends Command
{
    protected $signature = 'jabali:run-cron-jobs';

    protected $description = 'Run due user cron jobs and track their execution';

    public function handle(): int
    {
        $jobs = CronJob::where('is_active', true)->get();

        foreach ($jobs as $job) {
            if ($this->isDue($job->schedule)) {
                $this->runJob($job);
            }
        }

        return Command::SUCCESS;
    }

    protected function isDue(string $schedule): bool
    {
        $parts = explode(' ', $schedule);
        if (count($parts) !== 5) {
            return false;
        }

        [$minute, $hour, $dayOfMonth, $month, $dayOfWeek] = $parts;

        $now = now();

        return $this->matchesPart($minute, $now->minute)
            && $this->matchesPart($hour, $now->hour)
            && $this->matchesPart($dayOfMonth, $now->day)
            && $this->matchesPart($month, $now->month)
            && $this->matchesPart($dayOfWeek, $now->dayOfWeek);
    }

    protected function matchesPart(string $pattern, int $value): bool
    {
        // Handle *
        if ($pattern === '*') {
            return true;
        }

        // Handle */n (step values)
        if (str_starts_with($pattern, '*/')) {
            $step = (int) substr($pattern, 2);

            return $step > 0 && $value % $step === 0;
        }

        // Handle ranges (e.g., 1-5)
        if (str_contains($pattern, '-')) {
            [$start, $end] = explode('-', $pattern);

            return $value >= (int) $start && $value <= (int) $end;
        }

        // Handle lists (e.g., 1,3,5)
        if (str_contains($pattern, ',')) {
            $values = array_map('intval', explode(',', $pattern));

            return in_array($value, $values);
        }

        // Handle exact value
        return (int) $pattern === $value;
    }

    protected function runJob(CronJob $job): void
    {
        $username = $job->user->username ?? null;

        if (! $username) {
            Log::warning("Cron job {$job->id} has no valid user");

            return;
        }

        $this->info("Running cron job: {$job->name} (ID: {$job->id})");

        $startTime = microtime(true);

        // Strip output redirection from command so we can capture it
        $command = $job->command;
        $command = preg_replace('/\s*>\s*\/dev\/null.*$/', '', $command);
        $command = preg_replace('/\s*2>&1\s*$/', '', $command);

        // Run the command as the user
        $cmd = sprintf(
            'sudo -u %s bash -c %s 2>&1',
            escapeshellarg($username),
            escapeshellarg($command)
        );

        exec($cmd, $output, $exitCode);

        $duration = round(microtime(true) - $startTime, 2);
        $outputStr = implode("\n", $output);

        // Update the job record
        $job->update([
            'last_run_at' => now(),
            'last_run_status' => $exitCode === 0 ? 'success' : 'failed',
            'last_run_output' => substr($outputStr, 0, 10000), // Limit output size
        ]);

        if ($exitCode === 0) {
            $this->info("  Completed successfully in {$duration}s");
        } else {
            $this->error("  Failed with exit code {$exitCode} in {$duration}s");
            Log::warning("Cron job {$job->id} ({$job->name}) failed", [
                'exit_code' => $exitCode,
                'output' => $outputStr,
            ]);
        }
    }
}
