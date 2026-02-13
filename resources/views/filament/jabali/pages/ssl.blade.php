<x-filament-panels::page>
    {{-- Info Banner --}}
    <x-filament::section
        icon="heroicon-o-information-circle"
        icon-color="info"
    >
        <x-slot name="heading">
            {{ __('Free SSL Certificates') }}
        </x-slot>
        <x-slot name="description">
            {{ __("Let's Encrypt certificates are free and automatically issued for your domains.") }}
            {{ __('Certificates are valid for 90 days and will be automatically renewed when auto-renew is enabled.') }}
            {{ __('You can also install your own custom certificates if you have purchased one from a certificate authority.') }}
            <strong class="fi-color-warning fi-text-color-600 dark:fi-text-color-400">{{ __('Note: It may take 5-20 minutes for a new SSL certificate to be fully visible in your browser.') }}</strong>
        </x-slot>
    </x-filament::section>

    {{ $this->table }}

    <x-filament-actions::modals />
</x-filament-panels::page>
