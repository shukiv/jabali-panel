<x-filament-panels::page>
    {{-- Domain Selector --}}
    <x-filament::section>
        <x-slot name="heading">{{ __('Select Domain') }}</x-slot>
        @if($selectedDomainId)
            <x-slot name="headerEnd">
                @php $status = $this->getZoneStatus(); @endphp
                @if($status)
                    <div class="flex items-center gap-3">
                        @if($status['zone_file_exists'])
                            <x-filament::badge color="success">{{ __('Active') }}</x-filament::badge>
                        @else
                            <x-filament::badge color="danger">{{ __('Missing') }}</x-filament::badge>
                        @endif
                        <x-filament::badge color="gray">{{ $status['records_count'] }} {{ __('records') }}</x-filament::badge>
                        <x-filament::badge color="gray">{{ __('Owner') }}: {{ $status['user'] }}</x-filament::badge>
                        <x-filament::icon-button
                            wire:click="rebuildCurrentZone"
                            icon="heroicon-o-arrow-path"
                            color="gray"
                            size="sm"
                            :tooltip="__('Rebuild Zone')"
                        />
                        <x-filament::icon-button
                            wire:click="deleteCurrentZone"
                            wire:confirm="{{ __('Delete DNS zone for this domain? All records will be removed.') }}"
                            icon="heroicon-o-trash"
                            color="danger"
                            size="sm"
                            :tooltip="__('Delete Zone')"
                        />
                    </div>
                @endif
            </x-slot>
        @endif

        {{ $this->form }}
    </x-filament::section>

    @if($selectedDomainId)
        {{-- Pending Adds Preview --}}
        @if(count($pendingAdds) > 0)
            <x-filament::section icon="heroicon-o-plus-circle" icon-color="success" collapsible>
                <x-slot name="heading">{{ __('New Records to Add') }}</x-slot>
                <div class="divide-y divide-gray-200 dark:divide-white/10">
                    @foreach($pendingAdds as $index => $add)
                        <div class="flex items-center justify-between py-2 @if($loop->first) pt-0 @endif">
                            <div class="flex items-center gap-3">
                                <x-filament::badge color="success">{{ $add['type'] }}</x-filament::badge>
                                <span class="font-mono fi-section-header-heading">{{ $add['name'] }}</span>
                                <span class="fi-section-header-description">{{ __('→') }}</span>
                                <span class="font-mono fi-section-header-description truncate max-w-xs" title="{{ $add['content'] }}">{{ Str::limit($add['content'], 40) }}</span>
                            </div>
                            <x-filament::icon-button
                                wire:click="removePendingAdd({{ $index }})"
                                icon="heroicon-o-x-mark"
                                color="danger"
                                size="sm"
                                :tooltip="__('Remove')"
                            />
                        </div>
                    @endforeach
                </div>
            </x-filament::section>
        @endif

        {{-- Records Table --}}
        {{ $this->table }}
    @else
        <x-filament::section icon="heroicon-o-globe-alt" icon-color="gray">
            <x-slot name="heading">
                {{ __('No Domain Selected') }}
            </x-slot>
            <x-slot name="description">
                {{ __('Select a domain from the dropdown above to manage its DNS records.') }}
            </x-slot>
        </x-filament::section>
    @endif

    <x-filament-actions::modals />
</x-filament-panels::page>
