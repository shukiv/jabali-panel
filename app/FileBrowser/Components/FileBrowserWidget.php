<?php

declare(strict_types=1);

namespace App\FileBrowser\Components;

use App\FileBrowser\Adapters\FileBrowserAdapter;
use App\FileBrowser\Support\PathSanitizer;
use App\Support\Formatter;
use App\Support\SafeError;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Pagination\LengthAwarePaginator;
use Illuminate\Support\Str;
use Livewire\Component;

/**
 * Embeddable file browser widget.
 *
 * Usage:
 *
 *   @livewire('file-browser-widget', [
 *       'adapter' => $myAdapter,          // FileBrowserAdapter instance
 *       'readOnly' => true,               // hide all write actions
 *       'selectable' => true,             // show checkboxes, dispatch selection events
 *       'disabledFeatures' => ['view'],   // hide specific actions
 *   ])
 *
 * Events dispatched:
 *   'file-browser-selection' => ['paths' => [...]]  (when selectable and selection changes)
 */
class FileBrowserWidget extends Component implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    public string $currentPath = '';

    public bool $showHidden = false;

    public array $items = [];

    public bool $readOnly = false;

    public bool $selectable = false;

    /** @var list<string> */
    public array $disabledFeatures = [];

    protected FileBrowserAdapter $adapterInstance;

    public function mount(
        FileBrowserAdapter $adapter,
        bool $readOnly = false,
        bool $selectable = false,
        array $disabledFeatures = [],
        string $path = '',
    ): void {
        $this->adapterInstance = $adapter;
        $this->readOnly = $readOnly;
        $this->selectable = $selectable;
        $this->disabledFeatures = $disabledFeatures;

        if ($path !== '') {
            try {
                $this->currentPath = PathSanitizer::clean($path);
            } catch (Exception) {
                $this->currentPath = '';
            }
        }

        $this->loadDirectory();
    }

    public function getAdapter(): FileBrowserAdapter
    {
        return $this->adapterInstance;
    }

    // ─── Feature Checks ───────────────────────────────────────

    private const WRITE_FEATURES = [
        'upload', 'edit', 'trash', 'extract', 'permissions',
        'rename', 'newFolder', 'newFile', 'move', 'copy',
    ];

    protected function featureEnabled(string $feature): bool
    {
        if ($this->readOnly && in_array($feature, self::WRITE_FEATURES, true)) {
            return false;
        }

        return ! in_array($feature, $this->disabledFeatures, true);
    }

    // ─── Directory Operations ─────────────────────────────────

    public function loadDirectory(?string $path = null): void
    {
        if ($path !== null) {
            $this->currentPath = PathSanitizer::clean($path);
        }

        try {
            $result = $this->getAdapter()->files()->list($this->currentPath, $this->showHidden);
            $items = $result['items'] ?? [];

            if (! empty($this->currentPath)) {
                $parentPath = dirname($this->currentPath);
                if ($parentPath === '.') {
                    $parentPath = '';
                }
                array_unshift($items, [
                    'name' => '..',
                    'path' => $parentPath,
                    'is_dir' => true,
                    'size' => null,
                    'modified' => time(),
                    'permissions' => '----',
                    'is_parent' => true,
                ]);
            }

            $this->items = $items;
        } catch (Exception $e) {
            $this->items = [];
            \Filament\Notifications\Notification::make()
                ->title(__('Error loading directory'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    public function navigateTo(string $path): void
    {
        try {
            $this->currentPath = PathSanitizer::clean($path);
            $this->loadDirectory();
            $this->resetTable();
        } catch (Exception $e) {
            \Filament\Notifications\Notification::make()
                ->title(__('Invalid path'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    public function getPathBreadcrumbs(): array
    {
        $breadcrumbs = [['name' => __('Home'), 'path' => '']];

        if (! empty($this->currentPath)) {
            $parts = explode('/', $this->currentPath);
            $path = '';
            foreach ($parts as $part) {
                $path = empty($path) ? $part : $path.'/'.$part;
                $breadcrumbs[] = ['name' => $part, 'path' => $path];
            }
        }

        return $breadcrumbs;
    }

    // ─── Table ────────────────────────────────────────────────

    public function table(Table $table): Table
    {
        return $table
            ->paginated([100, 250, 500])
            ->defaultPaginationPageOption(100)
            ->records(function (?array $filters, ?string $search, int|string $page, int|string $recordsPerPage, ?string $sortColumn, ?string $sortDirection) {
                $records = collect($this->items)
                    ->mapWithKeys(function (array $item, int $index): array {
                        $key = $item['path'] ?? $item['name'] ?? (string) $index;

                        return [$key => $item];
                    })
                    ->all();
                $records = $this->filterRecords($records, $search);
                $records = $this->sortRecords($records, $sortColumn, $sortDirection);

                return $this->paginateRecords($records, $page, $recordsPerPage);
            })
            ->columns([
                TextColumn::make('name')
                    ->label(__('Name'))
                    ->icon(fn (array $record): string => match (true) {
                        ($record['is_parent'] ?? false) => 'heroicon-o-arrow-uturn-left',
                        $record['is_dir'] => 'heroicon-o-folder',
                        default => 'heroicon-o-document',
                    })
                    ->iconColor(fn (array $record): string => match (true) {
                        ($record['is_parent'] ?? false) => 'gray',
                        $record['is_dir'] => 'warning',
                        default => 'info',
                    })
                    ->weight('medium')
                    ->formatStateUsing(function (string $state, array $record): \Illuminate\Support\HtmlString {
                        $name = e($state);
                        if ($record['is_dir'] ?? false) {
                            $path = e($record['path'] ?? '');

                            return new \Illuminate\Support\HtmlString(
                                '<span x-data="{ loading: false }" class="inline-flex items-center gap-1">'
                                ."<button type=\"button\" wire:click=\"navigateTo('{$path}')\" @click=\"loading = true\" class=\"text-left hover:underline cursor-pointer\" x-bind:class=\"loading && 'opacity-50'\">{$name}</button>"
                                .'<span x-show="loading" x-cloak class="fi-loading-indicator">'
                                .'<svg viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg" class="h-4 w-4" style="animation: spin 1s linear infinite"><path opacity=".75" d="M12 2a10 10 0 0 1 10 10h-2a8 8 0 0 0-8-8V2Z" fill="currentColor"/></svg>'
                                .'</span>'
                                .'</span>'
                            );
                        }

                        return new \Illuminate\Support\HtmlString($name);
                    })
                    ->html()
                    ->searchable(),
                TextColumn::make('size')
                    ->label(__('Size'))
                    ->formatStateUsing(fn (array $record): string => $record['is_dir'] ? '—' : Formatter::bytes($record['size']))
                    ->color('gray'),
                TextColumn::make('modified')
                    ->label(__('Modified'))
                    ->formatStateUsing(fn (array $record): string => date('M d, Y H:i', $record['modified']))
                    ->color('gray'),
            ])
            ->recordActions([
                Action::make('download')
                    ->label(__('Download'))
                    ->icon('heroicon-o-arrow-down-tray')
                    ->color('gray')
                    ->visible(fn (array $record): bool => ! $record['is_dir'] && $this->featureEnabled('download'))
                    ->action(fn (array $record) => $this->downloadFile($record['path'])),
            ])
            ->headerActions([
                Action::make('toggleHidden')
                    ->label($this->showHidden ? __('Hide Hidden') : __('Show Hidden'))
                    ->icon($this->showHidden ? 'heroicon-o-eye-slash' : 'heroicon-o-eye')
                    ->color($this->showHidden ? 'warning' : 'gray')
                    ->action(function () {
                        $this->showHidden = ! $this->showHidden;
                        $this->loadDirectory();
                        $this->resetTable();
                    }),
                Action::make('refreshTable')
                    ->label(__('Refresh'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('gray')
                    ->action(function () {
                        $this->loadDirectory();
                        $this->resetTable();
                    }),
            ])
            ->checkIfRecordIsSelectableUsing(fn (array $record): bool => $this->selectable && ! ($record['is_parent'] ?? false))
            ->emptyStateHeading(__('This folder is empty'))
            ->emptyStateIcon('heroicon-o-folder-open')
            ->striped();
    }

    public function getTableRecordKey(Model|array $record): string
    {
        return is_array($record) ? $record['path'] : $record->getKey();
    }

    /**
     * Called by the parent page to get the currently selected file paths.
     *
     * @return list<string>
     */
    public function getSelectedPaths(): array
    {
        return $this->selectedTableRecords ?? [];
    }

    // ─── File Operations ──────────────────────────────────────

    public function downloadFile(string $path): void
    {
        if (! $this->featureEnabled('download')) {
            return;
        }

        try {
            $result = $this->getAdapter()->files()->read(PathSanitizer::clean($path));
            if ($result === null) {
                return;
            }
            $this->dispatch('download-file',
                content: $result['content'],
                filename: basename($path),
            );
        } catch (Exception $e) {
            \Filament\Notifications\Notification::make()
                ->title(__('Error reading file'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    // ─── Helpers ──────────────────────────────────────────────

    protected function filterRecords(array $records, ?string $search): array
    {
        if (! $search) {
            return $records;
        }

        $needle = Str::lower($search);

        return array_filter($records, function (array $record) use ($needle): bool {
            return Str::contains(Str::lower((string) ($record['name'] ?? '')), $needle);
        });
    }

    protected function sortRecords(array $records, ?string $sortColumn, ?string $sortDirection): array
    {
        if (! $sortColumn) {
            return $records;
        }

        $parent = null;
        $parentKey = null;
        foreach ($records as $key => $record) {
            if (($record['is_parent'] ?? false) === true) {
                $parent = $record;
                $parentKey = $key;
                unset($records[$key]);
                break;
            }
        }

        $direction = $sortDirection === 'asc' ? 'asc' : 'desc';

        uasort($records, function (array $a, array $b) use ($sortColumn, $direction): int {
            $aValue = $a[$sortColumn] ?? null;
            $bValue = $b[$sortColumn] ?? null;

            if (is_numeric($aValue) && is_numeric($bValue)) {
                $result = (float) $aValue <=> (float) $bValue;
            } else {
                $result = strcmp((string) $aValue, (string) $bValue);
            }

            return $direction === 'asc' ? $result : -$result;
        });

        if ($parent !== null && $parentKey !== null) {
            $records = [$parentKey => $parent] + $records;
        }

        return $records;
    }

    protected function paginateRecords(array $records, int|string $page, int|string $recordsPerPage): LengthAwarePaginator
    {
        $page = max(1, (int) $page);
        $perPage = max(1, (int) $recordsPerPage);

        $total = count($records);
        $items = array_slice($records, ($page - 1) * $perPage, $perPage, true);

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

    public function render()
    {
        return view('file-browser::components.file-browser-widget');
    }
}
