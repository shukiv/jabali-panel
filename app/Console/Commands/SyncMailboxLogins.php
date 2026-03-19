<?php

declare(strict_types=1);

namespace App\Console\Commands;

use App\Models\Mailbox;
use Carbon\Carbon;
use Illuminate\Console\Command;

class SyncMailboxLogins extends Command
{
    protected $signature = 'jabali:sync-mailbox-logins';

    protected $description = 'Update mailbox last_login_at from mail server logs';

    public function handle(): int
    {
        $backend = config('jabali.mail_backend');

        $logins = match ($backend) {
            'stalwart' => $this->parseStalwartLogins(),
            default => $this->parseDovecotLogins(),
        };

        if (empty($logins)) {
            return self::SUCCESS;
        }

        $updated = 0;
        foreach ($logins as $email => $timestamp) {
            $affected = Mailbox::query()
                ->whereHas('emailDomain.domain')
                ->where(function ($query) use ($email) {
                    $parts = explode('@', $email);
                    if (count($parts) === 2) {
                        $query->where('local_part', $parts[0])
                            ->whereHas('emailDomain.domain', fn ($q) => $q->where('domain', $parts[1]));
                    }
                })
                ->where(function ($query) use ($timestamp) {
                    $query->whereNull('last_login_at')
                        ->orWhere('last_login_at', '<', $timestamp);
                })
                ->update(['last_login_at' => $timestamp]);

            $updated += $affected;
        }

        if ($updated > 0) {
            $this->info("Updated last login for {$updated} mailbox(es).");
        }

        return self::SUCCESS;
    }

    /**
     * @return array<string, Carbon>
     */
    protected function parseStalwartLogins(): array
    {
        $logins = [];
        $logDir = '/var/log/stalwart-mail';

        if (! is_dir($logDir)) {
            return $logins;
        }

        $today = now()->format('Y-m-d');
        $yesterday = now()->subDay()->format('Y-m-d');
        $logFiles = array_filter([
            "{$logDir}/stalwart.log.{$today}",
            "{$logDir}/stalwart.log.{$yesterday}",
        ], 'file_exists');

        foreach ($logFiles as $logFile) {
            $handle = @fopen($logFile, 'r');
            if (! $handle) {
                continue;
            }

            while (($line = fgets($handle)) !== false) {
                if (! str_contains($line, 'auth.success')) {
                    continue;
                }

                if (! preg_match('/^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z)/', $line, $timeMatch)) {
                    continue;
                }

                if (! preg_match('/accountName\s*=\s*"([^"]+)"/', $line, $nameMatch)) {
                    continue;
                }

                $email = strtolower($nameMatch[1]);
                $timestamp = Carbon::parse($timeMatch[1]);

                if (! isset($logins[$email]) || $timestamp->isAfter($logins[$email])) {
                    $logins[$email] = $timestamp;
                }
            }

            fclose($handle);
        }

        return $logins;
    }

    /**
     * @return array<string, Carbon>
     */
    protected function parseDovecotLogins(): array
    {
        $logins = [];
        $logFile = '/var/log/mail.log';

        if (! file_exists($logFile)) {
            return $logins;
        }

        $handle = @fopen($logFile, 'r');
        if (! $handle) {
            return $logins;
        }

        while (($line = fgets($handle)) !== false) {
            if (! str_contains($line, 'imap-login') && ! str_contains($line, 'pop3-login')) {
                continue;
            }

            if (! str_contains($line, 'Login')) {
                continue;
            }

            if (! preg_match('/^(\w+\s+\d+\s+\d+:\d+:\d+)/', $line, $timeMatch)) {
                continue;
            }

            if (! preg_match('/user=<([^>]+)>/', $line, $userMatch)) {
                continue;
            }

            $email = strtolower($userMatch[1]);

            try {
                $timestamp = Carbon::parse($timeMatch[1]);
            } catch (\Exception) {
                continue;
            }

            if (! isset($logins[$email]) || $timestamp->isAfter($logins[$email])) {
                $logins[$email] = $timestamp;
            }
        }

        fclose($handle);

        return $logins;
    }
}
