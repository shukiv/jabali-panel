@if(count($this->getDomainOptions()) > 0)
    <x-filament::section icon="heroicon-o-globe-alt" icon-color="primary" class="mt-4">
        <x-slot name="heading">
            {{ __('Select Domain') }}
        </x-slot>
        <x-slot name="description">
            {{ __('Choose the domain you want to view logs for.') }}
        </x-slot>

        <div class="max-w-md">
            <x-filament::input.wrapper>
                <x-filament::input.select wire:model.live="selectedDomain">
                    @foreach($this->getDomainOptions() as $value => $label)
                        <option value="{{ $value }}">{{ $label }}</option>
                    @endforeach
                </x-filament::input.select>
            </x-filament::input.wrapper>
        </div>
    </x-filament::section>

    @if($this->selectedDomain && $this->statsGenerated)
        <x-filament::section icon="heroicon-o-check-circle" icon-color="success" class="mt-4">
            <x-slot name="heading">
                {{ __('Statistics Report Ready') }}
            </x-slot>
            <x-slot name="description">
                {{ __('Traffic analysis report has been generated for :domain', ['domain' => $this->selectedDomain]) }}
            </x-slot>

            <x-filament::button
                :href="$this->statsUrl"
                tag="a"
                target="_blank"
                icon="heroicon-o-arrow-top-right-on-square"
            >
                {{ __('View Report') }}
            </x-filament::button>
        </x-filament::section>
    @endif
@else
    <x-filament::section class="mt-4">
        <div class="flex flex-col items-center justify-center py-12">
            <div class="mb-4 rounded-full bg-gray-100 p-3 dark:bg-gray-500/20">
                <x-filament::icon icon="heroicon-o-globe-alt" :size="\Filament\Support\Enums\IconSize::Large" class="fi-color-gray fi-text-color-500" />
            </div>
            <h3 class="fi-section-header-heading">
                {{ __('No Domains Yet') }}
            </h3>
            <p class="mt-1 fi-section-header-description">
                {{ __('Add a domain first to view logs and statistics.') }}
            </p>
            <div class="mt-6">
                <x-filament::button href="{{ route('filament.jabali.pages.domains') }}" tag="a">
                    {{ __('Add Domain') }}
                </x-filament::button>
            </div>
        </div>
    </x-filament::section>
@endif
