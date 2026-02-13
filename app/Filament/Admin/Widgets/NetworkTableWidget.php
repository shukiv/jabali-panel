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

class NetworkTableWidget extends Component implements HasTable, HasSchemas, HasActions
{
    use InteractsWithTable;
    use InteractsWithSchemas;
    use InteractsWithActions;

    public array $interfaces = [];

    public function mount(): void
    {
        $this->loadNetwork();
    }

    protected function loadNetwork(): void
    {
        try {
            $agent = new AgentClient();
            $network = $agent->metricsNetwork()['data'] ?? [];

            $interfaces = [];
            foreach (($network['interfaces'] ?? []) as $name => $data) {
                $interfaces[] = [
                    'name' => $name,
                    'ip' => $data['ip'] ?? '-',
                    'rx' => $data['rx_human'] ?? '0',
                    'tx' => $data['tx_human'] ?? '0',
                ];
            }
            $this->interfaces = $interfaces;
        } catch (\Exception $e) {
            $this->interfaces = [];
        }
    }

    public function makeFilamentTranslatableContentDriver(): ?\Filament\Support\Contracts\TranslatableContentDriver
    {
        return null;
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->interfaces)
            ->columns([
                TextColumn::make('name')
                    ->label(__('Interface'))
                    ->weight('medium'),
                TextColumn::make('ip')
                    ->label(__('IP Address'))
                    ->fontFamily('mono')
                    ->color('gray'),
                TextColumn::make('rx')
                    ->label(__('Download'))
                    ->icon('heroicon-o-arrow-down')
                    ->badge()
                    ->color('success'),
                TextColumn::make('tx')
                    ->label(__('Upload'))
                    ->icon('heroicon-o-arrow-up')
                    ->badge()
                    ->color('info'),
            ])
            ->paginated(false)
            ->striped();
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
