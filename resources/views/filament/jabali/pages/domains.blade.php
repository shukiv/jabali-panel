<x-filament-panels::page>
    {{-- Info Banner --}}
    <x-filament::section
        icon="heroicon-o-information-circle"
        icon-color="info"
    >
        <x-slot name="heading">
            {{ __('Domain Management') }}
        </x-slot>
        <x-slot name="description">
            {{ __('Add and manage domains for your hosting account. Each domain gets its own document root folder where you can upload your website files. You can enable or disable domains and manage SSL certificates from this page.') }}
        </x-slot>
    </x-filament::section>

    {{ $this->table }}

    <x-filament-actions::modals />
</x-filament-panels::page>
