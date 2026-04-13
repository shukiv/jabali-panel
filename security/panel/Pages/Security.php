<?php

declare(strict_types=1);

namespace App\JabaliSecurity\Pages;

use App\JabaliSecurity\JabaliSecurityClient;
use App\JabaliSecurity\Widgets\BlocklistTable;
use App\JabaliSecurity\Widgets\BruteforceBlockedTable;
use App\JabaliSecurity\Widgets\CleanupRecordsTable;
use App\JabaliSecurity\Widgets\FirewallRulesTable;
use App\JabaliSecurity\Widgets\IncidentsTable;
use App\JabaliSecurity\Widgets\QuarantineTable;
use App\JabaliSecurity\Widgets\ScanUsersTable;
use App\JabaliSecurity\Widgets\ThreatFeedsTable;
use App\JabaliSecurity\Widgets\UsersTable;
use App\JabaliSecurity\Widgets\WafEventsTable;
use App\JabaliSecurity\Widgets\CrowdsecDecisionsTable;
use App\JabaliSecurity\Widgets\UnifiedBlocklistTable;
use App\JabaliSecurity\Widgets\GeoBlockTable;
use App\JabaliSecurity\Widgets\WebshieldRulesTable;
use App\JabaliSecurity\Widgets\WhitelistTable;
use App\JabaliSecurity\Widgets\YaraRulesTable;
use BackedEnum;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\EmbeddedTable;
use Filament\Schemas\Components\Grid;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\Text;
use Filament\Schemas\Components\Tabs;
use Filament\Schemas\Components\Tabs\Tab;
use Filament\Schemas\Schema;
use Filament\Support\Enums\FontWeight;
use Filament\Support\Enums\TextSize;
use Illuminate\Contracts\Support\Htmlable;
use Livewire\Attributes\Url;

class Security extends Page implements HasActions, HasForms
{
    use InteractsWithActions;
    use InteractsWithForms;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-shield-check';

    protected static ?int $navigationSort = 5;

    protected static ?string $slug = 'security';

    public static function tabUrl(string $tab, ?string $subtab = null): string
    {
        $params = ['tab' => $tab];
        if ($subtab) {
            $params['defense'] = $subtab;
        }
        return static::getUrl($params);
    }

    public static function canAccess(): bool
    {
        return auth()->user()?->isAdmin() ?? false;
    }

    protected string $view = 'jabali-security::security';

    #[Url(as: 'tab')]
    public string $activeTab = 'overview';

    #[Url(as: 'threats')]
    public string $threatsTab = 'scan';

    #[Url(as: 'defense')]
    public string $defenseTab = 'firewall';

    #[Url(as: 'intelligence')]
    public string $intelligenceTab = 'rules';

    public array $configData = [];

    public array $moduleStates = [];

    public bool $expertMode = false;

    public string $geoipLicenseKey = '';
    public string $geoipAction = 'block';

    public function mount(): void
    {
        $config = $this->client()->get('/config') ?? [];
        $this->loadConfigData($config);
        $this->loadModuleStates($config);
        $this->geoipAction = $config['GEOIP_ACTION'] ?? 'block';
    }

    protected function loadModuleStates(array $config): void
    {
        foreach (static::getModuleToggles() as $group) {
            foreach ($group as $key => $mod) {
                $this->moduleStates[$key] = in_array($config[$key] ?? 'no', ['yes', 'true', '1']);
            }
        }
    }

    /** Keys allowed through moduleStates Livewire binding. */
    private static function allowedModuleKeys(): array
    {
        $keys = [];
        foreach (static::getModuleToggles() as $group) {
            foreach ($group as $key => $mod) {
                $keys[] = $key;
            }
        }
        return $keys;
    }

    public function updatedModuleStates($value, string $key): void
    {
        if (! in_array($key, static::allowedModuleKeys(), true)) {
            return;
        }

        $newValue = $value ? 'yes' : 'no';
        $this->client()->patch('/config', [$key => $newValue]);

        $label = str_replace('_', ' ', str_replace('_ENABLED', '', $key));
        Notification::make()
            ->title(__(':feature :action', [
                'feature' => $label,
                'action' => $value ? __('enabled') : __('disabled'),
            ]))
            ->color($value ? 'success' : 'warning')
            ->send();
    }

    protected function loadConfigData(array $config): void
    {
        $data = [];
        foreach (static::$configCategories as $keys) {
            foreach ($keys as $key) {
                $val = $config[$key] ?? '';
                if (in_array($key, static::$booleanKeys)) {
                    $data['config_'.$key] = in_array($val, ['yes', 'true', '1']);
                } else {
                    $data['config_'.$key] = $val;
                }
            }
        }
        $this->configData = $data;
    }

    public function getTitle(): string|Htmlable
    {
        return __('Security');
    }

    public static function getNavigationLabel(): string
    {
        return __('Security');
    }

    protected function client(): JabaliSecurityClient
    {
        return JabaliSecurityClient::getInstance();
    }

    // ── Header Actions ───────────────────────────────────────────────

    protected function getHeaderActions(): array
    {
        return [];
    }

    // ── Forms ────────────────────────────────────────────────────────

    protected function getForms(): array
    {
        return ['securitySchema'];
    }

    public function securitySchema(Schema $schema): Schema
    {
        return $schema->components([
            Tabs::make(__('Security'))
                ->contained()
                ->livewireProperty('activeTab')
                ->tabs([
                    'overview' => Tab::make(__('Overview'))
                        ->icon('heroicon-o-home')
                        ->schema(array_merge(
                            $this->overviewStats(),
                            $this->overviewTab(),
                        )),
                    'defense' => Tab::make(__('Defense'))
                        ->icon('heroicon-o-shield-check')
                        ->schema([
                            Tabs::make(__('Defense'))
                                ->contained(false)
                                ->livewireProperty('defenseTab')
                                ->tabs([
                                    'firewall' => Tab::make(__('Firewall'))
                                        ->schema(array_merge(
                                            [Text::make(__('Manage UFW (Uncomplicated Firewall) rules to control inbound and outbound traffic. Add port rules, IP-based allow/deny rules, and manage application profiles.'))->size(TextSize::Small)->color('gray')],
                                            $this->firewallTab(),
                                        )),
                                    'waf' => Tab::make(__('WAF'))
                                        ->schema(array_merge(
                                            [Text::make(__('Web Application Firewall events from ModSecurity with OWASP Core Rule Set. Shows blocked attacks (SQLi, XSS, path traversal) and lets you manage CRS rules.'))->size(TextSize::Small)->color('gray')],
                                            $this->wafStats(),
                                            [EmbeddedTable::make(WafEventsTable::class)],
                                        )),
                                    'bruteforce' => Tab::make(__('IP Protection'))
                                        ->icon('heroicon-o-shield-check')
                                        ->schema(array_merge(
                                            [Text::make(__('Unified IP protection: brute-force detection, CrowdSec community intelligence, and threat feed blocking. All blocked IPs from every source in one view.'))->size(TextSize::Small)->color('gray')],
                                            $this->ipProtectionTabs(),
                                        )),
                                    'proactive' => Tab::make(__('Proactive'))
                                        ->schema(array_merge(
                                            [Text::make(__('Suspicious process killer: detects and terminates reverse shells, crypto miners, and other malicious processes.'))->size(TextSize::Small)->color('gray')],
                                            $this->proactiveStats(),
                                        )),
                                    'webshield' => Tab::make(__('WebShield'))
                                        ->schema(array_merge(
                                            [Text::make(__('Nginx-level protection: rate limiting, bot user-agent filtering, and JavaScript challenge pages for suspicious clients. Requires nginx include after install.'))->size(TextSize::Small)->color('gray')],
                                            $this->webshieldStats(),
                                            [EmbeddedTable::make(WebshieldRulesTable::class)],
                                        )),
                                    'geoip' => Tab::make(__('GeoIP'))
                                        ->icon('heroicon-o-globe-alt')
                                        ->schema(array_merge(
                                            [
                                                Text::make(__('Block or allow traffic by country using MaxMind GeoLite2 database.'))->size(TextSize::Small)->color('gray'),
                                            ],
                                            $this->geoipStats(),
                                            [
                                                Section::make(__('MaxMind Configuration'))
                                                    ->compact()
                                                    ->collapsible()
                                                    ->description(__('Sign up free at maxmind.com/en/geolite2/signup, then generate a license key under Account → Manage License Keys.'))
                                                    ->headerActions([
                                                        Action::make('saveGeoipSettings')
                                                            ->label(__('Save'))
                                                            ->icon('heroicon-o-check')
                                                            ->color('success')
                                                            ->size('xs')
                                                            ->action('saveGeoipSettings'),
                                                    ])
                                                    ->schema([
                                                        Grid::make(3)->schema([
                                                            TextInput::make('geoipLicenseKey')
                                                                ->label(__('MaxMind License Key'))
                                                                ->password()
                                                                ->revealable()
                                                                ->placeholder(__('Enter license key')),
                                                            Select::make('geoipAction')
                                                                ->label(__('Default Action'))
                                                                ->options([
                                                                    'block' => __('Block (403)'),
                                                                    'challenge' => __('Challenge (JS)'),
                                                                    'log' => __('Log only'),
                                                                ]),
                                                        ]),
                                                    ]),
                                            ],
                                            [EmbeddedTable::make(GeoBlockTable::class)],
                                        )),
                                ]),
                        ]),
                    'malware' => Tab::make(__('Malware Scanner'))
                        ->icon('heroicon-o-bug-ant')
                        ->schema([
                            Text::make(__('Real-time malware detection using heuristic, entropy, YARA-X, and ClamAV engines. Files are scanned on creation/modification. Detected threats are quarantined or cleaned automatically.'))
                                ->size(TextSize::Small)
                                ->color('gray'),
                            $this->malwareScannerTabs(),
                        ]),
                    'intelligence' => Tab::make(__('Intelligence'))
                        ->icon('heroicon-o-light-bulb')
                        ->schema([
                            Tabs::make(__('Intelligence'))
                                ->contained(false)
                                ->livewireProperty('intelligenceTab')
                                ->tabs([
                                    'rules' => Tab::make(__('Rules'))
                                        ->schema(array_merge(
                                            [Text::make(__('Detection engine status and YARA-X rule files used for signature-based malware scanning. Shows active scanners (heuristic, entropy, YARA, ClamAV) and rule file details.'))->size(TextSize::Small)->color('gray')],
                                            $this->rulesStats(),
                                            [EmbeddedTable::make(YaraRulesTable::class)],
                                        )),
                                    'threatintel' => Tab::make(__('Threat Intel'))
                                        ->schema([
                                            Text::make(__('Threat intelligence feeds providing IP reputation and malware hash databases. Feeds update automatically and can be used to auto-block known malicious IPs.'))
                                                ->size(TextSize::Small)
                                                ->color('gray'),
                                            EmbeddedTable::make(ThreatFeedsTable::class),
                                        ]),
                                    'users' => Tab::make(__('Users'))
                                        ->schema([
                                            Text::make(__('Per-user risk profiles showing incident counts and threat scores. Helps identify compromised hosting accounts that need attention.'))
                                                ->size(TextSize::Small)
                                                ->color('gray'),
                                            EmbeddedTable::make(UsersTable::class),
                                        ]),
                                ]),
                        ]),
                    'settings' => Tab::make(__('Settings'))
                        ->icon('heroicon-o-cog-6-tooth')
                        ->schema(array_merge(
                            [Text::make(__('Daemon configuration for all security modules. Changes are saved to /etc/jabali-security/jabali-security.conf. Click Save & Restart to apply.'))->size(TextSize::Small)->color('gray')],
                            $this->configTab(),
                        )),
                ]),
        ]);
    }

    // ── Stat Cards (schema-based, compact) ─────────────────────────


    public function getOverviewStatsDataProperty(): array
    {
        $s = $this->client()->get('/status');
        if (! $s) {
            return [['value' => __('Offline'), 'label' => __('Daemon'), 'icon' => 'heroicon-o-server', 'color' => 'danger']];
        }

        $cs = $this->client()->get('/crowdsec/status');
        $csConnected = $cs['connected'] ?? false;
        $csDecisions = $cs['active_decisions'] ?? 0;

        return [
            ['value' => (string) ($s['incidents_24h'] ?? 0), 'label' => __('Incidents'), 'icon' => 'heroicon-o-exclamation-triangle', 'color' => ($s['incidents_24h'] ?? 0) > 0 ? 'danger' : 'success'],
            ['value' => (string) ($s['attacks_blocked_24h'] ?? 0), 'label' => __('Blocked'), 'icon' => 'heroicon-o-shield-check', 'color' => ($s['attacks_blocked_24h'] ?? 0) > 0 ? 'warning' : 'success'],
            ['value' => (string) ($s['quarantined_count'] ?? 0), 'label' => __('Quarantine'), 'icon' => 'heroicon-o-lock-closed', 'color' => ($s['quarantined_count'] ?? 0) > 0 ? 'warning' : 'success'],
            ['value' => (string) ($s['watched_dirs'] ?? 0), 'label' => __('Watching'), 'icon' => 'heroicon-o-eye', 'color' => 'info'],
            ['value' => $csConnected ? (string) $csDecisions : __('Off'), 'label' => __('CrowdSec'), 'icon' => 'heroicon-o-globe-alt', 'color' => $csConnected ? 'success' : 'gray'],
            ['value' => ($s['running'] ?? false) ? __('Online') : __('Offline'), 'label' => __('Daemon'), 'icon' => 'heroicon-o-server', 'color' => ($s['running'] ?? false) ? 'success' : 'danger'],
        ];
    }

    protected function overviewStats(): array
    {
        $data = $this->getOverviewStatsDataProperty();
        return [Grid::make(3)->dense()->schema(array_map(fn ($s) => $this->dashboardCard($s), $data))];
    }



    private function getWafStatsData(): array
    {
        $s = $this->client()->get('/waf/stats') ?? [];
        return [
            ['value' => (string) ($s['total_events_24h'] ?? 0), 'label' => __('Events (24h)'), 'icon' => 'heroicon-o-bolt', 'color' => ($s['total_events_24h'] ?? 0) > 0 ? 'warning' : 'success'],
            ['value' => (string) ($s['blocked_24h'] ?? 0), 'label' => __('Blocked (24h)'), 'icon' => 'heroicon-o-no-symbol', 'color' => ($s['blocked_24h'] ?? 0) > 0 ? 'danger' : 'success'],
        ];
    }

    private function getCrowdsecStatsData(): array
    {
        $s = $this->client()->get('/crowdsec/status') ?? [];
        $connected = $s['connected'] ?? false;
        return [
            ['value' => $connected ? __('Connected') : __('Disconnected'), 'label' => __('LAPI'), 'icon' => 'heroicon-o-globe-alt', 'color' => $connected ? 'success' : 'gray'],
            ['value' => (string) ($s['active_decisions'] ?? 0), 'label' => __('Decisions'), 'icon' => 'heroicon-o-shield-check', 'color' => ($s['active_decisions'] ?? 0) > 0 ? 'warning' : 'success'],
            ['value' => (string) ($s['blocked_ips'] ?? 0), 'label' => __('Blocked IPs'), 'icon' => 'heroicon-o-no-symbol', 'color' => ($s['blocked_ips'] ?? 0) > 0 ? 'danger' : 'success'],
        ];
    }

    private function getBruteforceStatsData(): array
    {
        $s = $this->client()->get('/bruteforce/stats') ?? [];
        return [
            ['value' => (string) ($s['tracked_ips'] ?? 0), 'label' => __('Tracked IPs'), 'icon' => 'heroicon-o-signal', 'color' => 'info'],
            ['value' => (string) ($s['blocked_count'] ?? 0), 'label' => __('Blocked'), 'icon' => 'heroicon-o-no-symbol', 'color' => ($s['blocked_count'] ?? 0) > 0 ? 'danger' : 'success'],
        ];
    }

    private function getProactiveStatsData(): array
    {
        $s = $this->client()->get('/proactive/status') ?? [];
        return [
            ['value' => ($s['process_kill_enabled'] ?? false) ? __('Active') : __('Disabled'), 'label' => __('Process Killer'), 'icon' => 'heroicon-o-fire', 'color' => ($s['process_kill_enabled'] ?? false) ? 'success' : 'gray'],
            ['value' => (string) ($s['process_kill_count'] ?? 0), 'label' => __('Processes Killed'), 'icon' => 'heroicon-o-x-circle', 'color' => ($s['process_kill_count'] ?? 0) > 0 ? 'warning' : 'success'],
        ];
    }

    private function getWebshieldStatsData(): array
    {
        $s = $this->client()->get('/webshield/status') ?? [];
        return [
            ['value' => ($s['installed'] ?? false) ? __('Yes') : __('No'), 'label' => __('Installed'), 'icon' => 'heroicon-o-check-circle', 'color' => ($s['installed'] ?? false) ? 'success' : 'danger'],
            ['value' => ($s['rate_limiting'] ?? false) ? __('On') : __('Off'), 'label' => __('Rate Limiting'), 'icon' => 'heroicon-o-clock', 'color' => ($s['rate_limiting'] ?? false) ? 'success' : 'danger'],
            ['value' => ($s['bot_filtering'] ?? false) ? __('On') : __('Off'), 'label' => __('Bot Filtering'), 'icon' => 'heroicon-o-funnel', 'color' => ($s['bot_filtering'] ?? false) ? 'success' : 'danger'],
            ['value' => (string) ($s['blocked_ips_count'] ?? 0), 'label' => __('Blocked IPs'), 'icon' => 'heroicon-o-no-symbol', 'color' => ($s['blocked_ips_count'] ?? 0) > 0 ? 'danger' : 'success'],
            ['value' => (string) ($s['bot_blocked_24h'] ?? 0), 'label' => __('Bots Blocked'), 'icon' => 'heroicon-o-bug-ant', 'color' => ($s['bot_blocked_24h'] ?? 0) > 0 ? 'warning' : 'success'],
            ['value' => (string) ($s['rate_limited_24h'] ?? 0), 'label' => __('Rate Limited'), 'icon' => 'heroicon-o-clock', 'color' => ($s['rate_limited_24h'] ?? 0) > 0 ? 'warning' : 'success'],
        ];
    }

    private function getRulesStatsData(): array
    {
        $r = $this->client()->get('/rules') ?? [];
        return [
            ['value' => ($r['yara_enabled'] ?? false) ? __('Enabled') : __('Disabled'), 'label' => __('YARA'), 'icon' => 'heroicon-o-document-magnifying-glass', 'color' => ($r['yara_enabled'] ?? false) ? 'success' : 'gray'],
            ['value' => ($r['clamav_enabled'] ?? false) ? __('Enabled') : __('Disabled'), 'label' => __('ClamAV'), 'icon' => 'heroicon-o-shield-check', 'color' => ($r['clamav_enabled'] ?? false) ? 'success' : 'gray'],
            ['value' => implode(', ', $r['scanners'] ?? []), 'label' => __('Scanners'), 'icon' => 'heroicon-o-magnifying-glass', 'color' => 'info'],
        ];
    }

    protected function wafStats(): array
    {
        $data = $this->getWafStatsData();
        return [Grid::make(count($data))->dense()->schema(array_map(fn ($s) => $this->dashboardCard($s), $data))];
    }

    protected function malwareScannerTabs(): Tabs
    {
        $incidents = $this->client()->get('/incidents', ['limit' => 1000]);
        $incidentCount = is_array($incidents) ? count($incidents) : 0;
        $unresolvedCount = is_array($incidents) ? count(array_filter($incidents, fn ($i) => ! ($i['resolved'] ?? false))) : 0;

        $quarantine = $this->client()->get('/quarantine');
        $quarantineCount = is_array($quarantine) ? count(array_filter($quarantine, fn ($q) => ! ($q['restored'] ?? false) && ! ($q['deleted'] ?? false))) : 0;

        $cleanup = $this->client()->get('/cleanup/records');
        $cleanupCount = is_array($cleanup) ? count($cleanup) : 0;

        return Tabs::make(__('Malware Scanner'))
            ->contained(false)
            ->livewireProperty('threatsTab')
            ->tabs([
                'scan' => Tab::make(__('Scan'))
                    ->icon('heroicon-o-magnifying-glass')
                    ->schema([EmbeddedTable::make(ScanUsersTable::class)]),
                'incidents' => Tab::make(__('Incidents'))
                    ->icon('heroicon-o-exclamation-triangle')
                    ->badge($unresolvedCount > 0 ? $unresolvedCount : null)
                    ->badgeColor('danger')
                    ->schema([EmbeddedTable::make(IncidentsTable::class)]),
                'quarantine' => Tab::make(__('Quarantine'))
                    ->icon('heroicon-o-lock-closed')
                    ->badge($quarantineCount > 0 ? $quarantineCount : null)
                    ->badgeColor('warning')
                    ->schema([EmbeddedTable::make(QuarantineTable::class)]),
                'cleanup' => Tab::make(__('Cleanup'))
                    ->icon('heroicon-o-sparkles')
                    ->badge($cleanupCount > 0 ? $cleanupCount : null)
                    ->badgeColor('success')
                    ->schema([EmbeddedTable::make(CleanupRecordsTable::class)]),
            ]);
    }

    protected function ipProtectionTabs(): array
    {
        $unified = $this->client()->get('/blocklist/unified?per_page=1');
        $blockedCount = $unified['count'] ?? 0;
        $whitelist = $this->client()->get('/bruteforce/whitelist');
        $whitelistCount = $whitelist['count'] ?? count($whitelist['whitelist'] ?? []);

        return [
            Tabs::make(__('IP Protection'))
                ->contained(false)
                ->tabs([
                    'blocked' => Tab::make(__('Blocked'))
                        ->icon('heroicon-o-no-symbol')
                        ->badge($blockedCount > 0 ? $blockedCount : null)
                        ->badgeColor('danger')
                        ->schema([EmbeddedTable::make(UnifiedBlocklistTable::class)]),
                    'whitelist' => Tab::make(__('Whitelist'))
                        ->icon('heroicon-o-shield-check')
                        ->badge($whitelistCount > 0 ? $whitelistCount : null)
                        ->badgeColor('success')
                        ->schema([EmbeddedTable::make(WhitelistTable::class)]),
                ]),
        ];
    }

    protected function crowdsecStats(): array
    {
        $data = $this->getCrowdsecStatsData();
        return [Grid::make(count($data))->dense()->schema(array_map(fn ($s) => $this->dashboardCard($s), $data))];
    }

    protected function bruteforceStats(): array
    {
        $data = $this->getBruteforceStatsData();
        return [Grid::make(count($data))->dense()->schema(array_map(fn ($s) => $this->dashboardCard($s), $data))];
    }

    protected function proactiveStats(): array
    {
        $data = $this->getProactiveStatsData();
        return [Grid::make(count($data))->dense()->schema(array_map(fn ($s) => $this->dashboardCard($s), $data))];
    }

    protected function webshieldStats(): array
    {
        $data = $this->getWebshieldStatsData();
        return [Grid::make(3)->dense()->schema(array_map(fn ($s) => $this->dashboardCard($s), $data))];
    }

    protected function rulesStats(): array
    {
        $data = $this->getRulesStatsData();
        return [Grid::make(count($data))->dense()->schema(array_map(fn ($s) => $this->dashboardCard($s), $data))];
    }

    private function dashboardCard(array $stat): Section
    {
        return Section::make($stat['label'] . ': ' . $stat['value'])
            ->icon($stat['icon'])
            ->iconColor($stat['color'])
            ->schema([]);
    }

    // ── Overview Tab ─────────────────────────────────────────────────

    protected function overviewTab(): array
    {
        $modules = static::getModuleToggles();

        $coreToggles = [];
        foreach ($modules['core'] as $key => $mod) {
            $coreToggles[] = Toggle::make('moduleStates.'.$key)
                ->label(__($mod['label']))
                ->helperText(__($mod['desc']))
                ->live();
        }

        $advToggles = [];
        foreach ($modules['advanced'] as $key => $mod) {
            $advToggles[] = Toggle::make('moduleStates.'.$key)
                ->label(__($mod['label']))
                ->helperText(__($mod['desc']))
                ->live();
        }

        $attackMode = $this->client()->get('/attack-mode') ?? ['active' => false];
        $underAttack = $attackMode['active'] ?? false;

        return [
            Section::make($underAttack ? __('UNDER ATTACK MODE ACTIVE') : __('Under Attack Mode'))
                ->description($underAttack
                    ? __('Active defenses: process killer (threshold 50), auto-block IPs, brute-force limit 3 attempts/120s, WAF blocking, WebShield rate limiting (10 req/s), progressive bans (1h→24h→permanent).')
                    : __('Panic button for active attacks. Enables: process killer, auto-block IPs, WAF blocking, WebShield rate limiting, aggressive brute-force thresholds, and progressive IP bans.'))
                ->icon($underAttack ? 'heroicon-o-fire' : 'heroicon-o-shield-exclamation')
                ->iconColor($underAttack ? 'danger' : 'gray')
                ->headerActions([
                    Action::make('toggleAttackMode')
                        ->label($underAttack ? __('Disable Attack Mode') : __('I Am Under Attack!'))
                        ->icon($underAttack ? 'heroicon-o-shield-check' : 'heroicon-o-fire')
                        ->color($underAttack ? 'gray' : 'danger')
                        ->size('lg')
                        ->requiresConfirmation()
                        ->modalHeading($underAttack ? __('Disable Under Attack Mode?') : __('Enable Under Attack Mode?'))
                        ->modalDescription($underAttack
                            ? __('This will restore your previous defense settings.')
                            : __('This will activate aggressive defenses: process killer (threshold 50), auto-block IPs, brute-force threshold lowered to 3 attempts.'))
                        ->action($underAttack ? 'disableAttackMode' : 'enableAttackMode'),
                ])
                ->compact(),
            Section::make(__('Protection Modules'))->schema([Grid::make(3)->schema($coreToggles)])->compact(),
            Section::make(__('Advanced Protection'))->schema([Grid::make(3)->schema($advToggles)])->compact(),
        ];
    }

    // ── Firewall Tab ─────────────────────────────────────────────────

    protected function firewallTab(): array
    {
        $fw = $this->client()->get('/firewall/ufw/status') ?? ['available' => false];
        $active = $fw['active'] ?? false;

        return [
            Section::make(__('UFW Firewall'))
                ->description($active ? __('Enabled') : __('Disabled'))
                ->icon($active ? 'heroicon-o-shield-check' : 'heroicon-o-shield-exclamation')
                ->iconColor($active ? 'success' : 'danger')
                ->compact()
                ->headerActions([
                    Action::make('fw_toggle')
                        ->label($active ? __('Disable Firewall') : __('Enable Firewall'))
                        ->color($active ? 'danger' : 'success')
                        ->size('xs')
                        ->requiresConfirmation()
                        ->action($active ? 'disableFirewall' : 'enableFirewall'),
                ])
                ->schema([]),
            EmbeddedTable::make(FirewallRulesTable::class),
        ];
    }

    // ── Config Tab ───────────────────────────────────────────────────

    protected function configTab(): array
    {
        $expert = $this->expertMode;
        $tabs = [];
        $categoriesToShow = $expert ? static::$configCategories : static::$basicCategories;

        // Sub-sections within each tab for visual grouping
        $keySections = [
            'General' => [
                'Daemon' => ['LOG_LEVEL', 'LOG_DIR', 'DATA_DIR', 'QUARANTINE_DIR', 'WORKERS'],
                'File Watcher' => ['WATCH_DIRS'],
                'Performance' => ['RAPIDSCAN_WORKERS', 'RAPIDSCAN_MTIME_CACHE'],
            ],
            'Scanning' => [
                'File Filters' => ['SCAN_EXTENSIONS', 'MAX_FILE_SIZE', 'SKIP_DIRS'],
                'Detection Engines' => ['ENTROPY_THRESHOLD', 'YARA_RULES_DIR', 'BEHAVIOR_TTL', 'CLAMAV_ENABLED', 'CLAMAV_SOCKET', 'FRESHCLAM_ON_UPDATE'],
                'Scoring & Response' => ['SCORE_LOG', 'SCORE_QUARANTINE', 'SCORE_SUSPEND', 'AUTO_QUARANTINE', 'AUTO_SUSPEND', 'AUTO_BLOCK_IP'],
                'Scheduled Scans' => ['SCHEDULED_SCAN_ENABLED', 'SCHEDULED_SCAN_INTERVAL', 'SCHEDULED_SCAN_PATHS'],
                'Auto Cleanup' => ['CLEANUP_ENABLED', 'CLEANUP_AUTO', 'CLEANUP_BACKUP_DIR', 'CLEANUP_CMS_CHECKSUMS'],
            ],
            'Network' => [
                'WAF (ModSecurity)' => ['WAF_ENABLED', 'WAF_AUDIT_LOG', 'WAF_AUDIT_LOG_TYPE', 'WAF_RULES_DIR', 'WAF_OVERRIDES_FILE', 'WAF_CRS_AUTO_UPDATE', 'WAF_WEB_SERVER', 'WAF_NGINX_INCLUDE'],
                'CrowdSec' => ['CROWDSEC_ENABLED', 'CROWDSEC_LAPI_URL', 'CROWDSEC_BOUNCER_KEY', 'CROWDSEC_SYNC_INTERVAL'],
                'Threat Intelligence' => ['THREAT_INTEL_ENABLED', 'THREAT_INTEL_UPDATE_INTERVAL', 'THREAT_INTEL_FEEDS', 'THREAT_INTEL_AUTO_BLOCK', 'THREAT_INTEL_AUTO_BLOCK_THRESHOLD'],
                'UFW Firewall' => ['UFW_ENABLED'],
            ],
            'Modules' => [
                'Process Killer' => ['PROCESS_KILL_ENABLED', 'PROCESS_KILL_THRESHOLD', 'PROCESS_KILL_MIN_UID', 'PROCESS_KILL_WHITELIST', 'PROCESS_POLL_INTERVAL'],
                'WebShield' => ['WEBSHIELD_ENABLED', 'WEBSHIELD_RATE_LIMIT', 'WEBSHIELD_RATE_BURST', 'WEBSHIELD_CHALLENGE_ENABLED', 'WEBSHIELD_BOT_FILTERING', 'WEBSHIELD_NGINX_CONF_DIR', 'NGINX_ACCESS_LOG'],
            ],
        ];

        foreach ($categoriesToShow as $category => $allKeys) {
            $fields = [];
            $sections = $keySections[$category] ?? [$category => $allKeys];

            foreach ($sections as $sectionName => $sectionKeys) {
                $sectionFields = [];
                foreach ($sectionKeys as $key) {
                    if (! in_array($key, $allKeys)) {
                        continue; // Not in this mode's key list
                    }
                    if (! $expert && in_array($key, static::$expertKeys)) {
                        continue;
                    }
                    $help = static::configHelp()[$key] ?? '';
                    $fieldName = 'configData.config_'.$key;
                    if (in_array($key, static::$booleanKeys)) {
                        $sectionFields[] = Toggle::make($fieldName)
                            ->label($key)
                            ->helperText($help);
                    } elseif (isset(static::$selectKeys[$key])) {
                        $opts = array_combine(static::$selectKeys[$key], static::$selectKeys[$key]);
                        $sectionFields[] = Select::make($fieldName)
                            ->label($key)
                            ->options($opts)
                            ->helperText($help);
                    } else {
                        $sectionFields[] = TextInput::make($fieldName)
                            ->label($key)
                            ->helperText($help);
                    }
                }
                if (! empty($sectionFields)) {
                    $fields[] = Section::make(__($sectionName))
                        ->compact()
                        ->collapsible()
                        ->schema($sectionFields);
                }
            }

            if (! empty($fields)) {
                $tabs[$category] = Tab::make(__($category))->schema($fields);
            }
        }

        return [
            Section::make(__('Configuration'))
                ->headerActions([
                    Action::make('toggleExpertMode')
                        ->label($expert ? __('Basic Mode') : __('Expert Mode'))
                        ->icon($expert ? 'heroicon-o-eye-slash' : 'heroicon-o-eye')
                        ->color($expert ? 'warning' : 'gray')
                        ->size('xs')
                        ->action('toggleExpertMode'),
                    Action::make('saveAndRestart')
                        ->label(__('Save & Restart'))
                        ->icon('heroicon-o-check')
                        ->color('success')
                        ->size('xs')
                        ->action('saveAndRestart'),
                ])
                ->schema([
                    Tabs::make(__('config_tabs'))
                        ->contained(false)
                        ->tabs($tabs),
                ]),
        ];
    }


    public function toggleExpertMode(): void
    {
        $this->expertMode = ! $this->expertMode;
    }

    // ── Module Toggles ───────────────────────────────────────────────


    public function saveAndRestart(): void
    {
        $payload = [];
        foreach (static::$configCategories as $keys) {
            foreach ($keys as $key) {
                $formKey = 'config_'.$key;
                if (array_key_exists($formKey, $this->configData)) {
                    $val = $this->configData[$formKey];
                    if (in_array($key, static::$booleanKeys)) {
                        $val = $val ? 'yes' : 'no';
                    }
                    $payload[$key] = (string) $val;
                }
            }
        }

        if (! empty($payload)) {
            $result = $this->client()->patch('/config', $payload);
            if (! $result) {
                Notification::make()->title(__('Failed to save config'))->danger()->send();

                return;
            }
        }

        Notification::make()
            ->title(__('Settings saved'))
            ->body(count($payload).' '.__('settings applied'))
            ->success()
            ->send();
    }

    // ── Attack Mode Actions ────────────────────────────────────────────

    public function enableAttackMode(): void
    {
        $result = $this->client()->post('/attack-mode/enable');
        $actions = $result['actions_taken'] ?? [];
        $body = count($actions) > 0
            ? implode("\n", array_map(fn ($a) => "• {$a}", $actions))
            : __('Aggressive defenses activated');
        $body .= "\n\n" . implode("\n", [
            __('• Process killer threshold lowered to 50'),
            __('• Auto-block IPs on suspicious activity'),
            __('• Brute-force threshold: 3 attempts in 120 seconds'),
            __('• Block durations: 1h → 24h → permanent'),
            __('• WAF blocking enabled per-site'),
            __('• WebShield rate limiting: 10 req/s, burst 5'),
        ]);
        Notification::make()
            ->title($result ? __('Under Attack mode ENABLED') : __('Failed to enable attack mode'))
            ->body($body)
            ->{$result ? 'danger' : 'warning'}()
            ->duration(15000)
            ->send();
        $this->redirect(static::getUrl(['tab' => 'overview']));
    }

    public function disableAttackMode(): void
    {
        $result = $this->client()->post('/attack-mode/disable');
        $actions = implode(', ', $result['actions_taken'] ?? []);
        Notification::make()
            ->title($result ? __('Under Attack mode disabled') : __('Failed to disable attack mode'))
            ->body($actions ?: __('Normal settings restored'))
            ->{$result ? 'success' : 'danger'}()
            ->duration(10000)
            ->send();
        $this->redirect(static::getUrl(['tab' => 'overview']));
    }

    // ── GeoIP Actions ────────────────────────────────────────────────

    protected function geoipStats(): array
    {
        $data = $this->client()->get('/webshield/geo-rules') ?? [];
        $enabled = $data['enabled'] ?? false;
        $dbAvail = $data['db']['available'] ?? false;
        $ruleCount = count($data['rules'] ?? []);
        $dbDate = '';
        if ($dbAvail && isset($data['db']['build_epoch'])) {
            $dbDate = date('Y-m-d', (int) $data['db']['build_epoch']);
        }

        return [
            Grid::make(3)->dense()->schema([
                $this->dashboardCard(['value' => $enabled ? __('On') : __('Off'), 'label' => __('GeoIP'), 'icon' => 'heroicon-o-globe-alt', 'color' => $enabled ? 'success' : 'gray']),
                $this->dashboardCard(['value' => $dbAvail ? $dbDate : __('Missing'), 'label' => __('Database'), 'icon' => 'heroicon-o-circle-stack', 'color' => $dbAvail ? 'success' : 'danger']),
                $this->dashboardCard(['value' => (string) $ruleCount, 'label' => __('Country Rules'), 'icon' => 'heroicon-o-flag', 'color' => $ruleCount > 0 ? 'warning' : 'gray']),
            ]),
        ];
    }

    public function saveGeoipSettings(): void
    {
        $patch = ['GEOIP_ENABLED' => 'yes'];

        if (! empty($this->geoipLicenseKey)) {
            $patch['GEOIP_MAXMIND_LICENSE_KEY'] = $this->geoipLicenseKey;
        }
        if (! empty($this->geoipAction)) {
            $patch['GEOIP_ACTION'] = $this->geoipAction;
        }

        $result = $this->client()->patch('/config', $patch);

        Notification::make()
            ->title($result ? __('GeoIP settings saved') : __('Failed to save settings'))
            ->{($result ? 'success' : 'danger')}()
            ->send();

        $this->geoipLicenseKey = '';
    }

    // ── Firewall Actions ─────────────────────────────────────────────

    public function enableFirewall(): void
    {
        $result = $this->client()->post('/firewall/ufw/enable');
        Notification::make()
            ->title($result ? __('Firewall enabled') : __('Failed to enable firewall'))
            ->{($result ? "success" : "danger")}()
            ->send();
        $this->redirect(static::getUrl(['tab' => 'defense', 'defense' => 'firewall']));
    }

    public function disableFirewall(): void
    {
        $result = $this->client()->post('/firewall/ufw/disable');
        Notification::make()
            ->title($result ? __('Firewall disabled') : __('Failed to disable firewall'))
            ->{($result ? "success" : "danger")}()
            ->send();
        $this->redirect(static::getUrl(['tab' => 'defense', 'defense' => 'firewall']));
    }

    public static function configHelp(): array
    {
        return [
            'LOG_LEVEL' => __('Logging verbosity: debug shows everything, error shows only errors'),
            'LOG_DIR' => __('Directory for daemon log files'),
            'DATA_DIR' => __('Directory for SQLite database and cached data'),
            'QUARANTINE_DIR' => __('Where quarantined malicious files are moved to'),
            'WORKERS' => __('Number of parallel scan workers (1-32)'),
            'WATCH_DIRS' => __('Comma-separated directory globs to monitor for file changes'),
            'SCAN_EXTENSIONS' => __('File extensions to scan (comma-separated)'),
            'MAX_FILE_SIZE' => __('Maximum file size to scan in bytes (default 2MB)'),
            'SKIP_DIRS' => __('Directory names to skip during scanning'),
            'ENTROPY_THRESHOLD' => __('Entropy score threshold (0.0-8.0, default 4.5)'),
            'YARA_RULES_DIR' => __('Directory containing .yar rule files'),
            'CLAMAV_ENABLED' => __('ClamAV scanning: auto (detect), yes (require), no (disable)'),
            'CLAMAV_SOCKET' => __('Path to clamd Unix socket'),
            'FRESHCLAM_ON_UPDATE' => __('Run freshclam when reloading rules'),
            'SCORE_LOG' => __('Minimum score to log an incident'),
            'SCORE_QUARANTINE' => __('Minimum score to auto-quarantine a file'),
            'SCORE_SUSPEND' => __('Minimum score to auto-suspend a hosting account'),
            'PROCESS_POLL_INTERVAL' => __('Seconds between process scans'),
            'BEHAVIOR_TTL' => __('Seconds to keep behavioral tracking data'),
            'AUTO_QUARANTINE' => __('Automatically quarantine files exceeding score threshold'),
            'AUTO_SUSPEND' => __('Automatically suspend accounts exceeding score threshold'),
            'AUTO_BLOCK_IP' => __('Automatically block IP addresses associated with attacks'),
            'WAF_ENABLED' => __('Enable ModSecurity WAF event monitoring'),
            'WAF_AUDIT_LOG' => __('Path to ModSecurity audit log file'),
            'WAF_AUDIT_LOG_TYPE' => __('Audit log format: serial (single file) or concurrent'),
            'WAF_RULES_DIR' => __('Directory containing OWASP CRS rule files'),
            'WAF_OVERRIDES_FILE' => __('File for custom ModSecurity rule overrides'),
            'WAF_CRS_AUTO_UPDATE' => __('Automatically update OWASP Core Rule Set'),
            'WAF_WEB_SERVER' => __('Web server type: auto, nginx, or apache'),
            'WAF_NGINX_INCLUDE' => __('Nginx include file for per-site modsecurity on/off toggle'),
            'FIREWALL_BACKEND' => __('IP blocking backend: auto, nftables, iptables, or none'),
            'BRUTEFORCE_WHITELIST_IPS' => __('IPs that are never blocked (comma-separated)'),
            'UFW_ENABLED' => __('Enable UFW firewall rule management via the API'),
            'PROCESS_KILL_ENABLED' => __('Kill suspicious processes (reverse shells, crypto miners)'),
            'PROCESS_KILL_THRESHOLD' => __('Suspicion score threshold to kill a process (1-100)'),
            'PROCESS_KILL_MIN_UID' => __('Minimum UID to consider for process killing'),
            'PROCESS_KILL_WHITELIST' => __('Process names to never kill (comma-separated)'),
            'CLEANUP_ENABLED' => __('Enable malware cleanup engine'),
            'CLEANUP_AUTO' => __('Automatically clean detected malware'),
            'CLEANUP_BACKUP_DIR' => __('Directory for cleanup backups before modification'),
            'CLEANUP_CMS_CHECKSUMS' => __('Verify CMS file integrity using official checksums'),
            'SCHEDULED_SCAN_ENABLED' => __('Enable periodic full-directory scanning'),
            'SCHEDULED_SCAN_INTERVAL' => __('Hours between scheduled scans (1-8760)'),
            'SCHEDULED_SCAN_PATHS' => __('Paths to scan on schedule (comma-separated globs)'),
            'THREAT_INTEL_ENABLED' => __('Enable threat intelligence feed downloads'),
            'THREAT_INTEL_UPDATE_INTERVAL' => __('Hours between feed updates (1-168)'),
            'THREAT_INTEL_FEEDS' => __('Enabled feed names (comma-separated)'),
            'THREAT_INTEL_AUTO_BLOCK' => __('Automatically block IPs found in threat feeds'),
            'THREAT_INTEL_AUTO_BLOCK_THRESHOLD' => __('Number of feeds an IP must appear in before auto-blocking'),
            'WEBSHIELD_ENABLED' => __('Enable nginx-level bot filtering and rate limiting'),
            'WEBSHIELD_RATE_LIMIT' => __('Requests per second before rate limiting kicks in'),
            'WEBSHIELD_RATE_BURST' => __('Maximum burst of requests allowed'),
            'WEBSHIELD_CHALLENGE_ENABLED' => __('Serve JS challenge pages to suspicious clients'),
            'WEBSHIELD_BOT_FILTERING' => __('Block known malicious user agents'),
            'WEBSHIELD_NGINX_CONF_DIR' => __('Directory for WebShield nginx config snippets'),
            'NGINX_ACCESS_LOG' => __('Nginx access log path for counting blocked requests'),
            'RAPIDSCAN_WORKERS' => __('Parallel workers for rapid directory scans'),
            'RAPIDSCAN_MTIME_CACHE' => __('Cache file modification times to skip unchanged files'),
            'CROWDSEC_ENABLED' => __('CrowdSec community threat intelligence: auto (detect), yes, no'),
            'CROWDSEC_LAPI_URL' => __('CrowdSec Local API URL (default: http://127.0.0.1:8080)'),
            'CROWDSEC_BOUNCER_KEY' => __('Bouncer API key (generated by installer)'),
            'CROWDSEC_SYNC_INTERVAL' => __('Decision polling interval in seconds'),
            'GEOIP_ENABLED' => __('Enable GeoIP country blocking with MaxMind database'),
            'GEOIP_MAXMIND_LICENSE_KEY' => __('MaxMind license key (free at maxmind.com/en/geolite2/signup)'),
            'GEOIP_DB_PATH' => __('Path to GeoLite2-Country.mmdb database file'),
            'GEOIP_ACTION' => __('Default action for blocked countries: block, challenge, or log'),
            'GEOIP_BLOCKED_COUNTRIES' => __('Comma-separated ISO country codes to block (e.g. CN,RU,KP)'),
            'GEOIP_ALLOWED_COUNTRIES' => __('Whitelist mode: only these countries allowed (overrides blocked list)'),
        ];
    }

    // ── Static Data ──────────────────────────────────────────────────

    public static function getModuleToggles(): array
    {
        return [
            'core' => [
                'AUTO_QUARANTINE' => ['label' => 'Auto Quarantine', 'desc' => 'Quarantine files above score threshold'],
            ],
            'advanced' => [
                'WAF_ENABLED' => ['label' => 'WAF (ModSecurity)', 'desc' => 'Web application firewall with OWASP CRS'],
                'PROCESS_KILL_ENABLED' => ['label' => 'Process Killer', 'desc' => 'Kills reverse shells and miners'],
                'WEBSHIELD_ENABLED' => ['label' => 'WebShield', 'desc' => 'Nginx bot filtering and rate limiting'],
                'THREAT_INTEL_ENABLED' => ['label' => 'Threat Intelligence', 'desc' => 'IP reputation and malware hash feeds'],
                'CLEANUP_ENABLED' => ['label' => 'Auto Cleanup', 'desc' => 'Removes injected code from files'],
                'UFW_ENABLED' => ['label' => 'UFW Firewall', 'desc' => 'Manage system firewall rules'],
                'SCHEDULED_SCAN_ENABLED' => ['label' => 'Scheduled Scans', 'desc' => 'Periodic full-path scanning'],
                'AUTO_SUSPEND' => ['label' => 'Auto Suspend', 'desc' => 'Suspends accounts above score threshold'],
            ],
        ];
    }

    public static array $booleanKeys = [
        'AUTO_QUARANTINE', 'AUTO_SUSPEND', 'AUTO_BLOCK_IP',
        'WAF_ENABLED', 'WAF_CRS_AUTO_UPDATE',
        'PROCESS_KILL_ENABLED',
        'CLEANUP_ENABLED', 'CLEANUP_AUTO', 'CLEANUP_CMS_CHECKSUMS',
        'SCHEDULED_SCAN_ENABLED', 'THREAT_INTEL_ENABLED', 'THREAT_INTEL_AUTO_BLOCK',
        'WEBSHIELD_ENABLED', 'WEBSHIELD_CHALLENGE_ENABLED', 'WEBSHIELD_BOT_FILTERING',
        'RAPIDSCAN_MTIME_CACHE', 'FRESHCLAM_ON_UPDATE',
        'UFW_ENABLED',
    ];

    public static array $selectKeys = [
        'LOG_LEVEL' => ['debug', 'info', 'warning', 'error', 'critical'],
        'CLAMAV_ENABLED' => ['auto', 'yes', 'no'],
        'CROWDSEC_ENABLED' => ['auto', 'yes', 'no'],
        'FIREWALL_BACKEND' => ['auto', 'nftables', 'iptables', 'none'],
        'WAF_AUDIT_LOG_TYPE' => ['serial', 'concurrent'],
        'WAF_WEB_SERVER' => ['auto', 'nginx', 'apache'],
    ];

    /** Basic mode: consolidated categories, expert keys hidden. */
    public static array $basicCategories = [
        'General' => ['LOG_LEVEL', 'WORKERS', 'AUTO_QUARANTINE', 'AUTO_SUSPEND'],
        'Scanning' => ['CLAMAV_ENABLED', 'SCORE_LOG', 'SCORE_QUARANTINE', 'SCORE_SUSPEND', 'SCHEDULED_SCAN_ENABLED', 'SCHEDULED_SCAN_INTERVAL', 'CLEANUP_ENABLED', 'CLEANUP_AUTO'],
        'Network' => ['WAF_ENABLED', 'WAF_CRS_AUTO_UPDATE', 'CROWDSEC_ENABLED', 'UFW_ENABLED', 'THREAT_INTEL_ENABLED', 'THREAT_INTEL_AUTO_BLOCK'],
        'Modules' => ['PROCESS_KILL_ENABLED', 'PROCESS_KILL_THRESHOLD', 'WEBSHIELD_ENABLED', 'WEBSHIELD_RATE_LIMIT', 'WEBSHIELD_RATE_BURST'],
    ];

    /** Expert mode: all keys grouped into consolidated categories. */
    public static array $configCategories = [
        'General' => ['LOG_LEVEL', 'LOG_DIR', 'DATA_DIR', 'QUARANTINE_DIR', 'WORKERS', 'WATCH_DIRS', 'RAPIDSCAN_WORKERS', 'RAPIDSCAN_MTIME_CACHE'],
        'Scanning' => ['SCAN_EXTENSIONS', 'MAX_FILE_SIZE', 'SKIP_DIRS', 'ENTROPY_THRESHOLD', 'YARA_RULES_DIR', 'BEHAVIOR_TTL', 'CLAMAV_ENABLED', 'CLAMAV_SOCKET', 'FRESHCLAM_ON_UPDATE', 'SCORE_LOG', 'SCORE_QUARANTINE', 'SCORE_SUSPEND', 'AUTO_QUARANTINE', 'AUTO_SUSPEND', 'AUTO_BLOCK_IP', 'SCHEDULED_SCAN_ENABLED', 'SCHEDULED_SCAN_INTERVAL', 'SCHEDULED_SCAN_PATHS', 'CLEANUP_ENABLED', 'CLEANUP_AUTO', 'CLEANUP_BACKUP_DIR', 'CLEANUP_CMS_CHECKSUMS'],
        'Network' => ['WAF_ENABLED', 'WAF_AUDIT_LOG', 'WAF_AUDIT_LOG_TYPE', 'WAF_RULES_DIR', 'WAF_OVERRIDES_FILE', 'WAF_CRS_AUTO_UPDATE', 'WAF_WEB_SERVER', 'WAF_NGINX_INCLUDE', 'FIREWALL_BACKEND', 'BRUTEFORCE_WHITELIST_IPS', 'CROWDSEC_ENABLED', 'CROWDSEC_LAPI_URL', 'CROWDSEC_BOUNCER_KEY', 'CROWDSEC_SYNC_INTERVAL', 'UFW_ENABLED', 'THREAT_INTEL_ENABLED', 'THREAT_INTEL_UPDATE_INTERVAL', 'THREAT_INTEL_FEEDS', 'THREAT_INTEL_AUTO_BLOCK', 'THREAT_INTEL_AUTO_BLOCK_THRESHOLD'],
        'Modules' => ['PROCESS_KILL_ENABLED', 'PROCESS_KILL_THRESHOLD', 'PROCESS_KILL_MIN_UID', 'PROCESS_KILL_WHITELIST', 'PROCESS_POLL_INTERVAL', 'WEBSHIELD_ENABLED', 'WEBSHIELD_RATE_LIMIT', 'WEBSHIELD_RATE_BURST', 'WEBSHIELD_CHALLENGE_ENABLED', 'WEBSHIELD_BOT_FILTERING', 'WEBSHIELD_NGINX_CONF_DIR', 'NGINX_ACCESS_LOG'],
    ];

    /** Keys hidden in basic mode (file paths, internal tuning, advanced options). */
    public static array $expertKeys = [
        'LOG_DIR', 'DATA_DIR', 'QUARANTINE_DIR',
        'WATCH_DIRS', 'SCAN_EXTENSIONS', 'MAX_FILE_SIZE', 'SKIP_DIRS',
        'ENTROPY_THRESHOLD', 'YARA_RULES_DIR', 'PROCESS_POLL_INTERVAL', 'BEHAVIOR_TTL',
        'CLAMAV_SOCKET', 'FRESHCLAM_ON_UPDATE',
        'WAF_AUDIT_LOG', 'WAF_AUDIT_LOG_TYPE', 'WAF_RULES_DIR', 'WAF_OVERRIDES_FILE', 'WAF_WEB_SERVER', 'WAF_NGINX_INCLUDE',
        'FIREWALL_BACKEND',
        'PROCESS_KILL_MIN_UID', 'PROCESS_KILL_WHITELIST',
        'CLEANUP_BACKUP_DIR', 'CLEANUP_CMS_CHECKSUMS',
        'THREAT_INTEL_UPDATE_INTERVAL', 'THREAT_INTEL_FEEDS', 'THREAT_INTEL_AUTO_BLOCK_THRESHOLD',
        'WEBSHIELD_CHALLENGE_ENABLED', 'WEBSHIELD_BOT_FILTERING', 'WEBSHIELD_NGINX_CONF_DIR', 'NGINX_ACCESS_LOG',
        'CROWDSEC_LAPI_URL', 'CROWDSEC_BOUNCER_KEY', 'CROWDSEC_SYNC_INTERVAL',
        'RAPIDSCAN_WORKERS', 'RAPIDSCAN_MTIME_CACHE',
    ];
}
