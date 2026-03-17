<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Services\Agent\AgentClient;
use App\Support\SafeError;
use BackedEnum;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Support\Icons\Heroicon;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;

class EmailQueue extends Page implements HasActions, HasTable
{
    use InteractsWithActions;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedQueueList;

    protected static ?int $navigationSort = null;

    protected static ?string $slug = 'email-queue';

    protected static bool $shouldRegisterNavigation = false;

    protected string $view = 'filament.admin.pages.email-queue';

    public array $queueItems = [];

    protected bool $queueLoaded = false;

    public function getTitle(): string|Htmlable
    {
        return __('Email Queue Manager');
    }

    public static function getNavigationLabel(): string
    {
        return __('Email Queue');
    }

    public function mount(): void
    {
        $this->redirect(EmailLogs::getUrl());
    }

    protected function getAgent(): AgentClient
    {
        return app(AgentClient::class);
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
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }

        if ($refreshTable) {
            $this->resetTable();
        }
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(function () {
                if (! $this->queueLoaded) {
                    $this->loadQueue(false);
                }

                return collect($this->queueItems)
                    ->mapWithKeys(function (array $record, int $index): array {
                        $key = $record['id'] ?? (string) $index;

                        return [$key !== '' ? $key : (string) $index => $record];
                    })
                    ->all();
            })
            ->columns([
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
                    ->formatStateUsing(function (array $record): string {
                        $recipients = $record['recipients'] ?? [];
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
                    ->formatStateUsing(fn (array $record): string => $record['size'] ?? ''),
                TextColumn::make('status')
                    ->label(__('Status'))
                    ->wrap(),
            ])
            ->recordActions([
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
                            Notification::make()->title(__('Retry failed'))->body(SafeError::message($e))->danger()->send();
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
                            Notification::make()->title(__('Delete failed'))->body(SafeError::message($e))->danger()->send();
                        }
                    }),
            ])
            ->emptyStateHeading(__('Mail queue is empty'))
            ->emptyStateDescription(__('No deferred messages found.'))
            ->headerActions([
                Action::make('refresh')
                    ->label(__('Refresh'))
                    ->icon('heroicon-o-arrow-path')
                    ->action(fn () => $this->loadQueue()),
            ]);
    }
}
