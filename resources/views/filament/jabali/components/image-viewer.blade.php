<div class="flex flex-col items-center justify-center p-4">
    @if($imageData)
        <img
            src="{{ $imageData }}"
            alt="{{ $filename }}"
            class="max-w-full max-h-[70vh] rounded-lg shadow-lg object-contain"
        />
        <p class="mt-4 fi-section-header-description">{{ $filename }}</p>
    @else
        <div class="flex flex-col items-center justify-center py-12">
            <x-filament::icon
                icon="heroicon-o-photo"
                :size="\Filament\Support\Enums\IconSize::TwoExtraLarge"
                class="fi-color-gray fi-text-color-400"
            />
            <p class="mt-4 fi-section-header-description">{{ __('Unable to load image') }}</p>
        </div>
    @endif
</div>
