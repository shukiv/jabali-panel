@php
    $languages = config('languages.supported', []);
    $currentLocale = app()->getLocale();
    $currentLanguage = $languages[$currentLocale] ?? ['name' => 'English', 'flag' => '🇬🇧', 'native' => 'English'];
@endphp

<x-filament::dropdown placement="bottom-end" :teleport="true">
    <x-slot name="trigger">
        <button
            type="button"
            class="fi-icon-btn relative flex items-center justify-center rounded-lg outline-none transition duration-75 focus-visible:ring-2 disabled:pointer-events-none disabled:opacity-70 h-9 w-9 text-gray-400 hover:text-gray-500 focus-visible:ring-primary-600 dark:text-gray-500 dark:hover:text-gray-400 dark:focus-visible:ring-primary-500 hover:bg-gray-50 dark:hover:bg-white/5"
            title="{{ $currentLanguage['name'] }}"
        >
            <span class="text-xl leading-none">{{ $currentLanguage['flag'] }}</span>
        </button>
    </x-slot>

    <x-filament::dropdown.header class="px-3 py-2 border-b border-gray-200 dark:border-gray-700">
        <span class="text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-gray-400">
            {{ __('Select Language') }}
        </span>
    </x-filament::dropdown.header>

    <x-filament::dropdown.list class="max-h-64 overflow-y-auto">
        @foreach($languages as $code => $language)
            <a
                href="{{ route('language.switch', $code) }}"
                @class([
                    'fi-dropdown-list-item flex w-full items-center gap-3 px-3 py-2 text-sm transition-colors',
                    'bg-primary-50 dark:bg-primary-500/10' => $code === $currentLocale,
                    'hover:bg-gray-50 dark:hover:bg-white/5' => $code !== $currentLocale,
                ])
            >
                <span class="text-xl leading-none shrink-0">{{ $language['flag'] }}</span>
                <span class="flex flex-col min-w-0">
                    <span @class([
                        'font-medium truncate',
                        'text-primary-600 dark:text-primary-400' => $code === $currentLocale,
                        'text-gray-700 dark:text-gray-200' => $code !== $currentLocale,
                    ])>
                        {{ $language['native'] }}
                    </span>
                    @if($language['native'] !== $language['name'])
                        <span class="text-xs text-gray-500 dark:text-gray-400 truncate">
                            {{ $language['name'] }}
                        </span>
                    @endif
                </span>
                @if($code === $currentLocale)
                    <x-filament::icon
                        icon="heroicon-m-check"
                        class="ml-auto h-5 w-5 shrink-0 text-primary-600 dark:text-primary-400"
                    />
                @endif
            </a>
        @endforeach
    </x-filament::dropdown.list>
</x-filament::dropdown>
