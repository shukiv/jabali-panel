<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Models\User;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Support\Contracts\TranslatableContentDriver;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Livewire\Attributes\On;
use Livewire\Component;

class WhmAccountConfigTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public array $accounts = [];

    public array $selectedAccounts = [];

    public array $accountConfig = [];

    public function mount(array $accounts = [], array $selectedAccounts = [], array $accountConfig = []): void
    {
        // Use mount parameters if provided, otherwise load from session
        $this->accounts = ! empty($accounts) ? $accounts : session('whm_migration.accounts', []);
        $this->selectedAccounts = ! empty($selectedAccounts) ? $selectedAccounts : session('whm_migration.selectedAccounts', []);
        $this->accountConfig = ! empty($accountConfig) ? $accountConfig : session('whm_migration.accountConfig', []);
    }

    #[On('whm-config-updated')]
    public function refreshConfig(): void
    {
        // Reload from session since parent may have updated
        $this->accounts = session('whm_migration.accounts', []);
        $this->selectedAccounts = session('whm_migration.selectedAccounts', []);
        $this->accountConfig = session('whm_migration.accountConfig', []);
        $this->resetTable();
    }

    public function makeFilamentTranslatableContentDriver(): ?TranslatableContentDriver
    {
        return null;
    }

    protected function getRecords(): array
    {
        $records = [];

        foreach ($this->selectedAccounts as $cpanelUser) {
            $account = collect($this->accounts)->firstWhere('user', $cpanelUser);
            $config = $this->accountConfig[$cpanelUser] ?? [];

            $domain = $account['domain'] ?? '';
            $email = $config['email'] ?? $account['email'] ?? "{$cpanelUser}@{$domain}";

            $existingUser = User::where('username', $cpanelUser)->first();

            $records[] = [
                'user' => $cpanelUser,
                'domain' => $domain,
                'email' => $email,
                'exists' => $existingUser !== null,
                'diskused' => $account['diskused'] ?? '',
            ];
        }

        return $records;
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->getRecords())
            ->columns([
                IconColumn::make('exists')
                    ->label(__('Status'))
                    ->boolean()
                    ->trueIcon('heroicon-o-exclamation-triangle')
                    ->falseIcon('heroicon-o-user-plus')
                    ->trueColor('warning')
                    ->falseColor('success')
                    ->tooltip(fn (array $record): string => $record['exists']
                        ? __('User exists - will restore to existing account')
                        : __('New user will be created')),
                TextColumn::make('user')
                    ->label(__('Username'))
                    ->weight('bold'),
                TextColumn::make('domain')
                    ->label(__('Domain')),
                TextColumn::make('email')
                    ->label(__('Email'))
                    ->icon('heroicon-o-envelope'),
                TextColumn::make('diskused')
                    ->label(__('Size')),
            ])
            ->striped()
            ->paginated([10, 25, 50])
            ->defaultPaginationPageOption(10)
            ->emptyStateHeading(__('No accounts selected'))
            ->emptyStateDescription(__('Go back to Step 2 and select accounts to migrate'))
            ->emptyStateIcon('heroicon-o-user-group');
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
