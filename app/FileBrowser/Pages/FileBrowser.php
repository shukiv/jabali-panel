<?php

declare(strict_types=1);

namespace App\FileBrowser\Pages;

use App\FileBrowser\Adapters\FileBrowserAdapter;
use App\FileBrowser\FileBrowserPlugin;
use App\FileBrowser\Services\FileOperationService;
use App\FileBrowser\Support\PathSanitizer;
use App\FileBrowser\ValueObjects\FilePermission;
use App\Support\Formatter;
use App\Support\SafeError;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\CodeEditor;
use Filament\Forms\Components\CodeEditor\Enums\Language;
use Filament\Forms\Components\FileUpload;
use Filament\Forms\Components\Textarea;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Grid;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Schemas\Components\Utilities\Set;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Pagination\LengthAwarePaginator;
use Illuminate\Support\Str;
use Livewire\WithFileUploads;

class FileBrowser extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;
    use WithFileUploads;

    protected static ?string $slug = null;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-folder';

    protected static ?int $navigationSort = 6;

    protected string $view = 'file-browser::pages.file-browser';

    public string $currentPath = '';

    public bool $showHidden = false;

    protected $queryString = ['currentPath' => ['as' => 'path', 'except' => '']];

    public array $items = [];

    public static function getNavigationLabel(): string
    {
        try {
            $plugin = FileBrowserPlugin::get();
            $label = $plugin->getNavigationLabel();
            if ($label !== null) {
                return $label;
            }
        } catch (\Throwable) {
            // Plugin not registered
        }

        return __(config('file-browser.navigation.label', 'File Manager'));
    }

    public static function getNavigationIcon(): string|BackedEnum|null
    {
        try {
            $plugin = FileBrowserPlugin::get();
            $icon = $plugin->getNavigationIcon();
            if ($icon !== null) {
                return $icon;
            }
        } catch (\Throwable) {
            // Plugin not registered
        }

        return config('file-browser.navigation.icon', 'heroicon-o-folder');
    }

    public static function getSlug(?\Filament\Panel $panel = null): string
    {
        // Config is available early (before plugin boot), so check it first
        $configSlug = config('file-browser.navigation.slug');
        if ($configSlug !== null && $configSlug !== 'file-browser') {
            return $configSlug;
        }

        try {
            $plugin = FileBrowserPlugin::get();
            $slug = $plugin->getSlug();
            if ($slug !== null) {
                return $slug;
            }
        } catch (\Throwable) {
            // Plugin not registered
        }

        return $configSlug ?? 'file-browser';
    }

    public static function getNavigationSort(): ?int
    {
        try {
            $plugin = FileBrowserPlugin::get();
            $sort = $plugin->getNavigationSort();
            if ($sort !== null) {
                return $sort;
            }
        } catch (\Throwable) {
            // Plugin not registered
        }

        return config('file-browser.navigation.sort', 6);
    }

    public static function getNavigationGroup(): ?string
    {
        try {
            $plugin = FileBrowserPlugin::get();
            $group = $plugin->getNavigationGroup();
            if ($group !== null) {
                return $group;
            }
        } catch (\Throwable) {
            // Plugin not registered
        }

        return config('file-browser.navigation.group');
    }

    public function getTitle(): string|Htmlable
    {
        return __(config('file-browser.navigation.label', 'File Manager'));
    }

    public function getAdapter(): FileBrowserAdapter
    {
        return app(FileBrowserAdapter::class);
    }

    private ?FileOperationService $ops = null;

    protected function fileOps(): FileOperationService
    {
        if ($this->ops === null) {
            $this->ops = (new FileOperationService($this->getAdapter()))
                ->onSuccess(function () {
                    $this->loadDirectory();
                    $this->resetTable();
                });
        }

        return $this->ops;
    }

    public function mount(): void
    {
        $path = request()->get('path');
        if ($path !== null && $path !== '') {
            try {
                $this->currentPath = PathSanitizer::clean((string) $path);
            } catch (Exception $e) {
                $this->currentPath = '';
                Notification::make()
                    ->title(__('Invalid path'))
                    ->body(__('The requested path is not allowed.'))
                    ->danger()
                    ->send();
            }
        }
        $this->loadDirectory();
    }

    protected function getHeaderActions(): array
    {
        return [];
    }

    public function getMaxUploadSizeMb(): int
    {
        return (int) config('file-browser.max_upload_size_mb', 100);
    }

    public function showUploadError(): void
    {
        Notification::make()
            ->title(__('Upload failed'))
            ->body(__('File exceeds maximum upload size of :size MB', ['size' => $this->getMaxUploadSizeMb()]))
            ->danger()
            ->send();
    }

    // ─── Feature Checks ───────────────────────────────────────

    /**
     * Features disabled on this page instance. Subclasses override this
     * to hide actions without affecting the global plugin config.
     *
     * Example: protected array $disabledFeatures = ['upload', 'edit', 'trash', 'extract', 'permissions'];
     *
     * @var list<string>
     */
    protected array $disabledFeatures = [];

    /**
     * Whether this instance is read-only (disables all write operations).
     * Shortcut for disabling upload, edit, trash, extract, and permissions.
     */
    protected bool $readOnly = false;

    /**
     * Write operations disabled when readOnly is true.
     * 'download' and 'view' are deliberately excluded — they stay available in read-only mode.
     */
    private const WRITE_FEATURES = [
        'upload', 'edit', 'trash', 'extract', 'permissions',
        'rename', 'newFolder', 'newFile', 'move', 'copy',
    ];

    protected function featureEnabled(string $feature): bool
    {
        // Per-instance overrides take priority
        if ($this->readOnly && in_array($feature, self::WRITE_FEATURES, true)) {
            return false;
        }
        if (in_array($feature, $this->disabledFeatures, true)) {
            return false;
        }

        $adapter = $this->getAdapter();

        $adapterSupports = match ($feature) {
            'trash' => true, // TrashManager handles this separately
            'extract' => $adapter->archiver() !== null,
            'permissions' => $adapter->permissions() !== null,
            default => true,
        };

        if (! $adapterSupports) {
            return false;
        }

        try {
            $plugin = FileBrowserPlugin::get();

            return match ($feature) {
                'trash' => $plugin->hasTrash(),
                'extract' => $plugin->hasExtract(),
                'upload' => $plugin->hasUpload(),
                'edit' => $plugin->hasEdit(),
                'permissions' => $plugin->hasPermissions(),
                default => config("file-browser.features.{$feature}", true),
            };
        } catch (\Throwable) {
            return config("file-browser.features.{$feature}", true);
        }
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

            // Add ".." entry for navigating up (only if not in root)
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
            Notification::make()
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
            Notification::make()
                ->title(__('Invalid path'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    public function navigateUp(): void
    {
        if (empty($this->currentPath)) {
            return;
        }

        $parts = explode('/', $this->currentPath);
        array_pop($parts);
        $this->currentPath = implode('/', $parts);
        $this->loadDirectory();
        $this->resetTable();
    }

    public function openFolder(string $name): void
    {
        $newPath = empty($this->currentPath) ? $name : $this->currentPath.'/'.$name;
        $this->navigateTo($newPath);
        $this->resetTable();
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

    // ─── Warning Banner ───────────────────────────────────────

    public function getWarningMessage(): ?string
    {
        return config('file-browser.warning_message');
    }

    public function getWarningFolders(): array
    {
        return config('file-browser.warning_folders', []);
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
                TextColumn::make('permissions')
                    ->label(__('Permissions'))
                    ->badge()
                    ->color('gray')
                    ->formatStateUsing(fn (array $record): string => $record['permissions'] ?? '----'),
                TextColumn::make('modified')
                    ->label(__('Modified'))
                    ->formatStateUsing(fn (array $record): string => $this->formatDate($record['modified']))
                    ->color('gray'),
            ])
            ->recordActions([
                Action::make('view')
                    ->label(__('View'))
                    ->icon('heroicon-o-eye')
                    ->color('info')
                    ->visible(fn (array $record): bool => ! $record['is_dir'] && $this->isImage($record['name']) && $this->featureEnabled('view'))
                    ->modalHeading(fn (array $record): string => basename($record['path']))
                    ->modalWidth('4xl')
                    ->modalSubmitAction(false)
                    ->modalCancelActionLabel(__('Close'))
                    ->modalContent(fn (array $record) => view('file-browser::components.image-viewer', [
                        'imageData' => $this->getImageData($record['path']),
                        'filename' => basename($record['path']),
                    ])),
                Action::make('edit')
                    ->label(__('Edit'))
                    ->icon('heroicon-o-pencil-square')
                    ->color('gray')
                    ->visible(fn (array $record): bool => ! $record['is_dir'] && $this->isEditable($record['name']) && $this->featureEnabled('edit'))
                    ->modalHeading(fn (array $record): string => __('Edit').': '.basename($record['path']))
                    ->modalWidth('5xl')
                    ->modalSubmitActionLabel(__('Save Changes'))
                    ->fillForm(function (array $record): array {
                        $result = $this->fileOps()->read($record['path']);

                        return ['content' => $result ? base64_decode($result['content']) : ''];
                    })
                    ->form(fn (array $record): array => [
                        CodeEditor::make('content')
                            ->label('')
                            ->language($this->getLanguageForFile($record['name']))
                            ->wrap()
                            ->required(),
                    ])
                    ->action(function (array $data, array $record): void {
                        $this->fileOps()->saveFile($record['path'], $data['content']);
                    }),
                Action::make('download')
                    ->label(__('Download'))
                    ->icon('heroicon-o-arrow-down-tray')
                    ->color('gray')
                    ->visible(fn (array $record): bool => ! $record['is_dir'] && $this->featureEnabled('download'))
                    ->action(fn (array $record) => $this->downloadFile($record['path'])),
                Action::make('extract')
                    ->label(__('Extract'))
                    ->icon('heroicon-o-archive-box-arrow-down')
                    ->color('warning')
                    ->visible(fn (array $record): bool => ! $record['is_dir'] && $this->isExtractable($record['name']) && $this->featureEnabled('extract'))
                    ->requiresConfirmation()
                    ->modalHeading(__('Extract Archive'))
                    ->modalDescription(__('Extract the contents of this archive to the current folder?'))
                    ->modalIcon('heroicon-o-archive-box-arrow-down')
                    ->modalIconColor('warning')
                    ->modalSubmitActionLabel(__('Extract'))
                    ->action(fn (array $record) => $this->fileOps()->extract($record['path'])),
                Action::make('permissions')
                    ->label(__('Permissions'))
                    ->icon('heroicon-o-lock-closed')
                    ->color('gray')
                    ->visible(fn (array $record): bool => ! ($record['is_parent'] ?? false) && $this->featureEnabled('permissions'))
                    ->modalHeading(__('Change Permissions'))
                    ->modalDescription(fn (array $record): string => basename($record['path']))
                    ->modalIcon('heroicon-o-lock-closed')
                    ->modalIconColor('warning')
                    ->modalSubmitActionLabel(__('Apply'))
                    ->fillForm(function (array $record): array {
                        $result = $this->fileOps()->info($record['path']);
                        $perms = $result !== null ? ($result['info']['permissions'] ?? '0644') : '0644';

                        return FilePermission::fromOctal($perms)->toFormState();
                    })
                    ->form([
                        TextInput::make('mode')
                            ->label(__('Numeric Mode'))
                            ->placeholder(__('755'))
                            ->maxLength(4)
                            ->helperText(__('Enter octal mode (e.g., 755, 644)'))
                            ->live(onBlur: true)
                            ->afterStateUpdated(function (Set $set, ?string $state): void {
                                if ($state === null || $state === '' || ! preg_match('/^[0-7]{3,4}$/', $state)) {
                                    return;
                                }
                                $toggles = FilePermission::fromOctal($state)->toFormState();
                                unset($toggles['mode']);
                                foreach ($toggles as $key => $value) {
                                    $set($key, $value);
                                }
                            }),
                        Grid::make(3)
                            ->schema([
                                \Filament\Schemas\Components\Section::make(__('Owner'))
                                    ->schema([
                                        Toggle::make('owner_read')->label(__('Read'))->live()
                                            ->afterStateUpdated(fn (Get $get, Set $set) => $set('mode', FilePermission::fromToggles($get('/'))->toOctal())),
                                        Toggle::make('owner_write')->label(__('Write'))->live()
                                            ->afterStateUpdated(fn (Get $get, Set $set) => $set('mode', FilePermission::fromToggles($get('/'))->toOctal())),
                                        Toggle::make('owner_execute')->label(__('Execute'))->live()
                                            ->afterStateUpdated(fn (Get $get, Set $set) => $set('mode', FilePermission::fromToggles($get('/'))->toOctal())),
                                    ]),
                                \Filament\Schemas\Components\Section::make(__('Group'))
                                    ->schema([
                                        Toggle::make('group_read')->label(__('Read'))->live()
                                            ->afterStateUpdated(fn (Get $get, Set $set) => $set('mode', FilePermission::fromToggles($get('/'))->toOctal())),
                                        Toggle::make('group_write')->label(__('Write'))->live()
                                            ->afterStateUpdated(fn (Get $get, Set $set) => $set('mode', FilePermission::fromToggles($get('/'))->toOctal())),
                                        Toggle::make('group_execute')->label(__('Execute'))->live()
                                            ->afterStateUpdated(fn (Get $get, Set $set) => $set('mode', FilePermission::fromToggles($get('/'))->toOctal())),
                                    ]),
                                \Filament\Schemas\Components\Section::make(__('Others'))
                                    ->schema([
                                        Toggle::make('other_read')->label(__('Read'))->live()
                                            ->afterStateUpdated(fn (Get $get, Set $set) => $set('mode', FilePermission::fromToggles($get('/'))->toOctal())),
                                        Toggle::make('other_write')->label(__('Write'))->live()
                                            ->afterStateUpdated(fn (Get $get, Set $set) => $set('mode', FilePermission::fromToggles($get('/'))->toOctal())),
                                        Toggle::make('other_execute')->label(__('Execute'))->live()
                                            ->afterStateUpdated(fn (Get $get, Set $set) => $set('mode', FilePermission::fromToggles($get('/'))->toOctal())),
                                    ]),
                            ]),
                    ])
                    ->action(function (array $data, array $record): void {
                        $this->fileOps()->chmod($record['path'], $data);
                    }),
                Action::make('rename')
                    ->label(__('Rename'))
                    ->icon('heroicon-o-pencil')
                    ->color('gray')
                    ->visible(fn (array $record): bool => ! ($record['is_parent'] ?? false) && $this->featureEnabled('rename'))
                    ->modalHeading(__('Rename'))
                    ->form(fn (array $record): array => [
                        TextInput::make('name')
                            ->label(__('New Name'))
                            ->default($record['name'])
                            ->required(),
                    ])
                    ->action(function (array $data, array $record): void {
                        $this->fileOps()->rename($record['path'], $data['name']);
                    }),
                Action::make('moveToTrash')
                    ->label(__('Trash'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->visible(fn (array $record): bool => ! ($record['is_parent'] ?? false) && $this->featureEnabled('trash'))
                    ->requiresConfirmation()
                    ->action(fn (array $record) => $this->fileOps()->trashOrDelete($record['path'], $this->featureEnabled('trash'))),
            ])
            ->bulkActions($this->getBulkActions())
            ->checkIfRecordIsSelectableUsing(fn (array $record): bool => ! ($record['is_parent'] ?? false))
            ->headerActions([
                $this->newFolderAction(),
                $this->newFileAction(),
                $this->uploadAction(),
                $this->trashAction(),
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
            ->emptyStateHeading(__('This folder is empty'))
            ->emptyStateDescription(__('Create a new file or folder to get started'))
            ->emptyStateIcon('heroicon-o-folder-open')
            ->striped();
    }

    protected function getBulkActions(): array
    {
        $actions = [];

        if ($this->featureEnabled('trash')) {
            $actions[] = \Filament\Actions\BulkAction::make('bulkTrash')
                ->label(__('Trash'))
                ->icon('heroicon-o-trash')
                ->color('danger')
                ->size('sm')
                ->requiresConfirmation()
                ->modalHeading(__('Move to Trash'))
                ->modalDescription(__('Move selected items to trash? You can restore them later.'))
                ->modalSubmitActionLabel(__('Move to Trash'))
                ->modalIcon('heroicon-o-trash')
                ->modalIconColor('warning')
                ->deselectRecordsAfterCompletion()
                ->action(function (\Illuminate\Support\Collection $records): void {
                    $this->fileOps()->bulkTrashOrDelete($records, $this->featureEnabled('trash'));
                });
        }

        if ($this->featureEnabled('move')) {
            $actions[] = \Filament\Actions\BulkAction::make('bulkMove')
                ->label(__('Move'))
                ->icon('heroicon-o-arrow-right')
                ->color('warning')
                ->size('sm')
                ->modalHeading(__('Move Selected Items'))
                ->modalDescription(__('Select the destination folder'))
                ->modalSubmitActionLabel(__('Move'))
                ->form([
                    TextInput::make('destination')
                        ->label(__('Destination Path'))
                        ->placeholder(__('e.g., path/to/folder'))
                        ->required()
                        ->helperText(__('Enter the path relative to your root directory')),
                ])
                ->deselectRecordsAfterCompletion()
                ->action(function (\Illuminate\Support\Collection $records, array $data): void {
                    $this->fileOps()->bulkMove($records, $data['destination']);
                });
        }

        if ($this->featureEnabled('copy')) {
            $actions[] = \Filament\Actions\BulkAction::make('bulkCopy')
                ->label(__('Copy'))
                ->icon('heroicon-o-document-duplicate')
                ->color('info')
                ->size('sm')
                ->modalHeading(__('Copy Selected Items'))
                ->modalDescription(__('Select the destination folder'))
                ->modalSubmitActionLabel(__('Copy'))
                ->form([
                    TextInput::make('destination')
                        ->label(__('Destination Path'))
                        ->placeholder(__('e.g., path/to/folder'))
                        ->required()
                        ->helperText(__('Enter the path relative to your root directory')),
                ])
                ->deselectRecordsAfterCompletion()
                ->action(function (\Illuminate\Support\Collection $records, array $data): void {
                    $this->fileOps()->bulkCopy($records, $data['destination']);
                });
        }

        return $actions;
    }

    public function getTableRecordKey(Model|array $record): string
    {
        return is_array($record) ? $record['path'] : $record->getKey();
    }

    protected function filterRecords(array $records, ?string $search): array
    {
        if (! $search) {
            return $records;
        }

        $needle = Str::lower($search);

        return array_filter($records, function (array $record) use ($needle): bool {
            $name = (string) ($record['name'] ?? '');

            return Str::contains(Str::lower($name), $needle);
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

    // ─── Drag and Drop ────────────────────────────────────────

    public function uploadDroppedFile(string $filename, string $base64Content): void
    {
        if (! $this->featureEnabled('upload')) {
            return;
        }

        $maxSizeMb = $this->getMaxUploadSizeMb();
        $maxSizeBytes = $maxSizeMb * 1024 * 1024;

        $content = base64_decode($base64Content);
        if (strlen($content) > $maxSizeBytes) {
            Notification::make()
                ->title(__('Upload failed'))
                ->body(__('File too large (max :size MB)', ['size' => $maxSizeMb]))
                ->danger()
                ->send();

            return;
        }

        $this->fileOps()->upload($this->currentPath, $filename, $content);
    }

    // ─── Header Actions ───────────────────────────────────────

    protected function newFolderAction(): Action
    {
        return Action::make('newFolder')
            ->label(__('New Folder'))
            ->icon('heroicon-o-folder-plus')
            ->visible($this->featureEnabled('newFolder'))
            ->modalHeading(__('Create New Folder'))
            ->modalDescription(__('Enter a name for the new folder'))
            ->modalIcon('heroicon-o-folder-plus')
            ->modalIconColor('warning')
            ->modalSubmitActionLabel(__('Create Folder'))
            ->form([
                TextInput::make('name')
                    ->label(__('Folder Name'))
                    ->placeholder(__('my-folder'))
                    ->required()
                    ->autocomplete(false)
                    ->maxLength(255)
                    ->helperText(__('Use letters, numbers, hyphens, and underscores')),
            ])
            ->action(fn (array $data) => $this->fileOps()->mkdir($this->currentPath, $data['name']));
    }

    protected function newFileAction(): Action
    {
        return Action::make('newFile')
            ->label(__('New File'))
            ->icon('heroicon-o-document-plus')
            ->visible($this->featureEnabled('newFile'))
            ->modalHeading(__('Create New File'))
            ->modalDescription(__('Create a new file with optional content'))
            ->modalIcon('heroicon-o-document-plus')
            ->modalIconColor('info')
            ->modalSubmitActionLabel(__('Create File'))
            ->modalWidth('2xl')
            ->form([
                TextInput::make('name')
                    ->label(__('File Name'))
                    ->placeholder(__('example.php'))
                    ->required()
                    ->autocomplete(false)
                    ->maxLength(255)
                    ->helperText(__('Include the file extension (e.g., .php, .html, .txt)')),
                Textarea::make('content')
                    ->label(__('Content'))
                    ->placeholder(__('Enter file content here...'))
                    ->rows(12)
                    ->extraAttributes(['class' => 'font-mono text-sm']),
            ])
            ->action(function (array $data): void {
                $this->fileOps()->createFile($this->currentPath, $data['name'], $data['content'] ?? '');
            });
    }

    protected function uploadAction(): Action
    {
        if (! $this->featureEnabled('upload')) {
            return Action::make('upload')->hidden();
        }

        $maxSizeMb = $this->getMaxUploadSizeMb();

        return Action::make('upload')
            ->label(__('Upload'))
            ->icon('heroicon-o-arrow-up-tray')
            ->modalHeading(__('Upload Files'))
            ->modalDescription(__('Select files to upload to the current folder'))
            ->modalIcon('heroicon-o-arrow-up-tray')
            ->modalIconColor('success')
            ->modalSubmitActionLabel(__('Upload'))
            ->form([
                FileUpload::make('files')
                    ->label(__('Select Files'))
                    ->multiple()
                    ->storeFiles(false)
                    ->required()
                    ->maxSize($maxSizeMb * 1024)
                    ->validationMessages([
                        'max' => __('File exceeds maximum upload size of :size MB', ['size' => $maxSizeMb]),
                    ])
                    ->helperText(__('Maximum file size: :size MB', ['size' => $maxSizeMb])),
            ])
            ->action(function (array $data): void {
                $this->fileOps()->bulkUpload($data['files'] ?? [], $this->currentPath);
            });
    }

    protected function trashAction(): Action
    {
        if (! $this->featureEnabled('trash')) {
            return Action::make('trash')->hidden();
        }

        return Action::make('trash')
            ->label(__('Trash'))
            ->icon('heroicon-o-trash')
            ->color('danger')
            ->modalHeading(__('Trash'))
            ->modalDescription(__('View and manage deleted items'))
            ->modalIcon('heroicon-o-trash')
            ->modalWidth('5xl')
            ->modalSubmitAction(false)
            ->modalCancelActionLabel(__('Close'))
            ->modalContent(fn () => view('file-browser::components.trash-table-embed'));
    }

    // ─── File Operations ──────────────────────────────────────

    public function downloadFile(string $path): void
    {
        $result = $this->fileOps()->read($path);
        if ($result === null) {
            return;
        }
        $this->dispatch('download-file',
            content: $result['content'],
            filename: basename($path)
        );
    }

    // ─── Helpers ──────────────────────────────────────────────

    public function formatDate(int $timestamp): string
    {
        return date('M d, Y H:i', $timestamp);
    }

    public function isEditable(string $name): bool
    {
        $ext = strtolower(pathinfo($name, PATHINFO_EXTENSION));
        $editable = ['php', 'js', 'ts', 'css', 'scss', 'html', 'htm', 'json', 'xml', 'txt', 'md', 'yml', 'yaml', 'env', 'htaccess', 'conf', 'ini', 'sh', 'bash', 'log', 'sql', 'vue', 'jsx', 'tsx'];

        return in_array($ext, $editable) || empty($ext);
    }

    public function isExtractable(string $name): bool
    {
        $ext = strtolower(pathinfo($name, PATHINFO_EXTENSION));
        $extractable = ['zip', 'tar', 'gz', 'tgz', 'bz2', 'xz', 'rar', '7z'];

        if (preg_match('/\.(tar\.gz|tar\.bz2|tar\.xz)$/i', $name)) {
            return true;
        }

        return in_array($ext, $extractable);
    }

    public function isImage(string $name): bool
    {
        $ext = strtolower(pathinfo($name, PATHINFO_EXTENSION));

        return in_array($ext, ['jpg', 'jpeg', 'png', 'gif', 'webp', 'svg', 'bmp', 'ico']);
    }

    public function getImageData(string $path): ?string
    {
        $result = $this->fileOps()->read($path);
        if ($result === null) {
            return null;
        }
        $ext = strtolower(pathinfo($path, PATHINFO_EXTENSION));
        $mimeTypes = [
            'jpg' => 'image/jpeg',
            'jpeg' => 'image/jpeg',
            'png' => 'image/png',
            'gif' => 'image/gif',
            'webp' => 'image/webp',
            'svg' => 'image/svg+xml',
            'bmp' => 'image/bmp',
            'ico' => 'image/x-icon',
        ];
        $mime = $mimeTypes[$ext] ?? 'image/png';

        return 'data:'.$mime.';base64,'.$result['content'];
    }

    protected function getLanguageForFile(string $filename): ?Language
    {
        $ext = strtolower(pathinfo($filename, PATHINFO_EXTENSION));

        if (str_ends_with(strtolower($filename), '.blade.php')) {
            return Language::Php;
        }

        return match ($ext) {
            'php' => Language::Php,
            'js', 'mjs', 'cjs' => Language::JavaScript,
            'json' => Language::Json,
            'html', 'htm' => Language::Html,
            'css', 'scss', 'sass' => Language::Css,
            'xml' => Language::Xml,
            'sql' => Language::Sql,
            'md', 'markdown' => Language::Markdown,
            'yml', 'yaml' => Language::Yaml,
            'py' => Language::Python,
            'java' => Language::Java,
            'go' => Language::Go,
            'cpp', 'c', 'h', 'hpp' => Language::Cpp,
            default => null,
        };
    }

    // ─── Refresh listener ─────────────────────────────────────

    #[\Livewire\Attributes\On('trash-updated')]
    public function onTrashUpdated(): void
    {
        $this->loadDirectory();
        $this->resetTable();
    }
}
