@php
    // Group events by account — each 'heading' event starts a new section
    $sections = [];
    $currentSection = ['title' => __('General'), 'events' => []];
    foreach ($events as $event) {
        if (($event['level'] ?? '') === 'heading') {
            if (! empty($currentSection['events'])) {
                $sections[] = $currentSection;
            }
            $currentSection = ['title' => $event['text'], 'events' => []];
        } else {
            $currentSection['events'][] = $event;
        }
    }
    if (! empty($currentSection['events'])) {
        $sections[] = $currentSection;
    }
@endphp

<div x-data="{ showRaw: false }">
    {{-- Summary view --}}
    <div x-show="!showRaw" class="space-y-4">
        @forelse($sections as $section)
            <x-filament::section :heading="$section['title']" icon="heroicon-o-user" compact>
                @foreach($section['events'] as $event)
                    <div class="flex items-start gap-2 py-1">
                        @switch($event['level'] ?? 'info')
                            @case('success')
                                <x-heroicon-o-check-circle class="w-4 h-4 text-success-500 shrink-0 mt-0.5" />
                                <span class="text-sm">{{ $event['text'] }}</span>
                                @break
                            @case('error')
                                <x-heroicon-o-x-circle class="w-4 h-4 text-danger-500 shrink-0 mt-0.5" />
                                <span class="text-sm text-danger-600 dark:text-danger-400">{{ $event['text'] }}</span>
                                @break
                            @case('warn')
                                <x-heroicon-o-exclamation-triangle class="w-4 h-4 text-warning-500 shrink-0 mt-0.5" />
                                <span class="text-sm text-warning-600 dark:text-warning-400">{{ $event['text'] }}</span>
                                @break
                            @case('skip')
                                <x-heroicon-o-minus-circle class="w-4 h-4 text-gray-400 shrink-0 mt-0.5" />
                                <span class="text-sm text-gray-400">{{ $event['text'] }}</span>
                                @break
                            @default
                                <x-heroicon-o-information-circle class="w-4 h-4 text-gray-400 shrink-0 mt-0.5" />
                                <span class="text-sm text-gray-600 dark:text-gray-400">{{ $event['text'] }}</span>
                        @endswitch
                    </div>
                @endforeach
            </x-filament::section>
        @empty
            <p class="text-sm text-gray-500">{{ __('No details available.') }}</p>
        @endforelse
    </div>

    {{-- Raw log --}}
    <div x-show="showRaw" x-cloak>
        <x-filament::section :heading="__('Raw Log')" icon="heroicon-o-command-line" compact>
            <pre class="text-xs font-mono whitespace-pre-wrap max-h-96 overflow-y-auto">{{ implode("\n", $log) }}</pre>
        </x-filament::section>
    </div>

    <div class="mt-3">
        <x-filament::button size="sm" color="gray" x-on:click="showRaw = !showRaw">
            <span x-text="showRaw ? '{{ __('Show summary') }}' : '{{ __('Show raw log') }}'"></span>
        </x-filament::button>
    </div>
</div>
