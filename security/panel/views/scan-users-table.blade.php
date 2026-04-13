<div style="display: grid; gap: 1.5rem;">
    @if($this->scanning)
        <div wire:poll.1s="processNextScan">
            <x-filament::section>
                <x-slot name="heading">
                    <div style="display: flex; align-items: center; gap: 0.5rem;">
                        <x-filament::loading-indicator style="width: 1.25rem; height: 1.25rem;" />
                        {{ __('Scanning users: :progress', ['progress' => $this->scanProgress]) }}
                    </div>
                </x-slot>
                <x-slot name="description">{{ $this->scanPercent }}% {{ __('complete') }}</x-slot>
            </x-filament::section>
        </div>
    @endif

    @if($this->showResults)
        @php $results = $this->totalResults; @endphp
        <x-filament::section
            icon="{{ $results['threats'] > 0 ? 'heroicon-o-shield-exclamation' : 'heroicon-o-shield-check' }}"
            :icon-color="$results['threats'] > 0 ? 'danger' : 'success'"
        >
            <x-slot name="heading">{{ __('Scan Complete') }}</x-slot>
            <x-slot name="description">
                {{ __(':users users scanned, :files files checked, :threats threats found', [
                    'users' => $results['users'],
                    'files' => number_format($results['files']),
                    'threats' => $results['threats'],
                ]) }}
            </x-slot>
            <x-slot name="headerEnd">
                <x-filament::icon-button
                    icon="heroicon-o-x-mark"
                    wire:click="dismissResults"
                    :label="__('Dismiss')"
                />
            </x-slot>

            @if($results['threats'] > 0)
                @foreach($this->scanJobs as $username => $job)
                    @if(($job['threats_count'] ?? 0) > 0)
                        <x-filament::section collapsible collapsed :compact="true">
                            <x-slot name="heading">
                                <x-filament::badge color="danger">
                                    {{ $job['threats_count'] }} {{ __('threats') }}
                                </x-filament::badge>
                                {{ $username }} — /home/{{ $username }}
                            </x-slot>
                            @foreach($job['threats'] as $threat)
                                <p style="font-size: 0.8rem; opacity: 0.8;">
                                    {{ basename($threat['path'] ?? '') }}
                                    (score: {{ $threat['score'] ?? 0 }})
                                    @if(!empty($threat['findings']))
                                        &mdash; {{ collect($threat['findings'])->pluck('rule')->implode(', ') }}
                                    @endif
                                </p>
                            @endforeach
                        </x-filament::section>
                    @endif
                @endforeach
            @endif
        </x-filament::section>
    @endif

    {{ $this->table }}
</div>
