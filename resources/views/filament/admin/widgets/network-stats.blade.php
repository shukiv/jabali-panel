<x-filament-widgets::widget>
    <x-filament::section compact>
        <div
            wire:poll.5s="$refresh"
            class="grid grid-cols-2 gap-4"
        >
            @php $data = $this->getNetworkData(); @endphp
            <div>
                <div class="flex items-center gap-1 text-sm text-gray-500 dark:text-gray-400">
                    <x-filament::icon icon="heroicon-o-arrow-up" class="h-4 w-4 text-success-500" />
                    {{ __('Upload') }}
                </div>
                <p class="text-xl font-bold">{{ $data['tx_speed'] }}</p>
                <p class="text-xs text-success-500">{{ __('Total') }}: {{ $data['total_tx'] }}</p>
            </div>
            <div>
                <div class="flex items-center gap-1 text-sm text-gray-500 dark:text-gray-400">
                    <x-filament::icon icon="heroicon-o-arrow-down" class="h-4 w-4 text-info-500" />
                    {{ __('Download') }}
                </div>
                <p class="text-xl font-bold">{{ $data['rx_speed'] }}</p>
                <p class="text-xs text-info-500">{{ __('Total') }}: {{ $data['total_rx'] }}</p>
            </div>
        </div>
    </x-filament::section>
</x-filament-widgets::widget>
