<x-filament-panels::page>
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
                {{ __('Choose the domain you want to manage protected directories for.') }}
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
            {{-- Info Section --}}
            <x-filament::section
                icon="heroicon-o-information-circle"
                icon-color="info"
            >
                <x-slot name="heading">
                    {{ __('About Protected Directories') }}
                </x-slot>
                <x-slot name="description">
                    {{ __('Password-protect directories on your website. When visitors try to access a protected directory, they will be prompted to enter a username and password.') }}
                </x-slot>
            </x-filament::section>

            {{-- Protected Directories Table --}}
            <div class="-mt-4">
                {{ $this->table }}
            </div>
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
                    {{ __('Add a domain first to configure protected directories.') }}
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
