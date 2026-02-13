<x-filament-panels::page>
    @livewire(\App\Filament\Jabali\Widgets\EmailStatsWidget::class)

    {{ $this->emailForm }}

    <x-filament-actions::modals />
</x-filament-panels::page>
