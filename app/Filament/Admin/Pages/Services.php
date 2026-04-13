<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Models\AuditLog;
use App\Services\Agent\InteractsWithAgent;
use App\Support\SafeError;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Database\Eloquent\Model;

class Services extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithAgent;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-cog-6-tooth';

    protected static ?int $navigationSort = 10;

    public static function getNavigationLabel(): string
    {
        return __('Services');
    }

    protected string $view = 'filament.admin.pages.services';

    public array $services = [];

    protected ?array $managedServices = null;

    protected function getManagedServices(): array
    {
        if ($this->managedServices !== null) {
            return $this->managedServices;
        }

        $baseServices = [
            'jabali-panel' => ['name' => 'FrankenPHP', 'description' => __('Panel Web Server'), 'icon' => 'globe'],
            'nginx' => ['name' => 'Nginx', 'description' => __('Web Server'), 'icon' => 'globe'],
            'mariadb' => ['name' => 'MariaDB', 'description' => __('Database Server'), 'icon' => 'database'],
            'redis-server' => ['name' => 'Redis', 'description' => __('Cache Server'), 'icon' => 'bolt'],
            'stalwart-mail' => ['name' => 'Stalwart', 'description' => __('Mail Server'), 'icon' => 'envelope'],
        ];

        if ((bool) \App\Models\DnsSetting::get('postgres_enabled', false)) {
            $baseServices['postgresql'] = ['name' => 'PostgreSQL', 'description' => __('PostgreSQL Database'), 'icon' => 'database'];
        }

        $baseServices += [
            'jabali-agent' => ['name' => 'Jabali Agent', 'description' => __('Panel Agent Daemon'), 'icon' => 'cpu-chip'],
            'jabali-queue' => ['name' => 'Jabali Queue', 'description' => __('Background Job Worker'), 'icon' => 'queue-list'],
            'pdns' => ['name' => 'PowerDNS', 'description' => __('DNS Server'), 'icon' => 'server'],
            'ssh' => ['name' => 'SSH', 'description' => __('Secure Shell'), 'icon' => 'terminal'],
            'cron' => ['name' => 'Cron', 'description' => __('Task Scheduler'), 'icon' => 'clock'],
        ];

        $this->managedServices = [];
        foreach ($baseServices as $key => $config) {
            $this->managedServices[$key] = $config;

            if ($key === 'nginx') {
                foreach ($this->detectPhpFpmVersions() as $service => $phpConfig) {
                    $this->managedServices[$service] = $phpConfig;
                }
            }
        }

        return $this->managedServices;
    }

    protected function detectPhpFpmVersions(): array
    {
        $phpServices = [];
        $output = (array) glob('/lib/systemd/system/php*-fpm.service');

        foreach ($output as $servicePath) {
            if (! is_string($servicePath)) {
                continue;
            }
            if (preg_match('/php([\d.]+)-fpm\.service$/', $servicePath, $matches)) {
                $version = $matches[1];
                $serviceName = "php{$version}-fpm";
                $phpServices[$serviceName] = [
                    'name' => "PHP {$version} FPM",
                    'description' => __('PHP FastCGI Process Manager'),
                    'icon' => 'code',
                ];
            }
        }

        uksort($phpServices, function ($a, $b) {
            preg_match('/php([\d.]+)-fpm/', $a, $matchA);
            preg_match('/php([\d.]+)-fpm/', $b, $matchB);

            return version_compare($matchB[1] ?? '0', $matchA[1] ?? '0');
        });

        return $phpServices;
    }

    public function getTitle(): string|Htmlable
    {
        return __('Service Manager');
    }

    public function mount(): void
    {
        $this->loadServices();
    }

    public function loadServices(): void
    {
        $managedServices = $this->getManagedServices();

        try {
            $result = $this->agent()->call('service.list', [
                'services' => array_keys($managedServices),
            ]);

            if ($result->success) {
                $this->services = [];
                foreach ($result->get('services', []) as $name => $status) {
                    $isFailed = (bool) ($status['is_failed'] ?? false);

                    // jabali-queue runs as oneshot+timer (fires every 60s, completes in ~185ms).
                    // Listing it alongside always-on daemons produced a misleading "Stopped" badge
                    // between firings. Only surface it when systemd reports it as actually failed
                    // — otherwise this is infrastructure the operator shouldn't be babysitting.
                    if ($name === 'jabali-queue' && ! $isFailed) {
                        continue;
                    }

                    $config = $managedServices[$name] ?? [
                        'name' => ucfirst($name),
                        'description' => '',
                        'icon' => 'cog',
                    ];
                    $this->services[$name] = array_merge($config, [
                        'service' => $name,
                        'is_active' => $status['is_active'] ?? false,
                        'is_enabled' => $status['is_enabled'] ?? false,
                        'is_failed' => $isFailed,
                        'status' => $status['status'] ?? 'unknown',
                    ]);
                }
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Error loading services'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => array_values($this->services))
            ->columns([
                TextColumn::make('name')
                    ->label(__('Service'))
                    ->icon(fn (array $record): string => match ($record['icon'] ?? 'cog') {
                        'globe' => 'heroicon-o-globe-alt',
                        'code' => 'heroicon-o-code-bracket',
                        'database' => 'heroicon-o-circle-stack',
                        'bolt' => 'heroicon-o-bolt',
                        'envelope' => 'heroicon-o-envelope',
                        'inbox' => 'heroicon-o-inbox',
                        'shield' => 'heroicon-o-shield-check',
                        'server' => 'heroicon-o-server',
                        'key' => 'heroicon-o-key',
                        'lock' => 'heroicon-o-lock-closed',
                        'terminal' => 'heroicon-o-command-line',
                        'clock' => 'heroicon-o-clock',
                        'bug' => 'heroicon-o-bug-ant',
                        default => 'heroicon-o-cog-6-tooth',
                    })
                    ->iconColor(fn (array $record): string => $record['is_active'] ? 'success' : 'danger')
                    ->description(fn (array $record): string => $record['description'] ?? '')
                    ->weight('medium'),
                TextColumn::make('is_active')
                    ->label(__('Status'))
                    ->badge()
                    ->formatStateUsing(fn (array $record): string => $record['is_active'] ? __('Running') : __('Stopped'))
                    ->color(fn (array $record): string => $record['is_active'] ? 'success' : 'danger'),
                TextColumn::make('is_enabled')
                    ->label(__('Boot'))
                    ->badge()
                    ->formatStateUsing(fn (array $record): string => $record['is_enabled'] ? __('Enabled') : __('Disabled'))
                    ->color(fn (array $record): string => $record['is_enabled'] ? 'success' : 'warning'),
            ])
            ->recordActions([
                Action::make('start')
                    ->label(__('Start'))
                    ->icon('heroicon-o-play')
                    ->color('success')
                    ->size('sm')
                    ->visible(fn (array $record): bool => ! $record['is_active'])
                    ->action(fn (array $record) => $this->executeServiceAction($record['service'], 'start')),
                Action::make('stop')
                    ->label(__('Stop'))
                    ->icon('heroicon-o-stop')
                    ->color('danger')
                    ->size('sm')
                    ->visible(fn (array $record): bool => $record['is_active'])
                    ->requiresConfirmation()
                    ->modalHeading(__('Stop Service'))
                    ->modalIcon('heroicon-o-exclamation-triangle')
                    ->modalIconColor('warning')
                    ->modalDescription(fn (array $record): string => __('Warning: This will stop :service and may affect running websites and services. Are you sure you want to continue?', ['service' => $record['name']]))
                    ->modalSubmitActionLabel(__('Stop Service'))
                    ->action(fn (array $record) => $this->executeServiceAction($record['service'], 'stop')),
                Action::make('restart')
                    ->label(fn (array $record): string => $this->shouldReloadService($record['service']) ? __('Reload') : __('Restart'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('info')
                    ->size('sm')
                    ->visible(fn (array $record): bool => $record['is_active'])
                    ->action(fn (array $record) => $this->executeServiceAction(
                        $record['service'],
                        $this->shouldReloadService($record['service']) ? 'reload' : 'restart'
                    )),
                Action::make('enable')
                    ->label(__('Enable'))
                    ->icon('heroicon-o-check')
                    ->color('gray')
                    ->size('sm')
                    ->visible(fn (array $record): bool => ! $record['is_enabled'])
                    ->action(fn (array $record) => $this->executeServiceAction($record['service'], 'enable')),
                Action::make('disable')
                    ->label(__('Disable'))
                    ->icon('heroicon-o-x-mark')
                    ->color('warning')
                    ->size('sm')
                    ->visible(fn (array $record): bool => $record['is_enabled'])
                    ->requiresConfirmation()
                    ->modalHeading(__('Disable Service'))
                    ->modalIcon('heroicon-o-exclamation-triangle')
                    ->modalIconColor('warning')
                    ->modalDescription(fn (array $record): string => __('Warning: This will disable :service and it will not start automatically on boot. Are you sure you want to continue?', ['service' => $record['name']]))
                    ->modalSubmitActionLabel(__('Disable Service'))
                    ->action(fn (array $record) => $this->executeServiceAction($record['service'], 'disable')),
            ])
            ->headerActions([
                Action::make('refresh')
                    ->label(__('Refresh'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('gray')
                    ->action(function () {
                        $this->loadServices();
                        $this->resetTable();
                        Notification::make()->title(__('Services refreshed'))->success()->duration(1500)->send();
                    }),
            ])
            ->emptyStateHeading(__('No services found'))
            ->emptyStateDescription(__('Unable to load system services'))
            ->emptyStateIcon('heroicon-o-cog-6-tooth')
            ->striped();
    }

    public function getTableRecordKey(Model|array $record): string
    {
        return is_array($record) ? $record['service'] : $record->getKey();
    }

    protected function executeServiceAction(string $service, string $action): void
    {
        $actionPast = match ($action) {
            'start' => 'started',
            'stop' => 'stopped',
            'restart' => 'restarted',
            'reload' => 'reloaded',
            'enable' => 'enabled',
            'disable' => 'disabled',
            default => $action,
        };

        $this->agentCall(
            action: "service.{$action}",
            params: ['service' => $service],
            successTitle: $service.' '.$actionPast,
            errorTitle: 'Action failed',
            onSuccess: function () use ($actionPast, $service): void {
                AuditLog::logServiceAction($actionPast, $service);
                $this->loadServices();
                $this->resetTable();
            },
        );
    }

    protected function getHeaderActions(): array
    {
        return [
            Action::make('rebootServer')
                ->label(__('Restart Server'))
                ->icon('heroicon-o-power')
                ->color('danger')
                ->requiresConfirmation()
                ->modalHeading(__('Restart Server'))
                ->modalDescription(__('This will reboot the entire server. All services will be temporarily unavailable. Are you sure?'))
                ->modalSubmitActionLabel(__('Restart Now'))
                ->action(fn () => $this->agentCall(
                    action: 'server.reboot',
                    params: ['delay' => 5],
                    successTitle: __('Server restarting'),
                    errorTitle: __('Reboot failed'),
                    onSuccess: fn () => AuditLog::record('server', 'reboot', 'Server reboot initiated'),
                )),
        ];
    }

    protected function shouldReloadService(string $service): bool
    {
        if ($service === 'nginx') {
            return true;
        }

        return preg_match('/^php(\d+\.\d+)?-fpm$/', $service) === 1;
    }
}
