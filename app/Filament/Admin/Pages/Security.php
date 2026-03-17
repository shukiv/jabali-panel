<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Filament\Admin\Widgets\Security\BannedIpsTable;
use App\Filament\Admin\Widgets\Security\Fail2banLogsTable;
use App\Filament\Admin\Widgets\Security\JailsTable;
use App\Filament\Admin\Widgets\Security\LynisResultsTable;
use App\Filament\Admin\Widgets\Security\NiktoResultsTable;
use App\Filament\Admin\Widgets\Security\QuarantinedFilesTable;
use App\Filament\Admin\Widgets\Security\ThreatsTable;
use App\Filament\Admin\Widgets\Security\WpscanResultsTable;
use App\Jobs\InstallScannerTool;
use App\Jobs\RunClamavScan;
use App\Jobs\RunLynisScan;
use App\Jobs\RunNiktoScan;
use App\Jobs\RunWpscanScan;
use App\Models\AuditLog;
use App\Models\Setting;
use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Support\SafeError;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Action as FormAction;
use Filament\Actions\Action as TableAction;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Actions as FormActions;
use Filament\Schemas\Components\EmbeddedTable;
use Filament\Schemas\Components\Grid;
use Filament\Schemas\Components\Group;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\Tabs;
use Filament\Schemas\Components\Tabs\Tab;
use Filament\Schemas\Components\Text;
use Filament\Schemas\Components\View;
use Filament\Schemas\Schema;
use Filament\Support\Enums\Alignment;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Livewire\Attributes\On;
use Livewire\Attributes\Url;

class Security extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-shield-check';

    protected static ?int $navigationSort = 5;

    protected static ?string $slug = 'security';

    protected string $view = 'filament.admin.pages.security';

    // Firewall
    public bool $firewallInstalled = false;

    public bool $firewallEnabled = false;

    public array $firewallRules = [];

    public string $defaultIncoming = 'deny';

    public string $defaultOutgoing = 'allow';

    public string $firewallStatusText = '';

    public ?int $ruleToDelete = null;

    // WAF
    public bool $wafInstalled = false;

    public bool $wafEnabled = false;

    // Fail2ban
    public bool $fail2banInstalled = false;

    public bool $fail2banRunning = false;

    public string $fail2banVersion = '';

    public array $jails = [];

    public array $availableJails = [];

    public ?int $totalBanned = null;

    public array $fail2banLogs = [];

    public int $maxRetry = 5;

    public int $banTime = 600;

    public int $findTime = 600;

    // ClamAV
    public bool $clamavInstalled = false;

    public bool $clamavRunning = false;

    public string $clamavVersion = '';

    public int $signatureCount = 0;

    public string $lastUpdate = '';

    public bool $clamavLightMode = false;

    public array $signatureDatabases = [];

    public array $recentThreats = [];

    public array $quarantinedFiles = [];

    public bool $realtimeEnabled = false;

    public bool $realtimeRunning = false;

    // SSH Settings
    public bool $sshPasswordAuth = false;

    public bool $sshPubkeyAuth = true;

    public int $sshPort = 22;

    public bool $sshShellAccessEnabled = true;

    // Scanner properties
    public bool $lynisInstalled = false;

    public bool $wpscanInstalled = false;

    public bool $niktoInstalled = false;

    public string $lynisVersion = '';

    public string $wpscanVersion = '';

    public string $niktoVersion = '';

    public array $lynisResults = [];

    public array $wpscanResults = [];

    public array $niktoResults = [];

    public bool $isScanning = false;

    public string $currentScan = '';

    public string $scanOutput = '';

    public ?string $selectedWpSiteId = null;

    public ?array $data = [];

    public ?string $lastLynisScan = null;

    public ?string $lastWpscanScan = null;

    public ?string $lastNiktoScan = null;

    // ClamAV on-demand scan properties
    public ?string $selectedClamUser = null;

    public array $clamScanResults = [];

    public ?string $lastClamScan = null;

    #[Url(as: 'tab')]
    public ?string $activeTab = 'overview';

    public static function getNavigationLabel(): string
    {
        return __('Security');
    }

    public function getTitle(): string|Htmlable
    {
        return __('Security Center');
    }

    protected function getAgent(): AgentClient
    {
        return app(AgentClient::class);
    }

    protected function normalizeTabName(?string $tab): string
    {
        return match ($tab) {
            'overview', 'firewall', 'waf', 'fail2ban', 'antivirus', 'ssh', 'scanner' => $tab,
            default => 'overview',
        };
    }

    public function setTab(string $tab): void
    {
        $this->activeTab = $this->normalizeTabName($tab);

        if ($this->activeTab === 'fail2ban') {
            $this->loadFail2banStatus();
        }

        if ($this->activeTab === 'antivirus') {
            $this->loadClamavStatus();
            $this->loadClamScanResults();
        }

        if ($this->activeTab === 'scanner') {
            $this->checkScannerToolStatus();
            $this->loadLastScans();
        }
    }

    public function updatedActiveTab(): void
    {
        $this->setTab($this->activeTab ?? 'overview');
    }

    public function mount(): void
    {
        $this->activeTab = $this->normalizeTabName($this->activeTab);
        $this->loadFirewallStatus();
        $this->loadWafStatus();
        $this->loadFail2banStatusLight();
        $this->loadClamavStatusLight();
        $this->loadSshSettings();

        $this->data = [
            'selectedWpSiteId' => null,
            'selectedClamUser' => null,
        ];

        if ($this->activeTab === 'fail2ban') {
            $this->loadFail2banStatus();
        } elseif ($this->activeTab === 'antivirus') {
            $this->loadClamavStatus();
            $this->loadClamScanResults();
        } elseif ($this->activeTab === 'scanner') {
            $this->checkScannerToolStatus();
            $this->loadLastScans();
        }
    }

    #[On('refresh-security-data')]
    public function refreshSecurityData(): void
    {
        $this->loadWafStatus();
        $this->loadFail2banStatus();
        $this->loadClamavStatus();
    }

    protected function getForms(): array
    {
        return [
            'securityForm',
        ];
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->firewallRules)
            ->columns([
                TextColumn::make('number')
                    ->label(__('#'))
                    ->sortable()
                    ->width('60px'),
                TextColumn::make('action')
                    ->label(__('Action'))
                    ->badge()
                    ->color(fn (string $state): string => match (strtoupper($state)) {
                        'ALLOW' => 'success',
                        'DENY' => 'danger',
                        'LIMIT' => 'warning',
                        default => 'gray',
                    })
                    ->formatStateUsing(fn (string $state): string => strtoupper($state)),
                TextColumn::make('to')
                    ->label(__('To'))
                    ->default('-'),
                TextColumn::make('from')
                    ->label(__('From'))
                    ->default(__('Anywhere'))
                    ->formatStateUsing(fn (?string $state): string => $state ?: __('Anywhere')),
                TextColumn::make('direction')
                    ->label(__('Direction'))
                    ->badge()
                    ->color('gray'),
            ])
            ->recordAction(null)
            ->recordUrl(null)
            ->striped()
            ->emptyStateHeading(__('No firewall rules'))
            ->emptyStateDescription(__('Use the buttons above to add rules.'))
            ->emptyStateIcon('heroicon-o-shield-exclamation')
            ->actions([
                TableAction::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->action(fn (array $record) => $this->deleteRule($record['number'] ?? null)),
            ]);
    }

    public function securityForm(Schema $schema): Schema
    {
        return $schema
            ->schema([
                Tabs::make(__('Security Sections'))
                    ->contained()
                    ->livewireProperty('activeTab')
                    ->tabs([
                        'overview' => Tab::make(__('Overview'))
                            ->icon('heroicon-o-home')
                            ->schema($this->overviewTabContent()),
                        'firewall' => Tab::make(__('Firewall'))
                            ->icon('heroicon-o-shield-check')
                            ->schema($this->firewallTabContent()),
                        'waf' => Tab::make(__('ModSecurity / WAF'))
                            ->icon('heroicon-o-shield-exclamation')
                            ->schema($this->wafTabContent()),
                        'fail2ban' => Tab::make(__('Fail2ban'))
                            ->icon('heroicon-o-lock-closed')
                            ->schema($this->fail2banTabContent()),
                        'antivirus' => Tab::make(__('Antivirus'))
                            ->icon('heroicon-o-bug-ant')
                            ->schema($this->antivirusTabContent()),
                        'ssh' => Tab::make(__('SSH'))
                            ->icon('heroicon-o-command-line')
                            ->schema($this->sshTabContent()),
                        'scanner' => Tab::make(__('Vulnerability Scanner'))
                            ->icon('heroicon-o-magnifying-glass-circle')
                            ->schema($this->scannerTabContent()),
                    ]),
            ]);
    }

    protected function wafTabContent(): array
    {
        return [
            View::make('filament.admin.components.security-waf'),
        ];
    }

    protected function overviewTabContent(): array
    {
        return [
            Grid::make(['default' => 1, 'sm' => 3])
                ->schema([
                    Section::make($this->firewallEnabled ? __('Active') : __('Inactive'))
                        ->description(__('Firewall'))
                        ->icon('heroicon-o-shield-check')
                        ->iconColor($this->firewallEnabled ? 'success' : 'danger'),
                    Section::make($this->wafInstalled
                        ? ($this->wafEnabled ? __('Enabled') : __('Disabled'))
                        : __('Not Installed'))
                        ->description(__('WAF'))
                        ->icon('heroicon-o-shield-exclamation')
                        ->iconColor($this->wafInstalled
                            ? ($this->wafEnabled ? 'success' : 'danger')
                            : 'gray'),
                    Section::make($this->fail2banRunning ? __('Running') : __('Stopped'))
                        ->description(__('Fail2ban'))
                        ->icon('heroicon-o-lock-closed')
                        ->iconColor($this->fail2banRunning ? 'success' : 'danger'),
                    Section::make($this->totalBanned !== null ? (string) $this->totalBanned : __('N/A'))
                        ->description(__('IPs Banned'))
                        ->icon('heroicon-o-lock-closed')
                        ->iconColor($this->fail2banRunning ? 'success' : 'danger'),
                    Section::make((string) count($this->recentThreats))
                        ->description(__('Threats Detected'))
                        ->icon('heroicon-o-bug-ant')
                        ->iconColor($this->clamavInstalled ? 'success' : 'gray'),
                    Section::make($this->clamavRunning ? __('Running') : __('Stopped'))
                        ->description(__('Antivirus'))
                        ->icon('heroicon-o-shield-check')
                        ->iconColor($this->clamavRunning ? 'success' : 'danger'),
                ]),
            Section::make(__('Security Recommendations'))
                ->icon('heroicon-o-light-bulb')
                ->schema(fn () => [
                    View::make('filament.admin.components.security-recommendations'),
                ]),
            Section::make(__('Quick Actions'))
                ->icon('heroicon-o-bolt')
                ->schema([
                    FormActions::make([
                        FormAction::make('installFirewall')
                            ->label(__('Install Firewall'))
                            ->action('installFirewall')
                            ->visible(fn () => ! $this->firewallInstalled),
                        FormAction::make('installFail2ban')
                            ->label(__('Install Fail2ban'))
                            ->action('installFail2ban')
                            ->visible(fn () => ! $this->fail2banInstalled),
                        FormAction::make('installClamav')
                            ->label(__('Install Antivirus'))
                            ->action('installClamav')
                            ->visible(fn () => ! $this->clamavInstalled),
                    ])->visible(fn () => ! $this->firewallInstalled || ! $this->fail2banInstalled || ! $this->clamavInstalled),
                    Text::make(__('All essential security tools are installed.'))
                        ->visible(fn () => $this->firewallInstalled && $this->fail2banInstalled),
                ]),
            Section::make(__('Recent Audit Logs'))
                ->icon('heroicon-o-clipboard-document-list')
                ->schema([
                    EmbeddedTable::make(\App\Filament\Admin\Widgets\Security\AuditLogsTable::class),
                ]),
        ];
    }

    protected function firewallTabContent(): array
    {
        return [
            // Not installed state
            Section::make()
                ->schema([
                    Text::make(__('UFW Firewall is not installed.')),
                    FormActions::make([
                        FormAction::make('installFirewall')
                            ->label(__('Install Firewall'))
                            ->action('installFirewall'),
                    ])->alignment(Alignment::Center),
                ])
                ->visible(fn () => ! $this->firewallInstalled),
            // Installed state
            Group::make([
                Section::make(__('Firewall Status'))
                    ->icon('heroicon-o-shield-check')
                    ->iconColor($this->firewallEnabled ? 'success' : 'danger')
                    ->schema([
                        Grid::make(['default' => 1, 'sm' => 2, 'lg' => 4])
                            ->schema([
                                Section::make($this->firewallEnabled ? __('ACTIVE') : __('INACTIVE'))
                                    ->description(__('Status'))
                                    ->icon('heroicon-o-signal')
                                    ->iconColor($this->firewallEnabled ? 'success' : 'danger'),
                                Section::make(strtoupper($this->defaultIncoming))
                                    ->description(__('Default Incoming'))
                                    ->icon('heroicon-o-arrow-down-circle')
                                    ->iconColor($this->defaultIncoming === 'deny' ? 'success' : 'warning'),
                                Section::make(strtoupper($this->defaultOutgoing))
                                    ->description(__('Default Outgoing'))
                                    ->icon('heroicon-o-arrow-up-circle')
                                    ->iconColor($this->defaultOutgoing === 'allow' ? 'success' : 'warning'),
                                Section::make((string) count($this->firewallRules))
                                    ->description(__('Active Rules'))
                                    ->icon('heroicon-o-queue-list')
                                    ->iconColor('gray'),
                            ]),
                        FormActions::make([
                            FormAction::make('toggleFirewall')
                                ->label(fn () => $this->firewallEnabled ? __('Disable Firewall') : __('Enable Firewall'))
                                ->icon(fn () => $this->firewallEnabled ? 'heroicon-o-x-circle' : 'heroicon-o-shield-check')
                                ->color(fn () => $this->firewallEnabled ? 'danger' : 'success')
                                ->action('toggleFirewall'),
                            FormAction::make('reloadFirewall')
                                ->label(__('Reload'))
                                ->icon('heroicon-o-arrow-path')
                                ->color('gray')
                                ->outlined()
                                ->action('reloadFirewall'),
                            FormAction::make('resetFirewall')
                                ->label(__('Reset All'))
                                ->icon('heroicon-o-trash')
                                ->color('danger')
                                ->outlined()
                                ->action('resetFirewall'),
                        ]),
                    ]),
                Section::make(__('Add Rule'))
                    ->icon('heroicon-o-plus-circle')
                    ->schema([
                        FormActions::make([
                            FormAction::make('allowPort')
                                ->label(__('Allow Port'))
                                ->icon('heroicon-o-plus-circle')
                                ->color('success')
                                ->action('openAllowPort'),
                            FormAction::make('denyPort')
                                ->label(__('Block Port'))
                                ->icon('heroicon-o-x-circle')
                                ->color('danger')
                                ->action('openDenyPort'),
                            FormAction::make('allowIp')
                                ->label(__('Allow IP'))
                                ->icon('heroicon-o-check-circle')
                                ->color('success')
                                ->action('openAllowIp'),
                            FormAction::make('denyIp')
                                ->label(__('Block IP'))
                                ->icon('heroicon-o-no-symbol')
                                ->color('danger')
                                ->action('openDenyIp'),
                            FormAction::make('allowService')
                                ->label(__('Allow Service'))
                                ->icon('heroicon-o-server')
                                ->color('info')
                                ->action('openAllowService'),
                            FormAction::make('limitPort')
                                ->label(__('Rate Limit'))
                                ->icon('heroicon-o-clock')
                                ->color('warning')
                                ->action('openLimitPort'),
                        ]),
                    ]),
                Section::make(__('Firewall Rules'))
                    ->icon('heroicon-o-queue-list')
                    ->schema([
                        EmbeddedTable::make(),
                    ]),
                Section::make(__('Quick Tips'))
                    ->icon('heroicon-o-light-bulb')
                    ->collapsible()
                    ->collapsed()
                    ->schema([
                        Text::make(__('Allow Port: Opens a port for all incoming connections (e.g., 80 for web, 443 for HTTPS)')),
                        Text::make(__('Block IP: Denies all traffic from a specific IP address or subnet')),
                        Text::make(__('Rate Limit: Limits connections to prevent brute-force attacks (6 connections in 30 seconds)')),
                        Text::make(__('Default Policy: Controls what happens to traffic that doesn\'t match any rule')),
                        Text::make(__('Important: Always ensure SSH (port 22) is allowed before enabling the firewall!')),
                    ]),
            ])->visible(fn () => $this->firewallInstalled),
        ];
    }

    protected function fail2banTabContent(): array
    {
        return [
            // Not installed state
            Section::make()
                ->schema([
                    Text::make(__('Fail2ban is not installed.')),
                    FormActions::make([
                        FormAction::make('installFail2ban')
                            ->label(__('Install Fail2ban'))
                            ->action('installFail2ban'),
                    ])->alignment(Alignment::Center),
                ])
                ->visible(fn () => ! $this->fail2banInstalled),
            // Installed state
            Group::make([
                Section::make(__('Fail2ban Status'))
                    ->icon('heroicon-o-lock-closed')
                    ->headerActions([
                        FormAction::make('fail2banStatus')
                            ->label(fn () => $this->fail2banRunning ? __('Running') : __('Stopped'))
                            ->color(fn () => $this->fail2banRunning ? 'success' : 'danger')
                            ->badge(),
                        FormAction::make('startFail2ban')
                            ->label(__('Start'))
                            ->color('success')
                            ->size('sm')
                            ->visible(fn () => ! $this->fail2banRunning)
                            ->action('startFail2ban'),
                        FormAction::make('disableFail2ban')
                            ->label(__('Disable'))
                            ->color('warning')
                            ->size('sm')
                            ->visible(fn () => $this->fail2banRunning)
                            ->requiresConfirmation()
                            ->modalHeading(__('Disable Fail2ban'))
                            ->modalIcon('heroicon-o-exclamation-triangle')
                            ->modalIconColor('warning')
                            ->modalDescription(__('Warning: Fail2ban will be stopped and disabled. You can re-enable it later from this tab.'))
                            ->action('disableFail2ban'),
                    ])
                    ->schema([
                        Grid::make(['default' => 1, 'md' => 3])
                            ->schema([
                                TextInput::make('maxRetry')
                                    ->label(__('Max Retry'))
                                    ->numeric()
                                    ->minValue(1)
                                    ->maxValue(20),
                                TextInput::make('banTime')
                                    ->label(__('Ban Time (seconds)'))
                                    ->numeric()
                                    ->minValue(60),
                                TextInput::make('findTime')
                                    ->label(__('Find Time (seconds)'))
                                    ->numeric()
                                    ->minValue(60),
                            ]),
                        FormActions::make([
                            FormAction::make('saveFail2banSettings')
                                ->label(__('Save Settings'))
                                ->action('saveFail2banSettings'),
                        ]),
                    ]),
                Section::make(__('Protection Modules'))
                    ->icon('heroicon-o-shield-exclamation')
                    ->description(__('Enable or disable protection modules for different services.'))
                    ->schema([
                        EmbeddedTable::make(JailsTable::class, ['jails' => $this->availableJails])
                            ->key('fail2ban-jails-table'),
                    ]),
                Section::make(__('Banned IPs').' ('.($this->totalBanned ?? __('N/A')).')')
                    ->icon('heroicon-o-no-symbol')
                    ->schema([
                        EmbeddedTable::make(BannedIpsTable::class, ['jails' => $this->jails]),
                    ])
                    ->visible(fn () => ($this->totalBanned ?? 0) > 0),
                Section::make(__('Fail2ban Logs'))
                    ->icon('heroicon-o-document-text')
                    ->schema([
                        EmbeddedTable::make(Fail2banLogsTable::class, ['logs' => $this->fail2banLogs])
                            ->key('fail2ban-logs-table'),
                    ])
                    ->collapsible(),
            ])->visible(fn () => $this->fail2banInstalled),
        ];
    }

    protected function antivirusTabContent(): array
    {
        return [
            // Warning for non-installed
            Section::make(__('Memory Requirements'))
                ->icon('heroicon-o-exclamation-triangle')
                ->iconColor('warning')
                ->description(__('ClamAV requires significant memory (~500MB+). Only install if your server has at least 2GB RAM available.'))
                ->visible(fn () => ! $this->clamavInstalled),
            // Not installed state
            Section::make()
                ->schema([
                    Text::make(__('ClamAV Antivirus is not installed.')),
                    FormActions::make([
                        FormAction::make('installClamav')
                            ->label(__('Install ClamAV'))
                            ->action('installClamav')
                            ->requiresConfirmation()
                            ->modalDescription(__('ClamAV uses significant memory. Are you sure you want to install it?')),
                    ])->alignment(Alignment::Center),
                ])
                ->visible(fn () => ! $this->clamavInstalled),
            // Installed state
            Group::make([
                Section::make(__('Resource Warning'))
                    ->icon('heroicon-o-exclamation-triangle')
                    ->iconColor('warning')
                    ->description(__('ClamAV uses significant memory (~500MB+) and CPU resources. Running the daemon or real-time protection on low-resource servers may impact performance. Consider using on-demand scanning instead.')),
                Section::make(__('ClamAV Status'))
                    ->icon('heroicon-o-shield-check')
                    ->headerActions([
                        FormAction::make('daemonStatus')
                            ->label(fn () => __('Daemon').' '.($this->clamavRunning ? __('Running') : __('Stopped')))
                            ->color(fn () => $this->clamavRunning ? 'success' : 'gray')
                            ->badge(),
                        FormAction::make('toggleClamav')
                            ->label(fn () => $this->clamavRunning ? __('Disable') : __('Enable'))
                            ->color(fn () => $this->clamavRunning ? 'warning' : 'success')
                            ->size('sm')
                            ->action(fn () => $this->clamavRunning ? $this->disableClamav() : $this->enableClamav())
                            ->requiresConfirmation()
                            ->modalHeading(fn () => $this->clamavRunning ? __('Disable ClamAV') : __('Enable ClamAV'))
                            ->modalIcon('heroicon-o-exclamation-triangle')
                            ->modalIconColor('warning')
                            ->modalDescription(fn () => $this->clamavRunning
                                ? __('Warning: This will stop and disable ClamAV. You can re-enable it later.')
                                : __('Starting ClamAV daemon uses ~500MB RAM. Continue?')),
                        FormAction::make('updateSignatures')
                            ->label(__('Update Signatures'))
                            ->color('gray')
                            ->size('sm')
                            ->action('updateSignatures'),
                    ])
                    ->schema([
                        Grid::make(['default' => 1, 'md' => 3])
                            ->schema([
                                Section::make($this->clamavVersion ?: __('Unknown'))
                                    ->description(__('Version'))
                                    ->icon('heroicon-o-code-bracket')
                                    ->iconColor('gray'),
                                Section::make(number_format($this->signatureCount))
                                    ->description(__('Signatures'))
                                    ->icon('heroicon-o-document-text')
                                    ->iconColor('gray'),
                                Section::make($this->lastUpdate ?: __('Unknown'))
                                    ->description(__('Last Update'))
                                    ->icon('heroicon-o-clock')
                                    ->iconColor('gray'),
                            ]),
                        Grid::make(['default' => 1, 'md' => 2])
                            ->schema([
                                Section::make(__('Real-time Protection'))
                                    ->description(__('Monitors /home for new PHP, HTML, JS files'))
                                    ->icon($this->realtimeRunning ? 'heroicon-o-shield-check' : 'heroicon-o-shield-exclamation')
                                    ->iconColor($this->realtimeRunning ? 'success' : 'gray')
                                    ->schema([
                                        FormActions::make([
                                            FormAction::make('toggleRealtime')
                                                ->label(fn () => $this->realtimeRunning ? __('Disable') : __('Enable'))
                                                ->color(fn () => $this->realtimeRunning ? 'danger' : 'success')
                                                ->size('sm')
                                                ->action('toggleRealtime'),
                                        ]),
                                    ]),
                                Section::make(__('Database Mode'))
                                    ->description(fn () => $this->clamavLightMode
                                        ? __('Web hosting only: PHP, email, scripts (~50K sigs)')
                                        : __('Full database: all malware signatures'))
                                    ->icon($this->clamavLightMode ? 'heroicon-o-bolt' : 'heroicon-o-circle-stack')
                                    ->iconColor($this->clamavLightMode ? 'warning' : 'gray')
                                    ->schema([
                                        FormActions::make([
                                            FormAction::make('toggleLightMode')
                                                ->label(fn () => $this->clamavLightMode ? __('Full') : __('Light'))
                                                ->color(fn () => $this->clamavLightMode ? 'warning' : 'gray')
                                                ->size('sm')
                                                ->action('toggleLightMode')
                                                ->requiresConfirmation()
                                                ->modalDescription(fn () => $this->clamavLightMode
                                                    ? __('Switch to full database? This will download ~400MB of signatures.')
                                                    : __('Switch to lightweight mode? This reduces signatures to web hosting essentials only.')),
                                        ]),
                                    ]),
                            ]),
                    ]),
                Section::make(__('On-Demand Scanner'))
                    ->icon('heroicon-o-magnifying-glass')
                    ->description(__('Manually scan user directories or the entire server for malware and threats.'))
                    ->schema([
                        Grid::make(['default' => 1, 'md' => 2])
                            ->schema([
                                Section::make(__('Scan User Directory'))
                                    ->description(__('Scan a specific user\'s home directory for malware.'))
                                    ->icon('heroicon-o-user')
                                    ->iconColor('gray')
                                    ->schema([
                                        Select::make('selectedClamUser')
                                            ->label(__('Select User'))
                                            ->options(fn () => $this->getClamScanUsers())
                                            ->placeholder(__('Select a user to scan'))
                                            ->searchable()
                                            ->live(),
                                        FormActions::make([
                                            FormAction::make('runClamScanUser')
                                                ->label(fn () => $this->isScanning && $this->currentScan === 'clamav' ? __('Scanning...') : __('Scan User'))
                                                ->icon('heroicon-o-play')
                                                ->action('runClamScanUser')
                                                ->disabled(fn () => $this->isScanning || ! $this->selectedClamUser),
                                        ]),
                                    ]),
                                Section::make(__('Server-Wide Scan'))
                                    ->description(__('Scan all user directories (/home). This may take a long time.'))
                                    ->icon('heroicon-o-server')
                                    ->iconColor('warning')
                                    ->schema([
                                        Text::make(__('Server-wide scans can take 30+ minutes depending on data size.')),
                                        FormActions::make([
                                            FormAction::make('runClamScanServer')
                                                ->label(fn () => $this->isScanning && $this->currentScan === 'clamav' ? __('Scanning...') : __('Scan All Users'))
                                                ->icon('heroicon-o-server')
                                                ->color('warning')
                                                ->action('runClamScanServer')
                                                ->disabled(fn () => $this->isScanning)
                                                ->requiresConfirmation()
                                                ->modalDescription(__('This will scan all user directories and may take a long time. Continue?')),
                                        ]),
                                    ]),
                            ]),
                        Text::make($this->lastClamScan ? __('Last scan:').' '.$this->lastClamScan : '')
                            ->visible(fn () => (bool) $this->lastClamScan),
                    ]),
                Section::make(__('Scan Output'))
                    ->icon('heroicon-o-command-line')
                    ->collapsible()
                    ->schema(fn () => $this->buildClamScanOutputSchema())
                    ->visible(fn () => $this->isScanning && $this->currentScan === 'clamav' || ! empty($this->clamScanResults['raw_output'] ?? '')),
                Section::make(__('Scan Results'))
                    ->icon('heroicon-o-document-chart-bar')
                    ->collapsible()
                    ->schema(fn () => $this->buildClamavScanResultsSchema())
                    ->visible(fn () => ! empty($this->clamScanResults)),
                Section::make(__('Quarantined Files').' ('.count($this->quarantinedFiles).')')
                    ->icon('heroicon-o-archive-box')
                    ->schema([
                        EmbeddedTable::make(QuarantinedFilesTable::class, ['files' => $this->quarantinedFiles]),
                    ])
                    ->visible(fn () => count($this->quarantinedFiles) > 0),
                Section::make(__('Recent Threats'))
                    ->icon('heroicon-o-exclamation-triangle')
                    ->schema([
                        EmbeddedTable::make(ThreatsTable::class, ['threats' => $this->recentThreats]),
                    ])
                    ->visible(fn () => count($this->recentThreats) > 0),
            ])->visible(fn () => $this->clamavInstalled),
        ];
    }

    protected function sshTabContent(): array
    {
        return [
            // Current Configuration - 3 widgets on top
            Grid::make(['default' => 1, 'sm' => 3])
                ->schema([
                    Section::make($this->sshPasswordAuth ? __('Enabled') : __('Disabled'))
                        ->description(__('Password Auth'))
                        ->icon('heroicon-o-key')
                        ->iconColor($this->sshPasswordAuth ? 'warning' : 'success'),
                    Section::make($this->sshPubkeyAuth ? __('Enabled') : __('Disabled'))
                        ->description(__('Public Key Auth'))
                        ->icon('heroicon-o-finger-print')
                        ->iconColor($this->sshPubkeyAuth ? 'success' : 'danger'),
                    Section::make((string) $this->sshPort)
                        ->description(__('SSH Port'))
                        ->icon('heroicon-o-server')
                        ->iconColor($this->sshPort != 22 ? 'success' : 'gray'),
                ]),
            Section::make(__('Important Notice'))
                ->icon('heroicon-o-exclamation-triangle')
                ->iconColor('warning')
                ->description(__('Changing SSH settings can lock you out of the server. Ensure you have console access or an active SSH session before disabling password authentication.')),
            Section::make(__('SSH Authentication Settings'))
                ->icon('heroicon-o-command-line')
                ->schema([
                    Toggle::make('sshPasswordAuth')
                        ->label(__('Password Authentication'))
                        ->helperText(__('Allow users to log in using passwords. Disable for key-only access.')),
                    Toggle::make('sshPubkeyAuth')
                        ->label(__('Public Key Authentication'))
                        ->helperText(__('Allow users to log in using SSH keys. Recommended for security.')),
                    Toggle::make('sshShellAccessEnabled')
                        ->label(__('Terminal Access (Shell)'))
                        ->helperText(__('Allow users to enable SSH shell access from the user panel. wp-cli is available in the jailed shell.')),
                    TextInput::make('sshPort')
                        ->label(__('SSH Port'))
                        ->numeric()
                        ->minValue(1)
                        ->maxValue(65535)
                        ->helperText(__('Default is 22. Changing port can help reduce automated attacks.'))
                        ->columnSpan(1),
                    FormActions::make([
                        FormAction::make('saveSshSettings')
                            ->label(__('Save SSH Settings'))
                            ->action('saveSshSettings')
                            ->requiresConfirmation()
                            ->modalDescription(__('Are you sure? This will restart the SSH service immediately.')),
                    ]),
                ]),
        ];
    }

    protected function scannerTabContent(): array
    {
        return [
            Grid::make(['default' => 1, 'md' => 3])
                ->schema([
                    // Lynis
                    Section::make(__('Lynis'))
                        ->icon('heroicon-o-shield-check')
                        ->description(__('System security auditing tool. Checks configurations, permissions, and hardening settings.'))
                        ->headerActions([
                            FormAction::make('lynisStatus')
                                ->label(fn () => $this->lynisInstalled ? __('Installed') : __('Not Installed'))
                                ->color(fn () => $this->lynisInstalled ? 'success' : 'danger')
                                ->badge(),
                        ])
                        ->schema([
                            Group::make([
                                Text::make($this->lynisVersion)
                                    ->visible(fn () => $this->lynisInstalled && $this->lynisVersion),
                                Text::make($this->lastLynisScan ? __('Last:').' '.$this->lastLynisScan : '')
                                    ->visible(fn () => $this->lynisInstalled && $this->lastLynisScan),
                            ])->visible(fn () => $this->lynisInstalled),

                            FormActions::make([
                                FormAction::make('runLynisScan')
                                    ->label(fn () => $this->isScanning && $this->currentScan === 'lynis' ? __('Scanning...') : __('Run System Audit'))
                                    ->icon('heroicon-o-play')
                                    ->color('success')
                                    ->action('runLynisScan')
                                    ->disabled(fn () => $this->isScanning),
                            ])->visible(fn () => $this->lynisInstalled),

                            FormActions::make([
                                FormAction::make('installLynis')
                                    ->label(__('Install Lynis'))
                                    ->icon('heroicon-o-arrow-down-tray')
                                    ->action('installLynis'),
                            ])->visible(fn () => ! $this->lynisInstalled),
                        ]),

                    // WPScan
                    Section::make(__('WPScan'))
                        ->icon('heroicon-o-globe-alt')
                        ->description(__('WordPress vulnerability scanner. Checks for vulnerable plugins, themes, and core issues.'))
                        ->headerActions([
                            FormAction::make('wpscanStatus')
                                ->label(fn () => $this->wpscanInstalled ? __('Installed') : __('Not Installed'))
                                ->color(fn () => $this->wpscanInstalled ? 'success' : 'danger')
                                ->badge(),
                        ])
                        ->schema([
                            Group::make([
                                Text::make($this->wpscanVersion)
                                    ->visible(fn () => $this->wpscanInstalled && $this->wpscanVersion),
                                Text::make($this->lastWpscanScan ? __('Last:').' '.$this->lastWpscanScan : '')
                                    ->visible(fn () => $this->wpscanInstalled && $this->lastWpscanScan),

                                Select::make('selectedWpSiteId')
                                    ->label(__('WordPress Site'))
                                    ->options(fn () => $this->getLocalWordPressSites())
                                    ->placeholder(__('Select a WordPress site'))
                                    ->searchable()
                                    ->live()
                                    ->visible(fn () => count($this->getLocalWordPressSites()) > 0),

                                Text::make(__('No WordPress sites found'))
                                    ->visible(fn () => count($this->getLocalWordPressSites()) === 0),
                            ])->visible(fn () => $this->wpscanInstalled),

                            FormActions::make([
                                FormAction::make('runWpscanOnSite')
                                    ->label(fn () => $this->isScanning && $this->currentScan === 'wpscan' ? __('Scanning...') : __('Scan WordPress Site'))
                                    ->icon('heroicon-o-play')
                                    ->color('info')
                                    ->action('runWpscanOnSite')
                                    ->disabled(fn () => $this->isScanning || ! $this->selectedWpSiteId),
                            ])->visible(fn () => $this->wpscanInstalled && count($this->getLocalWordPressSites()) > 0),

                            FormActions::make([
                                FormAction::make('installWpscan')
                                    ->label(__('Install WPScan'))
                                    ->icon('heroicon-o-arrow-down-tray')
                                    ->action('installWpscan'),
                            ])->visible(fn () => ! $this->wpscanInstalled),
                        ]),

                    // Nikto
                    Section::make(__('Nikto'))
                        ->icon('heroicon-o-server')
                        ->description(__('Web server scanner. Finds server misconfigurations and known vulnerabilities on localhost.'))
                        ->headerActions([
                            FormAction::make('niktoStatus')
                                ->label(fn () => $this->niktoInstalled ? __('Installed') : __('Not Installed'))
                                ->color(fn () => $this->niktoInstalled ? 'success' : 'danger')
                                ->badge(),
                        ])
                        ->schema([
                            Group::make([
                                Text::make($this->niktoVersion)
                                    ->visible(fn () => $this->niktoInstalled && $this->niktoVersion),
                                Text::make($this->lastNiktoScan ? __('Last:').' '.$this->lastNiktoScan : '')
                                    ->visible(fn () => $this->niktoInstalled && $this->lastNiktoScan),
                            ])->visible(fn () => $this->niktoInstalled),

                            FormActions::make([
                                FormAction::make('runNiktoScan')
                                    ->label(fn () => $this->isScanning && $this->currentScan === 'nikto' ? __('Scanning...') : __('Scan Local Server'))
                                    ->icon('heroicon-o-play')
                                    ->color('warning')
                                    ->action('runNiktoScan')
                                    ->disabled(fn () => $this->isScanning),
                            ])->visible(fn () => $this->niktoInstalled),

                            FormActions::make([
                                FormAction::make('installNikto')
                                    ->label(__('Install Nikto'))
                                    ->icon('heroicon-o-arrow-down-tray')
                                    ->action('installNikto'),
                            ])->visible(fn () => ! $this->niktoInstalled),
                        ]),
                ]),

            // Lynis Results
            Section::make(__('Lynis System Audit Results'))
                ->icon('heroicon-o-document-chart-bar')
                ->schema([
                    Grid::make(['default' => 1, 'md' => 3])
                        ->schema([
                            Section::make((string) ($this->lynisResults['hardening_index'] ?? 0))
                                ->description(__('Hardening Index'))
                                ->icon('heroicon-o-shield-check')
                                ->iconColor(fn () => ($this->lynisResults['hardening_index'] ?? 0) >= 70 ? 'success' : (($this->lynisResults['hardening_index'] ?? 0) >= 50 ? 'warning' : 'danger')),
                            Section::make((string) count($this->lynisResults['warnings'] ?? []))
                                ->description(__('Warnings'))
                                ->icon('heroicon-o-exclamation-triangle')
                                ->iconColor('warning'),
                            Section::make((string) count($this->lynisResults['suggestions'] ?? []))
                                ->description(__('Suggestions'))
                                ->icon('heroicon-o-light-bulb')
                                ->iconColor('primary'),
                        ]),
                    Section::make(__('Warnings'))
                        ->icon('heroicon-o-exclamation-triangle')
                        ->iconColor('warning')
                        ->collapsible()
                        ->schema([
                            EmbeddedTable::make(LynisResultsTable::class, ['results' => $this->lynisResults, 'type' => 'warnings']),
                        ])
                        ->visible(fn () => ! empty($this->lynisResults['warnings'] ?? [])),
                    Section::make(__('Suggestions'))
                        ->icon('heroicon-o-light-bulb')
                        ->iconColor('info')
                        ->collapsible()
                        ->schema([
                            EmbeddedTable::make(LynisResultsTable::class, ['results' => $this->lynisResults, 'type' => 'suggestions']),
                        ])
                        ->visible(fn () => ! empty($this->lynisResults['suggestions'] ?? [])),
                ])
                ->visible(fn () => ! empty($this->lynisResults)),

            // WPScan Results
            Section::make(__('WPScan Results'))
                ->icon('heroicon-o-globe-alt')
                ->schema([
                    Section::make('WordPress '.($this->wpscanResults['version']['number'] ?? __('Unknown')))
                        ->icon('heroicon-o-code-bracket')
                        ->iconColor('info')
                        ->visible(fn () => isset($this->wpscanResults['version']['number'])),
                    EmbeddedTable::make(WpscanResultsTable::class, ['results' => $this->wpscanResults]),
                ])
                ->visible(fn () => ! empty($this->wpscanResults) && ! isset($this->wpscanResults['error'])),

            // Nikto Results
            Section::make(__('Nikto Scan Results'))
                ->icon('heroicon-o-server')
                ->schema([
                    Section::make(__('Vulnerabilities'))
                        ->icon('heroicon-o-exclamation-triangle')
                        ->iconColor('danger')
                        ->collapsible()
                        ->schema([
                            EmbeddedTable::make(NiktoResultsTable::class, ['results' => $this->niktoResults, 'type' => 'vulnerabilities']),
                        ])
                        ->visible(fn () => ! empty($this->niktoResults['vulnerabilities'] ?? [])),
                    Section::make(__('Information'))
                        ->icon('heroicon-o-information-circle')
                        ->iconColor('info')
                        ->collapsible()
                        ->schema([
                            EmbeddedTable::make(NiktoResultsTable::class, ['results' => $this->niktoResults, 'type' => 'info']),
                        ])
                        ->visible(fn () => ! empty($this->niktoResults['info'] ?? [])),
                ])
                ->visible(fn () => ! empty($this->niktoResults)),

            // Scan Output
            Section::make(__('Scan Output'))
                ->icon('heroicon-o-command-line')
                ->schema(fn () => $this->buildScanOutputSchema())
                ->visible(fn () => (bool) $this->scanOutput),
        ];
    }

    protected function getHeaderActions(): array
    {
        return [
        ];
    }

    // Load methods
    protected function loadFirewallStatus(): void
    {
        try {
            $result = $this->getAgent()->send('ufw.status');
            $this->firewallInstalled = true;
            $this->firewallEnabled = $result['active'] ?? false;
            $this->defaultIncoming = $result['default_incoming'] ?? 'deny';
            $this->defaultOutgoing = $result['default_outgoing'] ?? 'allow';
            $this->firewallStatusText = $result['status_text'] ?? '';

            $rulesResult = $this->getAgent()->send('ufw.list_rules');
            $this->firewallRules = $rulesResult['rules'] ?? [];
        } catch (Exception $e) {
            $this->firewallInstalled = false;
        }
    }

    protected function loadWafStatus(): void
    {
        $this->wafInstalled = $this->detectWaf();
        $this->wafEnabled = $this->wafInstalled && Setting::get('waf_enabled', '0') === '1';
    }

    protected function detectWaf(): bool
    {
        $paths = [
            '/etc/nginx/modsec/main.conf',
            '/etc/nginx/modsecurity.conf',
            '/etc/modsecurity/modsecurity.conf',
            '/etc/modsecurity/modsecurity.conf-recommended',
        ];

        foreach ($paths as $path) {
            if (file_exists($path)) {
                return true;
            }
        }

        return false;
    }

    protected function loadFail2banStatusLight(): void
    {
        try {
            $result = $this->getAgent()->send('fail2ban.status_light');
            $this->fail2banInstalled = $result['installed'] ?? false;
            $this->fail2banRunning = $result['running'] ?? false;
            $this->fail2banVersion = $result['version'] ?? 'Unknown';
            $this->jails = [];
            $this->availableJails = [];
            $this->totalBanned = null;
            $this->fail2banLogs = [];
        } catch (Exception $e) {
            $this->fail2banInstalled = false;
            $this->fail2banRunning = false;
            $this->fail2banVersion = '';
            $this->jails = [];
            $this->availableJails = [];
            $this->totalBanned = null;
            $this->fail2banLogs = [];
        }
    }

    protected function loadFail2banStatus(): void
    {
        try {
            $result = $this->getAgent()->send('fail2ban.status');
            $this->fail2banInstalled = $result['installed'] ?? false;

            if ($this->fail2banInstalled) {
                $this->fail2banRunning = $result['running'] ?? false;
                $this->fail2banVersion = $result['version'] ?? 'Unknown';
                $this->jails = $result['jails'] ?? [];
                $this->totalBanned = $result['total_banned'] ?? 0;
                $this->maxRetry = $result['max_retry'] ?? 5;
                $this->banTime = $result['ban_time'] ?? 600;
                $this->findTime = $result['find_time'] ?? 600;

                $jailsResult = $this->getAgent()->send('fail2ban.list_jails');
                $this->availableJails = $jailsResult['jails'] ?? [];

                $logsResult = $this->getAgent()->send('fail2ban.logs');
                $this->fail2banLogs = $logsResult['logs'] ?? [];
            } else {
                $this->fail2banRunning = false;
                $this->fail2banVersion = '';
                $this->jails = [];
                $this->availableJails = [];
                $this->totalBanned = null;
                $this->fail2banLogs = [];
            }
        } catch (Exception $e) {
            $this->fail2banInstalled = false;
            $this->fail2banRunning = false;
            $this->fail2banVersion = '';
            $this->jails = [];
            $this->availableJails = [];
            $this->totalBanned = null;
            $this->fail2banLogs = [];
        }
    }

    protected function loadClamavStatusLight(): void
    {
        try {
            $result = $this->getAgent()->send('clamav.status_light');
            $this->clamavInstalled = $result['installed'] ?? false;
            $this->clamavRunning = $result['running'] ?? false;
            $this->clamavVersion = $result['version'] ?? 'Unknown';
            $this->realtimeEnabled = $result['realtime_enabled'] ?? false;
            $this->realtimeRunning = $result['realtime_running'] ?? false;
            $this->clamavLightMode = $result['light_mode'] ?? false;

            $this->signatureCount = 0;
            $this->lastUpdate = '';
            $this->recentThreats = [];
            $this->quarantinedFiles = [];
            $this->signatureDatabases = [];
        } catch (Exception $e) {
            $this->clamavInstalled = false;
            $this->clamavRunning = false;
            $this->clamavVersion = '';
            $this->realtimeEnabled = false;
            $this->realtimeRunning = false;
            $this->clamavLightMode = false;

            $this->signatureCount = 0;
            $this->lastUpdate = '';
            $this->recentThreats = [];
            $this->quarantinedFiles = [];
            $this->signatureDatabases = [];
        }
    }

    protected function loadClamavStatus(): void
    {
        try {
            $result = $this->getAgent()->send('clamav.status');
            $this->clamavInstalled = $result['installed'] ?? false;

            if ($this->clamavInstalled) {
                $this->clamavRunning = $result['running'] ?? false;
                $this->clamavVersion = $result['version'] ?? 'Unknown';
                $this->signatureCount = $result['signature_count'] ?? 0;
                $this->lastUpdate = $result['last_update'] ?? '';
                $this->recentThreats = $result['recent_threats'] ?? [];
                $this->quarantinedFiles = $result['quarantined_files'] ?? [];
                $this->realtimeEnabled = $result['realtime_enabled'] ?? false;
                $this->realtimeRunning = $result['realtime_running'] ?? false;
                $this->clamavLightMode = $result['light_mode'] ?? false;
                $this->signatureDatabases = $result['signature_databases'] ?? [];
            } else {
                $this->clamavRunning = false;
                $this->clamavVersion = '';
                $this->signatureCount = 0;
                $this->lastUpdate = '';
                $this->recentThreats = [];
                $this->quarantinedFiles = [];
                $this->realtimeEnabled = false;
                $this->realtimeRunning = false;
                $this->clamavLightMode = false;
                $this->signatureDatabases = [];
            }
        } catch (Exception $e) {
            $this->clamavInstalled = false;
            $this->clamavRunning = false;
            $this->clamavVersion = '';
            $this->signatureCount = 0;
            $this->lastUpdate = '';
            $this->recentThreats = [];
            $this->quarantinedFiles = [];
            $this->realtimeEnabled = false;
            $this->realtimeRunning = false;
            $this->clamavLightMode = false;
            $this->signatureDatabases = [];
        }
    }

    protected function loadSshSettings(): void
    {
        try {
            $result = $this->getAgent()->send('ssh.get_settings');
            if ($result['success'] ?? false) {
                $this->sshPasswordAuth = $result['password_auth'] ?? false;
                $this->sshPubkeyAuth = $result['pubkey_auth'] ?? true;
                $this->sshPort = $result['port'] ?? 22;
            }
            $this->sshShellAccessEnabled = Setting::get('ssh_shell_access_enabled', '1') === '1';
        } catch (Exception $e) {
            // Use defaults
            $this->sshShellAccessEnabled = Setting::get('ssh_shell_access_enabled', '1') === '1';
        }
    }

    public function saveSshSettings(): void
    {
        try {
            if (! $this->sshPasswordAuth && ! $this->sshPubkeyAuth) {
                Notification::make()
                    ->title(__('Invalid Configuration'))
                    ->body(__('At least one authentication method must be enabled.'))
                    ->danger()
                    ->send();

                return;
            }

            $result = $this->getAgent()->send('ssh.save_settings', [
                'password_auth' => $this->sshPasswordAuth,
                'pubkey_auth' => $this->sshPubkeyAuth,
                'port' => $this->sshPort,
            ]);

            if ($result['success'] ?? false) {
                $previousShellAccessEnabled = Setting::get('ssh_shell_access_enabled', '1') === '1';
                Setting::set('ssh_shell_access_enabled', $this->sshShellAccessEnabled ? '1' : '0');
                if ($previousShellAccessEnabled && ! $this->sshShellAccessEnabled) {
                    $this->disableShellAccessForUsers();
                }
                Notification::make()
                    ->title(__('SSH settings saved'))
                    ->body(__('Changes will take effect immediately. Make sure you have key access if disabling passwords.'))
                    ->success()
                    ->send();
            } else {
                throw new Exception($result['error'] ?? __('Failed to save settings'));
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Failed to save SSH settings'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
        $this->loadSshSettings();
    }

    private function disableShellAccessForUsers(): void
    {
        $failures = 0;
        User::query()
            ->where('is_admin', false)
            ->select(['id', 'username', 'system_username', 'email'])
            ->chunk(100, function ($users) use (&$failures): void {
                foreach ($users as $user) {
                    $username = $this->resolveSystemUsername($user);
                    if ($username === '') {
                        $failures++;

                        continue;
                    }
                    try {
                        $result = $this->getAgent()->send('ssh.disable_shell', ['username' => $username]);
                        if (! ($result['success'] ?? false)) {
                            $failures++;
                        }
                    } catch (Exception) {
                        $failures++;
                    }
                }
            });

        if ($failures > 0) {
            Notification::make()
                ->title(__('Some users could not be updated'))
                ->body(__('Shell access was disabled globally, but some users could not be switched to SFTP-only.'))
                ->warning()
                ->send();
        }
    }

    private function resolveSystemUsername(User $user): string
    {
        return (string) ($user->system_username ?? $user->username ?? '');
    }

    // Firewall actions
    public function toggleFirewall(): void
    {
        try {
            $action = $this->firewallEnabled ? 'ufw.disable' : 'ufw.enable';
            $result = $this->getAgent()->send($action);

            if ($result['success'] ?? false) {
                $this->firewallEnabled = ! $this->firewallEnabled;
                $auditAction = $this->firewallEnabled ? 'enabled' : 'disabled';
                AuditLog::logFirewallAction($auditAction);
                Notification::make()
                    ->title($this->firewallEnabled ? __('Firewall enabled') : __('Firewall disabled'))
                    ->success()
                    ->send();
            } else {
                throw new Exception($result['message'] ?? __('Unknown error'));
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
        }

        $this->loadFirewallStatus();
    }

    public function installFirewall(): void
    {
        Notification::make()->title(__('Installing firewall...'))->info()->send();
        try {
            $this->getAgent()->send('ufw.enable');
            $this->getAgent()->send('ufw.allow_service', ['service' => 'ssh']);
            $this->getAgent()->send('ufw.allow_service', ['service' => 'http']);
            $this->getAgent()->send('ufw.allow_service', ['service' => 'https']);
            AuditLog::logFirewallAction('installed', 'default rules configured');
            Notification::make()->title(__('Firewall configured with default rules'))->success()->send();
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to configure firewall'))->body(SafeError::message($e))->danger()->send();
        }
        $this->loadFirewallStatus();
    }

    public function deleteRule(int $ruleNumber): void
    {
        $this->ruleToDelete = $ruleNumber;
        $this->mountAction('deleteRuleAction');
    }

    public function deleteRuleAction(): Action
    {
        return Action::make('deleteRuleAction')
            ->requiresConfirmation()
            ->modalHeading(__('Delete Firewall Rule'))
            ->modalDescription(fn () => __('Are you sure you want to delete rule #:number?', ['number' => $this->ruleToDelete]))
            ->modalSubmitActionLabel(__('Delete'))
            ->color('danger')
            ->action(function (): void {
                try {
                    $result = $this->getAgent()->send('ufw.delete_rule', ['rule_number' => $this->ruleToDelete]);
                    if ($result['success'] ?? false) {
                        AuditLog::logFirewallAction('deleted', "rule #{$this->ruleToDelete}");
                        Notification::make()->title(__('Rule deleted'))->success()->send();
                        $this->loadFirewallStatus();
                    } else {
                        throw new Exception($result['message'] ?? __('Unknown error'));
                    }
                } catch (Exception $e) {
                    Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                }
            });
    }

    public function setDefaultPolicy(string $direction): void
    {
        $this->mountAction('setDefaultPolicyAction', ['direction' => $direction]);
    }

    public function setDefaultPolicyAction(): Action
    {
        return Action::make('setDefaultPolicyAction')
            ->modalHeading(__('Set Default Policy'))
            ->form([
                Select::make('policy')
                    ->label(__('Policy'))
                    ->options([
                        'allow' => __('Allow'),
                        'deny' => __('Deny'),
                        'reject' => __('Reject'),
                    ])
                    ->required(),
            ])
            ->action(function (array $data, array $arguments): void {
                try {
                    $result = $this->getAgent()->send('ufw.set_default', [
                        'direction' => $arguments['direction'] ?? 'incoming',
                        'policy' => $data['policy'],
                    ]);
                    if ($result['success'] ?? false) {
                        Notification::make()->title(__('Default policy updated'))->success()->send();
                        $this->loadFirewallStatus();
                    } else {
                        throw new Exception($result['message'] ?? __('Unknown error'));
                    }
                } catch (Exception $e) {
                    Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                }
            });
    }

    public function reloadFirewall(): void
    {
        try {
            $result = $this->getAgent()->send('ufw.reload');
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Firewall reloaded'))->success()->send();
                $this->loadFirewallStatus();
            } else {
                throw new Exception($result['message'] ?? __('Unknown error'));
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function resetFirewall(): void
    {
        $this->mountAction('resetFirewallAction');
    }

    public function resetFirewallAction(): Action
    {
        return Action::make('resetFirewallAction')
            ->requiresConfirmation()
            ->modalHeading(__('Reset Firewall'))
            ->modalDescription(__('This will delete ALL firewall rules and disable the firewall. Are you sure?'))
            ->modalSubmitActionLabel(__('Reset Everything'))
            ->color('danger')
            ->action(function (): void {
                try {
                    $result = $this->getAgent()->send('ufw.reset');
                    if ($result['success'] ?? false) {
                        AuditLog::logFirewallAction('reset', 'all rules deleted');
                        Notification::make()->title(__('Firewall reset'))->body(__('All rules have been deleted.'))->success()->send();
                        $this->loadFirewallStatus();
                    } else {
                        throw new Exception($result['message'] ?? __('Unknown error'));
                    }
                } catch (Exception $e) {
                    Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                }
            });
    }

    protected function allowPortAction(): Action
    {
        return Action::make('allowPort')
            ->label(__('Allow Port'))
            ->icon('heroicon-o-plus-circle')
            ->color('success')
            ->form([
                TextInput::make('port')
                    ->label(__('Port'))
                    ->placeholder(__('e.g., 80, 443, 8000:8100'))
                    ->required()
                    ->helperText(__('Single port or range (e.g., 8000:8100)')),
                Select::make('protocol')
                    ->label(__('Protocol'))
                    ->options([
                        '' => __('Both (TCP & UDP)'),
                        'tcp' => __('TCP only'),
                        'udp' => __('UDP only'),
                    ])
                    ->default(''),
                TextInput::make('comment')
                    ->label(__('Comment (optional)'))
                    ->placeholder(__('e.g., Web server')),
            ])
            ->action(function (array $data): void {
                try {
                    $result = $this->getAgent()->send('ufw.allow_port', $data);
                    if ($result['success'] ?? false) {
                        $rule = "allow port {$data['port']}".($data['protocol'] ? "/{$data['protocol']}" : '');
                        AuditLog::logFirewallAction('added', $rule, $data);
                        Notification::make()->title(__('Port allowed'))->success()->send();
                        $this->loadFirewallStatus();
                    } else {
                        throw new Exception($result['error'] ?? $result['message'] ?? __('Unknown error'));
                    }
                } catch (Exception $e) {
                    Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                }
            });
    }

    protected function denyPortAction(): Action
    {
        return Action::make('denyPort')
            ->label(__('Block Port'))
            ->icon('heroicon-o-x-circle')
            ->color('danger')
            ->form([
                TextInput::make('port')
                    ->label(__('Port'))
                    ->placeholder(__('e.g., 3306'))
                    ->required(),
                Select::make('protocol')
                    ->label(__('Protocol'))
                    ->options([
                        '' => __('Both (TCP & UDP)'),
                        'tcp' => __('TCP only'),
                        'udp' => __('UDP only'),
                    ])
                    ->default(''),
                TextInput::make('comment')
                    ->label(__('Comment (optional)')),
            ])
            ->action(function (array $data): void {
                try {
                    $result = $this->getAgent()->send('ufw.deny_port', $data);
                    if ($result['success'] ?? false) {
                        $rule = "deny port {$data['port']}".($data['protocol'] ? "/{$data['protocol']}" : '');
                        AuditLog::logFirewallAction('added', $rule, $data);
                        Notification::make()->title(__('Port blocked'))->success()->send();
                        $this->loadFirewallStatus();
                    } else {
                        throw new Exception($result['error'] ?? $result['message'] ?? __('Unknown error'));
                    }
                } catch (Exception $e) {
                    Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                }
            });
    }

    protected function allowIpAction(): Action
    {
        return Action::make('allowIp')
            ->label(__('Allow IP'))
            ->icon('heroicon-o-check-circle')
            ->color('success')
            ->form([
                TextInput::make('ip')
                    ->label(__('IP Address'))
                    ->placeholder(__('e.g., 192.168.1.100 or 10.0.0.0/8'))
                    ->required()
                    ->helperText(__('Single IP or CIDR notation')),
                TextInput::make('port')
                    ->label(__('Port (optional)'))
                    ->placeholder(__('Leave empty to allow all ports')),
                Select::make('protocol')
                    ->label(__('Protocol'))
                    ->options([
                        '' => __('Any'),
                        'tcp' => __('TCP'),
                        'udp' => __('UDP'),
                    ])
                    ->default(''),
                TextInput::make('comment')
                    ->label(__('Comment (optional)')),
            ])
            ->action(function (array $data): void {
                try {
                    $result = $this->getAgent()->send('ufw.allow_ip', $data);
                    if ($result['success'] ?? false) {
                        $rule = "allow from {$data['ip']}".($data['port'] ? " to port {$data['port']}" : '');
                        AuditLog::logFirewallAction('added', $rule, $data);
                        Notification::make()->title(__('IP allowed'))->success()->send();
                        $this->loadFirewallStatus();
                    } else {
                        throw new Exception($result['error'] ?? $result['message'] ?? __('Unknown error'));
                    }
                } catch (Exception $e) {
                    Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                }
            });
    }

    protected function denyIpAction(): Action
    {
        return Action::make('denyIp')
            ->label(__('Block IP'))
            ->icon('heroicon-o-no-symbol')
            ->color('danger')
            ->form([
                TextInput::make('ip')
                    ->label(__('IP Address'))
                    ->placeholder(__('e.g., 192.168.1.100 or 10.0.0.0/8'))
                    ->required(),
                TextInput::make('port')
                    ->label(__('Port (optional)'))
                    ->placeholder(__('Leave empty to block all ports')),
                Select::make('protocol')
                    ->label(__('Protocol'))
                    ->options([
                        '' => __('Any'),
                        'tcp' => __('TCP'),
                        'udp' => __('UDP'),
                    ])
                    ->default(''),
                TextInput::make('comment')
                    ->label(__('Comment (optional)')),
            ])
            ->action(function (array $data): void {
                try {
                    $result = $this->getAgent()->send('ufw.deny_ip', $data);
                    if ($result['success'] ?? false) {
                        $rule = "deny from {$data['ip']}".($data['port'] ? " to port {$data['port']}" : '');
                        AuditLog::logFirewallAction('added', $rule, $data);
                        Notification::make()->title(__('IP blocked'))->success()->send();
                        $this->loadFirewallStatus();
                    } else {
                        throw new Exception($result['error'] ?? $result['message'] ?? __('Unknown error'));
                    }
                } catch (Exception $e) {
                    Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                }
            });
    }

    protected function allowServiceAction(): Action
    {
        return Action::make('allowService')
            ->label(__('Allow Service'))
            ->icon('heroicon-o-server')
            ->color('info')
            ->form([
                Select::make('service')
                    ->label(__('Service'))
                    ->options([
                        'ssh' => __('SSH (22)'),
                        'http' => __('HTTP (80)'),
                        'https' => __('HTTPS (443)'),
                        'ftp' => __('FTP (21)'),
                        'smtp' => __('SMTP (25)'),
                        'pop3' => __('POP3 (110)'),
                        'imap' => __('IMAP (143)'),
                        'dns' => __('DNS (53)'),
                        'mysql' => __('MySQL (3306)'),
                        'postgresql' => __('PostgreSQL (5432)'),
                    ])
                    ->required()
                    ->searchable(),
            ])
            ->action(function (array $data): void {
                try {
                    $result = $this->getAgent()->send('ufw.allow_service', $data);
                    if ($result['success'] ?? false) {
                        AuditLog::logFirewallAction('added', "allow service {$data['service']}", $data);
                        Notification::make()->title(__('Service allowed'))->success()->send();
                        $this->loadFirewallStatus();
                    } else {
                        throw new Exception($result['error'] ?? $result['message'] ?? __('Unknown error'));
                    }
                } catch (Exception $e) {
                    Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                }
            });
    }

    protected function limitPortAction(): Action
    {
        return Action::make('limitPort')
            ->label(__('Rate Limit'))
            ->icon('heroicon-o-clock')
            ->color('warning')
            ->form([
                TextInput::make('port')
                    ->label(__('Port'))
                    ->placeholder(__('e.g., 22'))
                    ->required()
                    ->helperText(__('Limit connections (6 in 30 seconds)')),
                Select::make('protocol')
                    ->label(__('Protocol'))
                    ->options([
                        'tcp' => __('TCP'),
                        'udp' => __('UDP'),
                    ])
                    ->default('tcp'),
            ])
            ->action(function (array $data): void {
                try {
                    $result = $this->getAgent()->send('ufw.limit_port', $data);
                    if ($result['success'] ?? false) {
                        AuditLog::logFirewallAction('added', "limit port {$data['port']}/{$data['protocol']}", $data);
                        Notification::make()->title(__('Rate limit applied'))->success()->send();
                        $this->loadFirewallStatus();
                    } else {
                        throw new Exception($result['error'] ?? $result['message'] ?? __('Unknown error'));
                    }
                } catch (Exception $e) {
                    Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                }
            });
    }

    public function getActionColor(string $action): string
    {
        return match (strtoupper($action)) {
            'ALLOW' => 'success',
            'DENY' => 'danger',
            'REJECT' => 'warning',
            'LIMIT' => 'warning',
            default => 'gray',
        };
    }

    // Firewall action mount helpers
    public function openAllowPort(): void
    {
        $this->mountAction('allowPort');
    }

    public function openDenyPort(): void
    {
        $this->mountAction('denyPort');
    }

    public function openAllowIp(): void
    {
        $this->mountAction('allowIp');
    }

    public function openDenyIp(): void
    {
        $this->mountAction('denyIp');
    }

    public function openAllowService(): void
    {
        $this->mountAction('allowService');
    }

    public function openLimitPort(): void
    {
        $this->mountAction('limitPort');
    }

    // Fail2ban actions
    public function installFail2ban(): void
    {
        Notification::make()->title(__('Installing Fail2ban...'))->info()->send();
        try {
            $result = $this->getAgent()->send('fail2ban.install');
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Fail2ban installed'))->success()->send();
            } else {
                throw new Exception($result['error'] ?? __('Installation failed'));
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to install Fail2ban'))->body(SafeError::message($e))->danger()->send();
        }
        $this->loadFail2banStatus();
    }

    public function startFail2ban(): void
    {
        try {
            $result = $this->getAgent()->send('service.enable', ['service' => 'fail2ban']);
            if ($result['success'] ?? false) {
                $result = $this->getAgent()->send('service.start', ['service' => 'fail2ban']);
            }
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Fail2ban started'))->success()->send();
            } else {
                throw new Exception($result['error'] ?? __('Failed to start'));
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to start Fail2ban'))->body(SafeError::message($e))->danger()->send();
        }
        $this->loadFail2banStatus();
    }

    public function stopFail2ban(): void
    {
        try {
            $result = $this->getAgent()->send('service.stop', ['service' => 'fail2ban']);
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Fail2ban stopped'))->success()->send();
            } else {
                throw new Exception($result['error'] ?? __('Failed to stop'));
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to stop Fail2ban'))->body(SafeError::message($e))->danger()->send();
        }
        $this->loadFail2banStatus();
    }

    public function disableFail2ban(): void
    {
        try {
            $result = $this->getAgent()->send('service.stop', ['service' => 'fail2ban']);
            if ($result['success'] ?? false) {
                $result = $this->getAgent()->send('service.disable', ['service' => 'fail2ban']);
            }
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Fail2ban disabled'))->success()->send();
            } else {
                throw new Exception($result['error'] ?? __('Failed to disable'));
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to disable Fail2ban'))->body(SafeError::message($e))->danger()->send();
        }
        $this->loadFail2banStatus();
    }

    public function saveFail2banSettings(): void
    {
        try {
            $result = $this->getAgent()->send('fail2ban.save_settings', [
                'max_retry' => $this->maxRetry,
                'ban_time' => $this->banTime,
                'find_time' => $this->findTime,
            ]);
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Settings saved'))->success()->send();
            } else {
                throw new Exception($result['error'] ?? __('Failed to save'));
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to save settings'))->body(SafeError::message($e))->danger()->send();
        }
        $this->loadFail2banStatus();
    }

    public function unbanIp(string $jail, string $ip): void
    {
        try {
            $result = $this->getAgent()->send('fail2ban.unban_ip', ['jail' => $jail, 'ip' => $ip]);
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Unbanned :ip', ['ip' => $ip]))->success()->send();
            } else {
                throw new Exception($result['error'] ?? __('Failed to unban'));
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to unban IP'))->body(SafeError::message($e))->danger()->send();
        }
        $this->loadFail2banStatus();
    }

    public function enableJail(string $jail): void
    {
        try {
            $result = $this->getAgent()->send('fail2ban.enable_jail', ['jail' => $jail]);
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Jail :jail enabled', ['jail' => $jail]))->success()->send();
            } else {
                throw new Exception($result['error'] ?? __('Failed to enable jail'));
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to enable jail'))->body(SafeError::message($e))->danger()->send();
        }
        $this->loadFail2banStatus();
    }

    public function disableJail(string $jail): void
    {
        try {
            $result = $this->getAgent()->send('fail2ban.disable_jail', ['jail' => $jail]);
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Jail :jail disabled', ['jail' => $jail]))->success()->send();
            } else {
                throw new Exception($result['error'] ?? __('Failed to disable jail'));
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to disable jail'))->body(SafeError::message($e))->danger()->send();
        }
        $this->loadFail2banStatus();
    }

    // ClamAV actions
    public function installClamav(): void
    {
        Notification::make()->title(__('Installing ClamAV...'))->body(__('This may take a few minutes.'))->info()->send();
        try {
            $result = $this->getAgent()->send('clamav.install');
            if ($result['success'] ?? false) {
                Notification::make()->title(__('ClamAV installed'))->body(__('Daemon disabled by default to save memory.'))->success()->send();
            } else {
                throw new Exception($result['error'] ?? __('Installation failed'));
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to install ClamAV'))->body(SafeError::message($e))->danger()->send();
        }
        $this->loadClamavStatus();
    }

    public function updateSignatures(): void
    {
        try {
            $result = $this->getAgent()->send('clamav.update_signatures');
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Signatures updated'))->success()->send();
            } else {
                Notification::make()->title(__('Update may have issues'))->body($result['output'] ?? '')->warning()->send();
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to update signatures'))->body(SafeError::message($e))->danger()->send();
        }
        $this->loadClamavStatus();
    }

    public function startClamav(): void
    {
        try {
            $result = $this->getAgent()->send('clamav.start');
            if ($result['success'] ?? false) {
                Notification::make()->title(__('ClamAV started'))->success()->send();
            } else {
                throw new Exception($result['error'] ?? __('Failed to start'));
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to start ClamAV'))->body(SafeError::message($e))->danger()->send();
        }
        $this->loadClamavStatus();
    }

    public function enableClamav(): void
    {
        try {
            $result = $this->getAgent()->send('service.enable', ['service' => 'clamav-daemon']);
            if ($result['success'] ?? false) {
                $result = $this->getAgent()->send('service.start', ['service' => 'clamav-daemon']);
            }
            if ($result['success'] ?? false) {
                Notification::make()->title(__('ClamAV enabled'))->success()->send();
            } else {
                throw new Exception($result['error'] ?? __('Failed to enable'));
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to enable ClamAV'))->body(SafeError::message($e))->danger()->send();
        }
        $this->loadClamavStatus();
    }

    public function stopClamav(): void
    {
        try {
            $result = $this->getAgent()->send('clamav.stop');
            if ($result['success'] ?? false) {
                Notification::make()->title(__('ClamAV stopped'))->success()->send();
            } else {
                throw new Exception($result['error'] ?? __('Failed to stop'));
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to stop ClamAV'))->body(SafeError::message($e))->danger()->send();
        }
        $this->loadClamavStatus();
    }

    public function disableClamav(): void
    {
        try {
            $result = $this->getAgent()->send('service.stop', ['service' => 'clamav-daemon']);
            if ($result['success'] ?? false) {
                $result = $this->getAgent()->send('service.disable', ['service' => 'clamav-daemon']);
            }
            if ($result['success'] ?? false) {
                Notification::make()->title(__('ClamAV disabled'))->success()->send();
            } else {
                throw new Exception($result['error'] ?? __('Failed to disable'));
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to disable ClamAV'))->body(SafeError::message($e))->danger()->send();
        }
        $this->loadClamavStatus();
    }

    public function toggleRealtime(): void
    {
        try {
            if ($this->realtimeRunning) {
                $result = $this->getAgent()->send('clamav.realtime_disable');
                $message = __('Real-time protection disabled');
            } else {
                $result = $this->getAgent()->send('clamav.realtime_enable');
                $message = __('Real-time protection enabled');
            }
            if ($result['success'] ?? false) {
                Notification::make()->title($message)->success()->send();
            } else {
                throw new Exception($result['error'] ?? __('Failed'));
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to toggle real-time protection'))->body(SafeError::message($e))->danger()->send();
        }
        $this->loadClamavStatus();
    }

    public function toggleLightMode(): void
    {
        try {
            $action = $this->clamavLightMode ? 'clamav.set_full_mode' : 'clamav.set_light_mode';
            $result = $this->getAgent()->send($action);

            if ($result['success'] ?? false) {
                $this->clamavLightMode = ! $this->clamavLightMode;
                $message = $this->clamavLightMode
                    ? __('Switched to lightweight mode - web hosting signatures only')
                    : __('Switched to full mode - all ClamAV signatures');
                Notification::make()
                    ->title($message)
                    ->body(__('Signature count: :count', ['count' => number_format($result['signature_count'] ?? 0)]))
                    ->success()
                    ->send();
            } else {
                throw new Exception($result['error'] ?? __('Failed to switch mode'));
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Failed to switch ClamAV mode'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
        $this->loadClamavStatus();
    }

    public function deleteQuarantined(string $filename): void
    {
        try {
            $result = $this->getAgent()->send('clamav.delete_quarantined', ['filename' => $filename]);
            if ($result['success'] ?? false) {
                Notification::make()->title(__('File deleted'))->success()->send();
            } else {
                throw new Exception($result['error'] ?? __('Failed to delete'));
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to delete file'))->body(SafeError::message($e))->danger()->send();
        }
        $this->loadClamavStatus();
    }

    // Scanner methods
    protected function checkScannerToolStatus(): void
    {
        try {
            $status = $this->getAgent()->send('scanner.status');

            $lynis = $status['lynis'] ?? [];
            $this->lynisInstalled = (bool) ($lynis['installed'] ?? false);
            $this->lynisVersion = (string) ($lynis['version'] ?? '');

            $wpscan = $status['wpscan'] ?? [];
            $this->wpscanInstalled = (bool) ($wpscan['installed'] ?? false);
            $this->wpscanVersion = (string) ($wpscan['version'] ?? '');

            $nikto = $status['nikto'] ?? [];
            $this->niktoInstalled = (bool) ($nikto['installed'] ?? false);
            $this->niktoVersion = (string) ($nikto['version'] ?? '');
        } catch (Exception) {
            // Keep previous values if the agent is unavailable.
        }
    }

    protected function loadLastScans(): void
    {
        $scanDir = storage_path('app/security-scans');

        if (file_exists("$scanDir/lynis-latest.json")) {
            $this->lastLynisScan = date('Y-m-d H:i:s', filemtime("$scanDir/lynis-latest.json"));
            $this->lynisResults = json_decode(file_get_contents("$scanDir/lynis-latest.json"), true) ?? [];
        }

        if (file_exists("$scanDir/wpscan-latest.json")) {
            $this->lastWpscanScan = date('Y-m-d H:i:s', filemtime("$scanDir/wpscan-latest.json"));
            $this->wpscanResults = json_decode(file_get_contents("$scanDir/wpscan-latest.json"), true) ?? [];
        }

        if (file_exists("$scanDir/nikto-latest.json")) {
            $this->lastNiktoScan = date('Y-m-d H:i:s', filemtime("$scanDir/nikto-latest.json"));
            $this->niktoResults = json_decode(file_get_contents("$scanDir/nikto-latest.json"), true) ?? [];
        }
    }

    public function installLynis(): void
    {
        InstallScannerTool::dispatch('lynis');
        Notification::make()
            ->title(__('Lynis installation queued'))
            ->body(__('It will run in the background. Refresh in a minute to see the updated status.'))
            ->info()
            ->send();
        $this->checkScannerToolStatus();
    }

    public function installWpscan(): void
    {
        InstallScannerTool::dispatch('wpscan');
        Notification::make()
            ->title(__('WPScan installation queued'))
            ->body(__('It will run in the background. Refresh in a minute to see the updated status.'))
            ->info()
            ->send();
        $this->checkScannerToolStatus();
    }

    public function installNikto(): void
    {
        InstallScannerTool::dispatch('nikto');
        Notification::make()
            ->title(__('Nikto installation queued'))
            ->body(__('It will run in the background. Refresh in a minute to see the updated status.'))
            ->info()
            ->send();
        $this->checkScannerToolStatus();
    }

    public function runLynisScan(): void
    {
        if (! $this->lynisInstalled) {
            Notification::make()->title(__('Lynis not installed'))->danger()->send();

            return;
        }

        Notification::make()
            ->title(__('Lynis scan queued'))
            ->body(__('It will run in the background. Refresh in a few minutes to see results.'))
            ->info()
            ->send();

        RunLynisScan::dispatch();
    }

    protected function parseLynisOutput(array $output): array
    {
        $results = [
            'hardening_index' => 0,
            'warnings' => [],
            'suggestions' => [],
            'tests_performed' => 0,
        ];

        $fullOutput = implode("\n", $output);

        if (preg_match('/Hardening index\s*:\s*(\d+)/i', $fullOutput, $matches)) {
            $results['hardening_index'] = (int) $matches[1];
        }

        if (preg_match('/Tests performed\s*:\s*(\d+)/i', $fullOutput, $matches)) {
            $results['tests_performed'] = (int) $matches[1];
        }

        preg_match_all('/\[WARNING\]\s*(.+)$/m', $fullOutput, $warningMatches);
        $results['warnings'] = $warningMatches[1] ?? [];

        preg_match_all('/\[SUGGESTION\]\s*(.+)$/m', $fullOutput, $suggestionMatches);
        $results['suggestions'] = $suggestionMatches[1] ?? [];

        return $results;
    }

    public function getLocalWordPressSites(): array
    {
        $sites = [];

        try {
            $users = \App\Models\User::where('is_admin', false)->get();

            foreach ($users as $user) {
                $result = $this->getAgent()->wpList($user->username);
                $userSites = $result['sites'] ?? [];

                foreach ($userSites as $site) {
                    $siteId = $site['domain'].($site['path'] ?? '');
                    $sites[$siteId] = $site['domain'].($site['path'] !== '/' ? ($site['path'] ?? '') : '');
                }
            }
        } catch (Exception $e) {
            // Return empty array on error
        }

        return $sites;
    }

    public function runWpscanOnSite(): void
    {
        if (! $this->wpscanInstalled) {
            Notification::make()->title(__('WPScan not installed'))->danger()->send();

            return;
        }

        if (! $this->selectedWpSiteId) {
            Notification::make()->title(__('Please select a WordPress site'))->danger()->send();

            return;
        }

        $url = 'https://'.$this->selectedWpSiteId;

        Notification::make()
            ->title(__('WPScan queued'))
            ->body(__('It will run in the background. Refresh in a few minutes to see results.'))
            ->info()
            ->send();

        RunWpscanScan::dispatch($url);
    }

    public function runNiktoScan(): void
    {
        if (! $this->niktoInstalled) {
            Notification::make()->title(__('Nikto not installed'))->danger()->send();

            return;
        }

        Notification::make()
            ->title(__('Nikto scan queued'))
            ->body(__('It will run in the background. Refresh in a few minutes to see results.'))
            ->info()
            ->send();

        RunNiktoScan::dispatch('localhost');
    }

    protected function parseNiktoTextOutput(array $output): array
    {
        $results = [
            'vulnerabilities' => [],
            'info' => [],
        ];

        foreach ($output as $line) {
            if (preg_match('/^\+\s*OSVDB-\d+:\s*(.+)/', $line, $matches)) {
                $results['vulnerabilities'][] = trim($matches[1]);
            } elseif (preg_match('/^\+\s*(.+)/', $line, $matches)) {
                $results['info'][] = trim($matches[1]);
            }
        }

        return $results;
    }

    // ClamAV on-demand scan methods
    public function getClamScanUsers(): array
    {
        $users = [];
        try {
            $systemUsers = \App\Models\User::where('is_admin', false)->get();
            foreach ($systemUsers as $user) {
                $users[$user->username] = $user->username.' ('.$user->email.')';
            }
        } catch (Exception $e) {
            // Return empty array on error
        }

        return $users;
    }

    public function runClamScanUser(): void
    {
        if (! $this->clamavInstalled) {
            Notification::make()->title(__('ClamAV not installed'))->danger()->send();

            return;
        }

        if (! $this->selectedClamUser) {
            Notification::make()->title(__('Please select a user'))->danger()->send();

            return;
        }

        $userDir = "/home/{$this->selectedClamUser}";

        Notification::make()
            ->title(__('ClamAV scan queued'))
            ->body(__('It will run in the background. Refresh in a few minutes to see results.'))
            ->info()
            ->send();

        RunClamavScan::dispatch($userDir, 'user', $this->selectedClamUser);
    }

    public function runClamScanServer(): void
    {
        if (! $this->clamavInstalled) {
            Notification::make()->title(__('ClamAV not installed'))->danger()->send();

            return;
        }

        Notification::make()
            ->title(__('Server-wide scan queued'))
            ->body(__('It will run in the background. Refresh in a few minutes to see results.'))
            ->info()
            ->send();

        RunClamavScan::dispatch('/home', 'server', null);
    }

    protected function parseClamScanOutput(array $output): array
    {
        $results = [
            'scanned_files' => 0,
            'infected_files' => 0,
            'threats' => [],
        ];

        $fullOutput = implode("\n", $output);

        if (preg_match('/Scanned files:\s*(\d+)/i', $fullOutput, $matches)) {
            $results['scanned_files'] = (int) $matches[1];
        }
        if (preg_match('/Infected files:\s*(\d+)/i', $fullOutput, $matches)) {
            $results['infected_files'] = (int) $matches[1];
        }

        foreach ($output as $line) {
            if (preg_match('/^(.+?):\s*(.+?)\s*FOUND$/i', $line, $matches)) {
                $results['threats'][] = [
                    'file' => trim($matches[1]),
                    'threat' => trim($matches[2]),
                ];
            }
        }

        return $results;
    }

    protected function loadClamScanResults(): void
    {
        $scanDir = storage_path('app/security-scans');
        if (file_exists("$scanDir/clamscan-latest.json")) {
            $this->lastClamScan = date('Y-m-d H:i:s', filemtime("$scanDir/clamscan-latest.json"));
            $this->clamScanResults = json_decode(file_get_contents("$scanDir/clamscan-latest.json"), true) ?? [];
        }
    }

    // Dynamic component builders for pure Filament UI
    protected function buildClamavScanResultsSchema(): array
    {
        if (empty($this->clamScanResults)) {
            return [Text::make(__('No scan results available'))];
        }

        $scannedFiles = $this->clamScanResults['scanned_files'] ?? 0;
        $infectedFiles = $this->clamScanResults['infected_files'] ?? 0;
        $scanType = $this->clamScanResults['scan_type'] ?? 'unknown';
        $target = $scanType === 'user' ? ($this->clamScanResults['username'] ?? '-') : __('Server');
        $threats = $this->clamScanResults['threats'] ?? [];

        $components = [
            Grid::make(['default' => 1, 'md' => 3])
                ->schema([
                    Section::make((string) $scannedFiles)
                        ->description(__('Files Scanned'))
                        ->icon('heroicon-o-document-magnifying-glass')
                        ->iconColor('primary'),
                    Section::make((string) $infectedFiles)
                        ->description(__('Infected Files'))
                        ->icon('heroicon-o-exclamation-triangle')
                        ->iconColor($infectedFiles > 0 ? 'danger' : 'success'),
                    Section::make($target)
                        ->description(__('Scan Target'))
                        ->icon('heroicon-o-folder')
                        ->iconColor('gray'),
                ]),
        ];

        if (! empty($threats)) {
            $threatComponents = [];
            foreach ($threats as $threat) {
                $threatComponents[] = Text::make($threat['threat'].': '.basename($threat['file']));
            }
            $components[] = Section::make(__('Threats Detected').' ('.count($threats).')')
                ->icon('heroicon-o-exclamation-triangle')
                ->iconColor('danger')
                ->schema($threatComponents);
        } else {
            $components[] = Section::make(__('No threats detected'))
                ->icon('heroicon-o-check-circle')
                ->iconColor('success')
                ->description(__('The scanned directory appears to be clean.'));
        }

        return $components;
    }

    protected function buildSshCurrentConfigSchema(): array
    {
        return [
            Section::make($this->sshPasswordAuth ? __('Enabled') : __('Disabled'))
                ->description(__('Password Authentication'))
                ->icon('heroicon-o-key')
                ->iconColor($this->sshPasswordAuth ? 'warning' : 'success')
                ->aside(),
            Section::make($this->sshPubkeyAuth ? __('Enabled') : __('Disabled'))
                ->description(__('Public Key Authentication'))
                ->icon('heroicon-o-finger-print')
                ->iconColor($this->sshPubkeyAuth ? 'success' : 'danger')
                ->aside(),
            Section::make((string) $this->sshPort)
                ->description(__('SSH Port'))
                ->icon('heroicon-o-server')
                ->iconColor('gray')
                ->aside(),
        ];
    }

    public function getSecurityRecommendations(): array
    {
        $keysOnly = ! $this->sshPasswordAuth && $this->sshPubkeyAuth;
        $nonDefaultPort = $this->sshPort !== 22;
        $firewallActive = $this->firewallEnabled;
        $wafActive = $this->wafInstalled && $this->wafEnabled;
        $antivirusActive = $this->clamavRunning;

        return [
            [
                'title' => __('Disable password authentication'),
                'description' => __('Use SSH keys only for better security'),
                'ok' => $keysOnly,
            ],
            [
                'title' => __('Use a non-default SSH port'),
                'description' => __('Reduce automated attacks by avoiding port 22'),
                'ok' => $nonDefaultPort,
            ],
            [
                'title' => __('Enable Fail2ban protection'),
                'description' => __('Automatically block brute-force attempts'),
                'ok' => $this->fail2banRunning,
            ],
            [
                'title' => __('Enable the firewall'),
                'description' => __('Restrict inbound access to required services'),
                'ok' => $firewallActive,
            ],
            [
                'title' => __('Enable ModSecurity (WAF)'),
                'description' => __('Add web attack protection for hosted sites'),
                'ok' => $wafActive,
            ],
            [
                'title' => __('Keep antivirus protection active'),
                'description' => __('Scan and block malware threats'),
                'ok' => $antivirusActive,
            ],
        ];
    }

    protected function buildClamScanOutputSchema(): array
    {
        $output = $this->clamScanResults['raw_output'] ?? '';

        if ($this->isScanning && $this->currentScan === 'clamav') {
            $output = $this->scanOutput;
        }

        if (! $output) {
            return [];
        }

        return [
            Text::make($output),
        ];
    }

    protected function buildScanOutputSchema(): array
    {
        if (! $this->scanOutput) {
            return [];
        }

        return [
            Text::make($this->scanOutput),
        ];
    }
}
