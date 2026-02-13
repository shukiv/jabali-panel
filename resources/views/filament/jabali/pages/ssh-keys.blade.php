<x-filament-panels::page>
    {{-- Info Banner --}}
    <x-filament::section
        icon="heroicon-o-information-circle"
        icon-color="info"
    >
        <x-slot name="heading">
            {{ __('SSH & SFTP Access') }}
        </x-slot>
        <x-slot name="description">
            {{ __('Connect to your server securely using SSH for terminal access or SFTP for file transfers. Generate a new key pair or add your existing public SSH keys below.') }}
        </x-slot>
    </x-filament::section>

    {{-- Generated Key Banner --}}
    @if($this->generatedKey)
        <x-filament::section
            icon="heroicon-o-check-circle"
            icon-color="success"
        >
            <x-slot name="heading">
                {{ __('SSH Key Generated:') }} {{ $this->generatedKey['name'] }}
            </x-slot>
            <x-slot name="description">
                {{ __('Your new :type key has been generated and the public key has been added to your account. Download your private key now - it won\'t be available later!', ['type' => strtoupper($this->generatedKey['type'])]) }}
            </x-slot>

            <div class="mt-3 flex flex-wrap gap-3">
                <x-filament::button wire:click="downloadPrivateKey" color="success" icon="heroicon-o-arrow-down-tray">
                    {{ __('Download OpenSSH Key') }}
                </x-filament::button>
                @if($this->generatedKey['ppk_key'])
                    <x-filament::button wire:click="downloadPpkKey" color="info" icon="heroicon-o-arrow-down-tray">
                        {{ __('Download PPK (FileZilla/WinSCP)') }}
                    </x-filament::button>
                @endif
                <x-filament::button wire:click="clearGeneratedKey" color="gray" outlined>
                    {{ __('Dismiss') }}
                </x-filament::button>
            </div>

            <x-filament::section
                icon="heroicon-o-exclamation-triangle"
                icon-color="warning"
                compact
                class="mt-3"
            >
                <x-slot name="heading">
                    {{ __('Important') }}
                </x-slot>
                <x-slot name="description">
                    {{ __('Save your private key securely! It will not be shown again. Anyone with this key can access your server.') }}
                </x-slot>
            </x-filament::section>
        </x-filament::section>
    @endif

    {{-- Connection Details --}}
    <x-filament::section icon="heroicon-o-command-line" compact>
        <x-slot name="heading">{{ __('Connection Details') }}</x-slot>

        <div class="flex flex-wrap items-center gap-x-6 gap-y-2">
            <div class="flex items-center gap-2">
                <span class="fi-section-header-description">{{ __('Host') }}:</span>
                <x-filament::badge
                    color="gray"
                    x-on:click="navigator.clipboard.writeText('{{ $this->sshHost }}'); $tooltip('{{ __('Copied!') }}')"
                    class="cursor-pointer"
                >
                    {{ $this->sshHost }}
                </x-filament::badge>
            </div>
            <div class="flex items-center gap-2">
                <span class="fi-section-header-description">{{ __('Port') }}:</span>
                <x-filament::badge
                    color="gray"
                    x-on:click="navigator.clipboard.writeText('{{ $this->sshPort }}'); $tooltip('{{ __('Copied!') }}')"
                    class="cursor-pointer"
                >
                    {{ $this->sshPort }}
                </x-filament::badge>
            </div>
            <div class="flex items-center gap-2">
                <span class="fi-section-header-description">{{ __('Username') }}:</span>
                <x-filament::badge
                    color="gray"
                    x-on:click="navigator.clipboard.writeText('{{ $this->sshUsername }}'); $tooltip('{{ __('Copied!') }}')"
                    class="cursor-pointer"
                >
                    {{ $this->sshUsername }}
                </x-filament::badge>
            </div>
            <div class="flex flex-wrap items-center gap-2 min-w-0">
                <span class="fi-section-header-description">{{ __('Command') }}:</span>
                <code
                    class="cursor-pointer rounded bg-gray-100 px-2 py-1 font-mono fi-section-header-description dark:bg-gray-800 break-all max-w-full"
                    x-on:click="navigator.clipboard.writeText('{{ $this->sshCommand }}'); $tooltip('{{ __('Copied!') }}')"
                >
                    {{ $this->sshCommand }}
                </code>
            </div>
        </div>
    </x-filament::section>

    {{-- Shell Access Toggle --}}
    @if($this->shellAccessAllowed)
        <x-filament::section icon="heroicon-o-command-line" compact>
            <x-slot name="heading">{{ __('Terminal Access') }}</x-slot>
            <x-slot name="headerEnd">
                <x-filament::badge :color="$this->shellEnabled ? 'success' : 'gray'">
                    {{ $this->shellEnabled ? __('Enabled') : __('SFTP Only') }}
                </x-filament::badge>
            </x-slot>

            <div class="flex flex-wrap items-center justify-between gap-4">
                <div class="fi-section-header-description">
                    @if($this->shellEnabled)
                        {{ __('You have jailed shell access with basic commands and wp-cli support (you can run wp-cli here).') }}
                    @else
                        {{ __('Currently SFTP-only. Enable shell access for terminal commands and wp-cli.') }}
                    @endif
                </div>
                <x-filament::button
                    wire:click="toggleShellAccess"
                    :color="$this->shellEnabled ? 'danger' : 'success'"
                    :icon="$this->shellEnabled ? 'heroicon-o-lock-closed' : 'heroicon-o-lock-open'"
                    size="sm"
                >
                    {{ $this->shellEnabled ? __('Disable Shell') : __('Enable Shell') }}
                </x-filament::button>
            </div>
        </x-filament::section>
    @endif

    {{-- SSH Keys Table --}}
    <x-filament::section icon="heroicon-o-key">
        <x-slot name="heading">{{ __('SSH Keys') }}</x-slot>
        <x-slot name="description">{{ __('Public keys authorized for SSH access to your account') }}</x-slot>
        <x-slot name="headerEnd">
            <x-filament::badge color="info">{{ count($this->sshKeys) }}</x-filament::badge>
        </x-slot>

        {{ $this->table }}
    </x-filament::section>

    {{-- Download Configs --}}
    <x-filament::section icon="heroicon-o-arrow-down-tray">
        <x-slot name="heading">{{ __('Quick Connect') }}</x-slot>
        <x-slot name="description">{{ __('Download a pre-configured file for your SFTP client and import it to connect instantly.') }}</x-slot>

        <div class="flex flex-wrap items-center gap-3">
            <x-filament::button wire:click="downloadFileZillaConfig" color="danger" icon="heroicon-o-arrow-down-tray" outlined>
                {{ __('FileZilla (.xml)') }}
            </x-filament::button>
            <x-filament::button wire:click="downloadWinScpConfig" color="info" icon="heroicon-o-arrow-down-tray" outlined>
                {{ __('WinSCP (.ini)') }}
            </x-filament::button>
        </div>
    </x-filament::section>

    <x-filament-actions::modals />
</x-filament-panels::page>
