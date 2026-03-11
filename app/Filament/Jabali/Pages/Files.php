<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Models\DnsSetting;
use App\Services\Agent\AgentClient;
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
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Pagination\LengthAwarePaginator;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Str;
use Livewire\WithFileUploads;

class Files extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;
    use WithFileUploads;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-folder';

    public static function getNavigationLabel(): string
    {
        return __('File Manager');
    }

    protected static ?int $navigationSort = 6;

    protected string $view = 'filament.jabali.pages.files';

    public string $currentPath = '';

    public bool $showHidden = false;

    protected $queryString = ['currentPath' => ['as' => 'path', 'except' => '']];

    public array $items = [];

    protected ?AgentClient $agent = null;

    public function getTitle(): string|Htmlable
    {
        return __('File Manager');
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
        // Check if path is provided in URL
        $path = request()->get('path');
        if ($path !== null && $path !== '') {
            try {
                $this->currentPath = $this->sanitizePath((string) $path);
            } catch (Exception $e) {
                // Invalid path from URL - reset to home directory
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
        return [
        ];
    }

    public function getUsername(): string
    {
        return Auth::user()->username;
    }

    public function getMaxUploadSizeMb(): int
    {
        return (int) DnsSetting::get('max_upload_size_mb', 100);
    }

    public function showUploadError(): void
    {
        Notification::make()
            ->title(__('Upload failed'))
            ->body(__('File exceeds maximum upload size of :size MB', ['size' => $this->getMaxUploadSizeMb()]))
            ->danger()
            ->send();
    }

    /**
     * Sanitize and validate a path to prevent directory traversal attacks.
     * Ensures the path stays within the user's home directory.
     *
     * @param  string  $path  The path to sanitize
     * @return string The sanitized path
     *
     * @throws Exception If the path is invalid or attempts traversal
     */
    protected function sanitizePath(string $path): string
    {
        // Remove null bytes
        $path = str_replace("\0", '', $path);

        // Normalize directory separators
        $path = str_replace('\\', '/', $path);

        // Remove leading/trailing slashes and whitespace
        $path = trim($path, "/ \t\n\r");

        // Block any path containing .. sequences (traversal attempt)
        if (preg_match('/(?:^|\/)\.\.(\/|$)/', $path)) {
            throw new Exception('Invalid path: directory traversal not allowed');
        }

        // Block absolute paths
        if (str_starts_with($path, '/')) {
            throw new Exception('Invalid path: absolute paths not allowed');
        }

        // Block paths starting with ~
        if (str_starts_with($path, '~')) {
            throw new Exception('Invalid path: home directory shortcuts not allowed');
        }

        // Remove redundant slashes and normalize
        $path = preg_replace('#/+#', '/', $path);

        // Remove . segments
        $parts = explode('/', $path);
        $parts = array_filter($parts, fn ($part) => $part !== '' && $part !== '.');

        return implode('/', $parts);
    }

    /**
     * Sanitize a filename to prevent path injection.
     *
     * @param  string  $filename  The filename to sanitize
     * @return string The sanitized filename
     *
     * @throws Exception If the filename is invalid
     */
    protected function sanitizeFilename(string $filename): string
    {
        // Remove null bytes
        $filename = str_replace("\0", '', $filename);

        // Remove any path separators from filename
        $filename = basename(str_replace('\\', '/', $filename));

        // Block hidden files starting with .. or just .
        if ($filename === '' || $filename === '.' || $filename === '..') {
            throw new Exception('Invalid filename');
        }

        // Block filenames that are just dots
        if (preg_match('/^\.+$/', $filename)) {
            throw new Exception('Invalid filename');
        }

        return $filename;
    }

    public function loadDirectory(?string $path = null): void
    {
        if ($path !== null) {
            $this->currentPath = $path;
        }

        try {
            $result = $this->getAgent()->fileList($this->getUsername(), $this->currentPath, $this->showHidden);
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
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function navigateTo(string $path): void
    {
        try {
            $this->currentPath = $this->sanitizePath($path);
            $this->loadDirectory();
            $this->resetTable();
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Invalid path'))
                ->body($e->getMessage())
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
                    ->action(fn (array $record) => $this->navigateTo($record['path']))
                    ->disabledClick(fn (array $record): bool => ! $record['is_dir'])
                    ->weight('medium')
                    ->searchable(),
                TextColumn::make('size')
                    ->label(__('Size'))
                    ->formatStateUsing(fn (array $record): string => $record['is_dir'] ? '—' : $this->formatSize($record['size']))
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
                    ->visible(fn (array $record): bool => ! $record['is_dir'] && $this->isImage($record['name']))
                    ->modalHeading(fn (array $record): string => basename($record['path']))
                    ->modalWidth('4xl')
                    ->modalSubmitAction(false)
                    ->modalCancelActionLabel(__('Close'))
                    ->modalContent(fn (array $record) => view('filament.jabali.components.image-viewer', [
                        'imageData' => $this->getImageData($record['path']),
                        'filename' => basename($record['path']),
                    ])),
                Action::make('edit')
                    ->label(__('Edit'))
                    ->icon('heroicon-o-pencil-square')
                    ->color('gray')
                    ->visible(fn (array $record): bool => ! $record['is_dir'] && $this->isEditable($record['name']))
                    ->modalHeading(fn (array $record): string => __('Edit').': '.basename($record['path']))
                    ->modalWidth('5xl')
                    ->modalSubmitActionLabel(__('Save Changes'))
                    ->fillForm(function (array $record): array {
                        try {
                            $result = $this->getAgent()->fileRead($this->getUsername(), $record['path']);

                            return ['content' => base64_decode($result['content'])];
                        } catch (Exception) {
                            return ['content' => ''];
                        }
                    })
                    ->form(fn (array $record): array => [
                        CodeEditor::make('content')
                            ->label('')
                            ->language($this->getLanguageForFile($record['name']))
                            ->wrap()
                            ->required(),
                    ])
                    ->action(function (array $data, array $record): void {
                        try {
                            $this->getAgent()->fileWrite($this->getUsername(), $record['path'], $data['content']);
                            Notification::make()->title(__('File saved'))->success()->send();
                        } catch (Exception $e) {
                            Notification::make()->title(__('Error saving file'))->body($e->getMessage())->danger()->send();
                        }
                    }),
                Action::make('download')
                    ->label(__('Download'))
                    ->icon('heroicon-o-arrow-down-tray')
                    ->color('gray')
                    ->visible(fn (array $record): bool => ! $record['is_dir'])
                    ->action(fn (array $record) => $this->downloadFile($record['path'])),
                Action::make('extract')
                    ->label(__('Extract'))
                    ->icon('heroicon-o-archive-box-arrow-down')
                    ->color('warning')
                    ->visible(fn (array $record): bool => ! $record['is_dir'] && $this->isExtractable($record['name']))
                    ->requiresConfirmation()
                    ->modalHeading(__('Extract Archive'))
                    ->modalDescription(__('Extract the contents of this archive to the current folder?'))
                    ->modalIcon('heroicon-o-archive-box-arrow-down')
                    ->modalIconColor('warning')
                    ->modalSubmitActionLabel(__('Extract'))
                    ->action(function (array $record): void {
                        try {
                            $this->getAgent()->fileExtract($this->getUsername(), $record['path']);
                            Notification::make()->title(__('Archive extracted successfully'))->success()->send();
                            $this->loadDirectory();
                            $this->resetTable();
                        } catch (Exception $e) {
                            Notification::make()->title(__('Error extracting archive'))->body($e->getMessage())->danger()->send();
                        }
                    }),
                Action::make('permissions')
                    ->label(__('Permissions'))
                    ->icon('heroicon-o-lock-closed')
                    ->color('gray')
                    ->visible(fn (array $record): bool => ! ($record['is_parent'] ?? false))
                    ->modalHeading(__('Change Permissions'))
                    ->modalDescription(fn (array $record): string => basename($record['path']))
                    ->modalIcon('heroicon-o-lock-closed')
                    ->modalIconColor('warning')
                    ->modalSubmitActionLabel(__('Apply'))
                    ->fillForm(function (array $record): array {
                        try {
                            $result = $this->getAgent()->fileInfo($this->getUsername(), $record['path']);
                            $perms = $result['info']['permissions'] ?? '0644';

                            return $this->parsePermissions($perms);
                        } catch (Exception) {
                            return $this->parsePermissions('0644');
                        }
                    })
                    ->form([
                        TextInput::make('mode')
                            ->label(__('Numeric Mode'))
                            ->placeholder(__('755'))
                            ->maxLength(4)
                            ->helperText(__('Enter octal mode (e.g., 755, 644)')),
                        Grid::make(3)
                            ->schema([
                                \Filament\Schemas\Components\Section::make(__('Owner'))
                                    ->schema([
                                        Toggle::make('owner_read')->label(__('Read')),
                                        Toggle::make('owner_write')->label(__('Write')),
                                        Toggle::make('owner_execute')->label(__('Execute')),
                                    ]),
                                \Filament\Schemas\Components\Section::make(__('Group'))
                                    ->schema([
                                        Toggle::make('group_read')->label(__('Read')),
                                        Toggle::make('group_write')->label(__('Write')),
                                        Toggle::make('group_execute')->label(__('Execute')),
                                    ]),
                                \Filament\Schemas\Components\Section::make(__('Others'))
                                    ->schema([
                                        Toggle::make('other_read')->label(__('Read')),
                                        Toggle::make('other_write')->label(__('Write')),
                                        Toggle::make('other_execute')->label(__('Execute')),
                                    ]),
                            ]),
                    ])
                    ->action(function (array $data, array $record): void {
                        try {
                            $mode = ! empty($data['mode']) ? $data['mode'] : $this->buildPermissionMode($data);
                            $this->getAgent()->fileChmod($this->getUsername(), $record['path'], $mode);
                            Notification::make()->title(__('Permissions changed'))->success()->send();
                            $this->loadDirectory();
                            $this->resetTable();
                        } catch (Exception $e) {
                            Notification::make()->title(__('Error changing permissions'))->body($e->getMessage())->danger()->send();
                        }
                    }),
                Action::make('rename')
                    ->label(__('Rename'))
                    ->icon('heroicon-o-pencil')
                    ->color('gray')
                    ->visible(fn (array $record): bool => ! ($record['is_parent'] ?? false))
                    ->modalHeading(__('Rename'))
                    ->form(fn (array $record): array => [
                        TextInput::make('name')
                            ->label(__('New Name'))
                            ->default($record['name'])
                            ->required(),
                    ])
                    ->action(function (array $data, array $record): void {
                        try {
                            $newName = $this->sanitizeFilename($data['name']);
                            // Build full new path by replacing filename in old path
                            $oldPath = $record['path'];
                            $newPath = dirname($oldPath).'/'.$newName;
                            $this->getAgent()->fileRename($this->getUsername(), $oldPath, $newPath);
                            Notification::make()->title(__('Renamed successfully'))->success()->send();
                            $this->loadDirectory();
                            $this->resetTable();
                        } catch (Exception $e) {
                            Notification::make()->title(__('Error renaming'))->body($e->getMessage())->danger()->send();
                        }
                    }),
                Action::make('moveToTrash')
                    ->label(__('Trash'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->visible(fn (array $record): bool => ! ($record['is_parent'] ?? false))
                    ->requiresConfirmation()
                    ->action(function (array $record): void {
                        try {
                            $this->getAgent()->fileTrash($this->getUsername(), $record['path']);
                            Notification::make()->title(__('Moved to trash'))->success()->send();
                            $this->loadDirectory();
                            $this->resetTable();
                        } catch (Exception $e) {
                            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
                        }
                    }),
            ])
            ->bulkActions([
                \Filament\Actions\BulkAction::make('bulkTrash')
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
                        $trashed = 0;
                        $failed = 0;
                        foreach ($records as $record) {
                            try {
                                $this->getAgent()->fileTrash($this->getUsername(), $record['path']);
                                $trashed++;
                            } catch (Exception $e) {
                                $failed++;
                            }
                        }
                        if ($trashed > 0) {
                            Notification::make()
                                ->title(__(':count item(s) moved to trash', ['count' => $trashed]))
                                ->success()
                                ->send();
                        }
                        if ($failed > 0) {
                            Notification::make()
                                ->title(__(':count item(s) failed', ['count' => $failed]))
                                ->danger()
                                ->send();
                        }
                        $this->loadDirectory();
                        $this->resetTable();
                    }),
                \Filament\Actions\BulkAction::make('bulkMove')
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
                            ->placeholder(__('e.g., domains/example.com/public_html'))
                            ->required()
                            ->helperText(__('Enter the path relative to your home directory')),
                    ])
                    ->deselectRecordsAfterCompletion()
                    ->action(function (\Illuminate\Support\Collection $records, array $data): void {
                        $moved = 0;
                        $failed = 0;
                        $destination = trim($data['destination'], '/');
                        foreach ($records as $record) {
                            try {
                                $filename = basename($record['path']);
                                $newPath = $destination.'/'.$filename;
                                $this->getAgent()->fileMove($this->getUsername(), $record['path'], $newPath);
                                $moved++;
                            } catch (Exception $e) {
                                $failed++;
                            }
                        }
                        if ($moved > 0) {
                            Notification::make()
                                ->title(__(':count item(s) moved', ['count' => $moved]))
                                ->success()
                                ->send();
                        }
                        if ($failed > 0) {
                            Notification::make()
                                ->title(__(':count item(s) failed to move', ['count' => $failed]))
                                ->danger()
                                ->send();
                        }
                        $this->loadDirectory();
                        $this->resetTable();
                    }),
                \Filament\Actions\BulkAction::make('bulkCopy')
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
                            ->placeholder(__('e.g., domains/example.com/public_html'))
                            ->required()
                            ->helperText(__('Enter the path relative to your home directory')),
                    ])
                    ->deselectRecordsAfterCompletion()
                    ->action(function (\Illuminate\Support\Collection $records, array $data): void {
                        $copied = 0;
                        $failed = 0;
                        $destination = trim($data['destination'], '/');
                        foreach ($records as $record) {
                            try {
                                $filename = basename($record['path']);
                                $newPath = $destination.'/'.$filename;
                                $this->getAgent()->fileCopy($this->getUsername(), $record['path'], $newPath);
                                $copied++;
                            } catch (Exception $e) {
                                $failed++;
                            }
                        }
                        if ($copied > 0) {
                            Notification::make()
                                ->title(__(':count item(s) copied', ['count' => $copied]))
                                ->success()
                                ->send();
                        }
                        if ($failed > 0) {
                            Notification::make()
                                ->title(__(':count item(s) failed to copy', ['count' => $failed]))
                                ->danger()
                                ->send();
                        }
                        $this->loadDirectory();
                        $this->resetTable();
                    }),
            ])
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

    // Drag and drop operations
    public function moveItem(string $sourcePath, string $destPath): void
    {
        try {
            $this->getAgent()->fileMove($this->getUsername(), $sourcePath, $destPath);

            Notification::make()
                ->title(__('Item moved successfully'))
                ->success()
                ->send();

            $this->loadDirectory();
            $this->resetTable();
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error moving item'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function uploadDroppedFile(string $filename, string $base64Content): void
    {
        try {
            // Sanitize filename to prevent path traversal
            $filename = $this->sanitizeFilename($filename);

            $maxSizeMb = $this->getMaxUploadSizeMb();
            $maxSizeBytes = $maxSizeMb * 1024 * 1024;

            // Base64 is ~33% larger than original, so decode first then check size
            $content = base64_decode($base64Content);
            if (strlen($content) > $maxSizeBytes) {
                throw new Exception(__('File too large (max :size MB)', ['size' => $maxSizeMb]));
            }

            $this->getAgent()->fileUpload(
                $this->getUsername(),
                $this->currentPath,
                $filename,
                $content
            );

            Notification::make()
                ->title(__('Uploaded: :filename', ['filename' => $filename]))
                ->success()
                ->send();

            $this->loadDirectory();
            $this->resetTable();
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Upload failed: :filename', ['filename' => $filename]))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    // Header Actions
    protected function newFolderAction(): Action
    {
        return Action::make('newFolder')
            ->label(__('New Folder'))
            ->icon('heroicon-o-folder-plus')
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
            ->action(function (array $data): void {
                try {
                    $folderName = $this->sanitizeFilename($data['name']);
                    $path = empty($this->currentPath)
                        ? $folderName
                        : $this->currentPath.'/'.$folderName;

                    $this->getAgent()->fileMkdir($this->getUsername(), $path);

                    Notification::make()
                        ->title(__('Folder created'))
                        ->success()
                        ->send();

                    $this->loadDirectory();
                    $this->resetTable();
                } catch (Exception $e) {
                    Notification::make()
                        ->title(__('Error creating folder'))
                        ->body($e->getMessage())
                        ->danger()
                        ->send();
                }
            });
    }

    protected function newFileAction(): Action
    {
        return Action::make('newFile')
            ->label(__('New File'))
            ->icon('heroicon-o-document-plus')
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
                try {
                    $fileName = $this->sanitizeFilename($data['name']);
                    $path = empty($this->currentPath)
                        ? $fileName
                        : $this->currentPath.'/'.$fileName;

                    $this->getAgent()->fileWrite($this->getUsername(), $path, $data['content'] ?? '');

                    Notification::make()
                        ->title(__('File created'))
                        ->success()
                        ->send();

                    $this->loadDirectory();
                    $this->resetTable();
                } catch (Exception $e) {
                    Notification::make()
                        ->title(__('Error creating file'))
                        ->body($e->getMessage())
                        ->danger()
                        ->send();
                }
            });
    }

    protected function uploadAction(): Action
    {
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
                    ->maxSize($maxSizeMb * 1024) // Convert MB to KB for Filament
                    ->validationMessages([
                        'max' => __('File exceeds maximum upload size of :size MB', ['size' => $maxSizeMb]),
                    ])
                    ->helperText(__('Maximum file size: :size MB', ['size' => $maxSizeMb])),
            ])
            ->action(function (array $data): void {
                $uploaded = 0;
                foreach ($data['files'] ?? [] as $file) {
                    try {
                        $filename = $this->sanitizeFilename($file->getClientOriginalName());
                        $content = file_get_contents($file->getRealPath());
                        $this->getAgent()->fileUpload(
                            $this->getUsername(),
                            $this->currentPath,
                            $filename,
                            $content
                        );
                        $uploaded++;
                    } catch (Exception $e) {
                        Notification::make()
                            ->title(__('Upload failed: :filename', ['filename' => $file->getClientOriginalName()]))
                            ->body($e->getMessage())
                            ->danger()
                            ->send();
                    }
                }

                if ($uploaded > 0) {
                    Notification::make()
                        ->title(__(':count file(s) uploaded', ['count' => $uploaded]))
                        ->success()
                        ->send();
                    $this->loadDirectory();
                    $this->resetTable();
                }
            });
    }

    protected function refreshAction(): Action
    {
        return Action::make('refresh')
            ->label(__('Refresh'))
            ->icon('heroicon-o-arrow-path')
            ->color('gray')
            ->action(function () {
                $this->loadDirectory();
                $this->resetTable();
            });
    }

    // Helper to get CodeEditor language based on file extension
    protected function getLanguageForFile(string $filename): ?Language
    {
        $ext = strtolower(pathinfo($filename, PATHINFO_EXTENSION));

        // Handle .blade.php
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

    // Row Actions
    public function downloadFile(string $path): void
    {
        try {
            $result = $this->getAgent()->fileRead($this->getUsername(), $path);
            $this->dispatch('download-file',
                content: base64_encode(base64_decode($result['content'])),
                filename: basename($path)
            );
        } catch (Exception $e) {
            Notification::make()->title(__('Error downloading'))->body($e->getMessage())->danger()->send();
        }
    }

    public function formatSize(?int $bytes): string
    {
        if ($bytes === null) {
            return '—';
        }
        if ($bytes < 1024) {
            return $bytes.' B';
        }
        if ($bytes < 1048576) {
            return round($bytes / 1024, 1).' KB';
        }
        if ($bytes < 1073741824) {
            return round($bytes / 1048576, 1).' MB';
        }

        return round($bytes / 1073741824, 1).' GB';
    }

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
        try {
            $result = $this->getAgent()->fileRead($this->getUsername(), $path);
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
        } catch (Exception $e) {
            return null;
        }
    }

    public function parsePermissions(string $mode): array
    {
        $mode = ltrim($mode, '0');
        $mode = str_pad($mode, 3, '0', STR_PAD_LEFT);

        $owner = (int) ($mode[0] ?? 0);
        $group = (int) ($mode[1] ?? 0);
        $other = (int) ($mode[2] ?? 0);

        return [
            'mode' => $mode,
            'owner_read' => (bool) ($owner & 4),
            'owner_write' => (bool) ($owner & 2),
            'owner_execute' => (bool) ($owner & 1),
            'group_read' => (bool) ($group & 4),
            'group_write' => (bool) ($group & 2),
            'group_execute' => (bool) ($group & 1),
            'other_read' => (bool) ($other & 4),
            'other_write' => (bool) ($other & 2),
            'other_execute' => (bool) ($other & 1),
        ];
    }

    public function buildPermissionMode(array $data): string
    {
        $owner = 0;
        $group = 0;
        $other = 0;

        if ($data['owner_read'] ?? false) {
            $owner += 4;
        }
        if ($data['owner_write'] ?? false) {
            $owner += 2;
        }
        if ($data['owner_execute'] ?? false) {
            $owner += 1;
        }

        if ($data['group_read'] ?? false) {
            $group += 4;
        }
        if ($data['group_write'] ?? false) {
            $group += 2;
        }
        if ($data['group_execute'] ?? false) {
            $group += 1;
        }

        if ($data['other_read'] ?? false) {
            $other += 4;
        }
        if ($data['other_write'] ?? false) {
            $other += 2;
        }
        if ($data['other_execute'] ?? false) {
            $other += 1;
        }

        return "{$owner}{$group}{$other}";
    }

    protected function trashAction(): Action
    {
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
            ->modalContent(fn () => view('filament.jabali.components.trash-table-embed'));
    }

    public function getTrashItems(): array
    {
        try {
            $result = $this->getAgent()->fileListTrash($this->getUsername());

            return $result['items'] ?? [];
        } catch (Exception) {
            return [];
        }
    }

    public function restoreFromTrash(string $trashName): void
    {
        try {
            $result = $this->getAgent()->fileRestore($this->getUsername(), $trashName);
            Notification::make()
                ->title(__('Restored'))
                ->body(__('Restored to: :path', ['path' => $result['restored_path'] ?? '']))
                ->success()
                ->send();
            $this->loadDirectory();
            $this->resetTable();
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
        }
    }

    public function deleteFromTrash(string $trashName): void
    {
        try {
            $trashPath = ".trash/$trashName";
            $this->getAgent()->fileDelete($this->getUsername(), $trashPath);
            Notification::make()->title(__('Permanently deleted'))->success()->send();
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
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
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
        }
    }
}
