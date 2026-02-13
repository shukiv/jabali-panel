<div class="space-y-4">
    <div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <x-filament::section compact>
            <x-slot name="heading">{{ __('Type') }}</x-slot>
            <x-filament::badge :color="match($record->type) {
                'ssl_errors' => 'warning',
                'backup_failures' => 'danger',
                'disk_quota' => 'warning',
                'login_failures' => 'danger',
                'ssh_logins' => 'info',
                'system_updates' => 'info',
                'service_health' => 'primary',
                default => 'gray',
            }">
                {{ $record->type_label }}
            </x-filament::badge>
        </x-filament::section>

        <x-filament::section compact>
            <x-slot name="heading">{{ __('Status') }}</x-slot>
            <x-filament::badge :color="match($record->status) {
                'sent' => 'success',
                'failed' => 'danger',
                'skipped' => 'gray',
                default => 'gray',
            }">
                {{ ucfirst($record->status) }}
            </x-filament::badge>
        </x-filament::section>

        <x-filament::section compact>
            <x-slot name="heading">{{ __('Date') }}</x-slot>
            <p class="fi-section-header-description">{{ $record->created_at->format('M d, Y H:i:s') }}</p>
        </x-filament::section>
    </div>

    <x-filament::section compact>
        <x-slot name="heading">{{ __('Recipients') }}</x-slot>
        @if(count($record->recipients) > 0)
            <div class="flex flex-wrap gap-2">
                @foreach($record->recipients as $recipient)
                    <x-filament::badge color="gray">
                        {{ $recipient }}
                    </x-filament::badge>
                @endforeach
            </div>
        @else
            <p class="fi-section-header-description italic">{{ __('No recipients') }}</p>
        @endif
    </x-filament::section>

    <x-filament::section compact>
        <x-slot name="heading">{{ __('Message') }}</x-slot>
        <p class="fi-section-header-description whitespace-pre-wrap">{{ $record->message }}</p>
    </x-filament::section>

    @if($record->context && count($record->context) > 0)
        <x-filament::section compact>
            <x-slot name="heading">{{ __('Details') }}</x-slot>
            <dl class="grid grid-cols-1 gap-2 sm:grid-cols-2">
                @foreach($record->context as $key => $value)
                    <div class="flex flex-col">
                        <dt class="fi-section-header-description">{{ $key }}</dt>
                        <dd class="fi-section-header-heading">{{ $value }}</dd>
                    </div>
                @endforeach
            </dl>
        </x-filament::section>
    @endif

    @if($record->error)
        <x-filament::section compact>
            <x-slot name="heading">
                <span class="fi-section-header-heading fi-color-danger fi-text-color-600 dark:fi-text-color-400">{{ __('Error') }}</span>
            </x-slot>
            <p class="fi-section-header-description fi-color-danger fi-text-color-600 dark:fi-text-color-400 font-mono">{{ $record->error }}</p>
        </x-filament::section>
    @endif
</div>
