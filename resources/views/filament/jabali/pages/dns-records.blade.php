@php
    use App\Models\Domain;
@endphp

<x-filament-panels::page>
    @if(Domain::where('user_id', auth()->id())->exists())
        {{-- Domain Selector --}}
        <x-filament::section>
            <x-slot name="heading">
                {{ __('Select Domain') }}
            </x-slot>
            <x-slot name="description">
                {{ __('Choose a domain to manage its DNS records.') }}
            </x-slot>

            {{ $this->form }}
        </x-filament::section>

        @if($selectedDomainId)
            {{-- Pending Adds Infolist --}}
            @if(count($pendingAdds) > 0)
                {{ $this->pendingAddsInfolist }}
            @endif

            {{-- Warning Banner --}}
            <x-filament::section
                icon="heroicon-o-exclamation-triangle"
                icon-color="warning"
                compact
            >
                <x-slot name="heading">
                    {{ __('Important') }}
                </x-slot>
                <x-slot name="description">
                    {{ __('Incorrect DNS changes can make your website or email unreachable. Changes may take up to 48 hours to propagate globally.') }}
                </x-slot>
            </x-filament::section>

            {{-- Pending Changes Summary --}}
            @if($this->hasPendingChanges())
                <x-filament::section
                    icon="heroicon-o-clock"
                    icon-color="info"
                    compact
                >
                    <x-slot name="heading">
                        {{ __('Unsaved Changes') }}
                    </x-slot>
                    <x-slot name="description">
                        {{ __('You have :count pending change(s). Click Save to apply them.', ['count' => $this->getPendingChangesCount()]) }}
                    </x-slot>
                </x-filament::section>
            @endif

            {{-- Records Table --}}
            {{ $this->table }}
        @else
            {{-- No Domain Selected --}}
            <x-filament::section
                icon="heroicon-o-cursor-arrow-rays"
                icon-color="gray"
            >
                <x-slot name="heading">
                    {{ __('No Domain Selected') }}
                </x-slot>
                <x-slot name="description">
                    {{ __('Select a domain from the dropdown above to view and manage its DNS records.') }}
                </x-slot>
            </x-filament::section>
        @endif
    @else
        {{-- No Domains Found --}}
        <x-filament::section
            icon="heroicon-o-globe-alt"
            icon-color="gray"
        >
            <x-slot name="heading">
                {{ __('No Domains Found') }}
            </x-slot>
            <x-slot name="description">
                {{ __('You need to add a domain before you can manage DNS records.') }}
            </x-slot>

            <x-filament::button
                :href="route('filament.jabali.pages.domains')"
                tag="a"
                icon="heroicon-o-plus"
            >
                {{ __('Add Domain') }}
            </x-filament::button>
        </x-filament::section>
    @endif

    <x-filament-actions::modals />
</x-filament-panels::page>
