<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Services\Agent\AgentClient;
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
        $serviceList = [
            'nginx' => 'Nginx',
            'mariadb' => 'MariaDB',
            'php-fpm' => 'PHP-FPM',
            'postfix' => 'Postfix',
            'dovecot' => 'Dovecot',
            'named' => 'BIND DNS',
            'redis-server' => 'Redis',
            'fail2ban' => 'Fail2Ban',
        ];

        $result = [];
        foreach ($serviceList as $service => $name) {
            $status = $this->checkServiceStatus($service);
            if ($status !== null) {
                $result[] = [
                    'key' => $service,
                    'name' => $name,
                    'active' => $status,
                ];
            }
        }

        $this->services = $result;
    }

    protected function checkServiceStatus(string $service): ?bool
    {
        exec("systemctl list-unit-files {$service}.service 2>/dev/null | grep -q {$service}", $output, $exists);
        if ($exists !== 0) {
            if ($service === 'mariadb') {
                exec('systemctl list-unit-files mysql.service 2>/dev/null | grep -q mysql', $output, $mysqlExists);
                if ($mysqlExists !== 0) {
                    return null;
                }
                $service = 'mysql';
            } elseif ($service === 'php-fpm') {
                exec("systemctl list-unit-files 'php*-fpm.service' 2>/dev/null | grep -oP 'php[0-9.]+-fpm'", $phpOutput);
                if (empty($phpOutput)) {
                    return null;
                }
                $service = trim($phpOutput[0]);
            } else {
                return null;
            }
        }

        exec("systemctl is-active {$service} 2>/dev/null", $statusOutput, $code);

        return $code === 0;
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
