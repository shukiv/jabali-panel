<?php

declare(strict_types=1);

namespace App\Console\Commands;

use App\Services\AdminNotificationService;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\Cache;

class CheckFail2banAlerts extends Command
{
    protected $signature = 'jabali:check-fail2ban';
    protected $description = 'Check fail2ban logs for recent bans and send notifications';

    public function handle(): int
    {
        $this->info('Checking fail2ban for recent bans...');

        $logFile = '/var/log/fail2ban.log';

        if (!file_exists($logFile)) {
            $this->info('Fail2ban log not found.');
            return Command::SUCCESS;
        }

        // Get last check position
        $lastPosition = (int) Cache::get('fail2ban_check_position', 0);
        $currentSize = filesize($logFile);

        // If log was rotated, start from beginning
        if ($currentSize < $lastPosition) {
            $lastPosition = 0;
        }

        $handle = fopen($logFile, 'r');
        if (!$handle) {
            $this->error('Cannot open fail2ban log.');
            return Command::FAILURE;
        }

        fseek($handle, $lastPosition);
        $banCount = 0;
        $bans = [];

        while (($line = fgets($handle)) !== false) {
            // Match fail2ban ban entries
            // Format: 2024-01-15 10:30:45,123 fail2ban.actions [12345]: NOTICE [sshd] Ban 192.168.1.100
            if (preg_match('/\[([^\]]+)\]\s+Ban\s+(\S+)/', $line, $matches)) {
                $service = $matches[1];
                $ip = $matches[2];

                $key = "{$service}:{$ip}";
                if (!isset($bans[$key])) {
                    $bans[$key] = [
                        'service' => $service,
                        'ip' => $ip,
                        'count' => 0,
                    ];
                }
                $bans[$key]['count']++;
                $banCount++;
            }
        }

        $newPosition = ftell($handle);
        fclose($handle);

        // Save new position
        Cache::put('fail2ban_check_position', $newPosition, now()->addDays(7));

        // Send notifications for each unique IP/service combination
        foreach ($bans as $ban) {
            $this->warn("Ban detected: {$ban['ip']} on {$ban['service']} ({$ban['count']} times)");
            AdminNotificationService::loginFailure($ban['ip'], $ban['service'], $ban['count']);
        }

        $this->info("Fail2ban check complete. {$banCount} ban(s) found.");
        return Command::SUCCESS;
    }
}
