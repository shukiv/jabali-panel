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

    @if($this->selectedDomain)
        <x-filament::section icon="heroicon-o-document-text" class="mt-4">
            <x-slot name="heading">
                {{ __('Log Viewer') }}
            </x-slot>
            <x-slot name="description">
                {{ __('Viewing :type for :domain', ['type' => $this->logType === 'access' ? __('access log') : __('error log'), 'domain' => $this->selectedDomain]) }}
                @if(!empty($this->logInfo))
                    ({{ $this->logInfo['file_size'] ?? '' }}, {{ __(':lines lines', ['lines' => $this->logInfo['lines'] ?? 100]) }})
                @endif
            </x-slot>

            <div class="flex flex-wrap items-center gap-2 gap-y-2 mb-4">
                <x-filament::button
                    wire:click="setLogType('access')"
                    :color="$this->logType === 'access' ? 'primary' : 'gray'"
                    :outlined="$this->logType !== 'access'"
                    size="sm"
                    icon="heroicon-o-arrow-right-circle"
                >
                    {{ __('Access Log') }}
                </x-filament::button>

                <x-filament::button
                    wire:click="setLogType('error')"
                    :color="$this->logType === 'error' ? 'danger' : 'gray'"
                    :outlined="$this->logType !== 'error'"
                    size="sm"
                    icon="heroicon-o-exclamation-triangle"
                >
                    {{ __('Error Log') }}
                </x-filament::button>
            </div>

            @if($this->logContent)
                <x-filament::input.wrapper class="w-full">
                    <textarea
                        readonly
                        rows="25"
                        class="fi-input w-full px-3 py-2 font-mono fi-section-header-description leading-relaxed whitespace-pre overflow-auto bg-gray-50 dark:bg-gray-900 resize-none"
                    >{{ $this->logContent }}</textarea>
                </x-filament::input.wrapper>
            @else
                <x-filament::section compact>
                    <x-slot name="heading">
                        {{ __('No Log Entries') }}
                    </x-slot>
                    <x-slot name="description">
                        {{ $this->logType === 'access'
                            ? __('No access log entries found. Logs will appear after visitors access your site.')
                            : __('No error log entries found. This is good - your site has no errors.') }}
                    </x-slot>
                </x-filament::section>
            @endif
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
