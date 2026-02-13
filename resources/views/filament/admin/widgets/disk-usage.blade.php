<x-filament-widgets::widget>
    <x-filament::section :heading="__('Disk Usage')" icon="heroicon-o-server-stack">
        @php $disk = $this->getData(); @endphp

        <div class="flex flex-col gap-4">
            @forelse(($disk['partitions'] ?? []) as $partition)
                <div class="flex flex-col gap-4 sm:flex-row sm:items-start">
                    <x-filament::icon
                        icon="heroicon-o-server-stack"
                        :size="\Filament\Support\Enums\IconSize::TwoExtraLarge"
                        class="fi-color-gray fi-text-color-400"
                    />

                    <div class="min-w-0 flex-1">
                        <p class="fi-section-header-heading">{{ $partition['mount'] ?? '/' }}</p>

                        @php $pct = $partition['usage_percent'] ?? 0; @endphp
                        <div class="mt-2 h-3 w-full overflow-hidden rounded-full bg-gray-200 dark:bg-gray-800">
                            <div class="h-full rounded-full bg-gray-400" style="width: {{ $pct }}%;"></div>
                        </div>

                        <p class="fi-section-header-description">
                            {{ $partition['used_human'] ?? '0 B' }} {{ __('used of') }} {{ $partition['total_human'] ?? '0 B' }}
                        </p>
                    </div>
                </div>
            @empty
                <p class="fi-section-header-description">{{ __('No disk data available') }}</p>
            @endforelse
        </div>
    </x-filament::section>
</x-filament-widgets::widget>
