<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Services\Agent\InteractsWithAgent;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Livewire\Component;

class ServicesTableWidget extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithAgent;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public array $services = [];

    public function mount(): void
    {
        $this->loadServices();
    }

    protected function loadServices(): void
    {
        $preferredOrder = [
            'jabali-panel',
            'nginx',
            'mariadb', // can fall back to mysql
            ...array_keys($this->detectPhpFpmServices()),
            'php-fpm',
            'stalwart-mail',
            'pdns',
            'redis-server',
        ];

        $labels = [
            'jabali-panel' => 'FrankenPHP',
            'nginx' => 'Nginx',
            'mariadb' => 'MariaDB',
            'mysql' => 'MariaDB',
            'php-fpm' => 'PHP-FPM',
            'stalwart-mail' => 'Stalwart',
            'pdns' => 'PowerDNS',
            'redis-server' => 'Redis',
        ];

        if ((bool) \App\Models\DnsSetting::get('postgres_enabled', false)) {
            array_splice($preferredOrder, 3, 0, ['postgresql']);
            $labels['postgresql'] = 'PostgreSQL';
        }

        $phpLabels = $this->detectPhpFpmServices();
        foreach ($phpLabels as $service => $name) {
            $labels[$service] = $name;
        }

        // Request statuses once from the agent.
        $servicesToCheck = array_values(array_unique(array_merge(
            array_keys($labels),
            array_values($this->serviceFallbackMap()),
        )));

        $statuses = [];
        try {
            $result = $this->agent()->call('service.list', ['services' => $servicesToCheck]);

            if ($result->success) {
                $services = $result->get('services', []);
                $statuses = is_array($services) ? $services : [];
            }
        } catch (Exception) {
            $statuses = [];
        }

        $rows = [];
        foreach ($preferredOrder as $service) {
            $resolved = $this->resolveServiceName($service, $statuses);
            if ($resolved === null) {
                continue;
            }

            $status = $statuses[$resolved] ?? null;
            if (! is_array($status) || $this->isMissingUnit($status['status_text'] ?? '')) {
                continue;
            }

            $rows[] = [
                'key' => $resolved,
                'name' => $labels[$resolved] ?? ($labels[$service] ?? ucfirst($resolved)),
                'active' => (bool) ($status['is_active'] ?? false),
            ];
        }

        $this->services = $rows;
    }

    /**
     * @return array<string, string>
     */
    protected function detectPhpFpmServices(): array
    {
        $services = [];

        foreach ((array) glob('/lib/systemd/system/php*-fpm.service') as $servicePath) {
            if (! is_string($servicePath)) {
                continue;
            }

            if (! preg_match('/php([\d.]+)-fpm\.service$/', $servicePath, $m)) {
                continue;
            }

            $version = $m[1];
            $serviceName = "php{$version}-fpm";
            $services[$serviceName] = "PHP {$version} FPM";
        }

        // Prefer higher versions first.
        uksort($services, static function (string $a, string $b): int {
            preg_match('/php([\d.]+)-fpm/', $a, $ma);
            preg_match('/php([\d.]+)-fpm/', $b, $mb);

            return version_compare($mb[1] ?? '0', $ma[1] ?? '0');
        });

        return $services;
    }

    /**
     * @return array<string, string>
     */
    protected function serviceFallbackMap(): array
    {
        return [
            'mariadb' => 'mysql',
        ];
    }

    /**
     * @param  array<string, mixed>  $statuses
     */
    protected function resolveServiceName(string $service, array $statuses): ?string
    {
        $status = $statuses[$service] ?? null;
        if (is_array($status) && ! $this->isMissingUnit($status['status_text'] ?? '')) {
            return $service;
        }

        $fallback = $this->serviceFallbackMap()[$service] ?? null;
        if ($fallback === null) {
            return null;
        }

        $fallbackStatus = $statuses[$fallback] ?? null;
        if (is_array($fallbackStatus) && ! $this->isMissingUnit($fallbackStatus['status_text'] ?? '')) {
            return $fallback;
        }

        return null;
    }

    protected function isMissingUnit(string $statusText): bool
    {
        $t = strtolower($statusText);

        return str_contains($t, 'could not be found')
            || str_contains($t, 'loaded: not-found')
            || str_contains($t, 'not-found');
    }

    public function makeFilamentTranslatableContentDriver(): ?\Filament\Support\Contracts\TranslatableContentDriver
    {
        return null;
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->services)
            ->columns([
                TextColumn::make('name')
                    ->label(__('Service'))
                    ->weight('medium'),
                IconColumn::make('active')
                    ->label(__('Status'))
                    ->boolean()
                    ->trueIcon('heroicon-o-check-circle')
                    ->falseIcon('heroicon-o-x-circle')
                    ->trueColor('success')
                    ->falseColor('danger'),
            ])
            ->actions([
                Action::make('start')
                    ->label(__('Start'))
                    ->icon('heroicon-o-play')
                    ->color('success')
                    ->size('sm')
                    ->visible(fn (array $record): bool => ! ($record['active'] ?? true))
                    ->action(function (array $record): void {
                        $this->agentCall(
                            action: 'service.start',
                            params: ['service' => $record['key']],
                            successTitle: 'Service started',
                            errorTitle: 'Failed to start service',
                            onSuccess: function () {
                                $this->loadServices();
                                $this->resetTable();
                            },
                        );
                    }),
                Action::make('restart')
                    ->label(fn (array $record): string => $this->shouldReloadService($record['key']) ? __('Reload') : __('Restart'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('warning')
                    ->size('sm')
                    ->visible(fn (array $record): bool => $record['active'] ?? false)
                    ->requiresConfirmation()
                    ->modalHeading(fn (array $record): string => $this->shouldReloadService($record['key']) ? __('Reload Service') : __('Restart Service'))
                    ->modalDescription(fn (array $record): string => $this->shouldReloadService($record['key'])
                        ? __('Are you sure you want to reload :service?', ['service' => $record['name']])
                        : __('Are you sure you want to restart :service?', ['service' => $record['name']])
                    )
                    ->action(function (array $record): void {
                        $action = $this->shouldReloadService($record['key']) ? 'reload' : 'restart';
                        $this->agentCall(
                            action: "service.{$action}",
                            params: ['service' => $record['key']],
                            successTitle: $action === 'reload' ? 'Service reloaded' : 'Service restarted',
                            errorTitle: $action === 'reload' ? 'Failed to reload service' : 'Failed to restart service',
                            onSuccess: function () {
                                sleep(1);
                                $this->loadServices();
                                $this->resetTable();
                            },
                        );
                    }),
            ])
            ->paginated(false)
            ->striped();
    }

    public function render()
    {
        return $this->getTable()->render();
    }

    protected function shouldReloadService(string $service): bool
    {
        if ($service === 'nginx') {
            return true;
        }

        return preg_match('/^php(\d+\.\d+)?-fpm$/', $service) === 1;
    }
}
