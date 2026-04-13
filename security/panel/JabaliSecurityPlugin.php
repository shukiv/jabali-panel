<?php

declare(strict_types=1);

namespace App\JabaliSecurity;

use App\JabaliSecurity\Pages\Security;
use Filament\Contracts\Plugin;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\Toggle;
use Filament\Panel;
use Filament\Schemas\Components\Section;
use Illuminate\Support\Facades\View;

class JabaliSecurityPlugin implements Plugin
{
    public static function make(): static
    {
        return app(static::class);
    }

    public static function get(): static
    {
        return filament(static::class);
    }

    public function getId(): string
    {
        return 'jabali-security';
    }

    public function register(Panel $panel): void
    {
        $panel->pages([
            Security::class,
        ]);
    }

    public function boot(Panel $panel): void
    {
        $viewPath = app_path('JabaliSecurity/views');
        if (is_dir($viewPath)) {
            View::addNamespace('jabali-security', $viewPath);
        }
    }

    // ── Notification Settings (embedded in Server Settings) ─────────

    /**
     * Return the schema components for security notification settings.
     * Called by ServerSettings to inject into the Notifications tab.
     *
     * @param  array  $data  Current notification data (keyed by field name)
     */
    public static function notificationSchema(array &$data): array
    {
        $client = new JabaliSecurityClient;
        $config = $client->get('/config') ?? [];

        // Populate data array with current security notification values
        $data['security_notify_incidents'] = in_array($config['NOTIFY_MIN_SEVERITY'] ?? 'high', ['low', 'medium', 'high', 'critical']);
        $data['security_notify_severity'] = $config['NOTIFY_MIN_SEVERITY'] ?? 'high';
        $data['security_notify_bruteforce'] = in_array($config['BRUTEFORCE_ENABLED'] ?? 'no', ['yes', 'true', '1']);
        $data['security_notify_waf'] = in_array($config['WAF_ENABLED'] ?? 'no', ['yes', 'true', '1']);
        $data['security_notify_quarantine'] = in_array($config['AUTO_QUARANTINE'] ?? 'no', ['yes', 'true', '1']);

        return [
            Section::make(__('Security Alerts'))
                ->description(__('Powered by Jabali Security'))
                ->icon('heroicon-o-shield-check')
                ->schema([
                    \Filament\Schemas\Components\Grid::make(['default' => 1, 'md' => 2])->schema([
                        Toggle::make('notificationsData.security_notify_incidents')
                            ->label(__('Malware & Incident Alerts'))
                            ->helperText(__('File scanning detections and incident reports')),
                        Toggle::make('notificationsData.security_notify_bruteforce')
                            ->label(__('Login Failure Alerts'))
                            ->helperText(__('Brute-force blocking on SSH and mail services')),
                        Toggle::make('notificationsData.security_notify_waf')
                            ->label(__('WAF Attack Alerts'))
                            ->helperText(__('ModSecurity blocked requests')),
                        Toggle::make('notificationsData.security_notify_quarantine')
                            ->label(__('Quarantine Alerts'))
                            ->helperText(__('Files moved to quarantine')),
                    ]),
                    Select::make('notificationsData.security_notify_severity')
                        ->label(__('Minimum Severity'))
                        ->options([
                            'low' => __('Low'),
                            'medium' => __('Medium'),
                            'high' => __('High'),
                            'critical' => __('Critical'),
                        ])
                        ->helperText(__('Only send alerts at or above this severity level')),
                ]),
        ];
    }

    /**
     * Save security notification settings to the daemon config.
     *
     * @param  array   $data   Form data from Server Settings
     * @param  string  $email  Admin email recipients from panel
     */
    public static function saveNotificationSettings(array $data, string $email): void
    {
        $client = new JabaliSecurityClient;
        $payload = ['NOTIFY_EMAIL' => $email];

        if (isset($data['security_notify_severity'])) {
            $payload['NOTIFY_MIN_SEVERITY'] = $data['security_notify_severity'];
        }

        $client->patch('/config', $payload);
    }
}
