@livewire(\App\Filament\Admin\Widgets\WhmAccountConfigTable::class, [
    'accounts' => $this->accounts,
    'selectedAccounts' => $this->selectedAccounts,
    'accountConfig' => $this->accountConfig,
], key('whm-account-config-table-' . count($this->selectedAccounts)))
