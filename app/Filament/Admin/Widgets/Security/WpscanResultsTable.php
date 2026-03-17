<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets\Security;

use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Livewire\Component;

class WpscanResultsTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public array $results = [];

    public function makeFilamentTranslatableContentDriver(): ?\Filament\Support\Contracts\TranslatableContentDriver
    {
        return null;
    }

    protected function getRecords(): array
    {
        $vulnerabilities = $this->results['vulnerabilities'] ?? [];

        return array_map(fn ($vuln, $index) => [
            'id' => $index,
            'title' => is_array($vuln) ? ($vuln['title'] ?? 'Unknown') : $vuln,
            'severity' => is_array($vuln) ? ($vuln['severity'] ?? 'unknown') : 'unknown',
        ], $vulnerabilities, array_keys($vulnerabilities));
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->getRecords())
            ->columns([
                TextColumn::make('title')
                    ->label(__('Vulnerability'))
                    ->icon('heroicon-o-exclamation-triangle')
                    ->iconColor('danger')
                    ->wrap()
                    ->searchable(),
                TextColumn::make('severity')
                    ->label(__('Severity'))
                    ->badge()
                    ->color(fn (string $state): string => match (strtolower($state)) {
                        'critical' => 'danger',
                        'high' => 'danger',
                        'medium' => 'warning',
                        'low' => 'info',
                        default => 'gray',
                    }),
            ])
            ->striped()
            ->emptyStateHeading(__('No vulnerabilities found'))
            ->emptyStateDescription(__('The WordPress site appears to be secure.'))
            ->emptyStateIcon('heroicon-o-check-circle');
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
