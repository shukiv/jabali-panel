<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Filament\Admin\Widgets\SslStatsOverview;
use App\Models\Domain;
use App\Models\SslCertificate;
use App\Models\User;
use App\Services\Agent\AgentClient;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Forms\Components\Select;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Filters\SelectFilter;
use Filament\Tables\Table;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Support\Facades\Artisan;

class SslManager extends Page implements HasTable
{
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-shield-check';

    protected static ?int $navigationSort = 8;

    public static function getNavigationLabel(): string
    {
        return __('SSL Manager');
    }

    public function getTitle(): string
    {
        return __('SSL Manager');
    }

    protected string $view = 'filament.admin.pages.ssl-manager';

    public bool $isRunning = false;

    public string $autoSslLog = '';

    public ?string $lastUpdated = null;

    protected ?AgentClient $agent = null;

    protected function getHeaderWidgets(): array
    {
        return [
            SslStatsOverview::class,
        ];
    }

    public function getHeaderWidgetsColumns(): int|array
    {
        return 6;
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
        $this->lastUpdated = now()->format('H:i:s');
    }

    public function table(Table $table): Table
    {
        return $table
            ->query(SslCertificate::with(['domain.user']))
            ->columns([
                TextColumn::make('domain.domain')
                    ->label(__('Domain'))
                    ->searchable()
                    ->sortable()
                    ->formatStateUsing(function ($state, SslCertificate $record) {
                        if ($record->service === 'mail') {
                            return 'mail.' . $state;
                        }

                        return $state;
                    })
                    ->description(fn (SslCertificate $record) => $record->domain?->user?->username ?? __('Unknown')),
                TextColumn::make('service')
                    ->label(__('Service'))
                    ->badge()
                    ->color(fn (string $state) => match ($state) {
                        'web' => 'info',
                        'mail' => 'warning',
                        default => 'gray',
                    })
                    ->formatStateUsing(fn (string $state) => match ($state) {
                        'web' => __('HTTPS'),
                        'mail' => __('Mail (IMAPS/SMTPS)'),
                        default => ucfirst($state),
                    }),
                TextColumn::make('type')
                    ->label(__('Type'))
                    ->badge()
                    ->color('gray')
                    ->formatStateUsing(fn ($state) => $state ? ucfirst(str_replace('_', ' ', $state)) : __('Unknown')),
                TextColumn::make('status')
                    ->label(__('Status'))
                    ->badge()
                    ->color(fn ($state) => match ($state) {
                        'active' => 'success',
                        'expired' => 'danger',
                        'expiring' => 'warning',
                        'failed' => 'danger',
                        default => 'gray',
                    })
                    ->formatStateUsing(fn ($state) => $state ? ucfirst($state) : __('Unknown')),
                TextColumn::make('expires_at')
                    ->label(__('Expires'))
                    ->date('M d, Y')
                    ->description(fn (SslCertificate $record) => $record->days_until_expiry !== null
                        ? __(':days days', ['days' => $record->days_until_expiry])
                        : null)
                    ->color(fn (SslCertificate $record) => match (true) {
                        $record->days_until_expiry !== null && $record->days_until_expiry <= 7 => 'danger',
                        $record->days_until_expiry !== null && $record->days_until_expiry <= 30 => 'warning',
                        default => 'gray',
                    }),
                TextColumn::make('last_check_at')
                    ->label(__('Last Check'))
                    ->since()
                    ->sortable(),
                TextColumn::make('last_error')
                    ->label(__('Error'))
                    ->limit(30)
                    ->tooltip(fn ($state) => $state)
                    ->color('danger')
                    ->placeholder(__('-')),
            ])
            ->filters([
                SelectFilter::make('service')
                    ->label(__('Service'))
                    ->options([
                        'web' => __('HTTPS'),
                        'mail' => __('Mail'),
                    ]),
                SelectFilter::make('status')
                    ->label(__('Status'))
                    ->options([
                        'active' => __('Active'),
                        'expiring' => __('Expiring Soon'),
                        'expired' => __('Expired'),
                        'failed' => __('Failed'),
                    ]),
                SelectFilter::make('user')
                    ->label(__('User'))
                    ->options(fn () => User::pluck('username', 'id')->toArray())
                    ->query(fn (Builder $query, array $data) => $data['value']
                        ? $query->whereHas('domain', fn ($q) => $q->where('user_id', $data['value']))
                        : $query),
            ])
            ->recordActions([
                Action::make('renew')
                    ->label(__('Renew'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('primary')
                    ->visible(fn (SslCertificate $record) => $record->type === 'lets_encrypt' && $record->status === 'active')
                    ->action(fn (SslCertificate $record) => $record->service === 'mail'
                        ? $this->renewMailSslForDomain($record->domain_id)
                        : $this->renewSslForDomain($record->domain_id)),
                Action::make('check')
                    ->label(__('Check'))
                    ->icon('heroicon-o-magnifying-glass')
                    ->color('gray')
                    ->action(fn (SslCertificate $record) => $this->checkSslForDomain($record->domain_id)),
            ])
            ->heading(__('SSL Certificates'))
            ->poll('30s');
    }

    public function issueSslForDomain(int $domainId): void
    {
        try {
            $domain = Domain::with('user')->findOrFail($domainId);

            $result = $this->getAgent()->sslIssue(
                $domain->domain,
                $domain->user->username,
                $domain->user->email,
                true
            );

            if ($result['success'] ?? false) {
                SslCertificate::updateOrCreate(
                    ['domain_id' => $domain->id, 'service' => 'web'],
                    [
                        'type' => 'lets_encrypt',
                        'status' => 'active',
                        'issuer' => "Let's Encrypt",
                        'certificate' => $result['certificate'] ?? null,
                        'issued_at' => now(),
                        'expires_at' => isset($result['valid_to']) ? \Carbon\Carbon::parse($result['valid_to']) : now()->addMonths(3),
                        'last_check_at' => now(),
                        'last_error' => null,
                        'renewal_attempts' => 0,
                        'auto_renew' => true,
                    ]
                );

                $domain->update(['ssl_enabled' => true]);

                Notification::make()
                    ->title(__('SSL Certificate Issued'))
                    ->body(__('Certificate issued for :domain', ['domain' => $domain->domain]))
                    ->success()
                    ->send();
            } else {
                SslCertificate::updateOrCreate(
                    ['domain_id' => $domain->id, 'service' => 'web'],
                    [
                        'type' => 'lets_encrypt',
                        'status' => 'failed',
                        'last_check_at' => now(),
                        'last_error' => $result['error'] ?? __('Unknown error'),
                    ]
                );

                Notification::make()
                    ->title(__('SSL Certificate Failed'))
                    ->body($result['error'] ?? __('Unknown error'))
                    ->danger()
                    ->send();
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }

        $this->lastUpdated = now()->format('H:i:s');
    }

    public function renewSslForDomain(int $domainId): void
    {
        try {
            $domain = Domain::with('user')->findOrFail($domainId);

            $result = $this->getAgent()->sslRenew($domain->domain, $domain->user->username);

            if ($result['success'] ?? false) {
                $ssl = $domain->sslCertificate;
                if ($ssl) {
                    $ssl->update([
                        'status' => 'active',
                        'issued_at' => now(),
                        'expires_at' => isset($result['valid_to']) ? \Carbon\Carbon::parse($result['valid_to']) : now()->addMonths(3),
                        'last_check_at' => now(),
                        'last_error' => null,
                        'renewal_attempts' => 0,
                    ]);
                }

                Notification::make()
                    ->title(__('Certificate Renewed'))
                    ->body(__('SSL certificate renewed for :domain', ['domain' => $domain->domain]))
                    ->success()
                    ->send();
            } else {
                Notification::make()
                    ->title(__('Renewal Failed'))
                    ->body($result['error'] ?? __('Unknown error'))
                    ->danger()
                    ->send();
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }

        $this->lastUpdated = now()->format('H:i:s');
    }

    public function checkSslForDomain(int $domainId): void
    {
        try {
            $domain = Domain::with('user')->findOrFail($domainId);

            $result = $this->getAgent()->sslCheck($domain->domain, $domain->user->username);

            if ($result['success'] ?? false) {
                $sslData = $result['ssl'] ?? [];

                if ($sslData['has_ssl'] ?? false) {
                    SslCertificate::updateOrCreate(
                        ['domain_id' => $domain->id, 'service' => 'web'],
                        [
                            'type' => $sslData['type'] ?? 'custom',
                            'status' => ($sslData['is_expired'] ?? false) ? 'expired' : 'active',
                            'issuer' => $sslData['issuer'],
                            'certificate' => $sslData['certificate'] ?? null,
                            'issued_at' => isset($sslData['valid_from']) ? \Carbon\Carbon::parse($sslData['valid_from']) : null,
                            'expires_at' => isset($sslData['valid_to']) ? \Carbon\Carbon::parse($sslData['valid_to']) : null,
                            'last_check_at' => now(),
                        ]
                    );
                    $domain->update(['ssl_enabled' => true]);
                }

                Notification::make()
                    ->title(__('Certificate Checked'))
                    ->body($sslData['has_ssl'] ? __('Found: :issuer', ['issuer' => $sslData['issuer']]) : __('No certificate found'))
                    ->success()
                    ->send();
            } else {
                Notification::make()
                    ->title(__('Check Failed'))
                    ->body($result['error'] ?? __('Unknown error'))
                    ->danger()
                    ->send();
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }

        $this->lastUpdated = now()->format('H:i:s');
    }

    public function issueMailSslForDomain(int $domainId): void
    {
        try {
            $domain = Domain::with('user')->findOrFail($domainId);
            $mailHostname = 'mail.' . $domain->domain;

            $result = $this->getAgent()->sslMailIssue($mailHostname, $domain->user->email);

            if ($result['success'] ?? false) {
                SslCertificate::updateOrCreate(
                    ['domain_id' => $domain->id, 'service' => 'mail'],
                    [
                        'type' => 'lets_encrypt',
                        'status' => 'active',
                        'issuer' => "Let's Encrypt",
                        'issued_at' => now(),
                        'expires_at' => isset($result['valid_to']) ? \Carbon\Carbon::parse($result['valid_to']) : now()->addMonths(3),
                        'last_check_at' => now(),
                        'last_error' => null,
                        'renewal_attempts' => 0,
                        'auto_renew' => true,
                    ]
                );

                Notification::make()
                    ->title(__('Mail SSL Certificate Issued'))
                    ->body(__('Certificate issued for :hostname', ['hostname' => $mailHostname]))
                    ->success()
                    ->send();
            } else {
                SslCertificate::updateOrCreate(
                    ['domain_id' => $domain->id, 'service' => 'mail'],
                    [
                        'type' => 'lets_encrypt',
                        'status' => 'failed',
                        'last_check_at' => now(),
                        'last_error' => $result['error'] ?? __('Unknown error'),
                    ]
                );

                Notification::make()
                    ->title(__('Mail SSL Certificate Failed'))
                    ->body($result['error'] ?? __('Unknown error'))
                    ->danger()
                    ->send();
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }

        $this->lastUpdated = now()->format('H:i:s');
    }

    public function renewMailSslForDomain(int $domainId): void
    {
        // For mail certs, re-issuing is the same as renewing
        $this->issueMailSslForDomain($domainId);
    }

    public function runAutoSsl(?string $domain = null): void
    {
        $this->isRunning = true;
        $this->autoSslLog = '';

        try {
            // Ensure log directory exists with proper permissions
            $logDir = storage_path('logs/ssl');
            if (! is_dir($logDir)) {
                @mkdir($logDir, 0775, true);
            }

            $params = [];
            if ($domain) {
                $params['--domain'] = $domain;
            }

            Artisan::call('jabali:ssl-check', $params);
            $this->autoSslLog = Artisan::output();

            Notification::make()
                ->title(__('SSL Check Complete'))
                ->body($domain
                    ? __('SSL check completed for :domain', ['domain' => $domain])
                    : __('SSL certificate check completed for all domains'))
                ->success()
                ->send();
        } catch (Exception $e) {
            $this->autoSslLog = __('Error: :message', ['message' => $e->getMessage()]);

            Notification::make()
                ->title(__('SSL Check Failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }

        $this->isRunning = false;
        $this->lastUpdated = now()->format('H:i:s');
    }

    public function runSslCheckForUser(int $userId): void
    {
        $this->isRunning = true;
        $this->autoSslLog = '';

        try {
            $user = User::findOrFail($userId);
            $domains = Domain::where('user_id', $userId)->pluck('domain')->toArray();

            if (empty($domains)) {
                $this->autoSslLog = __('No domains found for user :user', ['user' => $user->username]);
                Notification::make()
                    ->title(__('No Domains'))
                    ->body(__('User :user has no domains', ['user' => $user->username]))
                    ->warning()
                    ->send();
                $this->isRunning = false;

                return;
            }

            $this->autoSslLog = __('Checking SSL for :count domains of user :user', ['count' => count($domains), 'user' => $user->username])."\n\n";

            foreach ($domains as $domain) {
                Artisan::call('jabali:ssl-check', ['--domain' => $domain]);
                $this->autoSslLog .= Artisan::output()."\n";
            }

            Notification::make()
                ->title(__('SSL Check Complete'))
                ->body(__('SSL check completed for :count domains of user :user', ['count' => count($domains), 'user' => $user->username]))
                ->success()
                ->send();
        } catch (Exception $e) {
            $this->autoSslLog = __('Error: :message', ['message' => $e->getMessage()]);

            Notification::make()
                ->title(__('SSL Check Failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }

        $this->isRunning = false;
        $this->lastUpdated = now()->format('H:i:s');
    }

    public function issueAllPending(): void
    {
        $domainsWithoutSsl = Domain::whereDoesntHave('sslCertificate')
            ->orWhereHas('sslCertificate', function ($q) {
                $q->where('status', 'failed');
            })
            ->with('user')
            ->get();

        $issued = 0;
        $failed = 0;

        foreach ($domainsWithoutSsl as $domain) {
            try {
                $result = $this->getAgent()->sslIssue(
                    $domain->domain,
                    $domain->user->username,
                    $domain->user->email,
                    true
                );

                if ($result['success'] ?? false) {
                    SslCertificate::updateOrCreate(
                        ['domain_id' => $domain->id, 'service' => 'web'],
                        [
                            'type' => 'lets_encrypt',
                            'status' => 'active',
                            'issuer' => "Let's Encrypt",
                            'certificate' => $result['certificate'] ?? null,
                            'issued_at' => now(),
                            'expires_at' => isset($result['valid_to']) ? \Carbon\Carbon::parse($result['valid_to']) : now()->addMonths(3),
                            'last_check_at' => now(),
                            'last_error' => null,
                            'renewal_attempts' => 0,
                            'auto_renew' => true,
                        ]
                    );
                    $domain->update(['ssl_enabled' => true]);
                    $issued++;
                } else {
                    SslCertificate::updateOrCreate(
                        ['domain_id' => $domain->id, 'service' => 'web'],
                        [
                            'type' => 'lets_encrypt',
                            'status' => 'failed',
                            'last_check_at' => now(),
                            'last_error' => $result['error'] ?? __('Unknown error'),
                        ]
                    );
                    $failed++;
                }
            } catch (Exception $e) {
                $failed++;
            }
        }

        // Also issue mail certificates for domains that don't have one
        $domainsWithoutMailSsl = Domain::whereDoesntHave('mailSslCertificate')
            ->with('user')
            ->get();

        foreach ($domainsWithoutMailSsl as $domain) {
            try {
                $mailHostname = 'mail.' . $domain->domain;
                $result = $this->getAgent()->sslMailIssue($mailHostname, $domain->user->email);

                if ($result['success'] ?? false) {
                    SslCertificate::updateOrCreate(
                        ['domain_id' => $domain->id, 'service' => 'mail'],
                        [
                            'type' => 'lets_encrypt',
                            'status' => 'active',
                            'issuer' => "Let's Encrypt",
                            'issued_at' => now(),
                            'expires_at' => isset($result['valid_to']) ? \Carbon\Carbon::parse($result['valid_to']) : now()->addMonths(3),
                            'last_check_at' => now(),
                            'last_error' => null,
                            'renewal_attempts' => 0,
                            'auto_renew' => true,
                        ]
                    );
                    $issued++;
                } else {
                    $failed++;
                }
            } catch (Exception $e) {
                $failed++;
            }
        }

        Notification::make()
            ->title(__('Bulk SSL Issuance Complete'))
            ->body(__('Issued: :issued, Failed: :failed', ['issued' => $issued, 'failed' => $failed]))
            ->success()
            ->send();

        $this->lastUpdated = now()->format('H:i:s');
    }

    public function getLetsEncryptLog(): string
    {
        $logFiles = [
            '/var/log/letsencrypt/letsencrypt.log',
            '/var/log/certbot/letsencrypt.log',
            '/var/log/certbot.log',
        ];

        $logContent = '';
        $foundFile = null;

        foreach ($logFiles as $logFile) {
            if (file_exists($logFile)) {
                $foundFile = $logFile;
                $lines = file($logFile);
                $lastLines = array_slice($lines, -500);
                $logContent .= "=== {$logFile} ===\n".implode('', $lastLines);
                break;
            }
        }

        if (! $foundFile) {
            $certbotLogs = glob('/var/log/letsencrypt/*.log');
            if (! empty($certbotLogs)) {
                $foundFile = end($certbotLogs);
                $lines = file($foundFile);
                $lastLines = array_slice($lines, -500);
                $logContent = "=== {$foundFile} ===\n".implode('', $lastLines);
            }
        }

        if (! $foundFile) {
            return __("No Let's Encrypt/Certbot log files found.")."\n\n".__('Searched locations:')."\n".implode("\n", $logFiles)."\n/var/log/letsencrypt/*.log";
        }

        return $logContent;
    }

    protected function getHeaderActions(): array
    {
        return [
            Action::make('runAutoSsl')
                ->label(__('Run SSL Check'))
                ->icon('heroicon-o-play')
                ->color('success')
                ->modalHeading(__('Run SSL Check'))
                ->modalDescription(__('Check SSL certificates and automatically issue/renew them.'))
                ->modalWidth('md')
                ->form([
                    Select::make('scope')
                        ->label(__('Scope'))
                        ->options([
                            'all' => __('All Domains'),
                            'user' => __('Specific User'),
                            'domain' => __('Specific Domain'),
                        ])
                        ->default('all')
                        ->live()
                        ->required(),
                    Select::make('user_id')
                        ->label(__('User'))
                        ->options(fn () => User::pluck('username', 'id')->toArray())
                        ->searchable()
                        ->visible(fn ($get) => $get('scope') === 'user')
                        ->required(fn ($get) => $get('scope') === 'user'),
                    Select::make('domain')
                        ->label(__('Domain'))
                        ->options(fn () => Domain::pluck('domain', 'domain')->toArray())
                        ->searchable()
                        ->visible(fn ($get) => $get('scope') === 'domain')
                        ->required(fn ($get) => $get('scope') === 'domain'),
                ])
                ->action(function (array $data): void {
                    match ($data['scope']) {
                        'user' => $this->runSslCheckForUser((int) $data['user_id']),
                        'domain' => $this->runAutoSsl($data['domain']),
                        default => $this->runAutoSsl(),
                    };
                }),
            Action::make('issueAllPending')
                ->label(__('Issue All Pending'))
                ->icon('heroicon-o-shield-check')
                ->color('primary')
                ->requiresConfirmation()
                ->modalHeading(__('Issue SSL for All Pending Domains'))
                ->modalDescription(__('This will attempt to issue SSL certificates for all domains without active certificates. This may take a while.'))
                ->action(fn () => $this->issueAllPending()),
            Action::make('viewLog')
                ->label(__('View Log'))
                ->icon('heroicon-o-document-text')
                ->color('gray')
                ->modalHeading(__("Let's Encrypt Log"))
                ->modalWidth('4xl')
                ->modalContent(fn () => view('filament.admin.pages.ssl-log-modal', ['log' => $this->getLetsEncryptLog()]))
                ->modalSubmitAction(false)
                ->modalCancelActionLabel(__('Close')),
        ];
    }
}
