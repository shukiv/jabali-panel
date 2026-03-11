<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Services\Agent\AgentClient;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Notifications\Notification;
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
            'nginx',
            'mariadb', // can fall back to mysql
            ...array_keys($this->detectPhpFpmServices()),
            'php-fpm',
            'postfix',
            'dovecot',
            'named', // can fall back to bind9
            'redis-server',
            'fail2ban',
        ];

        $labels = [
            'nginx' => 'Nginx',
            'mariadb' => 'MariaDB',
            'mysql' => 'MariaDB',
            'php-fpm' => 'PHP-FPM',
            'postfix' => 'Postfix',
            'dovecot' => 'Dovecot',
            'named' => 'BIND DNS',
            'bind9' => 'BIND DNS',
            'redis-server' => 'Redis',
            'fail2ban' => 'Fail2Ban',
        ];

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
            $agent = new AgentClient(timeout: 5);
            $result = $agent->send('service.list', ['services' => $servicesToCheck]);

            if ($result['success'] ?? false) {
                $statuses = is_array($result['services'] ?? null) ? $result['services'] : [];
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
            'named' => 'bind9',
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
                        try {
                            $agent = new AgentClient;
                            $result = $agent->send('service.start', ['service' => $record['key']]);

                            if ($result['success'] ?? false) {
                                Notification::make()
                                    ->title(__('Service started'))
                                    ->body(__(':service has been started successfully.', ['service' => $record['name']]))
                                    ->success()
                                    ->send();

                                $this->loadServices();
                                $this->resetTable();
                            } else {
                                throw new \Exception($result['error'] ?? __('Failed to start service'));
                            }
                        } catch (\Exception $e) {
                            Notification::make()
                                ->title(__('Failed to start service'))
                                ->body($e->getMessage())
                                ->danger()
                                ->send();
                        }
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
                        try {
                            $agent = new AgentClient;
                            $action = $this->shouldReloadService($record['key']) ? 'reload' : 'restart';
                            $result = $agent->send("service.{$action}", ['service' => $record['key']]);

                            if ($result['success'] ?? false) {
                                $verb = $action === 'reload' ? __('reloaded') : __('restarted');
                                Notification::make()
                                    ->title($action === 'reload' ? __('Service reloaded') : __('Service restarted'))
                                    ->body(__(':service has been :action successfully.', ['service' => $record['name'], 'action' => $verb]))
                                    ->success()
                                    ->send();

                                sleep(1);
                                $this->loadServices();
                                $this->resetTable();
                            } else {
                                throw new \Exception($result['error'] ?? ($action === 'reload' ? __('Failed to reload service') : __('Failed to restart service')));
                            }
                        } catch (\Exception $e) {
                            Notification::make()
                                ->title($this->shouldReloadService($record['key']) ? __('Failed to reload service') : __('Failed to restart service'))
                                ->body($e->getMessage())
                                ->danger()
                                ->send();
                        }
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
