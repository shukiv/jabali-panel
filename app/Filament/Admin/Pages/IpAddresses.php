<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Models\DnsSetting;
use App\Services\Agent\AgentClient;
use App\Support\SafeError;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;

class IpAddresses extends Page implements HasActions, HasTable
{
    use InteractsWithActions;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-signal';

    protected static ?int $navigationSort = 9;

    protected string $view = 'filament.admin.pages.ip-addresses';

    public array $addresses = [];

    public array $interfaces = [];

    public ?string $defaultIp = null;

    public ?string $defaultIpv6 = null;

    public static function getNavigationLabel(): string
    {
        return __('IP Addresses');
    }

    public function getTitle(): string|Htmlable
    {
        return __('IP Address Manager');
    }

    public function mount(): void
    {
        $this->loadAddresses();
    }

    protected function getAgent(): AgentClient
    {
        return app(AgentClient::class);
    }

    protected function loadAddresses(): void
    {
        try {
            $result = $this->getAgent()->ipList();
            $this->addresses = $result['addresses'] ?? [];
            $this->interfaces = $result['interfaces'] ?? [];
        } catch (Exception $e) {
            $this->addresses = [];
            $this->interfaces = [];
        }

        $settings = DnsSetting::getAll();
        $this->defaultIp = $settings['default_ip'] ?? null;
        $this->defaultIpv6 = $settings['default_ipv6'] ?? null;

        $this->flushCachedTableRecords();
    }

    protected function getHeaderActions(): array
    {
        return [
            Action::make('refresh')
                ->label(__('Refresh'))
                ->icon('heroicon-o-arrow-path')
                ->color('gray')
                ->action(fn () => $this->loadAddresses()),
            Action::make('addIp')
                ->label(__('Add IP'))
                ->icon('heroicon-o-plus-circle')
                ->color('primary')
                ->modalHeading(__('Add IP Address'))
                ->modalDescription(__('Assign a new IPv4 or IPv6 address to a network interface.'))
                ->modalSubmitActionLabel(__('Add IP'))
                ->form([
                    TextInput::make('ip')
                        ->label(__('IP Address'))
                        ->placeholder(__('203.0.113.10'))
                        ->live()
                        ->afterStateUpdated(function (?string $state, callable $set): void {
                            if (! $state) {
                                return;
                            }

                            if (str_contains($state, ':')) {
                                $set('cidr', 64);

                                return;
                            }

                            $set('cidr', 24);
                        })
                        ->rule('ip')
                        ->required(),
                    TextInput::make('cidr')
                        ->label(__('CIDR'))
                        ->numeric()
                        ->minValue(1)
                        ->maxValue(128)
                        ->default(24)
                        ->required(),
                    Select::make('interface')
                        ->label(__('Interface'))
                        ->options(fn () => $this->getInterfaceOptions())
                        ->searchable()
                        ->required(),
                ])
                ->action(function (array $data): void {
                    try {
                        $result = $this->getAgent()->ipAdd($data['ip'], (int) $data['cidr'], $data['interface']);

                        if ($result['success'] ?? false) {
                            $message = $result['message'] ?? __('IP added successfully');
                            if (! ($result['persistent'] ?? true)) {
                                $message .= ' '.__('(Persistence not configured for this system)');
                            }

                            Notification::make()
                                ->title(__('IP address added'))
                                ->body($message)
                                ->success()
                                ->send();
                            $this->dispatch('notificationsSent');

                            $this->loadAddresses();
                            $this->dispatch('ip-defaults-updated');
                        } else {
                            throw new Exception($result['error'] ?? __('Failed to add IP address'));
                        }
                    } catch (Exception $e) {
                        Notification::make()
                            ->title(__('Failed to add IP'))
                            ->body(SafeError::message($e))
                            ->danger()
                            ->send();
                        $this->dispatch('notificationsSent');
                    }
                }),
        ];
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->addresses)
            ->columns([
                TextColumn::make('ip')
                    ->label(__('IP Address'))
                    ->fontFamily('mono')
                    ->copyable()
                    ->searchable(),
                TextColumn::make('version')
                    ->label(__('Version'))
                    ->badge()
                    ->formatStateUsing(fn ($state): string => (int) $state === 6 ? 'IPv6' : 'IPv4')
                    ->color(fn ($state): string => (int) $state === 6 ? 'primary' : 'info'),
                TextColumn::make('interface')
                    ->label(__('Interface'))
                    ->badge()
                    ->color('gray'),
                TextColumn::make('cidr')
                    ->label(__('CIDR'))
                    ->alignCenter(),
                TextColumn::make('scope')
                    ->label(__('Scope'))
                    ->badge()
                    ->color(fn (?string $state): string => $state === 'global' ? 'success' : 'gray')
                    ->formatStateUsing(fn (?string $state): string => $state ? ucfirst($state) : '-'),
                IconColumn::make('is_primary')
                    ->label(__('Primary'))
                    ->boolean(),
                TextColumn::make('default')
                    ->label(__('Default'))
                    ->getStateUsing(fn (array $record): ?string => $this->getDefaultLabel($record))
                    ->badge()
                    ->color('success')
                    ->placeholder(__('-')),
            ])
            ->recordActions([
                Action::make('setDefault')
                    ->label(fn (array $record): string => ($record['version'] ?? 4) === 6 ? __('Set Default IPv6') : __('Set Default IPv4'))
                    ->icon('heroicon-o-star')
                    ->color('primary')
                    ->visible(fn (array $record): bool => ! $this->isDefaultIp($record))
                    ->action(fn (array $record) => $this->setDefaultIp($record)),
                Action::make('remove')
                    ->label(__('Remove'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->visible(fn (array $record): bool => ! $this->isDefaultIp($record))
                    ->requiresConfirmation()
                    ->modalHeading(__('Remove IP Address'))
                    ->modalDescription(fn (array $record): string => __('Remove :ip from :iface?', ['ip' => $record['ip'] ?? '-', 'iface' => $record['interface'] ?? '-']))
                    ->modalSubmitActionLabel(__('Remove IP'))
                    ->action(fn (array $record) => $this->removeIp($record)),
            ])
            ->striped()
            ->paginated(false)
            ->emptyStateHeading(__('No IP addresses found'))
            ->emptyStateDescription(__('Add an IP address to begin managing assignments.'))
            ->emptyStateIcon('heroicon-o-signal');
    }

    protected function getInterfaceOptions(): array
    {
        if (empty($this->interfaces)) {
            $this->loadAddresses();
        }

        $options = [];
        foreach ($this->interfaces as $interface) {
            $options[$interface] = $interface;
        }

        return $options;
    }

    protected function getDefaultLabel(array $record): ?string
    {
        $ip = $record['ip'] ?? null;
        if (! $ip) {
            return null;
        }

        if ($this->defaultIp === $ip) {
            return __('Default IPv4');
        }

        if ($this->defaultIpv6 === $ip) {
            return __('Default IPv6');
        }

        return null;
    }

    protected function isDefaultIp(array $record): bool
    {
        $ip = $record['ip'] ?? null;
        if (! $ip) {
            return false;
        }

        return $ip === $this->defaultIp || $ip === $this->defaultIpv6;
    }

    protected function setDefaultIp(array $record): void
    {
        $ip = $record['ip'] ?? null;
        $version = (int) ($record['version'] ?? 4);

        if (! $ip) {
            return;
        }

        if ($version === 6) {
            DnsSetting::set('default_ipv6', $ip);
            $this->defaultIpv6 = $ip;
        } else {
            DnsSetting::set('default_ip', $ip);
            $this->defaultIp = $ip;
        }

        DnsSetting::clearCache();

        Notification::make()
            ->title(__('Default IP updated'))
            ->success()
            ->send();

        $this->dispatch('ip-defaults-updated');
    }

    protected function removeIp(array $record): void
    {
        $ip = $record['ip'] ?? null;
        $cidr = $record['cidr'] ?? null;
        $interface = $record['interface'] ?? null;

        if (! $ip || ! $cidr || ! $interface) {
            return;
        }

        try {
            $result = $this->getAgent()->ipRemove($ip, (int) $cidr, $interface);
            if (! ($result['success'] ?? false)) {
                throw new Exception($result['error'] ?? __('Failed to remove IP address'));
            }

            Notification::make()
                ->title(__('IP address removed'))
                ->success()
                ->send();
            $this->dispatch('notificationsSent');

            $this->loadAddresses();
            $this->dispatch('ip-defaults-updated');
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Failed to remove IP'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
            $this->dispatch('notificationsSent');
        }
    }
}
