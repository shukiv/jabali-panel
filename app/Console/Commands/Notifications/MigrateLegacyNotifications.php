<?php

declare(strict_types=1);

namespace App\Console\Commands\Notifications;

use App\Models\DnsSetting;
use App\Models\NotificationChannel;
use App\Models\NotificationTypeChannel;
use Illuminate\Console\Command;

/**
 * Seed the v2 notification tables from the legacy admin_email_recipients
 * setting. Idempotent — re-running after any config edit is safe; it
 * only creates rows when they're absent.
 *
 *   php artisan jabali:notifications:migrate
 *
 * Wired into jabali update so upgrades land with a working default.
 */
class MigrateLegacyNotifications extends Command
{
    protected $signature = 'jabali:notifications:migrate {--force : Seed even if channels already exist}';

    protected $description = 'Seed notification_channels + routing from legacy admin_email_recipients';

    public function handle(): int
    {
        $existing = NotificationChannel::query()->count();
        if ($existing > 0 && ! $this->option('force')) {
            $this->info("Notification channels already exist ({$existing}). Skipping (use --force to re-seed).");

            return self::SUCCESS;
        }

        $raw = (string) DnsSetting::get('admin_email_recipients', '');
        $recipients = array_values(array_filter(array_map('trim', explode(',', $raw))));

        if ($recipients === []) {
            $this->warn('No admin_email_recipients configured — creating a placeholder disabled email channel.');
            $channel = NotificationChannel::query()->firstOrCreate(
                ['name' => 'default_email'],
                ['type' => 'email', 'config' => ['recipients' => []], 'enabled' => false]
            );
        } else {
            $channel = NotificationChannel::query()->firstOrCreate(
                ['name' => 'default_email'],
                ['type' => 'email', 'config' => ['recipients' => $recipients], 'enabled' => true]
            );
            $this->info('Created default_email channel with '.count($recipients).' recipient(s).');
        }

        // Route all three severities to the default email channel by default.
        // Admins can then add more channels + deselect email where not wanted.
        foreach (NotificationTypeChannel::SEVERITIES as $severity) {
            NotificationTypeChannel::query()->firstOrCreate([
                'severity' => $severity,
                'channel_id' => $channel->id,
            ], ['priority' => 0]);
        }

        $this->info('Default severity routing installed (info/warning/critical -> default_email).');
        $this->newLine();
        $this->line('  Next step: enable the v2 pipeline by setting <fg=cyan>NOTIFICATIONS_V2_ENABLED=true</> in .env');
        $this->line('  Then visit Jabali admin -> Server Settings -> Notifications to add Telegram, ntfy, or Web Push channels.');

        return self::SUCCESS;
    }
}
