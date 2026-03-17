<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Filament\Jabali\Widgets\ActivityLogTable;
use App\Services\Agent\AgentClient;
use App\Support\Formatter;
use App\Support\SafeError;
use BackedEnum;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\EmbeddedTable;
use Filament\Schemas\Components\Tabs;
use Filament\Schemas\Components\Tabs\Tab;
use Filament\Schemas\Components\View;
use Filament\Schemas\Schema;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\Auth;
use Livewire\Attributes\Url;

class Logs extends Page implements HasActions, HasForms
{
    use InteractsWithActions;
    use InteractsWithForms;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-document-text';

    public static function getNavigationLabel(): string
    {
        return __('Logs & Statistics');
    }

    protected static ?int $navigationSort = 13;

    protected static ?string $slug = 'logs';

    protected string $view = 'filament.jabali.pages.logs';

    #[Url]
    public ?string $selectedDomain = null;

    #[Url(as: 'tab')]
    public string $activeTab = 'logs';

    public string $logType = 'access';

    public int $logLines = 100;

    public string $logContent = '';

    public array $logInfo = [];

    public bool $statsGenerated = false;

    public string $statsUrl = '';

    public array $domains = [];

    public function getTitle(): string|Htmlable
    {
        return __('Logs & Statistics');
    }

    public function mount(): void
    {
        $this->loadDomains();
        $this->activeTab = $this->normalizeTab($this->activeTab);

        if (! empty($this->domains) && ! $this->selectedDomain) {
            $this->selectedDomain = $this->domains[0]['domain'] ?? null;
        }

        if ($this->selectedDomain) {
            $this->loadLogs();
        }
    }

    public function updatedActiveTab(): void
    {
        $this->activeTab = $this->normalizeTab($this->activeTab);
        if ($this->activeTab === 'logs' && $this->selectedDomain) {
            $this->loadLogs();
        }
    }

    public function setTab(string $tab): void
    {
        $this->activeTab = $this->normalizeTab($tab);
    }

    protected function getForms(): array
    {
        return ['tabsForm'];
    }

    public function tabsForm(Schema $schema): Schema
    {
        return $schema->schema([
            Tabs::make(__('Log Sections'))
                ->contained()
                ->livewireProperty('activeTab')
                ->tabs([
                    'logs' => Tab::make(__('Logs'))
                        ->icon('heroicon-o-document-text')
                        ->schema([
                            View::make('filament.jabali.pages.logs-tab-logs'),
                        ]),
                    'stats' => Tab::make(__('Statistics'))
                        ->icon('heroicon-o-chart-bar')
                        ->schema([
                            View::make('filament.jabali.pages.logs-tab-stats'),
                        ]),
                    'activity' => Tab::make(__('Activity Log'))
                        ->icon('heroicon-o-clipboard-document-list')
                        ->schema([
                            EmbeddedTable::make(ActivityLogTable::class),
                        ]),
                ]),
        ]);
    }

    protected function normalizeTab(?string $tab): string
    {
        return match ($tab) {
            'logs', 'activity', 'stats' => (string) $tab,
            default => 'logs',
        };
    }

    protected function getAgent(): AgentClient
    {
        return app(AgentClient::class);
    }

    protected function getUsername(): string
    {
        return Auth::user()->username ?? Auth::user()->name ?? 'unknown';
    }

    protected function loadDomains(): void
    {
        try {
            $result = $this->getAgent()->send('domain.list', [
                'username' => $this->getUsername(),
            ]);

            $this->domains = ($result['success'] ?? false) ? ($result['domains'] ?? []) : [];
        } catch (\Throwable $exception) {
            $this->domains = [];
        }
    }

    public function getDomainOptions(): array
    {
        $options = [];
        foreach ($this->domains as $domain) {
            $d = $domain['domain'] ?? $domain;
            $options[$d] = $d;
        }

        return $options;
    }

    public function updatedSelectedDomain(): void
    {
        $this->statsGenerated = false;
        $this->statsUrl = '';
        $this->loadLogs();
    }

    public function setLogType(string $type): void
    {
        $this->logType = $type;
        $this->loadLogs();
    }

    public function loadLogs(): void
    {
        if (! $this->selectedDomain) {
            $this->logContent = '';
            $this->logInfo = [];

            return;
        }

        try {
            $result = $this->getAgent()->send('logs.tail', [
                'username' => $this->getUsername(),
                'domain' => $this->selectedDomain,
                'type' => $this->logType,
                'lines' => $this->logLines,
            ]);

            if ($result['success'] ?? false) {
                $this->logContent = $result['content'] ?? '';
                $this->logInfo = [
                    'file_size' => Formatter::bytes($result['file_size'] ?? 0),
                    'last_modified' => $result['last_modified'] ?? '',
                    'lines' => $result['lines'] ?? 0,
                ];
            } else {
                $this->logContent = '';
                $this->logInfo = [];
            }
        } catch (\Exception $e) {
            $this->logContent = '';
            $this->logInfo = [];
        }
    }

    public function refreshLogs(): void
    {
        $this->loadLogs();
        Notification::make()
            ->title(__('Logs refreshed'))
            ->success()
            ->send();
    }

    public function generateStats(): void
    {
        if (! $this->selectedDomain) {
            Notification::make()
                ->title(__('No domain selected'))
                ->danger()
                ->send();

            return;
        }

        try {
            $result = $this->getAgent()->send('logs.goaccess', [
                'username' => $this->getUsername(),
                'domain' => $this->selectedDomain,
                'period' => 'all',
            ]);

            if ($result['success'] ?? false) {
                $this->statsGenerated = true;
                $this->statsUrl = 'https://'.$this->selectedDomain.($result['report_url'] ?? '/stats/report.html');

                Notification::make()
                    ->title(__('Statistics generated'))
                    ->body(__('Report generated with :lines log entries', ['lines' => number_format($result['log_lines'] ?? 0)]))
                    ->success()
                    ->send();
            } else {
                Notification::make()
                    ->title(__('Error generating statistics'))
                    ->body($result['error'] ?? 'Unknown error')
                    ->danger()
                    ->send();
            }
        } catch (\Exception $e) {
            Notification::make()
                ->title(__('Error'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    protected function getHeaderActions(): array
    {
        return [

            Action::make('generateStats')
                ->label(__('Generate Statistics'))
                ->icon('heroicon-o-chart-bar')
                ->color('primary')
                ->visible(fn () => $this->selectedDomain !== null && $this->activeTab === 'stats')
                ->action(fn () => $this->generateStats()),

            Action::make('refreshLogs')
                ->label(__('Refresh'))
                ->icon('heroicon-o-arrow-path')
                ->color('gray')
                ->visible(fn () => $this->selectedDomain !== null && $this->activeTab === 'logs')
                ->action(fn () => $this->refreshLogs()),
        ];
    }
}
