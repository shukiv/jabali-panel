<x-filament-panels::page>
    {{-- Warning Banner --}}
    <x-filament::section
        icon="heroicon-o-exclamation-triangle"
        icon-color="warning"
    >
        <x-slot name="heading">
            {{ __('Caution') }}
        </x-slot>
        <x-slot name="description">
            {{ __('Changing PHP settings can affect your website performance and functionality. Incorrect values may cause errors or security vulnerabilities. Only modify settings if you understand their impact.') }}
        </x-slot>
    </x-filament::section>

    @if(count($domains) > 0)
        {{-- Domain Selector --}}
        <x-filament::section
            icon="heroicon-o-globe-alt"
            icon-color="primary"
        >
            <x-slot name="heading">
                {{ __('Select Domain') }}
            </x-slot>
            <x-slot name="description">
                {{ __('Choose the domain you want to configure PHP settings for.') }}
            </x-slot>

            <div class="max-w-md">
                <x-filament::input.wrapper>
                    <x-filament::input.select wire:model.live="selectedDomain" wire:change="selectDomain($event.target.value)">
                        @foreach($domains as $domain)
                            <option value="{{ $domain['domain'] }}">{{ $domain['domain'] }}</option>
                        @endforeach
                    </x-filament::input.select>
                </x-filament::input.wrapper>
            </div>
        </x-filament::section>

        @if($selectedDomain)
            {{-- PHP Settings Form --}}
            {{ $this->form }}
        @endif
    @else
        {{-- No Domains Empty State --}}
        <x-filament::section>
            <div class="flex flex-col items-center justify-center py-12">
                <div class="mb-4 rounded-full bg-gray-100 p-3 dark:bg-gray-500/20">
                    <x-filament::icon
                        icon="heroicon-o-globe-alt"
                        :size="\Filament\Support\Enums\IconSize::Large"
                        class="fi-color-gray fi-text-color-500"
                    />
                </div>
                <h3 class="fi-section-header-heading">
                    {{ __('No Domains Yet') }}
                </h3>
                <p class="mt-1 fi-section-header-description">
                    {{ __('Add a domain first to configure PHP settings.') }}
                </p>
                <div class="mt-6">
                    <x-filament::button
                        href="{{ route('filament.jabali.pages.domains') }}"
                        tag="a"
                    >
                        {{ __('Add Domain') }}
                    </x-filament::button>
                </div>
            </div>
        </x-filament::section>
    @endif

    <x-filament-actions::modals />
</x-filament-panels::page>
