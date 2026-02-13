@php
    $jabaliVersion = 'unknown';
    $versionFile = base_path('VERSION');
    if (file_exists($versionFile)) {
        $content = file_get_contents($versionFile);
        if (preg_match('/^VERSION=(.+)$/m', $content, $matches)) {
            $jabaliVersion = trim($matches[1]);
        }
    }
@endphp

<footer class="mt-auto border-t border-gray-200/20 px-6 py-5 dark:border-white/10">
    <div class="flex flex-col gap-4 md:flex-row md:items-center md:justify-between">
        <div class="flex items-center gap-3">
            <img
                src="{{ asset('images/jabali_logo.svg') }}"
                alt="{{ __('Jabali') }}"
                class="h-8 w-8 dark:filter dark:invert dark:brightness-200"
            >

            <div class="leading-tight">
                <div class="text-sm font-semibold text-gray-700 dark:text-gray-200">
                    {{ __('Jabali Panel') }}
                </div>

                <div class="text-xs text-gray-500 dark:text-gray-400">
                    {{ __('Web Hosting Control Panel') }}
                </div>
            </div>
        </div>

        <div class="flex flex-wrap items-center gap-x-4 gap-y-2 text-sm text-gray-500 dark:text-gray-400">
            <x-filament::link
                tag="a"
                href="https://github.com/shukiv/jabali-panel"
                target="_blank"
                rel="noopener"
                color="gray"
                size="sm"
            >
                {{ __('GitHub') }}
            </x-filament::link>

            <span class="text-gray-400/50 dark:text-white/20">•</span>

            <span>© {{ date('Y') }} Jabali</span>

            <x-filament::badge size="sm" color="gray">
                v{{ $jabaliVersion }}
            </x-filament::badge>
        </div>
    </div>
</footer>
