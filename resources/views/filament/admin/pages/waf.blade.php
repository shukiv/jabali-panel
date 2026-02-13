<x-filament-panels::page>
    @if(! $wafInstalled)
        <x-filament::section icon="heroicon-o-exclamation-triangle" icon-color="warning">
            <x-slot name="heading">{{ __('ModSecurity not detected') }}</x-slot>
            <x-slot name="description">
                {{ __('Install ModSecurity on the server to enable WAF controls. Settings can be saved now and applied later.') }}
            </x-slot>
        </x-filament::section>
    @endif

    <div class="mt-6">
        {{ $this->wafForm }}
        <div class="mt-4">
            <x-filament::button wire:click="saveWafSettings" icon="heroicon-o-check">
                {{ __('Save WAF Settings') }}
            </x-filament::button>
        </div>
    </div>

    <div class="mt-8">
        <x-filament::section icon="heroicon-o-list-bullet" icon-color="primary">
            <x-slot name="heading">{{ __('Whitelist Rules') }}</x-slot>
            <x-slot name="description">
                {{ __('Exclude trusted traffic from specific ModSecurity rule IDs.') }}
            </x-slot>
            @livewire('admin.waf-whitelist-table')
        </x-filament::section>
    </div>

    <div class="mt-8">
        <x-filament::section icon="heroicon-o-document-text" icon-color="primary">
            <x-slot name="heading">{{ __('Blocked Requests') }}</x-slot>
            <x-slot name="description">
                {{ __('Recent ModSecurity denials from the audit log. Use whitelist to allow trusted traffic.') }}
            </x-slot>
            {{ $this->table }}
        </x-filament::section>
    </div>

    <x-filament-actions::modals />
</x-filament-panels::page>
