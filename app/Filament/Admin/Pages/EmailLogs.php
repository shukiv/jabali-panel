<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Services\Agent\AgentClient;
use BackedEnum;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Tabs;
use Filament\Schemas\Components\Tabs\Tab;
use Filament\Schemas\Components\View;
use Filament\Schemas\Schema;
use Filament\Support\Icons\Heroicon;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Pagination\LengthAwarePaginator;
use Illuminate\Support\Str;
use Livewire\Attributes\Url;

class EmailLogs extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedInbox;

    protected static ?int $navigationSort = 14;

    protected static ?string $slug = 'email-logs';

    protected string $view = 'filament.admin.pages.email-logs';

    #[Url(as: 'tab')]
    public string $viewMode = 'logs';

    public array $logs = [];

    public array $queueItems = [];

    protected ?AgentClient $agent = null;

    protected bool $logsLoaded = false;

    protected bool $queueLoaded = false;

    public function getTitle(): string|Htmlable
    {
        return __('Email Logs');
    }

    public static function getNavigationLabel(): string
    {
        return __('Email Logs');
    }

    public function mount(): void
    {
        $this->viewMode = $this->normalizeViewMode($this->viewMode);

        if ($this->viewMode === 'queue') {
            $this->loadQueue(false);
        } else {
            $this->loadLogs(false);
        }
    }

    protected function getForms(): array
    {
        return ['logsForm'];
    }

    public function logsForm(Schema $schema): Schema
    {
        return $schema->schema([
            Tabs::make(__('Email Sections'))
                ->contained()
                ->livewireProperty('viewMode')
                ->tabs([
                    'logs' => Tab::make(__('Logs'))
                        ->icon('heroicon-o-document-text')
                        ->schema([
                            View::make('filament.admin.pages.email-logs-tab-table'),
                        ]),
                    'queue' => Tab::make(__('Queue'))
                        ->icon('heroicon-o-queue-list')
                        ->schema([
                            View::make('filament.admin.pages.email-logs-tab-table'),
                        ]),
                ]),
        ]);
    }

    protected function normalizeViewMode(?string $mode): string
    {
        return match ($mode) {
            'logs', 'queue' => (string) $mode,
            default => 'logs',
        };
    }

    public function updatedViewMode(): void
    {
        $mode = $this->normalizeViewMode($this->viewMode);
        if ($this->viewMode !== $mode) {
            $this->viewMode = $mode;
            return;
        }

        if ($mode === 'queue') {
            $this->loadQueue(false);
        } else {
            $this->loadLogs(false);
        }

        $this->resetTable();
    }

    protected function getAgent(): AgentClient
    {
        return $this->agent ??= new AgentClient;
    }

    public function loadLogs(bool $refreshTable = true): void
    {
        try {
            $result = $this->getAgent()->send('email.get_logs', [
                'limit' => 200,
            ]);

            $this->logs = $result['logs'] ?? [];
            $this->logsLoaded = true;
        } catch (\Exception $e) {
            $this->logs = [];
            $this->logsLoaded = true;

            Notification::make()
                ->title(__('Failed to load email logs'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }

        if ($refreshTable) {
            $this->resetTable();
        }
    }

    public function loadQueue(bool $refreshTable = true): void
    {
        try {
            $result = $this->getAgent()->send('mail.queue_list');
            $this->queueItems = $result['queue'] ?? [];
            $this->queueLoaded = true;
        } catch (\Exception $e) {
            $this->queueItems = [];
            $this->queueLoaded = true;
            Notification::make()
                ->title(__('Failed to load mail queue'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }

        if ($refreshTable) {
            $this->resetTable();
        }
    }

    public function setViewMode(string $mode): void
    {
        $mode = $this->normalizeViewMode($mode);
        if ($this->viewMode === $mode) {
            return;
        }

        $this->viewMode = $mode;
    }

    public function table(Table $table): Table
    {
        return $table
            ->paginated([25, 50, 100])
            ->defaultPaginationPageOption(25)
            ->records(function (?array $filters, ?string $search, int|string $page, int|string $recordsPerPage, ?string $sortColumn, ?string $sortDirection) {
                if ($this->viewMode === 'queue') {
                    if (! $this->queueLoaded) {
                        $this->loadQueue(false);
                    }
                    $records = $this->queueItems;
                } else {
                    if (! $this->logsLoaded) {
                        $this->loadLogs(false);
                    }
                    $records = $this->logs;
                }

                $records = $this->filterRecords($records, $search);
                $records = $this->sortRecords($records, $sortColumn, $sortDirection);

                return $this->paginateRecords($records, $page, $recordsPerPage);
            })
            ->columns($this->viewMode === 'queue' ? $this->getQueueColumns() : $this->getLogColumns())
            ->recordActions($this->viewMode === 'queue' ? $this->getQueueActions() : [])
            ->emptyStateHeading($this->viewMode === 'queue' ? __('Mail queue is empty') : __('No email logs found'))
            ->emptyStateDescription($this->viewMode === 'queue' ? __('No deferred messages found.') : __('Mail logs are empty or unavailable.'))
            ->headerActions([
                Action::make('refresh')
                    ->label(__('Refresh'))
                    ->icon('heroicon-o-arrow-path')
                    ->action(function (): void {
                        if ($this->viewMode === 'queue') {
                            $this->loadQueue();
                        } else {
                            $this->loadLogs();
                        }
                    }),
            ]);
    }

    protected function getLogColumns(): array
    {
        return [
            TextColumn::make('timestamp')
                ->label(__('Time'))
                ->formatStateUsing(function (array $record): string {
                    $timestamp = (int) ($record['timestamp'] ?? 0);

                    return $timestamp > 0 ? date('Y-m-d H:i:s', $timestamp) : '';
                })
                ->sortable(),
            TextColumn::make('queue_id')
                ->label(__('Queue ID'))
                ->fontFamily('mono')
                ->copyable()
                ->toggleable(),
            TextColumn::make('component')
                ->label(__('Component'))
                ->toggleable(isToggledHiddenByDefault: true),
            TextColumn::make('from')
                ->label(__('From'))
                ->wrap()
                ->searchable(),
            TextColumn::make('to')
                ->label(__('To'))
                ->wrap()
                ->searchable(),
            TextColumn::make('status')
                ->label(__('Status'))
                ->badge()
                ->formatStateUsing(fn (array $record): string => (string) ($record['status'] ?? 'unknown')),
            TextColumn::make('relay')
                ->label(__('Relay'))
                ->toggleable(),
            TextColumn::make('message')
                ->label(__('Details'))
                ->wrap()
                ->limit(80)
                ->toggleable(isToggledHiddenByDefault: true),
        ];
    }

    protected function getQueueColumns(): array
    {
        return [
            TextColumn::make('id')
                ->label(__('Queue ID'))
                ->fontFamily('mono')
                ->copyable(),
            TextColumn::make('arrival')
                ->label(__('Arrival')),
            TextColumn::make('sender')
                ->label(__('Sender'))
                ->wrap()
                ->searchable(),
            TextColumn::make('recipients')
                ->label(__('Recipients'))
                ->formatStateUsing(function ($state): string {
                    if (is_array($state)) {
                        $recipients = $state;
                    } elseif ($state === null || $state === '') {
                        $recipients = [];
                    } else {
                        $recipients = [(string) $state];
                    }

                    $recipients = array_values(array_filter(array_map(function ($recipient): ?string {
                        if (is_array($recipient)) {
                            return (string) ($recipient['address']
                                ?? $recipient['recipient']
                                ?? $recipient['email']
                                ?? $recipient[0]
                                ?? '');
                        }

                        return $recipient === null ? null : (string) $recipient;
                    }, $recipients), static fn (?string $value): bool => $value !== null && $value !== ''));

                    if (empty($recipients)) {
                        return __('Unknown');
                    }

                    $first = $recipients[0] ?? '';
                    $count = count($recipients);

                    return $count > 1 ? $first.' +'.($count - 1) : $first;
                })
                ->wrap(),
            TextColumn::make('size')
                ->label(__('Size'))
                ->formatStateUsing(fn ($state): string => is_scalar($state) ? (string) $state : ''),
            TextColumn::make('status')
                ->label(__('Status'))
                ->wrap(),
        ];
    }

    protected function getQueueActions(): array
    {
        return [
            Action::make('retry')
                ->label(__('Retry'))
                ->icon('heroicon-o-arrow-path')
                ->color('info')
                ->action(function (array $record): void {
                    try {
                        $result = $this->getAgent()->send('mail.queue_retry', ['id' => $record['id'] ?? '']);
                        if ($result['success'] ?? false) {
                            Notification::make()->title(__('Message retried'))->success()->send();
                            $this->loadQueue();
                        } else {
                            throw new \Exception($result['error'] ?? __('Failed to retry message'));
                        }
                    } catch (\Exception $e) {
                        Notification::make()->title(__('Retry failed'))->body($e->getMessage())->danger()->send();
                    }
                }),
            Action::make('delete')
                ->label(__('Delete'))
                ->icon('heroicon-o-trash')
                ->color('danger')
                ->requiresConfirmation()
                ->action(function (array $record): void {
                    try {
                        $result = $this->getAgent()->send('mail.queue_delete', ['id' => $record['id'] ?? '']);
                        if ($result['success'] ?? false) {
                            Notification::make()->title(__('Message deleted'))->success()->send();
                            $this->loadQueue();
                        } else {
                            throw new \Exception($result['error'] ?? __('Failed to delete message'));
                        }
                    } catch (\Exception $e) {
                        Notification::make()->title(__('Delete failed'))->body($e->getMessage())->danger()->send();
                    }
                }),
        ];
    }

    protected function filterRecords(array $records, ?string $search): array
    {
        $search = trim((string) $search);
        if ($search === '') {
            return $records;
        }

        $search = Str::lower($search);

        return array_values(array_filter($records, function (array $record) use ($search): bool {
            if ($this->viewMode === 'queue') {
                $recipients = $record['recipients'] ?? [];
                $haystack = implode(' ', array_filter([
                    (string) ($record['id'] ?? ''),
                    (string) ($record['sender'] ?? ''),
                    implode(' ', $recipients),
                    (string) ($record['status'] ?? ''),
                ]));
            } else {
                $haystack = implode(' ', array_filter([
                    (string) ($record['queue_id'] ?? ''),
                    (string) ($record['from'] ?? ''),
                    (string) ($record['to'] ?? ''),
                    (string) ($record['status'] ?? ''),
                    (string) ($record['message'] ?? ''),
                    (string) ($record['component'] ?? ''),
                ]));
            }

            return str_contains(Str::lower($haystack), $search);
        }));
    }

    protected function sortRecords(array $records, ?string $sortColumn, ?string $sortDirection): array
    {
        $direction = $sortDirection === 'asc' ? 'asc' : 'desc';

        if (! $sortColumn) {
            return $records;
        }

        usort($records, function (array $a, array $b) use ($sortColumn, $direction): int {
            $aValue = $a[$sortColumn] ?? null;
            $bValue = $b[$sortColumn] ?? null;

            if (is_numeric($aValue) && is_numeric($bValue)) {
                $result = (float) $aValue <=> (float) $bValue;
            } else {
                $result = strcmp((string) $aValue, (string) $bValue);
            }

            return $direction === 'asc' ? $result : -$result;
        });

        return $records;
    }

    protected function paginateRecords(array $records, int|string $page, int|string $recordsPerPage): LengthAwarePaginator
    {
        $page = max(1, (int) $page);
        $perPage = max(1, (int) $recordsPerPage);

        $total = count($records);
        $items = array_slice($records, ($page - 1) * $perPage, $perPage);

        return new LengthAwarePaginator(
            $items,
            $total,
            $perPage,
            $page,
            [
                'path' => request()->url(),
                'pageName' => $this->getTablePaginationPageName(),
            ],
        );
    }
}
