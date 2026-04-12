<?php

declare(strict_types=1);

namespace App\Console\Commands\Notifications;

use Illuminate\Console\Command;
use Minishlink\WebPush\VAPID;

/**
 * Generate a VAPID keypair for Web Push.
 *
 *   php artisan jabali:notifications:vapid --write-env
 *
 * Run once per installation. The keys are bound to the subscriptions —
 * if you regenerate, every admin must re-subscribe their browser. The
 * installer runs this automatically on first install.
 */
class GenerateVapidKeys extends Command
{
    protected $signature = 'jabali:notifications:vapid {--write-env : Append the keys to the project .env file}';

    protected $description = 'Generate a VAPID keypair for Web Push notifications';

    public function handle(): int
    {
        if (! class_exists(VAPID::class)) {
            $this->error('minishlink/web-push is not installed. Run: composer require minishlink/web-push');

            return self::FAILURE;
        }

        $keys = VAPID::createVapidKeys();

        $this->line("NOTIFICATIONS_VAPID_PUBLIC_KEY={$keys['publicKey']}");
        $this->line("NOTIFICATIONS_VAPID_PRIVATE_KEY={$keys['privateKey']}");

        if ($this->option('write-env')) {
            $envPath = base_path('.env');
            if (! file_exists($envPath)) {
                $this->error(".env not found at {$envPath}");

                return self::FAILURE;
            }

            $contents = file_get_contents($envPath) ?: '';
            $contents = $this->replaceOrAppend($contents, 'NOTIFICATIONS_VAPID_PUBLIC_KEY', $keys['publicKey']);
            $contents = $this->replaceOrAppend($contents, 'NOTIFICATIONS_VAPID_PRIVATE_KEY', $keys['privateKey']);

            file_put_contents($envPath, $contents);
            $this->info('.env updated');
        }

        return self::SUCCESS;
    }

    private function replaceOrAppend(string $contents, string $key, string $value): string
    {
        $line = "{$key}={$value}";
        if (preg_match("/^{$key}=.*$/m", $contents)) {
            return (string) preg_replace("/^{$key}=.*$/m", $line, $contents);
        }

        return rtrim($contents)."\n{$line}\n";
    }
}
