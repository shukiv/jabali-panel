<?php

declare(strict_types=1);

namespace App\Console\Commands;

use App\Models\DnsSetting;
use App\Models\User;
use App\Services\AdminNotificationService;
use Illuminate\Console\Command;

class CheckDiskQuotas extends Command
{
    protected $signature = 'jabali:check-quotas {--threshold=90 : Percentage threshold for warning}';
    protected $description = 'Check disk quotas and send notifications for users exceeding threshold';

    public function handle(): int
    {
        if (!DnsSetting::get('quotas_enabled', false)) {
            $this->info('Disk quotas are not enabled.');
            return Command::SUCCESS;
        }

        $threshold = (int) $this->option('threshold');
        $this->info("Checking disk quotas (threshold: {$threshold}%)...");

        $users = User::where('is_admin', false)->get();
        $warnings = 0;

        foreach ($users as $user) {
            $usage = $this->getUserQuotaUsage($user->username);

            if ($usage === null) {
                continue;
            }

            if ($usage['percent'] >= $threshold) {
                $this->warn("User {$user->username} at {$usage['percent']}% quota usage");
                AdminNotificationService::diskQuotaWarning($user->username, $usage['percent']);
                $warnings++;
            }
        }

        $this->info("Quota check complete. {$warnings} warning(s) sent.");
        return Command::SUCCESS;
    }

    private function getUserQuotaUsage(string $username): ?array
    {
        $output = [];
        $returnVar = 0;

        exec("quota -u {$username} 2>/dev/null", $output, $returnVar);

        if ($returnVar !== 0 || empty($output)) {
            return null;
        }

        // Parse quota output
        foreach ($output as $line) {
            if (preg_match('/^\s*\/\S+\s+(\d+)\s+(\d+)\s+(\d+)/', $line, $matches)) {
                $used = (int) $matches[1];
                $soft = (int) $matches[2];
                $hard = (int) $matches[3];

                $limit = $soft > 0 ? $soft : $hard;
                if ($limit > 0) {
                    return [
                        'used' => $used,
                        'limit' => $limit,
                        'percent' => (int) round(($used / $limit) * 100),
                    ];
                }
            }
        }

        return null;
    }
}
