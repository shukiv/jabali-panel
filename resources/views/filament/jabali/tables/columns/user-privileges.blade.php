@php
    $record = $getRecord();
    $user = $record['user'];
    $host = $record['host'];
    $grants = $this->getUserGrants($user, $host);
@endphp

<div class="flex flex-wrap items-center gap-1.5">
    @if(count($grants) > 0)
        @foreach($grants as $grant)
            @php
                $privs = $grant['privileges'] ?? [];
                if (is_string($privs)) {
                    $privs = array_map('trim', explode(',', $privs));
                }
                $isAll = count($privs) === 1 && in_array(strtoupper(trim($privs[0])), ['ALL', 'ALL PRIVILEGES']);
            @endphp
            <div class="inline-flex items-center gap-1">
                <x-filament::badge
                    :color="$isAll ? 'success' : 'warning'"
                    icon="heroicon-m-circle-stack"
                >
                    {{ $grant['database'] ?? __('Unknown') }}
                    @if($isAll)
                        ({{ __('Full Access') }})
                    @else
                        ({{ implode(', ', $privs) }})
                    @endif
                </x-filament::badge>
                <x-filament::icon-button
                    wire:click="revokePrivileges('{{ $user }}', '{{ $host }}', '{{ $grant['database'] ?? '' }}')"
                    icon="heroicon-m-x-mark"
                    color="danger"
                    size="xs"
                    :label="__('Revoke')"
                />
            </div>
        @endforeach
    @else
        <x-filament::badge color="gray" icon="heroicon-m-minus-circle">
            {{ __('No database access') }}
        </x-filament::badge>
    @endif
</div>
