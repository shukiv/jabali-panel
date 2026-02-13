<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Filament\Admin\Widgets\ServerChartsWidget;
use App\Filament\Admin\Widgets\ServerInfoWidget;
use App\Models\ServerProcess;
use App\Services\Agent\AgentClient;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\ActionGroup;
use Filament\Actions\BulkAction;
use Filament\Forms\Components\Radio;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Support\Enums\FontFamily;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Filters\SelectFilter;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Database\Eloquent\Collection;

class ServerStatus extends Page implements HasTable
{
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-chart-bar';

    protected static ?int $navigationSort = 4;

    protected string $view = 'filament.admin.pages.server-status';

    public array $overview = [];

    public array $disk = [];

    public array $network = [];

    public int $processTotal = 0;

    public int $processRunning = 0;

    public string $refreshInterval = '10s';

    public ?string $lastUpdated = null;

    public int $processLimit = 50;

    protected ?AgentClient $agent = null;

    public static function getNavigationLabel(): string
    {
        return __('Server Status');
    }

    public function getTitle(): string|Htmlable
    {
        return __('Server Status');
    }

    protected function getHeaderWidgets(): array
    {
        return [
            ServerChartsWidget::class,
            ServerInfoWidget::class,
        ];
    }

    protected function getHeaderActions(): array
    {
        return [];
    }

    public function setProcessLimit(int $limit): void
    {
        $this->processLimit = $limit;
        $this->loadMetrics();
    }

    public function setRefreshInterval(string $interval): void
    {
        $this->refreshInterval = $interval;
        $this->dispatch('refresh-interval-changed', interval: $interval);
    }

    public function getAgent(): AgentClient
    {
        if ($this->agent === null) {
            $this->agent = new AgentClient;
        }

        return $this->agent;
    }

    public function mount(): void
    {
        $this->loadMetrics();
    }

    public function loadMetrics(): void
    {
        try {
            $this->overview = $this->getAgent()->metricsOverview();
            $this->disk = $this->getAgent()->metricsDisk()['data'] ?? [];
            $this->network = $this->getAgent()->metricsNetwork()['data'] ?? [];

            // Get processes with configurable limit (0 = all)
            $limit = $this->processLimit === 0 ? 500 : $this->processLimit;
            $processData = $this->getAgent()->metricsProcesses($limit)['data'] ?? [];
            $this->processTotal = $processData['total'] ?? 0;
            $this->processRunning = $processData['running'] ?? 0;

            if (! empty($processData['top'])) {
                ServerProcess::captureProcesses($processData['top'], $this->processTotal);
                $this->flushCachedTableRecords();
            }

            $this->lastUpdated = now()->format('H:i:s');
        } catch (Exception $e) {
            $this->overview = ['error' => $e->getMessage()];
        }
    }

    public function table(Table $table): Table
    {
        return $table
            ->query(ServerProcess::latestBatch()->orderBy('cpu', 'desc'))
            ->columns([
                TextColumn::make('pid')
                    ->label(__('PID'))
                    ->fontFamily(FontFamily::Mono)
                    ->sortable()
                    ->searchable()
                    ->copyable()
                    ->copyMessage(__('PID copied'))
                    ->toggleable(),
                TextColumn::make('user')
                    ->label(__('User'))
                    ->badge()
                    ->color(fn ($state) => match ($state) {
                        'root' => 'danger',
                        'www-data', 'nginx', 'apache' => 'info',
                        'mysql', 'postgres' => 'warning',
                        default => 'gray',
                    })
                    ->sortable()
                    ->searchable()
                    ->toggleable(),
                TextColumn::make('command')
                    ->label(__('Command'))
                    ->limit(40)
                    ->tooltip(fn (ServerProcess $record) => $record->command)
                    ->searchable()
                    ->wrap()
                    ->toggleable(),
                TextColumn::make('cpu')
                    ->label(__('CPU %'))
                    ->suffix('%')
                    ->badge()
                    ->color(fn ($state) => $state > 50 ? 'danger' : ($state > 20 ? 'warning' : 'gray'))
                    ->sortable()
                    ->toggleable(),
                TextColumn::make('memory')
                    ->label(__('Mem %'))
                    ->suffix('%')
                    ->badge()
                    ->color(fn ($state) => $state > 50 ? 'danger' : ($state > 20 ? 'warning' : 'gray'))
                    ->sortable()
                    ->toggleable(),
            ])
            ->filters([
                SelectFilter::make('user')
                    ->label(__('User'))
                    ->options(fn () => ServerProcess::latestBatch()
                        ->distinct()
                        ->pluck('user', 'user')
                        ->toArray()
                    )
                    ->searchable()
                    ->preload(),
            ])
            ->recordActions([
                Action::make('kill')
                    ->label(__('Kill'))
                    ->icon('heroicon-o-x-circle')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(__('Kill Process'))
                    ->modalDescription(fn (ServerProcess $record) => __('Are you sure you want to kill process :pid (:command)?', [
                        'pid' => $record->pid,
                        'command' => substr($record->command, 0, 50),
                    ]))
                    ->modalIcon('heroicon-o-exclamation-triangle')
                    ->modalIconColor('danger')
                    ->form([
                        Radio::make('signal')
                            ->label(__('Signal'))
                            ->options([
                                '15' => __('SIGTERM (15) - Graceful termination'),
                                '9' => __('SIGKILL (9) - Force kill'),
                                '1' => __('SIGHUP (1) - Hangup/Reload'),
                            ])
                            ->default('15')
                            ->required()
                            ->helperText(__('SIGTERM allows the process to clean up. SIGKILL forces immediate termination.')),
                    ])
                    ->action(fn (ServerProcess $record, array $data) => $this->killProcess($record, (int) $data['signal'])),
            ])
            ->selectable()
            ->bulkActions([
                BulkAction::make('killSelected')
                    ->label(__('Kill Selected'))
                    ->icon('heroicon-o-x-circle')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(__('Kill Selected Processes'))
                    ->modalDescription(__('Are you sure you want to kill the selected processes? This action cannot be undone.'))
                    ->modalIcon('heroicon-o-exclamation-triangle')
                    ->modalIconColor('danger')
                    ->form([
                        Radio::make('signal')
                            ->label(__('Signal'))
                            ->options([
                                '15' => __('SIGTERM (15) - Graceful termination'),
                                '9' => __('SIGKILL (9) - Force kill'),
                            ])
                            ->default('15')
                            ->required(),
                    ])
                    ->action(fn (Collection $records, array $data) => $this->killProcesses($records, (int) $data['signal']))
                    ->deselectRecordsAfterCompletion(),
            ])
            ->headerActions([
                ActionGroup::make([
                    Action::make('limit25')
                        ->label(__('Show 25 processes'))
                        ->icon(fn () => $this->processLimit === 25 ? 'heroicon-o-check' : null)
                        ->action(fn () => $this->setProcessLimit(25)),
                    Action::make('limit50')
                        ->label(__('Show 50 processes'))
                        ->icon(fn () => $this->processLimit === 50 ? 'heroicon-o-check' : null)
                        ->action(fn () => $this->setProcessLimit(50)),
                    Action::make('limit100')
                        ->label(__('Show 100 processes'))
                        ->icon(fn () => $this->processLimit === 100 ? 'heroicon-o-check' : null)
                        ->action(fn () => $this->setProcessLimit(100)),
                    Action::make('limitAll')
                        ->label(__('Show all processes'))
                        ->icon(fn () => $this->processLimit === 0 ? 'heroicon-o-check' : null)
                        ->action(fn () => $this->setProcessLimit(0)),
                ])
                    ->label(fn () => __('Process Limit: :limit', ['limit' => $this->processLimit === 0 ? __('All') : $this->processLimit]))
                    ->icon('heroicon-o-queue-list')
                    ->color('gray')
                    ->button(),
                Action::make('refreshProcesses')
                    ->label(fn () => $this->lastUpdated ? __('Refresh (:time)', ['time' => $this->lastUpdated]) : __('Refresh'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('gray')
                    ->action(fn () => $this->loadMetrics()),
            ])
            ->heading(__('Process List'))
            ->description(__(':total total processes, :running running', ['total' => $this->processTotal, 'running' => $this->processRunning]))
            ->paginated([10, 25, 50, 100])
            ->defaultPaginationPageOption(25)
            ->poll($this->refreshInterval === 'off' ? null : $this->refreshInterval)
            ->striped()
            ->defaultSort('cpu', 'desc')
            ->persistFiltersInSession()
            ->persistSearchInSession();
    }

    public function killProcess(ServerProcess $process, int $signal = 15): void
    {
        try {
            $result = $this->getAgent()->send('system.kill_process', [
                'pid' => $process->pid,
                'signal' => $signal,
            ]);

            if ($result['success'] ?? false) {
                Notification::make()
                    ->title(__('Process killed'))
                    ->body(__('Process :pid has been terminated with signal :signal.', [
                        'pid' => $process->pid,
                        'signal' => $signal,
                    ]))
                    ->success()
                    ->send();

                // Refresh the process list
                $this->loadMetrics();
            } else {
                throw new Exception($result['error'] ?? __('Unknown error'));
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Failed to kill process'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function killProcesses(Collection $records, int $signal = 15): void
    {
        $killed = 0;
        $failed = 0;

        foreach ($records as $process) {
            try {
                $result = $this->getAgent()->send('system.kill_process', [
                    'pid' => $process->pid,
                    'signal' => $signal,
                ]);

                if ($result['success'] ?? false) {
                    $killed++;
                } else {
                    $failed++;
                }
            } catch (Exception $e) {
                $failed++;
            }
        }

        if ($killed > 0) {
            Notification::make()
                ->title(__('Processes killed'))
                ->body(__(':count process(es) terminated successfully.', ['count' => $killed]))
                ->success()
                ->send();
        }

        if ($failed > 0) {
            Notification::make()
                ->title(__('Some processes failed'))
                ->body(__(':count process(es) could not be killed.', ['count' => $failed]))
                ->warning()
                ->send();
        }

        // Refresh the process list
        $this->loadMetrics();
    }

    public function refresh(): void
    {
        $this->loadMetrics();
    }

    public function getListeners(): array
    {
        return [
            'refresh' => 'loadMetrics',
        ];
    }
}
