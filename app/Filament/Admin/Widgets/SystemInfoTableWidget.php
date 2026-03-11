<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Services\Agent\AgentClient;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Livewire\Component;

class SystemInfoTableWidget extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public array $info = [];

    public function mount(): void
    {
        $this->loadInfo();
    }

    protected function loadInfo(): void
    {
        try {
            $agent = new AgentClient;
            $overview = $agent->metricsOverview();
            $network = $agent->metricsNetwork()['data'] ?? [];

            // Get primary interface IP
            $primaryIp = '127.0.0.1';
            $interfaces = $network['interfaces'] ?? [];
            foreach ($interfaces as $name => $iface) {
                if ($name !== 'lo' && ! empty($iface['ip'])) {
                    $primaryIp = explode('/', $iface['ip'])[0];
                    break;
                }
            }

            // Parse OS info
            $osInfo = $overview['os'] ?? [];
            $osName = is_array($osInfo) ? ($osInfo['name'] ?? 'Linux') : 'Linux';
            $osVersion = is_array($osInfo) ? ($osInfo['version'] ?? '') : '';

            // Format memory
            $ramTotal = $overview['memory']['total'] ?? 0;
            $ramUsed = $overview['memory']['used'] ?? 0;
            $ramPercent = round($overview['memory']['usage_percent'] ?? 0, 1);

            // Shorten CPU model
            $cpuModel = $overview['cpu']['model'] ?? 'Unknown';
            $cpuModel = str_replace(['(R)', '(TM)', 'CPU', '@'], '', $cpuModel);
            $cpuModel = preg_replace('/\s+/', ' ', trim($cpuModel));
            if (strlen($cpuModel) > 35) {
                $cpuModel = substr($cpuModel, 0, 32).'...';
            }

            $this->info = [
                [
                    'category' => __('Server'),
                    'property' => __('Hostname'),
                    'value' => $overview['hostname'] ?? gethostname(),
                ],
                [
                    'category' => __('Server'),
                    'property' => __('Uptime'),
                    'value' => $overview['uptime']['human'] ?? 'N/A',
                ],
                [
                    'category' => __('Server'),
                    'property' => __('OS'),
                    'value' => trim("$osName $osVersion") ?: 'Linux',
                ],
                [
                    'category' => __('Server'),
                    'property' => __('IP Address'),
                    'value' => $primaryIp,
                ],
                [
                    'category' => __('Server'),
                    'property' => __('Connections'),
                    'value' => number_format($network['connections'] ?? 0),
                ],
                [
                    'category' => __('Hardware'),
                    'property' => __('Processor'),
                    'value' => $cpuModel,
                ],
                [
                    'category' => __('Hardware'),
                    'property' => __('CPU Cores'),
                    'value' => (string) ($overview['cpu']['cores'] ?? 1),
                ],
                [
                    'category' => __('Hardware'),
                    'property' => __('Memory'),
                    'value' => $this->formatMb($ramUsed).' / '.$this->formatMb($ramTotal)." ({$ramPercent}%)",
                ],
                [
                    'category' => __('Software'),
                    'property' => 'PHP',
                    'value' => PHP_VERSION,
                ],
                [
                    'category' => __('Software'),
                    'property' => __('Database'),
                    'value' => $this->getDatabaseVersion(),
                ],
                [
                    'category' => __('Software'),
                    'property' => __('Web Server'),
                    'value' => $this->getWebserverVersion(),
                ],
                [
                    'category' => __('Software'),
                    'property' => 'Laravel',
                    'value' => app()->version(),
                ],
            ];
        } catch (\Exception $e) {
            $this->info = [];
        }
    }

    protected function formatMb(int $mb): string
    {
        if ($mb >= 1024) {
            return round($mb / 1024, 1).' GB';
        }

        return $mb.' MB';
    }

    protected function getDatabaseVersion(): string
    {
        // Try SQL query first
        try {
            $version = \DB::select('SELECT VERSION() as version')[0]->version ?? '';
            if (preg_match('/^(\d+\.\d+\.\d+)/', $version, $matches)) {
                $dbType = str_contains(strtolower($version), 'mariadb') ? 'MariaDB' : 'MySQL';

                return "$dbType {$matches[1]}";
            }

            if ($version) {
                return $version;
            }
        } catch (\Exception $e) {
            // Fall through
        }

        // Fallback: ask the agent (no local shell usage in the web request).
        try {
            $agent = new AgentClient;
            $result = $agent->serverVersions();

            $mysql = $result['versions']['mysql'] ?? null;
            if (is_array($mysql)) {
                $type = $mysql['type'] ?? null;
                $ver = $mysql['version'] ?? null;
                if (is_string($type) && $type !== '' && is_string($ver) && $ver !== '') {
                    return "{$type} {$ver}";
                }
            }
        } catch (\Throwable) {
            // Best-effort.
        }

        return 'N/A';
    }

    protected function getWebserverVersion(): string
    {
        try {
            $agent = new AgentClient;
            $result = $agent->serverVersions();

            $nginx = $result['versions']['nginx'] ?? null;
            if (is_string($nginx) && $nginx !== '') {
                return "Nginx {$nginx}";
            }
        } catch (\Throwable) {
            // Best-effort.
        }

        return 'Nginx';
    }

    public function makeFilamentTranslatableContentDriver(): ?\Filament\Support\Contracts\TranslatableContentDriver
    {
        return null;
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->info)
            ->columns([
                TextColumn::make('category')
                    ->label(__('Category'))
                    ->badge()
                    ->color(fn (string $state): string => match ($state) {
                        __('Server') => 'primary',
                        __('Hardware') => 'warning',
                        __('Software') => 'success',
                        default => 'gray',
                    }),
                TextColumn::make('property')
                    ->label(__('Property'))
                    ->weight('medium'),
                TextColumn::make('value')
                    ->label(__('Value'))
                    ->fontFamily('mono')
                    ->color('gray'),
            ])
            ->paginated(false)
            ->striped();
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
