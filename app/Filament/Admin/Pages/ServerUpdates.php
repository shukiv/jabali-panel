<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Services\Agent\AgentClient;
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
use Illuminate\Support\Facades\Artisan;
use Illuminate\Support\Facades\File;
use Illuminate\Support\Facades\View;

class ServerUpdates extends Page implements HasActions, HasTable
{
    use InteractsWithActions;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedArrowPathRoundedSquare;

    protected static ?int $navigationSort = 22;

    protected static ?string $slug = 'server-updates';

    protected string $view = 'filament.admin.pages.server-updates';

    public array $packages = [];

    public ?string $currentVersion = null;

    public ?int $behindCount = null;

    public array $recentChanges = [];

    public ?string $lastCheckedAt = null;

    public ?string $jabaliOutput = null;

    public ?string $jabaliOutputTitle = null;

    public ?string $jabaliOutputAt = null;

    public bool $isChecking = false;

    public bool $isUpgrading = false;

    public array $refreshOutput = [];

    public ?string $refreshOutputAt = null;

    public ?string $refreshOutputTitle = null;

    protected ?AgentClient $agent = null;

    protected bool $updatesLoaded = false;

    public function getTitle(): string|Htmlable
    {
        return __('System Updates');
    }

    public static function getNavigationLabel(): string
    {
        return __('System Updates');
    }

    public function mount(): void
    {
        $this->loadUpdates(false);
        $this->loadVersionInfo();
    }

    public function getUpdateStatusLabelProperty(): string
    {
        if ($this->behindCount === null) {
            return __('Not checked');
        }

        if ($this->behindCount === 0) {
            return __('Up to date');
        }

        return __(':count commit(s) behind', ['count' => $this->behindCount]);
    }

    protected function getAgent(): AgentClient
    {
        return $this->agent ??= new AgentClient;
    }

    public function loadUpdates(bool $refreshTable = true, bool $refreshApt = false): void
    {
        try {
            $result = $this->getAgent()->updatesList($refreshApt);
            $this->packages = $result['packages'] ?? [];
            $this->updatesLoaded = true;

            if ($refreshApt) {
                $refreshOutput = $result['refresh_output'] ?? [];
                $refreshLines = is_array($refreshOutput) ? $refreshOutput : [$refreshOutput];
                $this->refreshOutput = ! empty(array_filter($refreshLines, static fn ($line) => $line !== null && $line !== ''))
                    ? $refreshLines
                    : [__('No output captured.')];
                $this->refreshOutputAt = now()->format('Y-m-d H:i:s');
                $this->refreshOutputTitle = __('Update Refresh Output');
            }

            $warnings = $result['warnings'] ?? [];
            if (! empty($warnings)) {
                Notification::make()
                    ->title(__('Update check completed with warnings'))
                    ->body(implode("\n", array_filter($warnings)))
                    ->warning()
                    ->send();
            }
        } catch (\Exception $e) {
            $this->packages = [];
            $this->updatesLoaded = true;
            if ($refreshApt) {
                $this->refreshOutput = [$e->getMessage()];
                $this->refreshOutputAt = now()->format('Y-m-d H:i:s');
                $this->refreshOutputTitle = __('Update Refresh Output');
            }
            Notification::make()
                ->title(__('Failed to load updates'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }

        if ($refreshTable) {
            $this->resetTable();
        }
    }

    public function loadVersionInfo(): void
    {
        $this->currentVersion = $this->readVersion();
    }

    public function checkForUpdates(): void
    {
        $this->isChecking = true;

        try {
            $exitCode = Artisan::call('jabali:upgrade', ['--check' => true]);
            $output = trim(Artisan::output());

            if ($exitCode !== 0) {
                throw new \RuntimeException($output !== '' ? $output : __('Update check failed.'));
            }

            $this->jabaliOutput = $output !== '' ? $output : __('No output captured.');
            $this->jabaliOutputTitle = __('Update Check Output');
            $this->jabaliOutputAt = now()->format('Y-m-d H:i:s');
            $this->lastCheckedAt = $this->jabaliOutputAt;
            $this->parseUpdateOutput($output);

            Notification::make()
                ->title(__('Update check completed'))
                ->success()
                ->send();
        } catch (\Throwable $e) {
            $this->jabaliOutput = $e->getMessage();
            $this->jabaliOutputTitle = __('Update Check Output');
            $this->jabaliOutputAt = now()->format('Y-m-d H:i:s');
            Notification::make()
                ->title(__('Update check failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        } finally {
            $this->isChecking = false;
        }
    }

    public function performUpgrade(): void
    {
        $this->isUpgrading = true;

        try {
            $exitCode = Artisan::call('jabali:upgrade', ['--force' => true]);
            $output = trim(Artisan::output());

            if ($exitCode !== 0) {
                throw new \RuntimeException($output !== '' ? $output : __('Upgrade failed.'));
            }

            $this->jabaliOutput = $output !== '' ? $output : __('No output captured.');
            $this->jabaliOutputTitle = __('Upgrade Output');
            $this->jabaliOutputAt = now()->format('Y-m-d H:i:s');
            $this->loadVersionInfo();
            $this->behindCount = 0;
            $this->recentChanges = [];

            Notification::make()
                ->title(__('Upgrade completed'))
                ->success()
                ->send();
        } catch (\Throwable $e) {
            $this->jabaliOutput = $e->getMessage();
            $this->jabaliOutputTitle = __('Upgrade Output');
            $this->jabaliOutputAt = now()->format('Y-m-d H:i:s');
            Notification::make()
                ->title(__('Upgrade failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        } finally {
            $this->isUpgrading = false;
        }
    }

    public function runUpdates(): void
    {
        try {
            $result = $this->getAgent()->updatesRun();
            $output = $result['output'] ?? [];
            $outputLines = is_array($output) ? $output : [$output];
            $this->refreshOutput = ! empty(array_filter($outputLines, static fn ($line) => $line !== null && $line !== ''))
                ? $outputLines
                : [__('No output captured.')];
            $this->refreshOutputAt = now()->format('Y-m-d H:i:s');
            $this->refreshOutputTitle = __('System Update Output');
            Notification::make()
                ->title(__('Updates completed'))
                ->success()
                ->send();
        } catch (\Exception $e) {
            $this->refreshOutput = [$e->getMessage()];
            $this->refreshOutputAt = now()->format('Y-m-d H:i:s');
            $this->refreshOutputTitle = __('System Update Output');
            Notification::make()
                ->title(__('Update failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }

        $this->loadUpdates();
        $this->dispatch('$refresh');
    }

    protected function parseUpdateOutput(string $output): void
    {
        $this->behindCount = null;
        $this->recentChanges = [];

        if (preg_match('/Updates available:\s+(\d+)/', $output, $matches)) {
            $this->behindCount = (int) $matches[1];
        } elseif (str_contains(strtolower($output), 'up to date')) {
            $this->behindCount = 0;
        }

        if (preg_match('/Recent changes:\s*(.+)$/s', $output, $matches)) {
            $lines = preg_split('/\r?\n/', trim($matches[1]));
            $this->recentChanges = array_values(array_filter($lines, static fn (string $line): bool => trim($line) !== ''));
        }
    }

    protected function readVersion(): string
    {
        $versionFile = base_path('VERSION');
        if (! File::exists($versionFile)) {
            return 'unknown';
        }

        $content = File::get($versionFile);
        if (preg_match('/VERSION=(.+)/', $content, $matches)) {
            return trim($matches[1]);
        }

        return 'unknown';
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(function () {
                if (! $this->updatesLoaded) {
                    $this->loadUpdates(false);
                }

                return collect($this->packages)
                    ->mapWithKeys(function (array $package, int $index): array {
                        $keyParts = [
                            $package['name'] ?? (string) $index,
                            $package['current_version'] ?? '',
                            $package['new_version'] ?? '',
                        ];
                        $key = implode('|', array_filter($keyParts, fn (string $part): bool => $part !== ''));

                        return [$key !== '' ? $key : (string) $index => $package];
                    })
                    ->all();
            })
            ->columns([
                TextColumn::make('name')
                    ->label(__('Package')),
                TextColumn::make('current_version')
                    ->label(__('Current Version')),
                TextColumn::make('new_version')
                    ->label(__('New Version')),
            ])
            ->emptyStateHeading(__('No updates available'))
            ->emptyStateDescription(__('Your system packages are up to date.'));
    }
}
