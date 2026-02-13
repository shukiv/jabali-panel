@livewire(\App\Filament\Admin\Widgets\WhmAccountsTable::class, [
    'accounts' => $this->accounts,
    'selectedAccounts' => $this->selectedAccounts,
], key('whm-accounts-table-' . count($this->accounts)))
