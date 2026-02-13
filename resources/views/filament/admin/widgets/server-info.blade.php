<x-filament-widgets::widget>
    <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
        {{-- Services --}}
        <x-filament::section icon="heroicon-o-server-stack" icon-color="primary">
            <x-slot name="heading">{{ __('Services') }}</x-slot>
            <livewire:app.filament.admin.widgets.services-table-widget />
        </x-filament::section>

        {{-- System Information --}}
        <x-filament::section icon="heroicon-o-information-circle" icon-color="info">
            <x-slot name="heading">{{ __('System Information') }}</x-slot>
            <livewire:app.filament.admin.widgets.system-info-table-widget />
        </x-filament::section>
    </div>

    {{-- Network --}}
    <div class="mt-6">
        <x-filament::section icon="heroicon-o-signal" icon-color="success">
            <x-slot name="heading">{{ __('Network Interfaces') }}</x-slot>
            <livewire:app.filament.admin.widgets.network-table-widget />
        </x-filament::section>
    </div>
</x-filament-widgets::widget>
