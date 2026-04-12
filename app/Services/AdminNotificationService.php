<?php

declare(strict_types=1);

namespace App\Services;

use App\Models\DnsSetting;
use App\Models\NotificationChannel;
use App\Models\NotificationLog;
use App\Services\Notifications\LegacyEmailChannelSync;
use App\Services\Notifications\NotificationDispatcher;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Facades\Mail;

class AdminNotificationService
{
    public static function send(string $type, string $subject, string $message, array $context = []): bool
    {
        $settingKey = "notify_{$type}";
        if (! DnsSetting::get($settingKey, true)) {
            // Log as skipped (notification type disabled)
            self::logNotification($type, $subject, $message, [], 'skipped', $context, 'Notification type disabled');

            return false;
        }

        // v2 pipeline is the ONLY path. If an explicit v2_enabled=false is
        // set (dev/debug escape hatch), we fall through to legacy email
        // delivery — but by default the dispatcher handles everything.
        if (config('notifications.v2_enabled', true)) {
            // Auto-mirror legacy admin_email_recipients into the well-known
            // default_email channel so 298 callers light up without any
            // manual UI setup on a fresh install.
            if (! NotificationChannel::query()->exists()) {
                app(LegacyEmailChannelSync::class)->sync();
            }

            /** @var NotificationDispatcher $dispatcher */
            $dispatcher = app(NotificationDispatcher::class);

            return $dispatcher->dispatch($type, $subject, $message, $context);
        }

        $recipients = DnsSetting::get('admin_email_recipients', '');
        if (empty($recipients)) {
            Log::warning("AdminNotification: Skipped {$type} notification — no admin email recipients configured in Server Settings");
            self::logNotification($type, $subject, $message, [], 'skipped', $context, 'No recipients configured');

            return false;
        }

        $recipientList = array_filter(array_map('trim', explode(',', $recipients)));
        if (empty($recipientList)) {
            self::logNotification($type, $subject, $message, [], 'skipped', $context, 'No valid recipients');

            return false;
        }

        try {
            $hostname = gethostname();
            $fullMessage = $message."\n\n---\n".
                           "Server: {$hostname}\n".
                           'Time: '.now()->format('Y-m-d H:i:s')."\n";

            if (! empty($context)) {
                $fullMessage .= "\nDetails:\n";
                foreach ($context as $key => $value) {
                    $fullMessage .= "- {$key}: {$value}\n";
                }
            }

            $sender = "webmaster@{$hostname}";

            Mail::raw($fullMessage, function ($mail) use ($recipientList, $sender, $subject, $hostname) {
                $mail->from($sender, "Jabali Panel ({$hostname})");
                $mail->to($recipientList);
                $mail->subject("[Jabali] {$subject}");
            });

            Log::info("AdminNotification sent: {$type} - {$subject}");
            self::logNotification($type, $subject, $message, $recipientList, 'sent', $context);

            return true;
        } catch (\Exception $e) {
            Log::error("AdminNotification failed: {$e->getMessage()}");
            self::logNotification($type, $subject, $message, $recipientList, 'failed', $context, $e->getMessage());

            return false;
        }
    }

    /**
     * Log a notification to the database.
     */
    protected static function logNotification(
        string $type,
        string $subject,
        string $message,
        array $recipients,
        string $status,
        ?array $context = null,
        ?string $error = null
    ): void {
        try {
            NotificationLog::log($type, $subject, $message, $recipients, $status, $context, $error);
        } catch (\Exception $e) {
            // Don't let logging failures break the notification system
            Log::error("Failed to log notification: {$e->getMessage()}");
        }
    }

    public static function sslError(string $domain, string $error): bool
    {
        return self::send(
            'ssl_errors',
            "SSL Certificate Error: {$domain}",
            "An SSL certificate error occurred for domain: {$domain}",
            ['Domain' => $domain, 'Error' => $error]
        );
    }

    public static function sslExpiring(string $domain, int $daysUntilExpiry): bool
    {
        return self::send(
            'ssl_errors',
            "SSL Certificate Expiring: {$domain}",
            "The SSL certificate for {$domain} will expire in {$daysUntilExpiry} days.",
            ['Domain' => $domain, 'Days Until Expiry' => $daysUntilExpiry]
        );
    }

    public static function diskQuotaWarning(string $username, int $usagePercent): bool
    {
        return self::send(
            'disk_quota',
            "Disk Quota Warning: {$username}",
            "User {$username} has reached {$usagePercent}% of their disk quota.",
            ['Username' => $username, 'Usage' => "{$usagePercent}%"]
        );
    }

    public static function loginFailure(string $ip, string $service, int $attempts): bool
    {
        return self::send(
            'login_failures',
            "Login Failure Alert: {$ip}",
            'Multiple failed login attempts detected.',
            ['IP Address' => $ip, 'Service' => $service, 'Attempts' => $attempts]
        );
    }

    public static function systemUpdatesAvailable(int $updateCount): bool
    {
        return self::send(
            'system_updates',
            'System Updates Available',
            "{$updateCount} system update(s) are available for your Jabali Panel.",
            ['Available Updates' => $updateCount]
        );
    }

    public static function sshLogin(string $username, string $ip, string $method = 'password'): bool
    {
        return self::send(
            'ssh_logins',
            "SSH Login: {$username}",
            'Successful SSH login detected.',
            ['Username' => $username, 'IP Address' => $ip, 'Auth Method' => $method]
        );
    }
}
