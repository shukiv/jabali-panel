<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Models\DnsRecord;
use App\Models\DnsSetting;
use App\Models\Domain;
use App\Services\Agent\AgentClient;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Notifications\Notification;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Support\Contracts\TranslatableContentDriver;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Livewire\Attributes\On;
use Livewire\Component;

class DomainIpAssignmentsTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public ?string $defaultIp = null;

    public ?string $defaultIpv6 = null;

    protected ?AgentClient $agent = null;

    public function makeFilamentTranslatableContentDriver(): ?TranslatableContentDriver
    {
        return null;
    }

    public function mount(): void
    {
        $this->loadDefaults();
    }

    #[On('ip-defaults-updated')]
    public function refreshDefaults(): void
    {
        $this->loadDefaults();
        $this->resetTable();
    }

    protected function loadDefaults(): void
    {
        $settings = DnsSetting::getAll();
        $this->defaultIp = $settings['default_ip'] ?? null;
        $this->defaultIpv6 = $settings['default_ipv6'] ?? null;
    }

    protected function getAgent(): AgentClient
    {
        return $this->agent ??= new AgentClient;
    }

    public function table(Table $table): Table
    {
        return $table
            ->query(Domain::query()->with('user')->orderBy('domain'))
            ->columns([
                TextColumn::make('domain')
                    ->label(__('Domain'))
                    ->searchable()
                    ->sortable()
                    ->description(fn (Domain $record) => $record->user?->username ?? __('Unknown')),
                TextColumn::make('ip_address')
                    ->label(__('IPv4'))
                    ->badge()
                    ->color(fn (Domain $record): string => $record->ip_address ? 'primary' : 'gray')
                    ->getStateUsing(fn (Domain $record): string => $this->formatIpDisplay($record->ip_address, $this->defaultIp))
                    ->description(fn (Domain $record): string => $this->formatIpDescription($record->ip_address, $this->defaultIp)),
                TextColumn::make('ipv6_address')
                    ->label(__('IPv6'))
                    ->badge()
                    ->color(fn (Domain $record): string => $record->ipv6_address ? 'primary' : 'gray')
                    ->getStateUsing(fn (Domain $record): string => $this->formatIpDisplay($record->ipv6_address, $this->defaultIpv6))
                    ->description(fn (Domain $record): string => $this->formatIpDescription($record->ipv6_address, $this->defaultIpv6)),
            ])
            ->recordActions([
                Action::make('assign')
                    ->label(__('Assign IPs'))
                    ->icon('heroicon-o-adjustments-horizontal')
                    ->color('primary')
                    ->modalHeading(fn (Domain $record): string => __('Assign IPs for :domain', ['domain' => $record->domain]))
                    ->modalDescription(__('Select the IPv4 and IPv6 addresses to use for this domain.'))
                    ->modalSubmitActionLabel(__('Save Assignments'))
                    ->form([
                        Select::make('ip_address')
                            ->label(__('IPv4 Address'))
                            ->options(fn () => $this->getIpv4Options())
                            ->placeholder(__('Use default IPv4'))
                            ->searchable()
                            ->nullable(),
                        Select::make('ipv6_address')
                            ->label(__('IPv6 Address'))
                            ->options(fn () => $this->getIpv6Options())
                            ->placeholder(__('Use default IPv6'))
                            ->searchable()
                            ->nullable(),
                    ])
                    ->fillForm(fn (Domain $record): array => [
                        'ip_address' => $record->ip_address,
                        'ipv6_address' => $record->ipv6_address,
                    ])
                    ->action(fn (Domain $record, array $data) => $this->assignIps($record, $data)),
            ])
            ->striped()
            ->emptyStateHeading(__('No domains found'))
            ->emptyStateDescription(__('Create a domain to assign IPs.'))
            ->emptyStateIcon('heroicon-o-globe-alt');
    }

    protected function formatIpDisplay(?string $assigned, ?string $default): string
    {
        if (! empty($assigned)) {
            return $assigned;
        }

        return $default ?: '-';
    }

    protected function formatIpDescription(?string $assigned, ?string $default): string
    {
        if (! empty($assigned)) {
            return __('Assigned');
        }

        return $default ? __('Default') : __('Not set');
    }

    /**
     * @return array<string, string>
     */
    protected function getIpv4Options(): array
    {
        return $this->getIpOptionsByVersion(4);
    }

    /**
     * @return array<string, string>
     */
    protected function getIpv6Options(): array
    {
        return $this->getIpOptionsByVersion(6);
    }

    /**
     * @return array<string, string>
     */
    protected function getIpOptionsByVersion(int $version): array
    {
        try {
            $result = $this->getAgent()->ipList();
        } catch (Exception) {
            return [];
        }

        $options = [];
        foreach (($result['addresses'] ?? []) as $address) {
            $addressVersion = (int) ($address['version'] ?? 4);
            if ($addressVersion !== $version) {
                continue;
            }

            $ip = $address['ip'] ?? null;
            if (! $ip) {
                continue;
            }

            $options[$ip] = $this->formatIpOptionLabel($address);
        }

        return $options;
    }

    protected function formatIpOptionLabel(array $address): string
    {
        $ip = $address['ip'] ?? '';
        $cidr = $address['cidr'] ?? null;
        $interface = $address['interface'] ?? null;
        $scope = $address['scope'] ?? null;

        $label = $ip;
        if ($cidr) {
            $label .= '/'.$cidr;
        }
        if ($interface) {
            $label .= ' • '.$interface;
        }
        if ($scope) {
            $label .= ' • '.$scope;
        }

        return $label;
    }

    protected function assignIps(Domain $record, array $data): void
    {
        $settings = DnsSetting::getAll();
        $defaultIp = $settings['default_ip'] ?? null;
        $defaultIpv6 = $settings['default_ipv6'] ?? null;

        $previousIpv4 = $record->ip_address ?: $defaultIp;
        $previousIpv6 = $record->ipv6_address ?: $defaultIpv6;

        $record->update([
            'ip_address' => $data['ip_address'] ?: null,
            'ipv6_address' => $data['ipv6_address'] ?: null,
        ]);

        $newIpv4 = $record->ip_address ?: $defaultIp;
        $newIpv6 = $record->ipv6_address ?: $defaultIpv6;
        $ttl = (int) ($settings['default_ttl'] ?? 3600);

        $this->updateDefaultDnsRecords($record, $previousIpv4, $newIpv4, $previousIpv6, $newIpv6, $ttl);
        $this->syncDnsZone($record);

        Notification::make()
            ->title(__('IP assignments updated'))
            ->success()
            ->send();
        $this->dispatch('notificationsSent');

        $this->resetTable();
    }

    protected function updateDefaultDnsRecords(Domain $domain, ?string $previousIpv4, ?string $newIpv4, ?string $previousIpv6, ?string $newIpv6, int $ttl): void
    {
        foreach (['@', 'www', 'mail'] as $name) {
            $this->updateDnsRecord($domain, $name, 'A', $previousIpv4, $newIpv4, $ttl);
            $this->updateDnsRecord($domain, $name, 'AAAA', $previousIpv6, $newIpv6, $ttl);
        }
    }

    protected function updateDnsRecord(Domain $domain, string $name, string $type, ?string $previousContent, ?string $newContent, int $ttl): void
    {
        $query = DnsRecord::query()
            ->where('domain_id', $domain->id)
            ->where('name', $name)
            ->where('type', $type);

        if (empty($newContent)) {
            if (! empty($previousContent)) {
                $query->where('content', $previousContent)->delete();
            }

            return;
        }

        if (! empty($previousContent)) {
            $updated = (clone $query)
                ->where('content', $previousContent)
                ->update(['content' => $newContent, 'ttl' => $ttl]);

            if ($updated > 0) {
                return;
            }
        }

        $exists = (clone $query)
            ->where('content', $newContent)
            ->exists();

        if (! $exists) {
            DnsRecord::create([
                'domain_id' => $domain->id,
                'name' => $name,
                'type' => $type,
                'content' => $newContent,
                'ttl' => $ttl,
                'priority' => null,
            ]);
        }
    }

    protected function syncDnsZone(Domain $domain): void
    {
        $settings = DnsSetting::getAll();
        $hostname = gethostname() ?: 'localhost';
        $serverIp = trim(shell_exec("hostname -I | awk '{print $1}'") ?? '') ?: '127.0.0.1';
        $serverIpv6 = $settings['default_ipv6'] ?? null;

        try {
            $records = $domain->dnsRecords()->get()->toArray();
            $this->getAgent()->send('dns.sync_zone', [
                'domain' => $domain->domain,
                'records' => $records,
                'ns1' => $settings['ns1'] ?? "ns1.{$hostname}",
                'ns2' => $settings['ns2'] ?? "ns2.{$hostname}",
                'admin_email' => $settings['admin_email'] ?? "admin.{$hostname}",
                'default_ip' => $settings['default_ip'] ?? $serverIp,
                'default_ipv6' => $serverIpv6,
                'default_ttl' => (int) ($settings['default_ttl'] ?? 3600),
            ]);
        } catch (Exception $e) {
            Notification::make()
                ->title(__('DNS sync failed'))
                ->body($e->getMessage())
                ->warning()
                ->send();
            $this->dispatch('notificationsSent');
        }
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
