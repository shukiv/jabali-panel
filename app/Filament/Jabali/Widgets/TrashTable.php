<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Widgets;

use App\Services\Agent\AgentClient;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Notifications\Notification;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Support\Facades\Auth;
use Livewire\Component;
use Exception;

class TrashTable extends Component implements HasTable, HasSchemas, HasActions
{
    use InteractsWithTable;
    use InteractsWithSchemas;
    use InteractsWithActions;

    protected static string $view = 'filament.jabali.widgets.trash-table';

    public array $trashItems = [];

    protected ?AgentClient $agent = null;

    public function mount(): void
    {
        $this->loadTrashItems();
    }

    public function getAgent(): AgentClient
    {
        if ($this->agent === null) {
            $this->agent = new AgentClient();
        }
        return $this->agent;
    }

    public function getUsername(): string
    {
        return Auth::user()->username;
    }

    public function loadTrashItems(): void
    {
        try {
            $result = $this->getAgent()->fileListTrash($this->getUsername());
            $this->trashItems = $result['items'] ?? [];
        } catch (Exception) {
            $this->trashItems = [];
        }
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->trashItems)
            ->columns([
                TextColumn::make('name')
                    ->label(__('Name'))
                    ->icon(fn (array $record): string => $record['is_dir'] ? 'heroicon-o-folder' : 'heroicon-o-document')
                    ->iconColor(fn (array $record): string => $record['is_dir'] ? 'warning' : 'primary')
                    ->weight('medium')
                    ->searchable(),
                TextColumn::make('trashed_at')
                    ->label(__('Deleted'))
                    ->formatStateUsing(fn (array $record): string => date('M d, Y H:i', $record['trashed_at']))
                    ->color('gray'),
            ])
            ->recordActions([
                Action::make('restore')
                    ->label(__('Restore'))
                    ->icon('heroicon-o-arrow-uturn-left')
                    ->color('success')
                    ->action(function (array $record): void {
                        $this->restoreItem($record['trash_name']);
                    }),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-x-mark')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(__('Delete Permanently'))
                    ->modalDescription(__('This item will be permanently deleted. This cannot be undone.'))
                    ->modalSubmitActionLabel(__('Delete'))
                    ->action(function (array $record): void {
                        $this->deleteItem($record['trash_name']);
                    }),
            ])
            ->headerActions([
                Action::make('emptyTrash')
                    ->label(__('Empty Trash'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(__('Empty Trash'))
                    ->modalDescription(__('All items in trash will be permanently deleted. This cannot be undone.'))
                    ->modalSubmitActionLabel(__('Empty Trash'))
                    ->visible(fn () => count($this->trashItems) > 0)
                    ->action(function (): void {
                        $this->emptyTrash();
                    }),
                Action::make('refresh')
                    ->label(__('Refresh'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('gray')
                    ->action(function (): void {
                        $this->loadTrashItems();
                        $this->resetTable();
                    }),
            ])
            ->emptyStateHeading(__('Trash is empty'))
            ->emptyStateDescription(__('Deleted items will appear here'))
            ->emptyStateIcon('heroicon-o-trash')
            ->striped();
    }

    public function getTableRecordKey(Model|array $record): string
    {
        return is_array($record) ? $record['trash_name'] : $record->getKey();
    }

    public function restoreItem(string $trashName): void
    {
        try {
            $result = $this->getAgent()->fileRestore($this->getUsername(), $trashName);
            Notification::make()
                ->title(__('Restored'))
                ->body(__('Restored to: :path', ['path' => $result['restored_path'] ?? '']))
                ->success()
                ->send();
            $this->loadTrashItems();
            $this->resetTable();
            $this->dispatch('trash-updated');
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function deleteItem(string $trashName): void
    {
        try {
            $trashPath = ".trash/$trashName";
            $this->getAgent()->fileDelete($this->getUsername(), $trashPath);
            Notification::make()
                ->title(__('Permanently deleted'))
                ->success()
                ->send();
            $this->loadTrashItems();
            $this->resetTable();
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function emptyTrash(): void
    {
        try {
            $result = $this->getAgent()->fileEmptyTrash($this->getUsername());
            Notification::make()
                ->title(__('Trash emptied'))
                ->body(__(':count items deleted', ['count' => $result['deleted'] ?? 0]))
                ->success()
                ->send();
            $this->loadTrashItems();
            $this->resetTable();
            $this->dispatch('trash-updated');
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function render()
    {
        return view(static::$view);
    }
}
